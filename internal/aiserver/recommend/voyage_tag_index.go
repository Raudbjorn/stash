package recommend

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/stashapp/stash/internal/aiserver/store"
	"github.com/stashapp/stash/pkg/aitag"
	"github.com/stashapp/stash/pkg/aitag/taxonomy"
)

const (
	defaultVoyageTagThreshold = 0.5
	defaultVoyageMaxSceneTags = 8
	maxVoyageBatchInputs      = 1000
)

type taxonomyVector struct {
	entry  taxonomy.Entry
	vector []float32
}

type rankedTaxonomyVector struct {
	taxonomyVector
	score float64
}

// CanTag reports whether the index has every dependency required to retrieve
// taxonomy labels directly from Voyage video embeddings.
func (v *VoyageSegmentIndex) CanTag() bool {
	return v != nil && strings.TrimSpace(v.APIKey) != "" && v.DB != nil && v.Taxonomy != nil
}

// AnalyzeVideoTags embeds a scene as video documents, embeds authoritative
// taxonomy entries as text queries, and converts nearest neighbours into
// temporal detections. It is read-only with respect to Stash tags and markers;
// the tagging service owns persistence and writeback.
func (v *VoyageSegmentIndex) AnalyzeVideoTags(ctx context.Context, sceneID int, videoPath string, duration, threshold float64, sink aitag.Sink) (*aitag.Result, error) {
	if !v.CanTag() {
		return nil, errors.New("Voyage video tagging is not configured")
	}
	if threshold <= 0 {
		threshold = defaultVoyageTagThreshold
	}
	if threshold > 1 {
		return nil, fmt.Errorf("Voyage tag similarity threshold %.3f exceeds 1", threshold)
	}

	sink.Report(aitag.Progress{Fraction: 0.05, Message: "Embedding Voyage video segments."})
	segments, err := v.Build(ctx, sceneID, videoPath, "", duration)
	if err != nil {
		return nil, err
	}
	if len(segments) == 0 {
		return nil, errors.New("Voyage produced no video segments")
	}

	sink.Report(aitag.Progress{Fraction: 0.45, Message: "Loading Voyage taxonomy vectors."})
	vectors, err := v.taxonomyVectors(ctx)
	if err != nil {
		return nil, err
	}
	if len(vectors) == 0 {
		return nil, errors.New("the active taxonomy contains no Voyage candidates")
	}

	ranked := make([]rankedTaxonomyVector, 0, len(vectors))
	for _, candidate := range vectors {
		best := -1.0
		for _, segment := range segments {
			score, err := cosineSimilarity(candidate.vector, segment.Vector)
			if err != nil {
				return nil, fmt.Errorf("compare taxonomy tag %q: %w", candidate.entry.Canonical, err)
			}
			best = max(best, score)
		}
		if best >= threshold {
			ranked = append(ranked, rankedTaxonomyVector{taxonomyVector: candidate, score: best})
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].entry.Canonical < ranked[j].entry.Canonical
	})
	maxTags := v.MaxSceneTags
	if maxTags <= 0 {
		maxTags = defaultVoyageMaxSceneTags
	}
	if len(ranked) > maxTags {
		ranked = ranked[:maxTags]
	}

	spans := make(aitag.SpansByCategory)
	supports := make([]map[string]any, 0, len(ranked))
	for _, candidate := range ranked {
		category := strings.TrimSpace(candidate.entry.Category)
		if category == "" {
			category = "Voyage"
		}
		byTag := spans[category]
		if byTag == nil {
			byTag = make(aitag.SpansByTag)
			spans[category] = byTag
		}
		labelSpans := make([]aitag.Span, 0, len(segments))
		for _, segment := range segments {
			score, _ := cosineSimilarity(candidate.vector, segment.Vector)
			if score < threshold {
				continue
			}
			end := segment.End
			confidence := score
			labelSpans = append(labelSpans, aitag.Span{Start: segment.Start, End: &end, Confidence: &confidence})
		}
		if len(labelSpans) == 0 {
			continue
		}
		byTag[candidate.entry.Canonical] = labelSpans
		supports = append(supports, map[string]any{
			"tag": candidate.entry.Canonical, "stash_id": candidate.entry.StashID,
			"frames": len(labelSpans), "span_count": len(labelSpans),
			"first_at": labelSpans[0].Start, "last_at": labelSpans[len(labelSpans)-1].EndOrStart(),
		})
	}

	categories := append([]string(nil), v.Categories...)
	result := &aitag.Result{
		SchemaVersion: 3,
		Duration:      duration,
		FrameInterval: 0,
		Models: []aitag.ModelInfo{{
			Name:       v.model(),
			Categories: categories,
			Type:       "voyage_multimodal_retrieval",
			Threshold:  threshold,
			Extra: map[string]any{
				"dimension": v.dimension(), "max_scene_tags": maxTags,
				"video_segments": len(segments),
			},
		}},
		Spans: spans,
		Metrics: map[string]any{
			"segments": len(segments), "taxonomy_entries": len(vectors),
			"label_supports": supports,
		},
	}
	sink.Report(aitag.Progress{Fraction: 0.9, Message: fmt.Sprintf("Matched %d Voyage taxonomy tags.", len(ranked))})
	return result, nil
}

