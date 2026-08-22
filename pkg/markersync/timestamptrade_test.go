package markersync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTTStub returns an httptest server implementing the two-step
// get-markers -> json-scene protocol. getMarkers maps a stash id to a
// timestamp.trade scene id (empty/absent => 404). scenes maps a scene id to a
// json-scene response body.
func newTTStub(t *testing.T, getMarkers map[string]string, scenes map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/get-markers/"):
			stashID := strings.TrimPrefix(r.URL.Path, "/get-markers/")
			sceneID, ok := getMarkers[stashID]
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"scene_id": ` + sceneID + `}`))
		case strings.HasPrefix(r.URL.Path, "/json-scene/"):
			sceneID := strings.TrimPrefix(r.URL.Path, "/json-scene/")
			body, ok := scenes[sceneID]
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(body))
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
}

// TestTimestampTradeSource_FetchMarkers_PropagatesServerError asserts that a
// genuine failure (a 5xx) is surfaced instead of being silently treated as
// "scene has no markers" - the fix for swallowing lookup errors.
func TestTimestampTradeSource_FetchMarkers_PropagatesServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	s := NewTimestampTradeSource(TimestampTradeOptions{Enabled: true})
	s.baseURL = srv.URL

	id := SceneIdentity{StashIDs: []StashID{{Endpoint: "e", StashID: "stash-abc"}}}

	if _, err := s.FetchMarkers(context.Background(), id); err == nil {
		t.Fatal("FetchMarkers: expected an error on HTTP 500, got nil (a server error must not be reported as 'no markers')")
	}
}

func TestTimestampTradeSource_FetchMarkers_MillisecondsToSeconds(t *testing.T) {
	srv := newTTStub(t,
		map[string]string{"stash-abc": "555"},
		map[string]string{"555": `{"markers": [
			{"name": "Intro", "tag_name": "Talking", "start_time": 90000},
			{"name": "Action", "tag_name": "", "start_time": 123456}
		]}`},
	)
	defer srv.Close()

	s := NewTimestampTradeSource(TimestampTradeOptions{Enabled: true})
	s.baseURL = srv.URL

	id := SceneIdentity{StashIDs: []StashID{
		{Endpoint: "https://ignored/graphql", StashID: "stash-abc"},
	}}

	markers, err := s.FetchMarkers(context.Background(), id)
	if err != nil {
		t.Fatalf("FetchMarkers: %v", err)
	}
	if len(markers) != 2 {
		t.Fatalf("got %d markers, want 2", len(markers))
	}

	// CRITICAL: 90000 ms must become 90.0 seconds.
	if markers[0].Seconds != 90.0 {
		t.Errorf("markers[0].Seconds = %v, want 90.0 (ms->sec conversion)", markers[0].Seconds)
	}
	if markers[0].Title != "Intro" || markers[0].PrimaryTag != "Talking" {
		t.Errorf("markers[0] title/tag = %q/%q, want Intro/Talking", markers[0].Title, markers[0].PrimaryTag)
	}
	// 123456 ms -> 123.456 seconds.
	if markers[1].Seconds != 123.456 {
		t.Errorf("markers[1].Seconds = %v, want 123.456", markers[1].Seconds)
	}
	// tag_name empty => PrimaryTag falls back to name.
	if markers[1].PrimaryTag != "Action" {
		t.Errorf("markers[1].PrimaryTag = %q, want Action (fallback to name)", markers[1].PrimaryTag)
	}
}

// TestTimestampTradeSource_FetchMarkers_EndTime asserts that an optional wire
// end_time (milliseconds) surfaces as EndSeconds in seconds, and that a marker
// without end_time leaves EndSeconds nil.
func TestTimestampTradeSource_FetchMarkers_EndTime(t *testing.T) {
	srv := newTTStub(t,
		map[string]string{"stash-abc": "555"},
		map[string]string{"555": `{"markers": [
			{"name": "Action", "tag_name": "T", "start_time": 90000, "end_time": 123456},
			{"name": "NoEnd", "tag_name": "T", "start_time": 5000}
		]}`},
	)
	defer srv.Close()

	s := NewTimestampTradeSource(TimestampTradeOptions{Enabled: true})
	s.baseURL = srv.URL

	id := SceneIdentity{StashIDs: []StashID{
		{Endpoint: "https://ignored/graphql", StashID: "stash-abc"},
	}}

	markers, err := s.FetchMarkers(context.Background(), id)
	if err != nil {
		t.Fatalf("FetchMarkers: %v", err)
	}
	if len(markers) != 2 {
		t.Fatalf("got %d markers, want 2", len(markers))
	}

	// end_time 123456 ms -> 123.456 seconds.
	if markers[0].EndSeconds == nil {
		t.Fatalf("markers[0].EndSeconds = nil, want 123.456 (ms->sec)")
	}
	if *markers[0].EndSeconds != 123.456 {
		t.Errorf("markers[0].EndSeconds = %v, want 123.456", *markers[0].EndSeconds)
	}
	// no end_time -> EndSeconds nil.
	if markers[1].EndSeconds != nil {
		t.Errorf("markers[1].EndSeconds = %v, want nil (no end_time on wire)", *markers[1].EndSeconds)
	}
}

func TestTimestampTradeSource_FetchMarkers_SecondStashIDWins(t *testing.T) {
	// Only the second stash id resolves via get-markers.
	srv := newTTStub(t,
		map[string]string{"second": "777"},
		map[string]string{"777": `{"markers": [{"name": "X", "tag_name": "T", "start_time": 5000}]}`},
	)
	defer srv.Close()

	s := NewTimestampTradeSource(TimestampTradeOptions{Enabled: true})
	s.baseURL = srv.URL

	id := SceneIdentity{StashIDs: []StashID{
		{Endpoint: "https://a/graphql", StashID: "first"},
		{Endpoint: "https://b/graphql", StashID: "second"},
	}}

	markers, err := s.FetchMarkers(context.Background(), id)
	if err != nil {
		t.Fatalf("FetchMarkers: %v", err)
	}
	if len(markers) != 1 || markers[0].Seconds != 5.0 {
		t.Fatalf("got %+v, want single marker at 5.0s", markers)
	}
}

func TestTimestampTradeSource_FetchMarkers_NoMatch(t *testing.T) {
	// No stash id resolves.
	srv := newTTStub(t, map[string]string{}, map[string]string{})
	defer srv.Close()

	s := NewTimestampTradeSource(TimestampTradeOptions{Enabled: true})
	s.baseURL = srv.URL

	id := SceneIdentity{StashIDs: []StashID{
		{Endpoint: "https://a/graphql", StashID: "unknown-1"},
		{Endpoint: "https://b/graphql", StashID: "unknown-2"},
	}}

	markers, err := s.FetchMarkers(context.Background(), id)
	if err != nil {
		t.Fatalf("FetchMarkers: %v", err)
	}
	if markers != nil {
		t.Errorf("expected nil markers when no stash id matches, got %v", markers)
	}
}

// TestTimestampTradeSource_FetchExtras exercises the Stage 5 Part A providers:
// a single /json-scene body carrying urls, galleries and movies is decoded and
// surfaced by FetchExtraURLs, FetchGalleries and FetchGroups.
func TestTimestampTradeSource_FetchExtras(t *testing.T) {
	// data["scene_id"] is 555; the movie's scenes[] carries an entry for 555 at
	// index 3 (and a decoy for a different scene) to prove scene-index matching.
	body := `{
		"scene_id": 555,
		"markers": [],
		"urls": ["https://example.com/a", "https://example.com/b"],
		"galleries": [
			{
				"files": [{"md5": "aaa111"}, {"md5": "bbb222"}],
				"urls": [{"url": "https://gallery.example/g1"}]
			}
		],
		"movies": [
			{
				"id": 42,
				"title": "The Collection",
				"description": "a synopsis",
				"release_date": "2021-05-06",
				"urls": [{"url": "https://studio.example/collection"}],
				"scenes": [
					{"scene_id": 999, "scene_index": 1},
					{"scene_id": 555, "scene_index": 3}
				]
			}
		]
	}`

	srv := newTTStub(t,
		map[string]string{"stash-abc": "555"},
		map[string]string{"555": body},
	)
	defer srv.Close()

	s := NewTimestampTradeSource(TimestampTradeOptions{Enabled: true})
	s.baseURL = srv.URL

	id := SceneIdentity{StashIDs: []StashID{
		{Endpoint: "https://ignored/graphql", StashID: "stash-abc"},
	}}
	ctx := context.Background()

	// --- extra urls ---
	urls, err := s.FetchExtraURLs(ctx, id)
	if err != nil {
		t.Fatalf("FetchExtraURLs: %v", err)
	}
	if len(urls) != 2 || urls[0] != "https://example.com/a" || urls[1] != "https://example.com/b" {
		t.Errorf("FetchExtraURLs = %v, want [a b]", urls)
	}

	// --- galleries ---
	gals, err := s.FetchGalleries(ctx, id)
	if err != nil {
		t.Fatalf("FetchGalleries: %v", err)
	}
	if len(gals) != 1 {
		t.Fatalf("got %d gallery refs, want 1", len(gals))
	}
	if len(gals[0].MD5s) != 2 || gals[0].MD5s[0] != "aaa111" || gals[0].MD5s[1] != "bbb222" {
		t.Errorf("gallery md5s = %v, want [aaa111 bbb222]", gals[0].MD5s)
	}
	if len(gals[0].URLs) != 1 || gals[0].URLs[0] != "https://gallery.example/g1" {
		t.Errorf("gallery urls = %v, want [https://gallery.example/g1]", gals[0].URLs)
	}

	// --- groups ---
	groups, err := s.FetchGroups(ctx, id)
	if err != nil {
		t.Fatalf("FetchGroups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("got %d group refs, want 1", len(groups))
	}
	g := groups[0]
	if g.ExternalID != "42" || g.Name != "The Collection" || g.Synopsis != "a synopsis" || g.Date != "2021-05-06" {
		t.Errorf("group = %+v, want id 42 / The Collection / a synopsis / 2021-05-06", g)
	}
	// scene index resolved from the scenes[] entry whose scene_id == 555.
	if g.SceneIndex == nil || *g.SceneIndex != 3 {
		t.Errorf("group SceneIndex = %v, want 3", g.SceneIndex)
	}
	// canonical movie URL appended (production host, regardless of baseURL).
	wantMovieURL := "https://timestamp.trade/movie/42"
	if len(g.URLs) != 2 || g.URLs[0] != "https://studio.example/collection" || g.URLs[1] != wantMovieURL {
		t.Errorf("group URLs = %v, want [studio url, %s]", g.URLs, wantMovieURL)
	}
}

// TestTimestampTradeSource_FetchSceneData_Memoised asserts the single-entry memo
// collapses the repeated resolve+scene fetches performed by FetchMarkers and the
// three remaining extras providers into ONE resolve GET and ONE scene GET for
// the same scene identity.
func TestTimestampTradeSource_FetchSceneData_Memoised(t *testing.T) {
	var resolveCalls, sceneCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/get-markers/"):
			resolveCalls++
			_, _ = w.Write([]byte(`{"scene_id": 555}`))
		case strings.HasPrefix(r.URL.Path, "/json-scene/"):
			sceneCalls++
			_, _ = w.Write([]byte(`{
				"scene_id": 555,
				"markers": [{"name": "M", "tag_name": "T", "start_time": 1000}],
				"urls": ["https://example.com/a"],
				"galleries": [{"files": [{"md5": "aaa"}], "urls": []}],
				"movies": [{"id": 42, "title": "G", "scenes": [{"scene_id": 555, "scene_index": 1}]}]
			}`))
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	s := NewTimestampTradeSource(TimestampTradeOptions{Enabled: true})
	s.baseURL = srv.URL

	id := SceneIdentity{StashIDs: []StashID{
		{Endpoint: "https://a/graphql", StashID: "stash-abc"},
	}}
	ctx := context.Background()

	if _, err := s.FetchMarkers(ctx, id); err != nil {
		t.Fatalf("FetchMarkers: %v", err)
	}
	if _, err := s.FetchGroups(ctx, id); err != nil {
		t.Fatalf("FetchGroups: %v", err)
	}
	if _, err := s.FetchGalleries(ctx, id); err != nil {
		t.Fatalf("FetchGalleries: %v", err)
	}
	if _, err := s.FetchExtraURLs(ctx, id); err != nil {
		t.Fatalf("FetchExtraURLs: %v", err)
	}

	if resolveCalls != 1 {
		t.Errorf("resolve GETs = %d, want 1 (memoised across 4 providers)", resolveCalls)
	}
	if sceneCalls != 1 {
		t.Errorf("scene GETs = %d, want 1 (memoised across 4 providers)", sceneCalls)
	}
}

// TestTimestampTradeSource_SubmitScene is the regression guard for the "unit
// trap": /submit-stash consumes marker times in SECONDS (stash-native) and the
// adapter must NOT multiply by 1000. It also asserts the request shape matches
// the plugin's fragment (endpoint, method, stash_ids, files.fingerprints).
func TestTimestampTradeSource_SubmitScene(t *testing.T) {
	var (
		gotPath   string
		gotMethod string
		body      ttWireScene
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		// timestamp.trade returns a non-JSON/empty body on success in practice.
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	s := NewTimestampTradeSource(TimestampTradeOptions{Enabled: true})
	s.baseURL = srv.URL

	scene := SceneSubmission{
		Title:   "My Scene",
		Details: "details here",
		Date:    "2024-01-02",
		URLs:    []string{"https://example/scene"},
		StashIDs: []StashIDRef{
			{Endpoint: "https://a/graphql", StashID: "s1"},
		},
		Performers: []NamedEntity{
			{Name: "Perf One", StashIDs: []StashIDRef{{Endpoint: "https://a/graphql", StashID: "p1"}}},
		},
		Tags:   []string{"Amateur"},
		Studio: &NamedEntity{Name: "Studio X", StashIDs: []StashIDRef{{Endpoint: "https://a/graphql", StashID: "st1"}}},
		Markers: []SubmissionMarker{
			{Title: "Intro", Seconds: 90.0, PrimaryTag: "Talking"},
		},
		Files: []SubmissionFile{
			{
				Basename: "scene.mp4",
				Duration: 1234.5,
				Size:     987654,
				Fingerprints: []Fingerprint{
					{Type: "oshash", Value: "abcdef"},
					{Type: "phash", Value: "0f0f0f0f"},
				},
			},
		},
	}

	if err := s.SubmitScene(context.Background(), scene); err != nil {
		t.Fatalf("SubmitScene: %v", err)
	}

	if gotPath != "/submit-stash" {
		t.Errorf("path = %q, want /submit-stash", gotPath)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}

	if body.Title != "My Scene" {
		t.Errorf("title = %q, want My Scene", body.Title)
	}

	if len(body.SceneMarkers) != 1 {
		t.Fatalf("got %d markers, want 1", len(body.SceneMarkers))
	}
	// CRITICAL: seconds preserved, NOT converted to 90000 ms.
	if body.SceneMarkers[0].Seconds != 90.0 {
		t.Errorf("scene_markers[0].seconds = %v, want 90.0 (seconds preserved, NOT 90000)", body.SceneMarkers[0].Seconds)
	}
	if body.SceneMarkers[0].PrimaryTag.Name != "Talking" {
		t.Errorf("scene_markers[0].primary_tag.name = %q, want Talking", body.SceneMarkers[0].PrimaryTag.Name)
	}

	if len(body.StashIDs) != 1 || body.StashIDs[0].StashID != "s1" || body.StashIDs[0].Endpoint != "https://a/graphql" {
		t.Errorf("stash_ids = %+v, want [{https://a/graphql s1}]", body.StashIDs)
	}

	if len(body.Files) != 1 {
		t.Fatalf("got %d files, want 1", len(body.Files))
	}
	f := body.Files[0]
	if f.Basename != "scene.mp4" || f.Size != 987654 || f.Duration != 1234.5 {
		t.Errorf("file = %+v, want basename scene.mp4 size 987654 duration 1234.5", f)
	}
	if len(f.Fingerprints) != 2 || f.Fingerprints[0].Type != "oshash" || f.Fingerprints[0].Value != "abcdef" {
		t.Errorf("files[0].fingerprints = %+v, want oshash/abcdef first", f.Fingerprints)
	}

	if body.Studio == nil || body.Studio.Name != "Studio X" {
		t.Errorf("studio = %+v, want name Studio X", body.Studio)
	}
	if len(body.Performers) != 1 || body.Performers[0].Name != "Perf One" {
		t.Errorf("performers = %+v, want [Perf One]", body.Performers)
	}
}
