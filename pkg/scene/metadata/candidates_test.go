package metadata

import (
	"reflect"
	"testing"
)

func TestExtractNameCandidates(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		exclude func(string) bool
		want    []string
	}{
		{
			name: "simple name pair",
			text: "Jane Doe - First Scene",
			want: []string{"Jane Doe"},
		},
		{
			name:    "filename with delimiters, studio name excluded",
			text:    "Studio Name - Jane Doe - Scene Title 1080p",
			exclude: func(c string) bool { return c == "studio name" },
			want:    []string{"Jane Doe"},
		},
		{
			name: "stop word breaks pair",
			text: "The Best Scene",
			want: nil,
		},
		{
			name: "prose tokens do not form candidate",
			text: "You Make",
			want: nil,
		},
		{
			name: "prose prefix leaves only performer name",
			text: "You Make Skinny Kenzie Reeves",
			want: []string{"Kenzie Reeves"},
		},
		{
			name: "weak token prefix cannot cross into performer name",
			text: "Skinny Kenzie Reeves",
			want: []string{"Kenzie Reeves"},
		},
		{
			name:    "excluded studio name",
			text:    "Studio Site - Jane Doe",
			exclude: func(c string) bool { return c == "studio site" },
			want:    []string{"Jane Doe"},
		},
		{
			name: "all lowercase is not name-like",
			text: "jane doe scene",
			want: nil,
		},
		{
			name: "duplicate pairs deduped",
			text: "Jane Doe and Jane Doe again",
			want: []string{"Jane Doe"},
		},
		{
			// #reported: sentence-cased marketing lead-in and a descriptor
			// word must not misalign the pairing away from the real name.
			name: "sentence-cased lead-in and descriptor word excluded",
			text: "You Make Skinny Kenzie Reeves Cum",
			want: []string{"Kenzie Reeves"},
		},
		{
			name: "relationship/genre descriptor pair produces no candidate",
			text: "Step Mom Comes Every Time",
			want: nil,
		},
		{
			name: "genre tag pair produces no candidate",
			text: "Goth Girls Secret Teen",
			want: nil,
		},
		{
			name: "filler-word pair produces no candidate",
			text: "Dont Leave My Eyes Only",
			want: nil,
		},
		{
			name: "real name survives amid descriptor noise",
			text: "Are Friends With Cory Chase",
			want: []string{"Cory Chase"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractNameCandidates(tt.text, tt.exclude)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ExtractNameCandidates() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestHeuristicNamePlausibilityScorerRejectsNonNameTokens(t *testing.T) {
	const minCandidatePlausibility = 0.3

	for _, candidate := range []string{"You Make", "Skinny Kenzie"} {
		t.Run(candidate, func(t *testing.T) {
			score := (HeuristicNamePlausibilityScorer{}).Score(candidate)
			if score >= minCandidatePlausibility {
				t.Fatalf("Score(%q) = %v, want below %v", candidate, score, minCandidatePlausibility)
			}
		})
	}
}

func TestImplausiblePerformerName(t *testing.T) {
	for _, name := range []string{"XXX (Bilatinmen)", "I (Bilatinmen)", "Anal", "XXX", "I"} {
		if !ImplausiblePerformerName(name) {
			t.Errorf("ImplausiblePerformerName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"Jane Doe", "Cher", "María O’Neil"} {
		if ImplausiblePerformerName(name) {
			t.Errorf("ImplausiblePerformerName(%q) = true, want false", name)
		}
	}
}
