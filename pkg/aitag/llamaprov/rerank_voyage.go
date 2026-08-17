package llamaprov

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/stashapp/stash/pkg/aitag/taxonomy"
)

const (
	defaultVoyageRerankModel    = "rerank-2.5-lite"
	defaultVoyageRerankEndpoint = "https://api.voyageai.com/v1/rerank"
)

var ErrVoyageNoAPIKey = errors.New("voyage rerank disabled: no api key")

// VoyageReranker uses Voyage's cross-encoder rerank API.
type VoyageReranker struct {
	APIKey   string
	Model    string
	Endpoint string
	Client   *http.Client
}

func (r *VoyageReranker) Rerank(ctx context.Context, query string, candidates []taxonomy.Entry, topK int) ([]taxonomy.Entry, error) {
	if r == nil || strings.TrimSpace(r.APIKey) == "" {
		return nil, ErrVoyageNoAPIKey
	}
	if len(candidates) == 0 || topK <= 0 {
		return []taxonomy.Entry{}, nil
	}
	model := strings.TrimSpace(r.Model)
	if model == "" {
		model = defaultVoyageRerankModel
	}
	endpoint := strings.TrimSpace(r.Endpoint)
	if endpoint == "" {
		endpoint = defaultVoyageRerankEndpoint
	}
	topK = min(topK, len(candidates))
	documents := make([]string, len(candidates))
	for i, candidate := range candidates {
		documents[i] = voyageCandidateDocument(candidate)
	}
	payload, err := json.Marshal(struct {
		Query           string   `json:"query"`
		Documents       []string `json:"documents"`
		Model           string   `json:"model"`
		TopK            int      `json:"top_k"`
		ReturnDocuments bool     `json:"return_documents"`
	}{
		Query: query, Documents: documents, Model: model, TopK: topK,
	})
	if err != nil {
		return nil, fmt.Errorf("encode voyage rerank request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create voyage rerank request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+r.APIKey)
	req.Header.Set("Content-Type", "application/json")
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("voyage rerank request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("voyage rerank: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var decoded struct {
		Data []struct {
			Index          int     `json:"index"`
			RelevanceScore float64 `json:"relevance_score"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode voyage rerank response: %w", err)
	}
	ret := make([]taxonomy.Entry, 0, min(topK, len(decoded.Data)))
	seen := make(map[int]bool, len(decoded.Data))
	for _, result := range decoded.Data {
		if result.Index < 0 || result.Index >= len(candidates) || seen[result.Index] {
			return nil, fmt.Errorf("voyage rerank returned invalid candidate index %d", result.Index)
		}
		seen[result.Index] = true
		ret = append(ret, candidates[result.Index])
		if len(ret) == topK {
			break
		}
	}
	return ret, nil
}

func voyageCandidateDocument(entry taxonomy.Entry) string {
	parts := []string{entry.Canonical}
	parts = append(parts, entry.Aliases...)
	parts = append(parts, entry.Category, entry.Description)
	return strings.Join(parts, " | ")
}
