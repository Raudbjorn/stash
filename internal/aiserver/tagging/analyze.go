package tagging

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/stashapp/stash/internal/aiserver/store"
	"github.com/stashapp/stash/pkg/aitag"
	"github.com/stashapp/stash/pkg/aitag/llamaprov"
	"github.com/stashapp/stash/pkg/aitag/taxonomy"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/txn"
)

// The analysis pipeline: resolve the scene's file, run the provider, store the
// raw spans, cluster them into markers, write them back.
//
// Raw spans are stored before clustering, and clustering happens on the
// writeback path. That separation is what makes re-tuning cheap: changing a
// threshold regenerates markers from stored spans without re-running inference
// over the library.

// Service runs analyses.
type Service struct {
	repo   models.Repository
	db     *store.DB
	writer *Writer

	mu              sync.RWMutex
	provider        aitag.Provider
	rules           *aitag.Rules
	params          aitag.ClusterParams
	mergeSecs       float64
	defaultInterval float64
	ffmpegPath      string
}

// Config configures the service.
type Config struct {
	// Provider performs inference. Nil means analysis is unavailable, which is
	// a state rather than an error: Stash runs fine without it.
	Provider aitag.Provider
	// Rules drive marker generation. Nil loads nothing and generates no
	// markers, which is what an installation with no category files should do.
	Rules *aitag.Rules
	// Params tune the clustering.
	Params aitag.ClusterParams
	// MaxSpanMergeSeconds is the stage-one gap tolerance. The upstream code
	// defaults to 2 but the shipped config overrides it to 4, so this is passed
	// explicitly rather than defaulted in the collapse.
	MaxSpanMergeSeconds float64
	// DefaultFrameInterval is the active provider's sampling default.
	DefaultFrameInterval float64
	// FFmpegPath is the shared decoder used by analysis and evaluation.
	FFmpegPath string
}

// NewService builds the analysis service.
func NewService(repo models.Repository, db *store.DB, cfg Config) *Service {
	params := cfg.Params
	if params == (aitag.ClusterParams{}) {
		params = aitag.DefaultClusterParams()
	}
	merge := cfg.MaxSpanMergeSeconds
	if merge <= 0 {
		merge = 4
	}
	interval := cfg.DefaultFrameInterval
	if interval <= 0 {
		interval = 2
	}
	ffmpegPath := cfg.FFmpegPath
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}

	return &Service{
		repo:            repo,
		db:              db,
		writer:          NewWriter(repo, db),
		provider:        cfg.Provider,
		rules:           cfg.Rules,
		params:          params,
		mergeSecs:       merge,
		defaultInterval: interval,
		ffmpegPath:      ffmpegPath,
	}
}

// TaxonomyStatus describes the active taxonomy snapshot.
type TaxonomyStatus struct {
	Endpoint             string
	Entries              int
	RefreshedAt          time.Time
	CandidatesByCategory []string
}

// RefreshTaxonomy forces a complete fetch without restarting the AI server.
func (s *Service) RefreshTaxonomy(ctx context.Context, endpoint, apiKey string) (TaxonomyStatus, error) {
	s.mu.RLock()
	analyzer, ok := s.provider.(*llamaprov.TaxonomyAnalyzer)
	s.mu.RUnlock()
	if !ok || analyzer.Client == nil {
		return TaxonomyStatus{}, errors.New("taxonomy analysis is not active")
	}
	cache, err := analyzer.Client.Refresh(ctx, endpoint, apiKey)
	if err != nil {
		return TaxonomyStatus{}, err
	}
	return taxonomyStatus(analyzer, cache), nil
}

// TaxonomyStatus returns the active cache without triggering network access.
func (s *Service) TaxonomyStatus() TaxonomyStatus {
	s.mu.RLock()
	analyzer, ok := s.provider.(*llamaprov.TaxonomyAnalyzer)
	s.mu.RUnlock()
	if !ok || analyzer.Client == nil {
		return TaxonomyStatus{}
	}
	return taxonomyStatus(analyzer, analyzer.Client.Status())
}

