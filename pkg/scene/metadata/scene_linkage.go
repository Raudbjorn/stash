package metadata

import (
	"math"
	"sort"
	"strings"
	"time"
)

const (
	SceneCandidateAccept = "accept"
	SceneCandidateReview = "review"
	SceneCandidateReject = "reject"
)

type SceneKey struct {
	Provider    string
	StudioID    string
	Title       string
	ReleaseDate *time.Time
	Duration    float64
}

type RemoteCandidate struct {
	Scene                 SceneKey
	Provider              string
	NormalizedStudioID    string
	NormalizedTitleTokens []string
	ReleaseDateFrom       *time.Time
	ReleaseDateTo         *time.Time
	DurationFrom          float64
	DurationTo            float64
}

type SceneMeta struct {
	ProviderSceneID string
	Fingerprint     string
	StudioID        string
	ReleaseDate     *time.Time
	Duration        float64
	DurationExact   bool
	PerformerIDs    []string
}

type RemoteSceneMeta = SceneMeta

type SceneCandidateContribution struct {
	Field  string
	Weight float64
	Reason string
}

type SceneCandidateScore struct {
	Score         float64
	Decision      string
	Contributions []SceneCandidateContribution
}

type ScoredRemoteScene struct {
	Index int
	SceneCandidateScore
}

func BlockScenes(scenes []SceneKey) []RemoteCandidate {
	ret := make([]RemoteCandidate, 0, len(scenes))
	for _, scene := range scenes {
		candidate := RemoteCandidate{
			Scene:                 scene,
			Provider:              strings.TrimSpace(strings.ToLower(scene.Provider)),
			NormalizedStudioID:    NormalizeKey(scene.StudioID),
			NormalizedTitleTokens: normalizedUniqueTokens(scene.Title),
		}
		if scene.ReleaseDate != nil {
			from := scene.ReleaseDate.AddDate(0, 0, -30)
			to := scene.ReleaseDate.AddDate(0, 0, 30)
			candidate.ReleaseDateFrom = &from
			candidate.ReleaseDateTo = &to
		}
		if scene.Duration > 0 {
			candidate.DurationFrom = scene.Duration * 0.9
			candidate.DurationTo = scene.Duration * 1.1
		}
		ret = append(ret, candidate)
	}
	return ret
}

func ScoreCandidate(local SceneMeta, remote RemoteSceneMeta) SceneCandidateScore {
	ret := SceneCandidateScore{}
	contradiction := func(field, reason string) SceneCandidateScore {
		ret.Score = math.Inf(-1)
		ret.Decision = SceneCandidateReject
		ret.Contributions = append(ret.Contributions, SceneCandidateContribution{
			Field: field, Weight: math.Inf(-1), Reason: reason,
		})
		return ret
	}
	if local.StudioID != "" && remote.StudioID != "" && NormalizeKey(local.StudioID) != NormalizeKey(remote.StudioID) {
		return contradiction("studio_id", "incompatible studio IDs")
	}
	if local.Duration > 0 && remote.Duration > 0 && (local.DurationExact || remote.DurationExact) {
		delta := math.Abs(local.Duration-remote.Duration) / math.Max(local.Duration, remote.Duration)
		if delta > 0.1 {
			return contradiction("duration", "exact duration differs by more than ten percent")
		}
	}
	add := func(field string, weight float64, reason string) {
		ret.Score += weight
		ret.Contributions = append(ret.Contributions, SceneCandidateContribution{Field: field, Weight: weight, Reason: reason})
	}
	if local.ProviderSceneID != "" && local.ProviderSceneID == remote.ProviderSceneID {
		add("provider_scene_id", 1, "exact provider scene ID")
	}
	if local.Fingerprint != "" && local.Fingerprint == remote.Fingerprint {
		add("fingerprint", 1, "exact fingerprint")
	}
	if local.StudioID != "" && remote.StudioID != "" && NormalizeKey(local.StudioID) == NormalizeKey(remote.StudioID) {
		add("studio_id", 0.5, "exact normalized studio ID")
	}
	if local.ReleaseDate != nil && remote.ReleaseDate != nil {
		delta := local.ReleaseDate.Sub(*remote.ReleaseDate)
		if delta < 0 {
			delta = -delta
		}
		if delta <= 30*24*time.Hour {
			add("release_date", 0.3, "release date within thirty days")
		}
	}
	if local.Duration > 0 && remote.Duration > 0 {
		delta := math.Abs(local.Duration-remote.Duration) / math.Max(local.Duration, remote.Duration)
		if delta <= 0.1 {
			add("duration", 0.2, "duration within ten percent")
		}
	}
	if performerJaccard(local.PerformerIDs, remote.PerformerIDs) >= 0.5 {
		add("performers", 0.5, "performer Jaccard at least one half")
	}
	switch {
	case ret.Score >= 2:
		ret.Decision = SceneCandidateAccept
	case ret.Score >= 1:
		ret.Decision = SceneCandidateReview
	default:
		ret.Decision = SceneCandidateReject
	}
	return ret
}

func ScoreCandidates(local SceneMeta, remote []RemoteSceneMeta) []ScoredRemoteScene {
	ret := make([]ScoredRemoteScene, 0, len(remote))
	for idx, candidate := range remote {
		ret = append(ret, ScoredRemoteScene{Index: idx, SceneCandidateScore: ScoreCandidate(local, candidate)})
	}
	sort.SliceStable(ret, func(left, right int) bool { return ret[left].Score > ret[right].Score })
	for idx := 1; idx < len(ret); idx++ {
		if ret[idx].Decision == SceneCandidateAccept {
			ret[idx].Decision = SceneCandidateReview
		}
	}
	if len(ret) > 0 && ret[0].Decision == SceneCandidateAccept && len(ret) > 1 && ret[0].Score-ret[1].Score < 0.3 {
		ret[0].Decision = SceneCandidateReview
	}
	return ret
}

func normalizedUniqueTokens(value string) []string {
	seen := make(map[string]struct{})
	for _, token := range strings.Fields(NormalizeKey(value)) {
		seen[token] = struct{}{}
	}
	ret := make([]string, 0, len(seen))
	for token := range seen {
		ret = append(ret, token)
	}
	sort.Strings(ret)
	return ret
}

func performerJaccard(left, right []string) float64 {
	leftSet := make(map[string]struct{}, len(left))
	rightSet := make(map[string]struct{}, len(right))
	for _, value := range left {
		if key := NormalizeKey(value); key != "" {
			leftSet[key] = struct{}{}
		}
	}
	for _, value := range right {
		if key := NormalizeKey(value); key != "" {
			rightSet[key] = struct{}{}
		}
	}
	if len(leftSet) == 0 && len(rightSet) == 0 {
		return 0
	}
	intersection := 0
	for value := range leftSet {
		if _, ok := rightSet[value]; ok {
			intersection++
		}
	}
	union := len(leftSet) + len(rightSet) - intersection
	return float64(intersection) / float64(union)
}
