package llamaprov

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/stashapp/stash/pkg/aitag"
	"github.com/stashapp/stash/pkg/aitag/native"
	"github.com/stashapp/stash/pkg/aitag/taxonomy"
)

const defaultMinCandidates = 3

var descriptionSplitRE = regexp.MustCompile(`[ ,;:/()\[\]\.]+`)

// DefaultTaxonomyCategories returns the StashDB categories used when none are
// configured.
func DefaultTaxonomyCategories() []string {
	return []string{"Acts", "Accessories", "Themes", "Roles", "Genitals", "Surfaces", "Clothing"}
}

// TaxonomyAnalyzer wraps a llama VLM with a taxonomy-grounded two-pass video
// analyzer. The underlying provider remains available to legacy VLM evaluation.
type TaxonomyAnalyzer struct {
	Provider      *Provider
	Client        *taxonomy.Client
	Categories    []string
	MaxCandidates int
	MaxPerFrame   int
	MinCandidates int
}

func (a *TaxonomyAnalyzer) Name() string { return a.Provider.Name() }

func (a *TaxonomyAnalyzer) Capabilities() aitag.Capability { return a.Provider.Capabilities() }

func (a *TaxonomyAnalyzer) Available(ctx context.Context) error { return a.Provider.Available(ctx) }

func (a *TaxonomyAnalyzer) Models(ctx context.Context) ([]aitag.ModelInfo, error) {
	return a.Provider.Models(ctx)
}

func (a *TaxonomyAnalyzer) AnalyzeVideo(ctx context.Context, path string, opts aitag.Options, sink aitag.Sink) (*aitag.Result, error) {
	return a.Analyze(ctx, path, opts, sink)
}

func (a *TaxonomyAnalyzer) AnalyzeImages(ctx context.Context, paths []string, opts aitag.Options) (*aitag.ImageResult, error) {
	return a.Provider.AnalyzeImages(ctx, paths, opts)
}

func (a *TaxonomyAnalyzer) Close() error { return a.Provider.Close() }

// Analyze describes each frame, selects taxonomy candidates from that caption,
// then asks the VLM to verify only those candidates.
func (a *TaxonomyAnalyzer) Analyze(ctx context.Context, videoPath string, opts aitag.Options, sink aitag.Sink) (*aitag.Result, error) {
	if a == nil || a.Provider == nil {
		return nil, fmt.Errorf("llama VLM provider is required")
	}
	if a.Client == nil {
		return nil, fmt.Errorf("taxonomy client is required")
	}
	if err := a.Provider.waitUntilReady(ctx); err != nil {
		return nil, err
	}
	started := time.Now()
	interval := opts.FrameInterval
	if interval <= 0 {
		interval = a.Provider.interval
	}
	categories := append([]string(nil), a.Categories...)
	if len(categories) == 0 {
		categories = DefaultTaxonomyCategories()
	}
	byCategory, err := a.Client.CandidatesByCategory(ctx, categories)
	if err != nil {
		return nil, err
	}
	pool := flattenCandidates(byCategory)
	if len(pool) == 0 {
		return nil, fmt.Errorf("taxonomy has no candidates in categories %s", strings.Join(categories, ", "))
	}

	duration, err := native.ProbeDuration(ctx, a.Provider.ffmpegPath, videoPath)
	if err != nil {
		duration = 0
	}
	frames, err := native.OpenFrames(ctx, a.Provider.ffmpegPath, videoPath, native.ExtractOptions{
		Interval: interval,
		Size:     frameSize,
		VR:       opts.VR,
	})
	if err != nil {
		return nil, err
	}
	defer frames.Close()

	detected := make([]aitag.Frame, 0)
	resolvedIDs := make(map[string]string)
	var buffer []byte
	var lastTime float64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		frame, err := frames.Next(buffer)
		if err != nil {
			return nil, err
		}
		if frame == nil {
			break
		}
		buffer = frame.RGB
		lastTime = frame.Time
		classifierFrame := aitag.Frame{
			RGB: frame.RGB, Width: frames.Width, Height: frames.Height, Time: frame.Time,
		}
		description, err := a.Provider.Describe(ctx, classifierFrame)
		if err != nil {
			return nil, fmt.Errorf("describe frame %.3f: %w", frame.Time, err)
		}
		candidates := a.selectCandidates(description, pool)
		labels := make([]string, len(candidates))
		for i := range candidates {
			labels[i] = candidates[i].Canonical
		}
		decisions, err := a.Provider.Verify(ctx, classifierFrame, labels)
		if err != nil {
			return nil, fmt.Errorf("verify frame %.3f: %w", frame.Time, err)
		}
		hits := make([]aitag.Detection, 0, len(labels))
		for _, label := range labels {
			if !decisions[label] {
				continue
			}
			entry, ok := a.Client.Resolve(ctx, label)
			if !ok {
				return nil, fmt.Errorf("verified taxonomy label %q no longer resolves", label)
			}
			hits = append(hits, aitag.Detection{Tag: entry.Canonical})
			resolvedIDs[entry.Canonical] = entry.StashID
		}
		detected = append(detected, aitag.Frame{
			Index:  frame.Time,
			Labels: map[string][]aitag.Detection{a.Provider.category: hits},
		})
		fraction := -1.0
		if duration > 0 {
			fraction = min((frame.Time+interval)/duration, 1)
		}
		sink.Report(aitag.Progress{
			Fraction: fraction,
			Frames:   frames.Count(),
			Message:  fmt.Sprintf("Classified %d frames", frames.Count()),
		})
	}
	if err := frames.Finish(); err != nil {
		return nil, err
	}
	if frames.Count() == 0 {
		return nil, native.ErrNoFrames
	}
	if duration == 0 {
		duration = lastTime + interval
	}
	sort.SliceStable(detected, func(i, j int) bool { return detected[i].Index < detected[j].Index })
	spans := aitag.CollapseFrames(detected, interval, a.Provider.maxMerge)
	aitag.SortSpans(spans)
	model := a.Provider.modelInfo(interval)
	cacheStatus := a.Client.Status()
	endpoint := cacheStatus.Endpoint
	refreshedAt := ""
	if !cacheStatus.UpdatedAt.IsZero() {
		refreshedAt = cacheStatus.UpdatedAt.UTC().Format(time.RFC3339)
	}
	model.Extra = map[string]any{
		"provider":              ProviderName,
		"model":                 model.Name,
		"taxonomy_endpoint":     endpoint,
		"taxonomy_refreshed_at": refreshedAt,
		"candidate_pool_size":   len(pool),
		"frames":                frames.Count(),
		"stash_ids":             resolvedIDs,
	}
	elapsed := time.Since(started)
	return &aitag.Result{
		SchemaVersion: 3,
		Duration:      duration,
		FrameInterval: interval,
		Models:        []aitag.ModelInfo{model},
		Spans:         spans,
		Metrics: map[string]any{
			"frames":        frames.Count(),
			"elapsed_ms":    elapsed.Milliseconds(),
			"total_seconds": elapsed.Seconds(),
		},
	}, nil
}

