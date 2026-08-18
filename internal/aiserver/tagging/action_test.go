package tagging

import (
	"reflect"
	"testing"

	"github.com/stashapp/stash/pkg/aitag"
)

func TestSummariseReportsFailedScenesAndErrors(t *testing.T) {
	results := []*AnalyzeResult{
		{SceneID: 101, Provider: "llama_vlm", Spans: 2, Markers: 1, Write: &WritebackResult{TagsApplied: 2, TagsCreated: 1}},
		{SceneID: 202, Provider: "error", Error: "decode failed"},
	}

	summary := summarise(results)
	if got, want := summary["failed"], 1; got != want {
		t.Fatalf("failed = %v, want %v", got, want)
	}
	if got, want := summary["failed_scenes"], []int{202}; !reflect.DeepEqual(got, want) {
		t.Fatalf("failed_scenes = %v, want %v", got, want)
	}
	if got, want := summary["tags_applied"], 2; got != want {
		t.Fatalf("tags_applied = %v, want %v", got, want)
	}
	if got, want := summary["tags_created"], 1; got != want {
		t.Fatalf("tags_created = %v, want %v", got, want)
	}
	returned, ok := summary["results"].([]*AnalyzeResult)
	if !ok || len(returned) != 2 || returned[1].Error == "" {
		t.Fatalf("results do not preserve the scene error: %#v", summary["results"])
	}
}

func TestSummariseOutcomeFailsWhenEverySceneFails(t *testing.T) {
	results := []*AnalyzeResult{{
		SceneID:  107133,
		Provider: "error",
		Error:    "verify frame 1920.000: decode llama VLM decisions: unexpected end of JSON input",
	}}
	summary, err := summariseOutcome(results)
	if err == nil || err.Error() != results[0].Error {
		t.Fatalf("error = %v", err)
	}
	if got, want := summary["failed"], 1; got != want {
		t.Fatalf("failed = %v, want %v", got, want)
	}
}

func TestSummariseOutcomeAllowsPartialSuccess(t *testing.T) {
	results := []*AnalyzeResult{
		{SceneID: 101, Provider: "llama_vlm", Spans: 2},
		{SceneID: 202, Provider: "error", Error: "decode failed"},
	}
	if _, err := summariseOutcome(results); err != nil {
		t.Fatalf("partial result returned error: %v", err)
	}
}

func TestDetectedSceneTagNamesKeepsCanonicalLabelsWithoutMarkerRules(t *testing.T) {
	spans := aitag.SpansByCategory{
		"Acts": {
			"Known": {{Start: 0}},
			"Novel": {{Start: 2}},
		},
	}
	markers := []aitag.Marker{{Tag: "Known", RenamedTag: "Known_AI"}}
	got := detectedSceneTagNames(spans, markers)
	want := []string{"Known_AI", "Novel"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scene tags = %q, want %q", got, want)
	}
}
