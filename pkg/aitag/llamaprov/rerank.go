package llamaprov

import (
	"context"

	"github.com/stashapp/stash/pkg/aitag/taxonomy"
)

// Reranker optionally reorders the locally fused taxonomy candidates.
type Reranker interface {
	Rerank(ctx context.Context, query string, candidates []taxonomy.Entry, topK int) ([]taxonomy.Entry, error)
}