func taxonomyStatus(analyzer *llamaprov.TaxonomyAnalyzer, cache taxonomy.Cache) TaxonomyStatus {
	categories := append([]string(nil), analyzer.Categories...)
	if len(categories) == 0 {
		categories = llamaprov.DefaultTaxonomyCategories()
	}
	return TaxonomyStatus{
		Endpoint:             cache.Endpoint,
		Entries:              len(cache.Entries),
		RefreshedAt:          cache.UpdatedAt,
		CandidatesByCategory: categories,
	}
}

// SetProvider swaps the provider, which is how a settings change takes effect
// without restarting the subsystem.
func (s *Service) SetProvider(p aitag.Provider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.provider = p
}

// Provider returns the active provider, or nil.
func (s *Service) Provider() aitag.Provider {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.provider
}

// Close releases the provider's resources: ONNX sessions, model memory and
// sockets. Called when the AI server stops, so an enable/disable toggle does
// not leak a session per cycle.
func (s *Service) Close() error {
	s.mu.Lock()
	provider := s.provider
	s.provider = nil
	s.mu.Unlock()

	if provider == nil {
		return nil
	}
	return provider.Close()
}

// SetRules swaps the marker rules.
func (s *Service) SetRules(rules *aitag.Rules) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = rules
}

func (s *Service) snapshot() (aitag.Provider, *aitag.Rules, aitag.ClusterParams, float64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.provider, s.rules, s.params, s.mergeSecs
}

// ErrNoProvider reports that no inference provider is configured.
var ErrNoProvider = errors.New("no AI tagging provider is configured")

// ErrNothingToRegenerate reports a scene with no stored analysis to rebuild
// markers from. Distinct from an empty result: regeneration REPLACES, so
// proceeding with nothing would delete the markers it was asked to rebuild.
var ErrNothingToRegenerate = errors.New("no stored spans to regenerate from")

// ErrNoFile reports a scene Stash has no readable file for.
var ErrNoFile = errors.New("the scene has no file to analyse")

// AnalyzeRequest is one scene analysis.
type AnalyzeRequest struct {
	SceneID int
	Options aitag.Options
	// Writeback controls marker creation. Zero value writes nothing, which is
	// useful for populating results without touching the library.
	Writeback WritebackOptions
	// SkipWriteback stores results without generating markers.
	SkipWriteback bool
	// StoreEmbeddings caches per-frame vectors for the trained head and
	// similarity search. Only meaningful when the provider supplies them.
	StoreEmbeddings bool
}

// AnalyzeResult summarises what an analysis produced.
type AnalyzeResult struct {
	SceneID  int              `json:"scene_id"`
	RunID    int64            `json:"run_id"`
	Provider string           `json:"provider"`
	Duration float64          `json:"duration"`
	Spans    int              `json:"spans"`
	Markers  int              `json:"markers"`
	Elapsed  float64          `json:"elapsed_seconds"`
	Error    string           `json:"error,omitempty"`
	Frames   int              `json:"frames"`
	Write    *WritebackResult `json:"writeback,omitempty"`
}