func (a *TaxonomyAnalyzer) selectCandidates(description string, pool []taxonomy.Entry) []taxonomy.Entry {
	terms := descriptionTerms(description)
	type scoredCandidate struct {
		entry taxonomy.Entry
		score int
	}
	scored := make([]scoredCandidate, 0)
	for _, entry := range pool {
		if score := entryDescriptionScore(entry, terms); score > 0 {
			scored = append(scored, scoredCandidate{entry: entry, score: score})
		}
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			return scored[i].entry.Canonical < scored[j].entry.Canonical
		}
		return scored[i].score > scored[j].score
	})
	matched := make([]taxonomy.Entry, len(scored))
	for i := range scored {
		matched[i] = scored[i].entry
	}
	minCandidates := a.MinCandidates
	if minCandidates <= 0 {
		minCandidates = defaultMinCandidates
	}
	maxCandidates := a.MaxCandidates
	if maxCandidates < minCandidates {
		maxCandidates = minCandidates
	}
	maxPerFrame := a.MaxPerFrame
	if maxPerFrame <= 0 || maxPerFrame > maxCandidates {
		maxPerFrame = maxCandidates
	}
	if maxPerFrame < minCandidates {
		maxPerFrame = minCandidates
	}
	if len(matched) == 0 {
		count := min(maxCandidates, len(pool))
		if count < minCandidates {
			count = min(minCandidates, len(pool))
		}
		matched = append(matched, pool[:count]...)
	}
	if len(matched) > maxPerFrame {
		matched = matched[:maxPerFrame]
	}
	return matched
}

func flattenCandidates(byCategory map[string][]taxonomy.Entry) []taxonomy.Entry {
	seen := make(map[string]struct{})
	out := make([]taxonomy.Entry, 0)
	for _, entries := range byCategory {
		for _, entry := range entries {
			if _, ok := seen[entry.StashID]; ok {
				continue
			}
			seen[entry.StashID] = struct{}{}
			out = append(out, entry)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Canonical == out[j].Canonical {
			return out[i].StashID < out[j].StashID
		}
		return out[i].Canonical < out[j].Canonical
	})
	return out
}

type descriptionIndex struct {
	words map[string]struct{}
}

func descriptionTerms(description string) descriptionIndex {
	parts := descriptionSplitRE.Split(strings.ToLower(description), -1)
	index := descriptionIndex{words: make(map[string]struct{}, len(parts))}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if len(part) >= 3 {
			index.words[part] = struct{}{}
		}
	}
	return index
}

func entryDescriptionScore(entry taxonomy.Entry, index descriptionIndex) int {
	score := nameDescriptionScore(entry.Canonical, index)
	for _, alias := range entry.Aliases {
		score = max(score, nameDescriptionScore(alias, index))
	}
	return score
}

func nameDescriptionScore(name string, index descriptionIndex) int {
	parts := descriptionSplitRE.Split(strings.ToLower(name), -1)
	terms := 0
	matches := 0
	for _, part := range parts {
		if len(part) < 3 {
			continue
		}
		terms++
		if _, ok := index.words[part]; ok {
			matches++
		}
	}
	if matches == 0 {
		return 0
	}
	if matches == terms {
		return 1000 + 100/terms
	}
	return matches * 100 / terms
}

var _ aitag.Provider = (*TaxonomyAnalyzer)(nil)
