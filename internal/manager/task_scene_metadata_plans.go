package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/scene/metadata"
	"github.com/stashapp/stash/pkg/scene/metadata/entity"
	"github.com/stashapp/stash/pkg/sliceutil/stringslice"
	"github.com/stashapp/stash/pkg/stashbox"
)

type ProviderFieldMode string

const (
	ProviderFieldModeObserve ProviderFieldMode = "observe"
	ProviderFieldModeMerge   ProviderFieldMode = "merge"
	ProviderFieldModeReplace ProviderFieldMode = "replace"
)

func (m ProviderFieldMode) IsValid() bool {
	switch m {
	case ProviderFieldModeObserve, ProviderFieldModeMerge, ProviderFieldModeReplace:
		return true
	default:
		return false
	}
}

func (m ProviderFieldMode) String() string {
	return string(m)
}

func (m *ProviderFieldMode) UnmarshalGQL(value interface{}) error {
	raw, ok := value.(string)
	if !ok {
		return fmt.Errorf("provider field mode must be a string")
	}
	parsed := ProviderFieldMode(strings.ToLower(raw))
	if !parsed.IsValid() {
		return fmt.Errorf("%q is not a valid provider field mode", raw)
	}
	*m = parsed
	return nil
}

func (m ProviderFieldMode) MarshalGQL(w io.Writer) {
	_, _ = fmt.Fprintf(w, "%q", strings.ToUpper(string(m)))
}

type ProviderPolicy struct {
	Endpoint      string            `json:"endpoint"`
	Priority      int               `json:"priority"`
	PerformerMode ProviderFieldMode `json:"performerMode"`
	StudioMode    ProviderFieldMode `json:"studioMode"`
	DateMode      ProviderFieldMode `json:"dateMode"`
	TitleMode     ProviderFieldMode `json:"titleMode"`
}

type SceneMetadataPlanState string

const (
	SceneMetadataPlanProposed SceneMetadataPlanState = "proposed"
	SceneMetadataPlanAccepted SceneMetadataPlanState = "accepted"
	SceneMetadataPlanApplied  SceneMetadataPlanState = "applied"
	SceneMetadataPlanRejected SceneMetadataPlanState = "rejected"
	SceneMetadataPlanStale    SceneMetadataPlanState = "stale"
)

func (s *SceneMetadataPlanState) UnmarshalGQL(value interface{}) error {
	raw, ok := value.(string)
	if !ok {
		return fmt.Errorf("scene metadata plan state must be a string")
	}
	state := SceneMetadataPlanState(strings.ToLower(raw))
	switch state {
	case SceneMetadataPlanProposed, SceneMetadataPlanAccepted, SceneMetadataPlanApplied,
		SceneMetadataPlanRejected, SceneMetadataPlanStale:
		*s = state
		return nil
	default:
		return fmt.Errorf("%q is not a valid scene metadata plan state", raw)
	}
}

func (s SceneMetadataPlanState) MarshalGQL(writer io.Writer) {
	fmt.Fprintf(writer, "%q", strings.ToUpper(string(s)))
}

type RemoteSceneCandidateDecision string

const (
	RemoteCandidateDecisionAccept RemoteSceneCandidateDecision = "accept"
	RemoteCandidateDecisionReview RemoteSceneCandidateDecision = "review"
	RemoteCandidateDecisionReject RemoteSceneCandidateDecision = "reject"
)

func (s *RemoteSceneCandidateDecision) UnmarshalGQL(value interface{}) error {
	raw, ok := value.(string)
	if !ok {
		return fmt.Errorf("remote scene candidate decision must be a string")
	}
	switch RemoteSceneCandidateDecision(strings.ToLower(raw)) {
	case RemoteCandidateDecisionAccept, RemoteCandidateDecisionReview, RemoteCandidateDecisionReject:
		*s = RemoteSceneCandidateDecision(strings.ToLower(raw))
		return nil
	default:
		return fmt.Errorf("%q is not a valid remote scene candidate decision", raw)
	}
}

func (s RemoteSceneCandidateDecision) MarshalGQL(writer io.Writer) {
	fmt.Fprintf(writer, "%q", strings.ToUpper(string(s)))
}

