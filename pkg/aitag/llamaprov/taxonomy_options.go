package llamaprov

import "github.com/stashapp/stash/pkg/aitag/taxonomy"

const defaultVoyageQueryInstruction = "Return only tags visible in the described frame."

// TaxonomyOptions configures local candidate selection, support acceptance, and
// the optional external rerank pass.
type TaxonomyOptions struct {
	Categories         []string
	MaxCandidates      int
	MaxPerFrame        int
	MinCandidates      int
	AcceptMode         AcceptMode
	MinSupport         int
	MinSupportFraction float64
	Reranker           Reranker
	RerankTopK         int
	VoyageQueryInstr   string
}

func NewTaxonomyAnalyzer(p *Provider, c *taxonomy.Client, opts TaxonomyOptions) *TaxonomyAnalyzer {
	instruction := opts.VoyageQueryInstr
	if instruction == "" {
		instruction = defaultVoyageQueryInstruction
	}
	return &TaxonomyAnalyzer{
		Provider:           p,
		Client:             c,
		Categories:         append([]string(nil), opts.Categories...),
		MaxCandidates:      opts.MaxCandidates,
		MaxPerFrame:        opts.MaxPerFrame,
		MinCandidates:      opts.MinCandidates,
		AcceptMode:         opts.AcceptMode,
		MinSupport:         opts.MinSupport,
		MinSupportFraction: opts.MinSupportFraction,
		Reranker:           opts.Reranker,
		RerankTopK:         opts.RerankTopK,
		VoyageQueryInstr:   instruction,
	}
}
