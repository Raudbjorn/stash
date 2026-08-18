package taxonomy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestCacheSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "taxonomy.json")
	want := Cache{
		Path: path, Endpoint: "https://stashdb.org/graphql",
		UpdatedAt: time.Date(2026, 8, 13, 1, 2, 3, 0, time.UTC),
		Entries: map[string]Entry{
			"tag-1": {StashID: "tag-1", Canonical: "Dildo Play", Category: "Acts", Aliases: []string{"Dildo"}},
		},
	}
	if err := want.Save(path); err != nil {
		t.Fatal(err)
	}
	var got Cache
	if err := got.Load(path); err != nil {
		t.Fatal(err)
	}
	entry := got.Entries["tag-1"]
	if got.Endpoint != want.Endpoint || !got.UpdatedAt.Equal(want.UpdatedAt) || entry.Canonical != "Dildo Play" || len(entry.Aliases) != 1 {
		t.Fatalf("loaded cache = %#v", got)
	}
}

func TestCacheFetchPaginatesAndReplacesFile(t *testing.T) {
	var mu sync.Mutex
	fetch := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Variables struct {
				Input struct {
					Page int `json:"page"`
				} `json:"input"`
			} `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		mu.Lock()
		defer mu.Unlock()
		page := request.Variables.Input.Page
		var count int
		var tags []map[string]any
		if fetch == 0 {
			count = 2
			if page == 1 {
				tags = []map[string]any{{"id": "one", "name": "Dildo", "aliases": []string{}, "category": map[string]any{"id": "a", "name": "Accessories"}}}
			} else {
				tags = []map[string]any{{"id": "two", "name": "Handjob", "aliases": []string{"Hand Job"}, "category": map[string]any{"id": "b", "name": "Acts"}}}
				fetch = 1
			}
		} else {
			count = 1
			tags = []map[string]any{{"id": "three", "name": "Nudity", "aliases": []string{}, "category": map[string]any{"id": "c", "name": "Themes"}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"queryTags": map[string]any{"count": count, "tags": tags}}})
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "taxonomy.json")
	cache := Cache{Path: path}
	if err := cache.Fetch(context.Background(), server.URL, "key"); err != nil {
		t.Fatal(err)
	}
	if len(cache.Entries) != 2 || cache.Entries["two"].Canonical != "Handjob" {
		t.Fatalf("first fetch = %#v", cache.Entries)
	}
	firstFile, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Fetch(context.Background(), server.URL, "key"); err != nil {
		t.Fatal(err)
	}
	if len(cache.Entries) != 1 || cache.Entries["three"].Canonical != "Nudity" {
		t.Fatalf("second fetch = %#v", cache.Entries)
	}
	secondFile, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstFile) == string(secondFile) {
		t.Fatal("second fetch did not replace the cache file")
	}
}

func TestClientUsesStaleCacheWhenRefreshFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := &Client{
		Endpoint: server.URL,
		Cache: &Cache{
			Endpoint:  server.URL,
			UpdatedAt: time.Now().Add(-7 * 24 * time.Hour),
			Entries: map[string]Entry{
				"tag-1": {
					StashID:   "tag-1",
					Canonical: "Dildo Play",
					Category:  "Acts",
				},
			},
		},
	}
	candidates, err := client.CandidatesByCategory(context.Background(), []string{"Acts"})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates["Acts"]) != 1 || candidates["Acts"][0].StashID != "tag-1" {
		t.Fatalf("candidates = %#v", candidates)
	}
}