// AnalyzeScene runs the whole pipeline over one scene.
func (s *Service) AnalyzeScene(ctx context.Context, req AnalyzeRequest, sink aitag.Sink) (*AnalyzeResult, error) {
	provider, rules, params, mergeSecs := s.snapshot()
	if provider == nil {
		return nil, ErrNoProvider
	}

	started := time.Now()

	path, err := s.scenePath(ctx, req.SceneID)
	if err != nil {
		return nil, err
	}

	sink.Report(aitag.Progress{Fraction: 0, Message: "Starting analysis."})

	result, err := provider.AnalyzeVideo(ctx, path, req.Options, sink)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("%s returned no result", provider.Name())
	}

	frameInterval := result.FrameInterval
	if frameInterval <= 0 {
		frameInterval = req.Options.FrameInterval
	}
	if frameInterval <= 0 {
		frameInterval = s.defaultInterval
	}
	result.FrameInterval = frameInterval

	// Ordering is a precondition of clustering, not an assumption about the
	// provider: an unsorted set silently produces overlapping markers.
	aitag.SortSpans(result.Spans)

	out := &AnalyzeResult{
		SceneID:  req.SceneID,
		Provider: provider.Name(),
		Duration: result.Duration,
		Spans:    aitag.CountSpans(result.Spans),
	}
	out.Frames = metricInt(result.Metrics["frames"])

	runID, err := s.storeRun(ctx, provider.Name(), req.SceneID, req.Options, result)
	if err != nil {
		return nil, err
	}
	out.RunID = runID

	if req.StoreEmbeddings && result.Embeddings != nil && s.db != nil {
		if err := s.db.StoreEmbeddings(ctx, provider.Name(), store.StoredEmbeddings{
			SceneID:       req.SceneID,
			Model:         result.Embeddings.Model,
			Dim:           result.Embeddings.Dim,
			FrameInterval: frameInterval,
			Times:         result.Embeddings.Times,
			Vectors:       result.Embeddings.Data,
		}); err != nil {
			// Cached vectors are an optimisation; losing them costs a
			// re-analysis later, not this analysis now.
			logger.Errorf("could not cache embeddings for scene %d: %v", req.SceneID, err)
		}
	}

	if !req.SkipWriteback && rules != nil {
		markers := aitag.Cluster(result, rules, params)
		out.Markers = len(markers)

		write, err := s.writer.Write(ctx, provider.Name(), req.SceneID, runID, markers, frameInterval, req.Writeback)
		if err != nil {
			return out, fmt.Errorf("write markers: %w", err)
		}
		out.Write = &write
	}

	out.Elapsed = time.Since(started).Seconds()
	sink.Report(aitag.Progress{
		Fraction: 1,
		Message:  fmt.Sprintf("Stored %d spans and %d markers.", out.Spans, out.Markers),
	})

	// Unused here but load-bearing at the call site: the collapse gap is part
	// of the service's configuration and a provider that returns raw frames
	// (the native one) needs it.
	_ = mergeSecs
	return out, nil
}

// RegenerateMarkers rebuilds markers from stored spans, without re-analysing.
//
// This is what makes the raw spans worth storing separately: re-tuning the
// clustering across a whole library costs a database read per scene instead of
// hours of inference.
func (s *Service) RegenerateMarkers(ctx context.Context, service string, sceneID int, opts WritebackOptions) (*AnalyzeResult, error) {
	_, rules, params, _ := s.snapshot()
	if rules == nil {
		return nil, errors.New("no marker rules are configured")
	}
	if s.db == nil {
		return nil, errors.New("the AI database is not available")
	}

	run, err := s.db.GetLatestSceneRun(ctx, service, sceneID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, fmt.Errorf("%w: scene %d has no stored analysis for %s",
			ErrNothingToRegenerate, sceneID, service)
	}

	// Read by LABEL, not by resolved tag id. GetSceneTimespans drops anything
	// unresolved, which is right for the recommenders that join on tag ids and
	// catastrophic here: a scene whose labels have no matching Stash tags would
	// regenerate to ZERO markers and, because regeneration replaces, would
	// DELETE the markers a previous run had correctly created.
	// Restricted to the LATEST run. Reading every run would feed the clustering
	// one overlapping copy of every span per analysis, so a scene analysed three
	// times would regenerate to visibly different marker boundaries than it
	// produced the first time.
	buckets, err := s.db.GetSceneSpansByLabel(ctx, service, sceneID, run.RunID)
	if err != nil {
		return nil, err
	}

	result := &aitag.Result{
		Duration:      durationFromParams(run.InputParams),
		FrameInterval: frameIntervalFromParams(run.InputParams),
		Spans:         aitag.SpansByCategory{},
	}
	if result.FrameInterval <= 0 {
		result.FrameInterval = 2
	}

	for category, byLabel := range buckets {
		spans := aitag.SpansByTag{}
		for label, list := range byLabel {
			converted := make([]aitag.Span, 0, len(list))
			for _, span := range list {
				end := span.End
				converted = append(converted, aitag.Span{
					Start:      span.Start,
					End:        &end,
					Confidence: span.Confidence,
				})
			}
			spans[label] = converted
		}
		result.Spans[category] = spans
	}

	// A scene with no stored spans is not a reason to delete what is there: it
	// means the analysis was never run, or was run under a different service.
	// Replacing markers with nothing would silently destroy them.
	if aitag.CountSpans(result.Spans) == 0 {
		return nil, fmt.Errorf("%w: scene %d has no stored spans for %s",
			ErrNothingToRegenerate, sceneID, service)
	}

	markers := aitag.Cluster(result, rules, params)
	write, err := s.writer.Write(ctx, service, sceneID, run.RunID, markers, result.FrameInterval, opts)
	if err != nil {
		return nil, err
	}

	return &AnalyzeResult{
		SceneID:  sceneID,
		RunID:    run.RunID,
		Provider: service,
		Duration: result.Duration,
		Spans:    aitag.CountSpans(result.Spans),
		Markers:  len(markers),
		Write:    &write,
	}, nil
}

