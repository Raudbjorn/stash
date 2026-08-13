package tagging

import (
	"reflect"
	"testing"
)

func TestSummariseReportsFailedScenesAndErrors(t *testing.T) {
	results := []*AnalyzeResult{
		{SceneID: 101, Provider: "llama_vlm", Spans: 2, Markers: 1},
		{SceneID: 202, Provider: "error", Error: "decode failed"},
	}

	summary := summarise(results)
	if got, want := summary["failed"], 1; got != want {
		t.Fatalf("failed = %v, want %v", got, want)
	}
	if got, want := summary["failed_scenes"], []int{202}; !reflect.DeepEqual(got, want) {
		t.Fatalf("failed_scenes = %v, want %v", got, want)
	}
	returned, ok := summary["results"].([]*AnalyzeResult)
	if !ok || len(returned) != 2 || returned[1].Error == "" {
		t.Fatalf("results do not preserve the scene error: %#v", summary["results"])
	}
}
