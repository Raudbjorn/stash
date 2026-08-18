package api

import (
	"reflect"
	"testing"
)

func TestAvailableAIModelValues(t *testing.T) {
	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{
			name: "local VLM",
			got:  availableVLMModels(),
			want: []string{"internvl3-2b", "smolvlm2-2.2b", "qwen2.5-vl-7b"},
		},
		{
			name: "Voyage reranker",
			got:  availableVoyageRerankModels(),
			want: []string{"rerank-2.5", "rerank-2.5-lite", "rerank-2", "rerank-2-lite", "rerank-1", "rerank-lite-1"},
		},
		{
			name: "Voyage video",
			got:  availableVoyageVideoModels(),
			want: []string{"voyage-multimodal-3.5", "voyage-multimodal-3"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !reflect.DeepEqual(test.got, test.want) {
				t.Fatalf("models = %q, want %q", test.got, test.want)
			}
		})
	}
}

func TestAvailableVoyageModelValuesAreImmutable(t *testing.T) {
	rerank := availableVoyageRerankModels()
	video := availableVoyageVideoModels()
	rerank[0] = "changed"
	video[0] = "changed"
	if availableVoyageRerankModels()[0] != "rerank-2.5" || availableVoyageVideoModels()[0] != "voyage-multimodal-3.5" {
		t.Fatal("available model values exposed mutable package state")
	}
}

func TestTaggingResultServiceMapsAnalysisProfiles(t *testing.T) {
	tests := map[string]string{
		"local":            "llama_vlm",
		"local_voyage":     "llama_vlm",
		"voyage":           "voyage_multimodal",
		"skier_aitagging":  "skier_aitagging",
		"unknown-provider": "unknown-provider",
	}
	for selection, want := range tests {
		if got := taggingResultService(selection, "llama_vlm"); got != want {
			t.Errorf("selection %q mapped to %q, want %q", selection, got, want)
		}
	}
}