const (
	sceneMetadataActionPerformerIDs    = "performer_ids"
	sceneMetadataActionCreatePerformer = "create_performer"
	sceneMetadataActionStudioID        = "studio_id"
	sceneMetadataActionCreateStudio    = "create_studio"
	sceneMetadataActionDate            = "date"
	sceneMetadataActionTitle           = "title"
	sceneMetadataActionGroups          = "groups"
	sceneMetadataActionFileMetadata    = "file_metadata"
	sceneMetadataActionRemoteScene     = "remote_scene_match"
)

type SourceExcerpt struct {
	Kind       string
	Label      string
	RawText    string
	Normalized string
}

type PerformerCandidate struct {
	Candidate    string
	Status       string
	PerformerID  int
	MatchingIDs  []int
	ScraperIDs   []string
	Reason       string
	ProposedName string
}

type RemoteSceneCandidate struct {
	Endpoint      string
	RemoteID      string
	Title         string
	Provenance    string
	Score         float64
	Decision      RemoteSceneCandidateDecision
	Contributions []CandidateContribution
}

type CandidateContribution struct {
	Field  string
	Weight float64
	Reason string
}

type SuggestedField struct {
	ActionID    string
	Kind        string
	PayloadJSON string
	State       SceneMetadataPlanState
	ReasonCodes []string
}

type AnalysisPlan struct {
	RunID            string
	SceneID          int
	Sources          []SourceExcerpt
	LocalCandidates  []PerformerCandidate
	RemoteCandidates []RemoteSceneCandidate
	Suggested        []SuggestedField
	ReasonCodes      []string
	Ambiguity        bool
	StaleSceneHash   string
	PolicyVersion    string
	ModelFingerprint string
	State            SceneMetadataPlanState
	CreatedAt        time.Time
	AppliedAt        *time.Time
}

type sceneMetadataActionPayload struct {
	PerformerIDs []int                     `json:"performerIDs,omitempty"`
	Performer    *models.Performer         `json:"performer,omitempty"`
	StudioID     *int                      `json:"studioID,omitempty"`
	Studio       *models.CreateStudioInput `json:"studio,omitempty"`
	Date         *string                   `json:"date,omitempty"`
	Title        *string                   `json:"title,omitempty"`
	Groups       []models.GroupsScenes     `json:"groups,omitempty"`
	File         *sceneMetadataFileUpdate  `json:"file,omitempty"`
	Endpoint     string                    `json:"endpoint,omitempty"`
	RemoteID     string                    `json:"remoteID,omitempty"`
}

type sceneMetadataFileUpdate struct {
	ID             models.FileID     `json:"id"`
	Title          string            `json:"title"`
	Comment        string            `json:"comment"`
	Encoder        string            `json:"encoder"`
	Tags           map[string]string `json:"tags"`
	CreationTime   time.Time         `json:"creationTime"`
	MetadataProbed bool              `json:"metadataProbed"`
}

func newSceneMetadataFileUpdate(file *models.VideoFile) *sceneMetadataFileUpdate {
	if file == nil {
		return nil
	}
	tags := make(map[string]string, len(file.Tags))
	for key, value := range file.Tags {
		tags[key] = value
	}
	return &sceneMetadataFileUpdate{
		ID: file.ID, Title: file.Title, Comment: file.Comment, Encoder: file.Encoder,
		Tags: tags, CreationTime: file.CreationTime, MetadataProbed: file.MetadataProbed,
	}
}

func newSceneMetadataRunID() string {
	return uuid.NewString()
}

func SceneMetadataModelFingerprint(modelKey string) string {
	if modelKey == "" {
		return ""
	}
	spec, ok := entity.FindModel(modelKey)
	if !ok {
		return ""
	}
	modelSHA := ""
	for _, artifact := range spec.Artifacts {
		if artifact.LocalPath == "model.onnx" {
			modelSHA = artifact.SHA256
			break
		}
	}
	prompt := strings.Join(spec.Labels, ",")
	digest := sha256.Sum256([]byte(spec.Key + spec.Revision + modelSHA + prompt))
	return hex.EncodeToString(digest[:])
}

