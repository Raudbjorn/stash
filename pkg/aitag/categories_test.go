package aitag

import (
	"strings"
	"testing"
)

// The shipped actions.csv is committed as testdata so the parser is exercised
// against the real file rather than an invented one. Its values were tuned
// against real footage; a parser that silently mangled them would change every
// marker boundary with nothing to point at.
func TestLoadRealCategoryRules(t *testing.T) {
	rules, err := LoadRulesDir("testdata/categories")
	if err != nil {
		t.Fatalf("LoadRulesDir: %v", err)
	}

	if !rules.HasCategory("actions") {
		t.Fatalf("actions category not loaded; got %v", rules.CategoryNames())
	}

	actions := rules.Categories["actions"]
	if len(actions) < 30 {
		t.Errorf("loaded %d action rules, expected the full file", len(actions))
	}

	rule, ok := rules.Lookup("actions", "Blowjob")
	if !ok {
		t.Fatal("Blowjob has no rule")
	}
	// The rename is what distinguishes a generated marker from a user's own.
	if rule.RenamedTag != "Blowjob_AI" {
		t.Errorf("RenamedTag = %q, want Blowjob_AI", rule.RenamedTag)
	}

	// A tag name containing a slash must survive CSV parsing intact.
	if _, ok := rules.Lookup("actions", "Ball Licking/Sucking"); !ok {
		t.Error("a tag containing a slash was not parsed")
	}

	// "20s" must resolve to 20 seconds, not to 20% or to zero.
	if got := rules.RequiredDuration(rule, 600); got != 20 {
		t.Errorf("RequiredDuration = %v, want 20", got)
	}
	if got := rules.MinMarkerDuration(rule, 600); got != 12 {
		t.Errorf("MinMarkerDuration = %v, want 12", got)
	}
	if got := rules.TagThreshold(rule); got != 0.5 {
		t.Errorf("TagThreshold = %v, want 0.5", got)
	}
}

func TestFormatDurationOrPercent(t *testing.T) {
	cases := []struct {
		value    string
		duration float64
		want     float64
	}{
		{"12", 600, 12},
		{"20s", 600, 20},
		{"5%", 600, 30},
		{"0.5", 600, 0.5},
		{" 20s ", 600, 20},
		{"100%", 1200, 1200},

		// An unparseable value yields zero, which the caller reads as "do not
		// mark this tag". That is the reference's behaviour and it fails in the
		// safe direction: a malformed rule generates nothing rather than a
		// marker per frame.
		{"nonsense", 600, 0},
		{"", 600, 0},
		{"%", 600, 0},
		{"s", 600, 0},
	}

	for _, tc := range cases {
		if got := FormatDurationOrPercent(tc.value, tc.duration); got != tc.want {
			t.Errorf("FormatDurationOrPercent(%q, %v) = %v, want %v",
				tc.value, tc.duration, got, tc.want)
		}
	}
}

// A blank cell must take the configured default, not parse as zero - zero means
// "never mark this tag", which is a very different outcome.
func TestBlankCellsTakeDefaults(t *testing.T) {
	csv := "OriginalTag,RenamedTag,MinMarkerDuration,MaxGap,RequiredDuration,TagThreshold\n" +
		"Sparse,Sparse_AI,,,,\n"

	parsed, err := ParseRulesCSV(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("ParseRulesCSV: %v", err)
	}

	rules := NewRules()
	rules.Categories["actions"] = parsed

	rule, ok := rules.Lookup("actions", "Sparse")
	if !ok {
		t.Fatal("Sparse was not parsed")
	}

	if got := rules.MinMarkerDuration(rule, 600); got != 12 {
		t.Errorf("MinMarkerDuration = %v, want the default 12", got)
	}
	if got := rules.MaxGap(rule, 600); got != 6 {
		t.Errorf("MaxGap = %v, want the default 6", got)
	}
	if got := rules.TagThreshold(rule); got != 0.5 {
		t.Errorf("TagThreshold = %v, want the default 0.5", got)
	}
}

// A label the category does not list gets no rule at all, rather than the
// defaults. A rules file is also the list of what is worth marking, so treating
// an unlisted label as "use the defaults" would generate markers nobody asked
// for.
func TestUnlistedLabelsAreNotGivenDefaults(t *testing.T) {
	rules, err := LoadRulesDir("testdata/categories")
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := rules.Lookup("actions", "Something The Model Invented"); ok {
		t.Error("an unlisted label was given a rule")
	}
	if _, ok := rules.Lookup("no-such-category", "Blowjob"); ok {
		t.Error("a label in an unconfigured category was given a rule")
	}
}

// An unlisted label must produce no markers even when it has plenty of spans,
// which is the behaviour the lookup above exists to produce.
func TestClusterSkipsUnconfiguredLabels(t *testing.T) {
	rules, err := LoadRulesDir("testdata/categories")
	if err != nil {
		t.Fatal(err)
	}

	spans := SpansByCategory{
		"actions": SpansByTag{
			"Blowjob":  denseSpans(0, 120, 2),
			"Invented": denseSpans(0, 120, 2),
		},
		"unconfigured": SpansByTag{
			"Blowjob": denseSpans(0, 120, 2),
		},
	}

	markers := Cluster(&Result{Duration: 600, FrameInterval: 2, Spans: spans}, rules, DefaultClusterParams())
	if len(markers) == 0 {
		t.Fatal("the configured label produced no markers")
	}
	for _, m := range markers {
		if m.Tag == "Invented" {
			t.Error("an unlisted label produced a marker")
		}
		if m.Category == "unconfigured" {
			t.Error("an unconfigured category produced a marker")
		}
	}
}

// denseSpans builds one span per sampled frame across a range.
func denseSpans(start, end, interval float64) []Span {
	var out []Span
	confidence := 0.9
	for at := start; at < end; at += interval {
		out = append(out, Span{Start: at, Confidence: &confidence})
	}
	return out
}