func (v *VoyageSegmentIndex) taxonomyVectors(ctx context.Context) ([]taxonomyVector, error) {
	byCategory, err := v.Taxonomy.CandidatesByCategory(ctx, v.Categories)
	if err != nil {
		return nil, fmt.Errorf("load Voyage taxonomy candidates: %w", err)
	}
	entries := make([]taxonomy.Entry, 0)
	seen := make(map[string]struct{})
	for _, category := range sortedMapKeys(byCategory) {
		for _, entry := range byCategory[category] {
			if _, ok := seen[entry.StashID]; ok {
				continue
			}
			seen[entry.StashID] = struct{}{}
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].StashID < entries[j].StashID })

	modelKey := v.cacheModel()
	cached, err := v.DB.GetTaxonomyEmbeddings(ctx, voyageSegmentService, modelKey)
	if err != nil {
		return nil, err
	}
	ret := make([]taxonomyVector, len(entries))
	missingIndexes := make([]int, 0)
	missingTexts := make([]string, 0)
	missingHashes := make([]string, 0)
	for i, entry := range entries {
		text := taxonomyEmbeddingText(entry)
		hash := contentHash(text)
		item, ok := cached[entry.StashID]
		if ok && item.ContentHash == hash && item.Dim == v.dimension() && len(item.Vector) == item.Dim {
			ret[i] = taxonomyVector{entry: entry, vector: item.Vector}
			continue
		}
		missingIndexes = append(missingIndexes, i)
		missingTexts = append(missingTexts, text)
		missingHashes = append(missingHashes, hash)
	}

	for start := 0; start < len(missingTexts); start += maxVoyageBatchInputs {
		end := min(start+maxVoyageBatchInputs, len(missingTexts))
		vectors, err := v.embedTexts(ctx, missingTexts[start:end])
		if err != nil {
			return nil, fmt.Errorf("embed Voyage taxonomy entries %d-%d: %w", start+1, end, err)
		}
		stored := make([]store.StoredTaxonomyEmbedding, len(vectors))
		for offset, vector := range vectors {
			missingAt := start + offset
			retAt := missingIndexes[missingAt]
			entry := entries[retAt]
			ret[retAt] = taxonomyVector{entry: entry, vector: vector}
			stored[offset] = store.StoredTaxonomyEmbedding{
				StashID: entry.StashID, Model: modelKey, Dim: len(vector),
				ContentHash: missingHashes[missingAt], Vector: vector,
			}
		}
		if err := v.DB.StoreTaxonomyEmbeddings(ctx, voyageSegmentService, stored); err != nil {
			return nil, err
		}
	}
	return ret, nil
}