type sceneFingerprintFinder interface {
	FindScenesByFingerprints(context.Context, []models.Fingerprints) ([][]*models.ScrapedScene, error)
}

type sceneSearchFinder interface {
	QueryScene(context.Context, string) ([]*models.ScrapedScene, error)
}

type configuredSceneFingerprintFinder struct {
	Endpoint string
	Finder   sceneFingerprintFinder
}

type remoteSceneMatch struct {
	candidate RemoteSceneCandidate
	remote    *models.ScrapedScene
}

func (j *analyzeSceneMetadataJob) discoverRemoteScenes(ctx context.Context, scene *models.Scene, primary *models.VideoFile) ([]RemoteSceneCandidate, *RemoteSceneCandidate, *models.ScrapedScene, error) {
	if scene == nil || primary == nil {
		return nil, nil, nil, nil
	}
	configuredEndpoints := make(map[string]struct{}, len(j.configuredStashBoxes))
	for _, box := range j.configuredStashBoxes {
		if box != nil {
			configuredEndpoints[normalizeStashBoxEndpoint(box.Endpoint)] = struct{}{}
		}
	}
	for _, stashID := range scene.StashIDs.List() {
		if _, configured := configuredEndpoints[normalizeStashBoxEndpoint(stashID.Endpoint)]; configured &&
			strings.TrimSpace(stashID.StashID) != "" {
			return nil, nil, nil, nil
		}
	}

	finders := j.sceneFingerprintFinders
	if len(finders) == 0 {
		for _, box := range j.configuredStashBoxes {
			if box == nil {
				continue
			}
			finders = append(finders, configuredSceneFingerprintFinder{
				Endpoint: box.Endpoint,
				Finder:   stashbox.NewClient(*box),
			})
		}
	}

	fingerprints := primary.Base().Fingerprints.Filter(
		models.FingerprintTypeMD5,
		models.FingerprintTypeOshash,
		models.FingerprintTypePhash,
	)
	matches := make([]remoteSceneMatch, 0)
	seen := make(map[string]struct{})
	addRemote := func(endpoint, provenance string, remote *models.ScrapedScene) {
		if remote == nil || remote.RemoteSiteID == nil || strings.TrimSpace(*remote.RemoteSiteID) == "" {
			return
		}
		remoteID := strings.TrimSpace(*remote.RemoteSiteID)
		key := normalizeStashBoxEndpoint(endpoint) + "\x00" + remoteID
		if _, duplicate := seen[key]; duplicate {
			return
		}
		seen[key] = struct{}{}
		title := ""
		if remote.Title != nil {
			title = strings.TrimSpace(*remote.Title)
		}
		matches = append(matches, remoteSceneMatch{
			candidate: RemoteSceneCandidate{
				Endpoint: endpoint, RemoteID: remoteID, Title: title,
				Provenance: provenance, Decision: RemoteCandidateDecisionReview,
			},
			remote: remote,
		})
	}

	if len(fingerprints) > 0 {
		for _, provider := range finders {
			results, err := provider.Finder.FindScenesByFingerprints(ctx, []models.Fingerprints{fingerprints})
			if err != nil {
				if ctx.Err() != nil {
					return nil, nil, nil, ctx.Err()
				}
				continue
			}
			if len(results) == 0 {
				continue
			}
			for _, remote := range results[0] {
				addRemote(provider.Endpoint, "fingerprint", remote)
			}
		}
	}

	if len(matches) == 0 {
		query, safe := j.remoteSceneSearchQuery(scene)
		if safe {
			for _, provider := range finders {
				searcher, ok := provider.Finder.(sceneSearchFinder)
				if !ok {
					continue
				}
				results, err := searcher.QueryScene(ctx, query)
				if err != nil {
					if ctx.Err() != nil {
						return nil, nil, nil, ctx.Err()
					}
					continue
				}
				for _, remote := range results {
					addRemote(provider.Endpoint, "search", remote)
				}
			}
		}
	}

	j.rankRemoteSceneMatches(scene, primary, matches)
	sort.SliceStable(matches, func(left, right int) bool {
		return matches[left].candidate.Score > matches[right].candidate.Score
	})
	var chosen *remoteSceneMatch
	if len(matches) == 1 && matches[0].candidate.Provenance == "fingerprint" {
		chosen = &matches[0]
	} else if len(matches) > 0 && remoteSceneMatchIsSafe(matches) {
		chosen = &matches[0]
	}
	if chosen == nil && len(matches) > 0 && j.identifyRemoteScene != nil {
		remote, endpoint, err := j.identifyRemoteScene(ctx, scene)
		if err != nil {
			return nil, nil, nil, err
		}
		if remote != nil && remote.RemoteSiteID != nil && strings.TrimSpace(*remote.RemoteSiteID) != "" {
			remoteID := strings.TrimSpace(*remote.RemoteSiteID)
			normalizedEndpoint := normalizeStashBoxEndpoint(endpoint)
			for idx := range matches {
				if normalizeStashBoxEndpoint(matches[idx].candidate.Endpoint) == normalizedEndpoint &&
					matches[idx].candidate.RemoteID == remoteID {
					matches[idx].candidate.Provenance = "scene_identifier"
					matches[idx].remote = remote
					chosen = &matches[idx]
					break
				}
			}
			if chosen == nil {
				addRemote(endpoint, "scene_identifier", remote)
				chosen = &matches[len(matches)-1]
			}
		}
	}
	if chosen != nil {
		chosen.candidate.Decision = RemoteCandidateDecisionAccept
	}
	candidates := make([]RemoteSceneCandidate, 0, len(matches))
	for idx := range matches {
		candidates = append(candidates, matches[idx].candidate)
	}
	if chosen == nil {
		return candidates, nil, nil, nil
	}
	selected := chosen.candidate
	return candidates, &selected, chosen.remote, nil
}

