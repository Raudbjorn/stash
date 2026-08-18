package tagging

import (
	"testing"

	"github.com/stashapp/stash/pkg/aitag/llamaprov"
)

func TestNewVoyageRerankerRequiresKeyAndModel(t *testing.T) {
	if got := newVoyageReranker(Settings{VLMVoyageRerankModel: "rerank-2.5-lite"}); got != nil {
		t.Fatalf("reranker without key = %#v, want nil", got)
	}
	if got := newVoyageReranker(Settings{VLMVoyageAPIKey: "secret"}); got != nil {
		t.Fatalf("reranker without model = %#v, want nil", got)
	}

	got := newVoyageReranker(Settings{
		VLMVoyageAPIKey:      "secret",
		VLMVoyageRerankModel: "rerank-2.5-lite",
		VLMVoyageEndpoint:    "https://example.test/rerank",
	})
	reranker, ok := got.(*llamaprov.VoyageReranker)
	if !ok {
		t.Fatalf("reranker = %T, want *VoyageReranker", got)
	}
	if reranker.APIKey != "secret" || reranker.Model != "rerank-2.5-lite" || reranker.Endpoint != "https://example.test/rerank" {
		t.Fatalf("reranker config = %#v", reranker)
	}
}