func (v *VoyageSegmentIndex) embedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	inputs := make([]any, len(texts))
	for i, text := range texts {
		inputs[i] = map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}
	}
	payload, err := json.Marshal(map[string]any{
		"inputs": inputs, "model": v.model(), "input_type": "query", "truncation": false,
		"output_dimension": v.dimension(),
	})
	if err != nil {
		return nil, err
	}
	response, err := v.embeddingRequest(ctx, payload, len(texts))
	if err != nil {
		return nil, err
	}
	ret := make([][]float32, len(response))
	for i, vector := range response {
		ret[i], err = v.prepareVector(vector)
		if err != nil {
			return nil, fmt.Errorf("prepare taxonomy vector %d: %w", i, err)
		}
	}
	return ret, nil
}

func (v *VoyageSegmentIndex) embeddingRequest(ctx context.Context, payload []byte, expected int) ([][]float32, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.endpoint(), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+v.APIKey)
	req.Header.Set("Content-Type", "application/json")
	client := v.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var decoded struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&decoded); err != nil {
		return nil, err
	}
	if len(decoded.Data) != expected {
		return nil, fmt.Errorf("Voyage returned %d embeddings, want %d", len(decoded.Data), expected)
	}
	ret := make([][]float32, expected)
	for _, item := range decoded.Data {
		if item.Index < 0 || item.Index >= expected || len(item.Embedding) == 0 || ret[item.Index] != nil {
			return nil, errors.New("Voyage multimodal response contained invalid embedding indexes")
		}
		ret[item.Index] = item.Embedding
	}
	return ret, nil
}

func (v *VoyageSegmentIndex) prepareVector(vector []float32) ([]float32, error) {
	dimension := v.dimension()
	if len(vector) < dimension {
		return nil, fmt.Errorf("Voyage embedding dimension %d is smaller than configured dimension %d", len(vector), dimension)
	}
	ret := append([]float32(nil), vector[:dimension]...)
	var norm float64
	for _, value := range ret {
		norm += float64(value) * float64(value)
	}
	if norm == 0 {
		return nil, errors.New("Voyage returned a zero embedding")
	}
	scale := float32(1 / math.Sqrt(norm))
	for i := range ret {
		ret[i] *= scale
	}
	return ret, nil
}

func (v *VoyageSegmentIndex) model() string {
	if model := strings.TrimSpace(v.Model); model != "" {
		return model
	}
	return defaultVoyageSegmentModel
}

func (v *VoyageSegmentIndex) endpoint() string {
	if endpoint := strings.TrimSpace(v.Endpoint); endpoint != "" {
		return endpoint
	}
	return defaultVoyageSegmentEndpoint
}

func (v *VoyageSegmentIndex) dimension() int {
	if v.Dimension > 0 {
		return v.Dimension
	}
	return defaultVoyageDimension
}

func (v *VoyageSegmentIndex) cacheModel() string {
	return fmt.Sprintf("%s/dim-%d", v.model(), v.dimension())
}

func taxonomyEmbeddingText(entry taxonomy.Entry) string {
	var b strings.Builder
	b.WriteString("Video taxonomy tag: ")
	b.WriteString(entry.Canonical)
	if entry.Category != "" {
		b.WriteString(". Category: ")
		b.WriteString(entry.Category)
	}
	if len(entry.Aliases) > 0 {
		b.WriteString(". Also known as: ")
		b.WriteString(strings.Join(entry.Aliases, ", "))
	}
	if entry.Description != "" {
		b.WriteString(". Description: ")
		b.WriteString(entry.Description)
	}
	return b.String()
}

func contentHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func cosineSimilarity(left, right []float32) (float64, error) {
	if len(left) == 0 || len(left) != len(right) {
		return 0, fmt.Errorf("embedding dimensions differ: %d and %d", len(left), len(right))
	}
	var dot, leftNorm, rightNorm float64
	for i, value := range left {
		l := float64(value)
		r := float64(right[i])
		dot += l * r
		leftNorm += l * l
		rightNorm += r * r
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0, errors.New("cannot compare a zero embedding")
	}
	return dot / math.Sqrt(leftNorm*rightNorm), nil
}

func sortedMapKeys[V any](values map[string]V) []string {
	ret := make([]string, 0, len(values))
	for key := range values {
		ret = append(ret, key)
	}
	sort.Strings(ret)
	return ret
}