func (j *analyzeSceneMetadataJob) remoteSceneSearchQuery(scene *models.Scene) (string, bool) {
	title := strings.TrimSpace(scene.Title)
	key := metadata.NormalizeKey(title)
	if key == "" || key == "scene" || key == "video" || key == "untitled" {
		return "", false
	}
	if len(strings.Fields(key)) < 2 && len([]rune(key)) < 10 {
		return "", false
	}
	parts := []string{title}
	if studioName := j.localStudioName(scene.StudioID); studioName != "" {
		parts = append(parts, studioName)
	}
	if scene.Date != nil {
		parts = append(parts, scene.Date.String()[:4])
	}
	return strings.Join(parts, " "), true
}
func (j *analyzeSceneMetadataJob) rankRemoteSceneMatches(scene *models.Scene, primary *models.VideoFile, matches []remoteSceneMatch) {
	local := metadata.SceneMeta{}
	if scene.Date != nil {
		local.ReleaseDate = &scene.Date.Time
	}
	if primary != nil {
		local.Duration = primary.Duration
		local.DurationExact = primary.Duration > 0
	}
	if scene.StudioID != nil {
		local.StudioID = strconv.Itoa(*scene.StudioID)
	}
	if scene.PerformerIDs.Loaded() {
		for name := range j.localPerformerNames(scene.PerformerIDs.List()) {
			local.PerformerIDs = append(local.PerformerIDs, name)
		}
	}
	remote := make([]metadata.RemoteSceneMeta, 0, len(matches))
	for _, match := range matches {
		candidate := metadata.RemoteSceneMeta{}
		if match.candidate.Provenance == "fingerprint" {
			candidate.Fingerprint = "exact"
			local.Fingerprint = "exact"
		}
		if match.remote != nil {
			candidate.Duration = float64Value(match.remote.Duration)
			candidate.DurationExact = match.remote.Duration != nil
			if match.remote.Date != nil {
				if parsed, err := models.ParseDate(strings.TrimSpace(*match.remote.Date)); err == nil {
					candidate.ReleaseDate = &parsed.Time
				}
			}
			if match.remote.Studio != nil && match.remote.Studio.StoredID != nil {
				candidate.StudioID = strings.TrimSpace(*match.remote.Studio.StoredID)
			}
			for _, performer := range match.remote.Performers {
				if performer != nil {
					candidate.PerformerIDs = append(candidate.PerformerIDs, stringValue(performer.Name))
				}
			}
		}
		remote = append(remote, candidate)
	}
	scores := metadata.ScoreCandidates(local, remote)
	ordered := make([]remoteSceneMatch, 0, len(matches))
	for _, scored := range scores {
		match := matches[scored.Index]
		match.candidate.Score = scored.Score
		match.candidate.Decision = RemoteSceneCandidateDecision(scored.Decision)
		match.candidate.Contributions = make([]CandidateContribution, 0, len(scored.Contributions))
		for _, contribution := range scored.Contributions {
			match.candidate.Contributions = append(match.candidate.Contributions, CandidateContribution{
				Field: contribution.Field, Weight: contribution.Weight, Reason: contribution.Reason,
			})
		}
		ordered = append(ordered, match)
	}
	copy(matches, ordered)
}

