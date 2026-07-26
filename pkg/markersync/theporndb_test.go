package markersync

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestThePornDBSource_FetchMarkers(t *testing.T) {
	const apiKey = "secret-key"

	var gotAuth string
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data": {"markers": [
			{"title": "Blowjob", "start_time": 90},
			{"title": "Doggy", "start_time": 123.5}
		]}}`))
	}))
	defer srv.Close()

	s := NewThePornDBSource(ThePornDBOptions{APIKey: apiKey, Enabled: true})
	s.baseURL = srv.URL // exercise the real FetchMarkers against the stub

	id := SceneIdentity{StashIDs: []StashID{
		{Endpoint: "https://other.example/graphql", StashID: "nope"},
		{Endpoint: ThePornDBEndpoint, StashID: "abc-123"},
	}}

	markers, err := s.FetchMarkers(context.Background(), id)
	if err != nil {
		t.Fatalf("FetchMarkers: %v", err)
	}

	if want := "Bearer " + apiKey; gotAuth != want {
		t.Errorf("Authorization header = %q, want %q", gotAuth, want)
	}
	if want := "/scenes/abc-123"; gotPath != want {
		t.Errorf("request path = %q, want %q", gotPath, want)
	}

	if len(markers) != 2 {
		t.Fatalf("got %d markers, want 2", len(markers))
	}
	// ThePornDB times are in seconds and pass through unchanged.
	if markers[0].Seconds != 90.0 {
		t.Errorf("markers[0].Seconds = %v, want 90.0", markers[0].Seconds)
	}
	if markers[0].Title != "Blowjob" || markers[0].PrimaryTag != "Blowjob" {
		t.Errorf("markers[0] title/tag = %q/%q", markers[0].Title, markers[0].PrimaryTag)
	}
	if markers[1].Seconds != 123.5 {
		t.Errorf("markers[1].Seconds = %v, want 123.5", markers[1].Seconds)
	}
}

func TestThePornDBSource_MarkerTagAndPrefix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data": {"markers": [{"title": "Kiss", "start_time": 10}]}}`))
	}))
	defer srv.Close()

	s := NewThePornDBSource(ThePornDBOptions{
		APIKey:      "k",
		Enabled:     true,
		MarkerTag:   "TPDB",
		TitlePrefix: "[tpdb] ",
	})
	s.baseURL = srv.URL

	id := SceneIdentity{StashIDs: []StashID{{Endpoint: ThePornDBEndpoint, StashID: "x"}}}
	markers, err := s.FetchMarkers(context.Background(), id)
	if err != nil {
		t.Fatalf("FetchMarkers: %v", err)
	}
	if len(markers) != 1 {
		t.Fatalf("got %d markers, want 1", len(markers))
	}
	if markers[0].Title != "[tpdb] Kiss" {
		t.Errorf("Title = %q, want %q", markers[0].Title, "[tpdb] Kiss")
	}
	if markers[0].PrimaryTag != "Kiss" {
		t.Errorf("PrimaryTag = %q, want %q", markers[0].PrimaryTag, "Kiss")
	}
	if len(markers[0].ExtraTags) != 1 || markers[0].ExtraTags[0] != "TPDB" {
		t.Errorf("ExtraTags = %v, want [TPDB]", markers[0].ExtraTags)
	}
}

func TestThePornDBSource_NoMatchingStashID(t *testing.T) {
	s := NewThePornDBSource(ThePornDBOptions{APIKey: "k", Enabled: true})
	id := SceneIdentity{StashIDs: []StashID{
		{Endpoint: "https://other.example/graphql", StashID: "x"},
	}}

	markers, err := s.FetchMarkers(context.Background(), id)
	if err != nil {
		t.Fatalf("FetchMarkers: %v", err)
	}
	if markers != nil {
		t.Errorf("expected nil markers when no ThePornDB stash id, got %v", markers)
	}
}

func TestThePornDBSource_Enabled(t *testing.T) {
	tests := []struct {
		name       string
		apiKey     string
		enabled    bool
		wantEnable bool
		wantReason string
	}{
		{"enabled with key", "k", true, true, ""},
		{"empty key", "", true, false, "TPDB skipped: no API key"},
		{"disabled", "k", false, false, "TPDB skipped: disabled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewThePornDBSource(ThePornDBOptions{APIKey: tt.apiKey, Enabled: tt.enabled})
			if got := s.Enabled(); got != tt.wantEnable {
				t.Errorf("Enabled() = %v, want %v", got, tt.wantEnable)
			}
			if got := s.DisabledReason(); got != tt.wantReason {
				t.Errorf("DisabledReason() = %q, want %q", got, tt.wantReason)
			}
		})
	}
}
