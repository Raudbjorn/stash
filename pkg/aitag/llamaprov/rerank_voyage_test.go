package llamaprov

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/stashapp/stash/pkg/aitag/taxonomy"
)

func TestVoyageRerankerReordersCandidates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("Authorization = %q", got)
		}
		var request struct {
			Query     string   `json:"query"`
			Documents []string `json:"documents"`
			Model     string   `json:"model"`
			TopK      int      `json:"top_k"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Query != "couch blowjob" || request.Model != "rerank-2.5-lite" || request.TopK != 2 || len(request.Documents) != 2 {
			t.Errorf("request = %#v", request)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"index": 1, "relevance_score": 0.9},
				{"index": 0, "relevance_score": 0.3},
			},
		})
	}))
	defer server.Close()

	candidates := []taxonomy.Entry{
		{StashID: "couch", Canonical: "Couch"},
		{StashID: "blowjob", Canonical: "Blowjob"},
	}
	reranker := &VoyageReranker{APIKey: "secret", Model: "rerank-2.5-lite", Endpoint: server.URL, Client: server.Client()}
	got, err := reranker.Rerank(context.Background(), "couch blowjob", candidates, 2)
	if err != nil {
		t.Fatal(err)
	}
	if want := []taxonomy.Entry{candidates[1], candidates[0]}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reranked = %#v, want %#v", got, want)
	}
}

func TestVoyageRerankerReportsUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"detail":"unauthorized"}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	reranker := &VoyageReranker{APIKey: "bad", Endpoint: server.URL, Client: server.Client()}
	_, err := reranker.Rerank(context.Background(), "query", []taxonomy.Entry{{Canonical: "One"}}, 1)
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("error = %v, want HTTP 401", err)
	}
}

func TestVoyageRerankerNoKeyShortCircuits(t *testing.T) {
	reranker := &VoyageReranker{}
	_, err := reranker.Rerank(context.Background(), "query", []taxonomy.Entry{{Canonical: "One"}}, 1)
	if !errors.Is(err, ErrVoyageNoAPIKey) {
		t.Fatalf("error = %v, want ErrVoyageNoAPIKey", err)
	}
}
