package metadata

import (
	"math"
	"testing"
	"time"
)

func TestBlockScenesNormalizesCandidateWindows(t *testing.T) {
	date := time.Date(2025, 4, 3, 0, 0, 0, 0, time.UTC)
	blocked := BlockScenes([]SceneKey{{
		Provider: " HTTPS://BOX.EXAMPLE/GRAPHQL ", StudioID: " Studio_One ",
		Title: "Feature.Title Feature", ReleaseDate: &date, Duration: 100,
	}})
	if len(blocked) != 1 {
		t.Fatalf("blocked candidates = %d", len(blocked))
	}
	got := blocked[0]
	if got.Provider != "https://box.example/graphql" || got.NormalizedStudioID != "studio one" {
		t.Fatalf("normalized block = %+v", got)
	}
	if len(got.NormalizedTitleTokens) != 2 || math.Abs(got.DurationFrom-90) > 1e-9 || math.Abs(got.DurationTo-110) > 1e-9 {
		t.Fatalf("title/duration block = %+v", got)
	}
	if got.ReleaseDateFrom == nil || got.ReleaseDateTo == nil || got.ReleaseDateFrom.AddDate(0, 0, 30) != date || got.ReleaseDateTo.AddDate(0, 0, -30) != date {
		t.Fatalf("date block = %+v", got)
	}
}

func TestScoreCandidatesClassifiesFiftyCandidateFixture(t *testing.T) {
	date := time.Date(2025, 4, 3, 0, 0, 0, 0, time.UTC)
	local := SceneMeta{
		ProviderSceneID: "scene-id", Fingerprint: "fingerprint", StudioID: "studio-id",
		ReleaseDate: &date, Duration: 100, DurationExact: true,
		PerformerIDs: []string{"one", "two"},
	}
	candidates := make([]RemoteSceneMeta, 0, 50)
	candidates = append(candidates, local)
	for range 3 {
		candidates = append(candidates, RemoteSceneMeta{
			ProviderSceneID: "different", Fingerprint: "fingerprint", StudioID: "studio-id",
			Duration: 100, PerformerIDs: []string{"one", "two"},
		})
	}
	candidates = append(candidates,
		RemoteSceneMeta{Fingerprint: "fingerprint", StudioID: "other-studio", Duration: 100},
		RemoteSceneMeta{Fingerprint: "fingerprint", StudioID: "studio-id", Duration: 200, DurationExact: true},
		RemoteSceneMeta{ProviderSceneID: "scene-id", StudioID: "other-studio", Duration: 100},
	)
	for range 43 {
		candidates = append(candidates, RemoteSceneMeta{Duration: 0})
	}
	if len(candidates) != 50 {
		t.Fatalf("fixture size = %d", len(candidates))
	}

	scores := ScoreCandidates(local, candidates)
	counts := map[string]int{}
	for _, score := range scores {
		counts[score.Decision]++
	}
	if counts[SceneCandidateAccept] != 1 || counts[SceneCandidateReview] != 3 || counts[SceneCandidateReject] != 46 {
		t.Fatalf("decision counts = %+v", counts)
	}
	if scores[0].Index != 0 || scores[0].Score != 3.5 || scores[0].Decision != SceneCandidateAccept {
		t.Fatalf("clear winner = %+v", scores[0])
	}
	for _, index := range []int{4, 5, 6} {
		score := ScoreCandidate(local, candidates[index])
		if !math.IsInf(score.Score, -1) || score.Decision != SceneCandidateReject {
			t.Fatalf("contradiction %d = %+v", index, score)
		}
	}
}

func TestScoreCandidatesCloseTopTwoRequireReview(t *testing.T) {
	local := SceneMeta{Fingerprint: "same", StudioID: "studio", Duration: 100, PerformerIDs: []string{"one"}}
	remotes := []RemoteSceneMeta{
		{Fingerprint: "same", StudioID: "studio", Duration: 100, PerformerIDs: []string{"one"}},
		{Fingerprint: "same", StudioID: "studio", Duration: 100, PerformerIDs: []string{"one"}},
	}
	scores := ScoreCandidates(local, remotes)
	if scores[0].Score < 2 || scores[0].Decision != SceneCandidateReview {
		t.Fatalf("close top score = %+v", scores[0])
	}
}

func BenchmarkScoreCandidate(b *testing.B) {
	date := time.Date(2025, 4, 3, 0, 0, 0, 0, time.UTC)
	local := SceneMeta{
		ProviderSceneID: "scene-id", Fingerprint: "fingerprint", StudioID: "studio-id",
		ReleaseDate: &date, Duration: 100, DurationExact: true,
		PerformerIDs: []string{"one", "two", "three", "four"},
	}
	remote := RemoteSceneMeta{
		ProviderSceneID: "scene-id", Fingerprint: "fingerprint", StudioID: "studio-id",
		ReleaseDate: &date, Duration: 100, DurationExact: true,
		PerformerIDs: []string{"one", "two", "three", "four"},
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = ScoreCandidate(local, remote)
	}
}