func (j *analyzeSceneMetadataJob) localStudioName(studioID *int) string {
	if studioID == nil {
		return ""
	}
	for _, studio := range j.studioRecords {
		if studio.ID == *studioID {
			return studio.Name
		}
	}
	return ""
}

func (j *analyzeSceneMetadataJob) localPerformerNames(ids []int) map[string]struct{} {
	wanted := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	ret := make(map[string]struct{}, len(ids))
	for _, performer := range j.performerRecords {
		if _, ok := wanted[performer.ID]; ok {
			ret[metadata.NormalizeKey(performer.Name)] = struct{}{}
		}
	}
	return ret
}

func remoteSceneMatchIsSafe(matches []remoteSceneMatch) bool {
	return len(matches) > 0 && matches[0].candidate.Decision == RemoteCandidateDecisionAccept
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func float64Value(value *int) float64 {
	if value == nil {
		return 0
	}
	return float64(*value)
}

func sceneMetadataPolicyVersion(input AnalyzeSceneMetadataInput) string {
	payload := struct {
		PerformerScrapers  []string
		PerformerBoxes     []string
		StudioScrapers     []string
		StudioBoxes        []string
		PerformerThreshold float64
		DateThreshold      float64
		OverwriteDate      bool
		OverwriteTitle     bool
		UseDetails         bool
		ProviderPolicies   []ProviderPolicy
		ReplacePerformers  bool
	}{
		PerformerScrapers:  append([]string(nil), input.PerformerVerifierScraperIDs...),
		PerformerBoxes:     append([]string(nil), input.PerformerVerifierStashBoxEndpoints...),
		StudioScrapers:     append([]string(nil), input.StudioVerifierScraperIDs...),
		StudioBoxes:        append([]string(nil), input.StudioVerifierStashBoxEndpoints...),
		PerformerThreshold: thresholdValue(input.PerformerConfidenceThreshold, defaultPerformerConfidenceThreshold),
		DateThreshold:      thresholdValue(input.DateConfidenceThreshold, defaultDateConfidenceThreshold),
		OverwriteDate:      input.OverwriteExistingDate,
		OverwriteTitle:     input.OverwriteExistingTitle,
		UseDetails:         input.UseDetails,
		ProviderPolicies:   append([]ProviderPolicy(nil), input.ProviderPolicies...),
		ReplacePerformers:  input.ReplaceLocalPerformersFromRemote,
	}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func thresholdValue(value *float64, fallback float64) float64 {
	if value != nil {
		return *value
	}
	return fallback
}

func sourceExcerpts(sources []metadata.Source) []SourceExcerpt {
	result := make([]SourceExcerpt, 0, len(sources))
	for _, source := range sources {
		result = append(result, SourceExcerpt{
			Kind: string(source.Kind), Label: source.Label,
			RawText: source.RawText, Normalized: source.Normalized,
		})
	}
	return result
}

func candidateFromResolution(resolution performerResolution) PerformerCandidate {
	candidate := PerformerCandidate{
		Candidate: resolution.Candidate, Status: string(resolution.Status),
		PerformerID: resolution.PerformerID,
		MatchingIDs: append([]int(nil), resolution.MatchingIDs...),
		ScraperIDs:  append([]string(nil), resolution.ScraperIDs...),
		Reason:      resolution.Reason,
	}
	if resolution.Proposed != nil {
		candidate.ProposedName = resolution.Proposed.Name
	}
	return candidate
}

func staleSceneHash(
	scene *models.Scene,
	performerIDs []int,
	groups []models.GroupsScenes,
	stashIDs []models.StashID,
	primary *models.VideoFile,
) string {
	return staleSceneHashWithPrimary(
		scene, performerIDs, groups, stashIDs, newSceneMetadataFileUpdate(primary),
	)
}

func staleSceneHashWithPrimary(
	scene *models.Scene,
	performerIDs []int,
	groups []models.GroupsScenes,
	stashIDs []models.StashID,
	primary *sceneMetadataFileUpdate,
) string {
	ids := append([]int(nil), performerIDs...)
	sort.Ints(ids)
	sortedGroups := append([]models.GroupsScenes(nil), groups...)
	sort.Slice(sortedGroups, func(left, right int) bool {
		if sortedGroups[left].GroupID != sortedGroups[right].GroupID {
			return sortedGroups[left].GroupID < sortedGroups[right].GroupID
		}
		leftIndex, rightIndex := -1, -1
		if sortedGroups[left].SceneIndex != nil {
			leftIndex = *sortedGroups[left].SceneIndex
		}
		if sortedGroups[right].SceneIndex != nil {
			rightIndex = *sortedGroups[right].SceneIndex
		}
		return leftIndex < rightIndex
	})
	sortedStashIDs := append([]models.StashID(nil), stashIDs...)
	sort.Slice(sortedStashIDs, func(left, right int) bool {
		if sortedStashIDs[left].Endpoint != sortedStashIDs[right].Endpoint {
			return sortedStashIDs[left].Endpoint < sortedStashIDs[right].Endpoint
		}
		return sortedStashIDs[left].StashID < sortedStashIDs[right].StashID
	})
	date := ""
	if scene.Date != nil {
		date = scene.Date.String()
	}
	payload, _ := json.Marshal(struct {
		Title        string
		Date         string
		PerformerIDs []int
		StudioID     *int
		Organized    bool
		Groups       []models.GroupsScenes
		StashIDs     []models.StashID
		PrimaryFile  *sceneMetadataFileUpdate
	}{
		Title: scene.Title, Date: date, PerformerIDs: ids, StudioID: scene.StudioID,
		Organized: scene.Organized, Groups: sortedGroups, StashIDs: sortedStashIDs,
		PrimaryFile: primary,
	})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func actionPayload(value sceneMetadataActionPayload) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func (j *analyzeSceneMetadataJob) persistAnalysisPlan(ctx context.Context, plan *AnalysisPlan) error {
	if j.repository.SceneMetadataPlan == nil {
		return fmt.Errorf("scene metadata plan store is unavailable")
	}
	proposal, err := json.Marshal(plan)
	if err != nil {
		return fmt.Errorf("encode scene metadata plan: %w", err)
	}
	record := models.SceneMetadataPlanRecord{
		RunID: plan.RunID, SceneID: plan.SceneID, ProposalJSON: string(proposal),
		ModelFingerprint: plan.ModelFingerprint, PolicyVersion: plan.PolicyVersion,
		State: string(plan.State), CreatedAt: plan.CreatedAt, AppliedAt: plan.AppliedAt,
	}
	plan.Suggested = dedupAnalysisPlanSuggestions(plan.Suggested)
	actions := make([]models.SceneMetadataPlanActionRecord, 0, len(plan.Suggested))
	for _, suggestion := range plan.Suggested {
		reasons, _ := json.Marshal(suggestion.ReasonCodes)
		actions = append(actions, models.SceneMetadataPlanActionRecord{
			Kind: suggestion.Kind, PayloadJSON: suggestion.PayloadJSON,
			State: string(suggestion.State), ReasonCodes: string(reasons),
		})
	}
	return j.repository.WithTxn(ctx, func(ctx context.Context) error {
		return j.repository.SceneMetadataPlan.UpsertSceneMetadataPlan(ctx, &record, actions)
	})
}

func dedupAnalysisPlanSuggestions(suggestions []SuggestedField) []SuggestedField {
	seenNames := make(map[string]struct{}, len(suggestions))
	deduped := make([]SuggestedField, 0, len(suggestions))
	for _, suggestion := range suggestions {
		if suggestion.Kind == sceneMetadataActionCreatePerformer {
			var payload sceneMetadataActionPayload
			if err := json.Unmarshal([]byte(suggestion.PayloadJSON), &payload); err == nil &&
				payload.Performer != nil {
				key := metadata.NormalizeKey(payload.Performer.Name)
				if _, exists := seenNames[key]; exists {
					continue
				}
				seenNames[key] = struct{}{}
			}
		}
		deduped = append(deduped, suggestion)
	}
	return deduped
}
func (s *Manager) SceneMetadataPlans(
	ctx context.Context,
	sceneIDs []string,
	runID *string,
	state *SceneMetadataPlanState,
	latestOnly bool,
	limit *int,
) ([]*AnalysisPlan, error) {
	ids, err := stringslice.StringSliceToIntSlice(sceneIDs)
	if err != nil {
		return nil, fmt.Errorf("parse scene IDs: %w", err)
	}
	var stateFilter *string
	if state != nil {
		value := string(*state)
		stateFilter = &value
	}
	var (
		records []models.SceneMetadataPlanRecord
		actions []models.SceneMetadataPlanActionRecord
	)
	if err := s.Repository.WithReadTxn(ctx, func(ctx context.Context) error {
		var err error
		if latestOnly {
			records, err = s.Repository.SceneMetadataPlan.FindLatestSceneMetadataPlans(ctx, ids, runID, stateFilter)
		} else {
			records, err = s.Repository.SceneMetadataPlan.FindSceneMetadataPlans(ctx, ids, runID, stateFilter, limit)
		}
		if err != nil || len(records) == 0 {
			return err
		}
		runIDs := make([]string, 0, len(records))
		recordSceneIDs := make([]int, 0, len(records))
		seenRuns := make(map[string]struct{}, len(records))
		seenScenes := make(map[int]struct{}, len(records))
		for _, record := range records {
			if _, seen := seenRuns[record.RunID]; !seen {
				runIDs = append(runIDs, record.RunID)
				seenRuns[record.RunID] = struct{}{}
			}
			if _, seen := seenScenes[record.SceneID]; !seen {
				recordSceneIDs = append(recordSceneIDs, record.SceneID)
				seenScenes[record.SceneID] = struct{}{}
			}
		}
		actions, err = s.Repository.SceneMetadataPlan.FindSceneMetadataPlanActionsForRuns(
			ctx, runIDs, recordSceneIDs,
		)
		return err
	}); err != nil {
		return nil, err
	}
	type planKey struct {
		runID   string
		sceneID int
	}
	actionsByPlan := make(map[planKey][]models.SceneMetadataPlanActionRecord, len(records))
	for _, action := range actions {
		key := planKey{runID: action.RunID, sceneID: action.SceneID}
		actionsByPlan[key] = append(actionsByPlan[key], action)
	}
	result := make([]*AnalysisPlan, 0, len(records))
	for _, record := range records {
		var plan AnalysisPlan
		if err := json.Unmarshal([]byte(record.ProposalJSON), &plan); err != nil {
			return nil, fmt.Errorf("decode scene metadata plan %s/%d: %w", record.RunID, record.SceneID, err)
		}
		plan.State, plan.AppliedAt = SceneMetadataPlanState(record.State), record.AppliedAt
		plan.Suggested = nil
		for _, action := range actionsByPlan[planKey{runID: record.RunID, sceneID: record.SceneID}] {
			var reasons []string
			_ = json.Unmarshal([]byte(action.ReasonCodes), &reasons)
			plan.Suggested = append(plan.Suggested, SuggestedField{
				ActionID: sceneMetadataActionIdentifier(action.RunID, action.SceneID, action.ID),
				Kind:     action.Kind, PayloadJSON: action.PayloadJSON,
				State: SceneMetadataPlanState(action.State), ReasonCodes: reasons,
			})
		}
		result = append(result, &plan)
	}
	return result, nil
}

func sceneMetadataActionIdentifier(runID string, sceneID, actionID int) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d", runID, sceneID, actionID)))
	return fmt.Sprintf("%d:%s", actionID, hex.EncodeToString(digest[:]))
}