// storeRun writes the raw result to the AI database.
func (s *Service) storeRun(ctx context.Context, service string, sceneID int, opts aitag.Options, result *aitag.Result) (int64, error) {
	if s.db == nil {
		return 0, nil
	}

	timespans := map[string]map[string][]store.FrameDetection{}
	for category, byTag := range result.Spans {
		converted := map[string][]store.FrameDetection{}
		for tag, spans := range byTag {
			list := make([]store.FrameDetection, 0, len(spans))
			for _, span := range spans {
				list = append(list, store.FrameDetection{Start: span.Start, End: span.End})
			}
			converted[tag] = list
		}
		timespans[category] = converted
	}

	models := make([]store.ModelInput, 0, len(result.Models))
	for _, m := range result.Models {
		interval := m.FrameInterval
		models = append(models, store.ModelInput{
			ModelID:       m.Identifier,
			Name:          m.Name,
			Version:       m.Version,
			Type:          nonEmpty(m.Type),
			Categories:    m.Categories,
			FrameInterval: &interval,
			Extra:         m.Extra,
		})
	}

	frameInterval := result.FrameInterval
	duration := result.Duration
	metrics := make(map[string]any, len(result.Metrics)+2)
	for key, value := range result.Metrics {
		metrics[key] = value
	}
	metrics["frames_sampled"] = metricInt(result.Metrics["frames"])
	metrics["taxonomy_entries"] = s.TaxonomyStatus().Entries

	return s.db.StoreSceneRun(ctx, store.SceneRunInput{
		Service: service,
		SceneID: sceneID,
		InputParams: map[string]any{
			"frame_interval": frameInterval,
			"threshold":      opts.Threshold,
			"vr_video":       opts.VR,
			"duration":       duration,
			"metrics":        metrics,
		},
		Timespans:        timespans,
		FrameInterval:    &frameInterval,
		Duration:         &duration,
		SchemaVersion:    result.SchemaVersion,
		Models:           models,
		ResolveReference: s.tagResolver(ctx),
	})
}

func metricInt(value any) int {
	switch value := value.(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case float32:
		return int(value)
	default:
		return 0
	}
}

// tagResolver maps a provider label to a Stash tag id for the aggregates.
//
// Aggregates exist to be joined against Stash tags, so a label that resolves to
// nothing gets timespans but no aggregate - the raw record is kept either way.
func (s *Service) tagResolver(ctx context.Context) func(label, category string) *int {
	return func(label, category string) *int {
		id, err := s.writer.resolveTag(ctx, label, false)
		if err != nil {
			logger.Debugf("could not resolve AI label %q: %v", label, err)
			return nil
		}
		return id
	}
}

// scenePath returns the primary file path of a scene.
func (s *Service) scenePath(ctx context.Context, sceneID int) (string, error) {
	if s.repo.Scene == nil {
		return "", ErrNoFile
	}

	var path string
	err := txn.WithReadTxn(ctx, s.repo.TxnManager, func(ctx context.Context) error {
		scene, err := s.repo.Scene.Find(ctx, sceneID)
		if err != nil {
			return err
		}
		if scene == nil {
			return fmt.Errorf("scene %d does not exist", sceneID)
		}
		if err := scene.LoadFiles(ctx, s.repo.Scene); err != nil {
			return err
		}
		if primary := scene.Files.Primary(); primary != nil {
			path = primary.Path
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", ErrNoFile
	}
	return path, nil
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func durationFromParams(params map[string]any) float64 {
	if v, ok := params["duration"].(float64); ok {
		return v
	}
	return 0
}

func frameIntervalFromParams(params map[string]any) float64 {
	if v, ok := params["frame_interval"].(float64); ok {
		return v
	}
	return 0
}
