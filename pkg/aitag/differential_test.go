package aitag

import (
	"bytes"
	"encoding/json"
	"math"
	"math/rand"
	"os/exec"
	"testing"
)

// The differential test the plan calls for: the same input through the Go port
// and through the reference Python, with every difference explained.
//
// This is what makes the two "faithful port" claims checkable rather than
// asserted. The reference lives in testdata as a verbatim extract, so it can be
// re-diffed against upstream when upstream changes.

// refCase is what the reference driver reads.
type refCase struct {
	Frames          []map[string]any          `json:"frames"`
	FrameInterval   float64                   `json:"frame_interval"`
	MaxMergeSeconds float64                   `json:"max_merge_seconds"`
	Cluster         bool                      `json:"cluster"`
	Duration        float64                   `json:"duration"`
	CategoryConfig  map[string]map[string]any `json:"category_config"`
	CSVDefaults     map[string]any            `json:"csv_defaults"`
	Params          map[string]float64        `json:"params"`
}

type refSpan struct {
	Start      float64  `json:"start"`
	End        *float64 `json:"end"`
	Confidence *float64 `json:"confidence"`
}

type refMarker struct {
	Category        string  `json:"category"`
	RenamedTag      string  `json:"renamed_tag"`
	Start           float64 `json:"start"`
	End             float64 `json:"end"`
	TotalConfidence float64 `json:"total_confidence"`
}

type refOutput struct {
	Spans   map[string]map[string][]refSpan `json:"spans"`
	Markers []refMarker                     `json:"markers"`
}

func runReference(t *testing.T, in refCase) refOutput {
	t.Helper()

	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("python3", "testdata/reference.py")
	cmd.Stdin = bytes.NewReader(body)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("reference implementation failed: %v\n%s", err, stderr.String())
	}

	var out refOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("reference output is not JSON: %v\n%s", err, stdout.String())
	}
	return out
}

func requirePython(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available; the differential test needs the reference")
	}
}

// buildFrames turns the reference's frame dicts into the Go shape.
func buildFrames(raw []map[string]any) []Frame {
	frames := make([]Frame, 0, len(raw))
	for _, entry := range raw {
		frame := Frame{Labels: map[string][]Detection{}}
		for key, value := range entry {
			if key == "frame_index" {
				frame.Index = value.(float64)
				continue
			}
			for _, item := range value.([]any) {
				switch typed := item.(type) {
				case string:
					frame.Labels[key] = append(frame.Labels[key], Detection{Tag: typed})
				case []any:
					confidence := typed[1].(float64)
					frame.Labels[key] = append(frame.Labels[key],
						Detection{Tag: typed[0].(string), Confidence: &confidence})
				}
			}
		}
		frames = append(frames, frame)
	}
	return frames
}

const epsilon = 1e-9

func floatsEqual(a, b float64) bool { return math.Abs(a-b) <= epsilon }

func compareSpans(t *testing.T, want map[string]map[string][]refSpan, got SpansByCategory) {
	t.Helper()

	if len(want) != len(got) {
		t.Errorf("category count: reference %d, port %d", len(want), len(got))
	}

	for category, wantTags := range want {
		gotTags, ok := got[category]
		if !ok {
			t.Errorf("port is missing category %q", category)
			continue
		}
		for tag, wantSpans := range wantTags {
			gotSpans, ok := gotTags[tag]
			if !ok {
				t.Errorf("port is missing %s/%s", category, tag)
				continue
			}
			if len(wantSpans) != len(gotSpans) {
				t.Errorf("%s/%s: reference produced %d spans, port %d",
					category, tag, len(wantSpans), len(gotSpans))
				continue
			}
			for i := range wantSpans {
				w, g := wantSpans[i], gotSpans[i]
				if !floatsEqual(w.Start, g.Start) {
					t.Errorf("%s/%s[%d] start: reference %v, port %v", category, tag, i, w.Start, g.Start)
				}
				switch {
				case w.End == nil && g.End != nil:
					t.Errorf("%s/%s[%d]: reference left the span open, port closed it at %v",
						category, tag, i, *g.End)
				case w.End != nil && g.End == nil:
					t.Errorf("%s/%s[%d]: reference closed the span at %v, port left it open",
						category, tag, i, *w.End)
				case w.End != nil && g.End != nil && !floatsEqual(*w.End, *g.End):
					t.Errorf("%s/%s[%d] end: reference %v, port %v", category, tag, i, *w.End, *g.End)
				}
			}
		}
	}
}

func compareMarkers(t *testing.T, want []refMarker, got []Marker) {
	t.Helper()

	if len(want) != len(got) {
		t.Errorf("marker count: reference %d, port %d\nreference=%+v\nport=%+v",
			len(want), len(got), want, got)
		return
	}

	for i := range want {
		w, g := want[i], got[i]
		if w.Category != g.Category || w.RenamedTag != g.RenamedTag {
			t.Errorf("marker %d identity: reference %s/%s, port %s/%s",
				i, w.Category, w.RenamedTag, g.Category, g.RenamedTag)
		}
		if !floatsEqual(w.Start, g.Start) || !floatsEqual(w.End, g.End) {
			t.Errorf("marker %d bounds: reference [%v,%v], port [%v,%v]",
				i, w.Start, w.End, g.Start, g.End)
		}
		// The accumulated confidence is order-dependent by construction, so an
		// exact match here is the strongest evidence the merge ordering was
		// reproduced rather than merely approximated.
		if math.Abs(w.TotalConfidence-g.TotalConfidence) > 1e-6 {
			t.Errorf("marker %d totalConfidence: reference %v, port %v",
				i, w.TotalConfidence, g.TotalConfidence)
		}
	}
}

// A hand-built case exercising the awkward parts of the collapse: a run that
// merges, a gap that does not, a confidence change mid-run, and a second
// category.
func TestCollapseMatchesReference(t *testing.T) {
	requirePython(t)

	conf := func(v float64) []any { return []any{"Blowjob", v} }

	frames := []map[string]any{
		{"frame_index": 0.0, "actions": []any{conf(0.9)}, "bodyparts": []any{"Feet"}},
		{"frame_index": 2.0, "actions": []any{conf(0.9)}, "bodyparts": []any{"Feet"}},
		{"frame_index": 4.0, "actions": []any{conf(0.9)}},
		// Confidence changes: the reference starts a new span even though the
		// frames are adjacent, because the merge compares confidences exactly.
		{"frame_index": 6.0, "actions": []any{conf(0.8)}},
		{"frame_index": 8.0, "actions": []any{conf(0.8)}},
		// A gap past max_merge_seconds.
		{"frame_index": 40.0, "actions": []any{conf(0.8)}},
		{"frame_index": 42.0, "actions": []any{conf(0.8)}, "bodyparts": []any{"Feet"}},
	}

	for _, maxMerge := range []float64{0, 2, 4} {
		in := refCase{Frames: frames, FrameInterval: 2, MaxMergeSeconds: maxMerge}
		want := runReference(t, in)
		got := CollapseFrames(buildFrames(frames), 2, maxMerge)
		compareSpans(t, want.Spans, got)
	}
}

// The property that matters most: over random inputs, the port and the
// reference agree on every span boundary.
func TestCollapseMatchesReferenceOnRandomInput(t *testing.T) {
	requirePython(t)

	rng := rand.New(rand.NewSource(20260726))
	tags := []string{"Blowjob", "Anal Fucking", "Kissing"}

	for trial := 0; trial < 12; trial++ {
		frameInterval := []float64{1, 2, 4}[rng.Intn(3)]
		maxMerge := []float64{0, 2, 4}[rng.Intn(3)]

		var frames []map[string]any
		at := 0.0
		for i := 0; i < 60; i++ {
			// Irregular sampling: real runs skip frames the decoder dropped.
			at += frameInterval * float64(1+rng.Intn(3))

			var actions []any
			for _, tag := range tags {
				if rng.Float64() < 0.5 {
					continue
				}
				if rng.Float64() < 0.3 {
					// No confidence at all, exercising the None == None path.
					actions = append(actions, tag)
					continue
				}
				// Quantized to two decimals, which is the only reason equality
				// ever holds and therefore the only regime worth testing.
				actions = append(actions, []any{tag, QuantizeConfidence(rng.Float64())})
			}
			if actions == nil {
				continue
			}
			frames = append(frames, map[string]any{"frame_index": at, "actions": actions})
		}
		if len(frames) == 0 {
			continue
		}

		in := refCase{Frames: frames, FrameInterval: frameInterval, MaxMergeSeconds: maxMerge}
		want := runReference(t, in)
		got := CollapseFrames(buildFrames(frames), frameInterval, maxMerge)
		compareSpans(t, want.Spans, got)
	}
}

// testRules is a small category configuration shared by the clustering cases.
func testRules() (*Rules, map[string]map[string]any, map[string]any) {
	rules := NewRules()
	rules.Categories["actions"] = CategoryRules{
		"Blowjob": {
			OriginalTag: "Blowjob", RenamedTag: "Blowjob_AI",
			MinMarkerDuration: "12", MaxGap: "6", RequiredDuration: "20s", TagThreshold: "0.5",
		},
		"Anal Fucking": {
			OriginalTag: "Anal Fucking", RenamedTag: "Anal Fucking_AI",
			MinMarkerDuration: "5", MaxGap: "4", RequiredDuration: "15s", TagThreshold: "0.5",
		},
		// A percentage minimum, which resolves against the video's length.
		"Kissing": {
			OriginalTag: "Kissing", RenamedTag: "Kissing_AI",
			MinMarkerDuration: "2%", MaxGap: "6", RequiredDuration: "20s", TagThreshold: "0.6",
		},
	}

	config := map[string]map[string]any{
		"actions": {
			"Blowjob": map[string]any{
				"RenamedTag": "Blowjob_AI", "MinMarkerDuration": "12",
				"MaxGap": "6", "RequiredDuration": "20s", "TagThreshold": "0.5",
			},
			"Anal Fucking": map[string]any{
				"RenamedTag": "Anal Fucking_AI", "MinMarkerDuration": "5",
				"MaxGap": "4", "RequiredDuration": "15s", "TagThreshold": "0.5",
			},
			"Kissing": map[string]any{
				"RenamedTag": "Kissing_AI", "MinMarkerDuration": "2%",
				"MaxGap": "6", "RequiredDuration": "20s", "TagThreshold": "0.6",
			},
		},
	}

	defaults := map[string]any{
		"MinMarkerDuration": 12, "MaxGap": 6, "RequiredDuration": 20, "TagThreshold": 0.5,
	}
	return rules, config, defaults
}

// The whole pipeline, both stages, against the reference.
func TestClusteringMatchesReference(t *testing.T) {
	requirePython(t)

	rules, config, defaults := testRules()
	params := DefaultClusterParams()

	rng := rand.New(rand.NewSource(31415))
	tags := []string{"Blowjob", "Anal Fucking", "Kissing"}

	for trial := 0; trial < 15; trial++ {
		frameInterval := []float64{2, 4}[rng.Intn(2)]
		duration := 600 + float64(rng.Intn(1800))

		var frames []map[string]any
		at := 0.0
		for at < duration {
			at += frameInterval * float64(1+rng.Intn(2))

			var actions []any
			for _, tag := range tags {
				if rng.Float64() < 0.4 {
					continue
				}
				actions = append(actions, []any{tag, QuantizeConfidence(0.3 + rng.Float64()*0.7)})
			}
			if actions == nil {
				continue
			}
			frames = append(frames, map[string]any{"frame_index": at, "actions": actions})
		}
		if len(frames) < 5 {
			continue
		}

		in := refCase{
			Frames:          frames,
			FrameInterval:   frameInterval,
			MaxMergeSeconds: 4,
			Cluster:         true,
			Duration:        duration,
			CategoryConfig:  config,
			CSVDefaults:     defaults,
			Params: map[string]float64{
				"density_weight": params.DensityWeight,
				"gap_factor":     params.GapFactor,
				"average_factor": params.AverageFactor,
				"min_gap":        params.MinGap,
			},
		}
		want := runReference(t, in)

		spans := CollapseFrames(buildFrames(frames), frameInterval, 4)
		result := &Result{Duration: duration, FrameInterval: frameInterval, Spans: spans}
		got := Cluster(result, rules, params)

		compareSpans(t, want.Spans, spans)
		compareMarkers(t, want.Markers, got)
	}
}

// Spans with no confidence at all - the HTTP provider's v3 route omits them -
// must still produce markers rather than being silently discarded.
func TestClusteringMatchesReferenceWithoutConfidences(t *testing.T) {
	requirePython(t)

	rules, config, defaults := testRules()
	params := DefaultClusterParams()

	var frames []map[string]any
	for at := 0.0; at < 200; at += 2 {
		// A long run, a gap, a second run.
		if at > 60 && at < 120 {
			continue
		}
		frames = append(frames, map[string]any{"frame_index": at, "actions": []any{"Blowjob"}})
	}

	in := refCase{
		Frames: frames, FrameInterval: 2, MaxMergeSeconds: 4, Cluster: true,
		Duration: 600, CategoryConfig: config, CSVDefaults: defaults,
		Params: map[string]float64{
			"density_weight": params.DensityWeight, "gap_factor": params.GapFactor,
			"average_factor": params.AverageFactor, "min_gap": params.MinGap,
		},
	}
	want := runReference(t, in)

	spans := CollapseFrames(buildFrames(frames), 2, 4)
	result := &Result{Duration: 600, FrameInterval: 2, Spans: spans}
	got := Cluster(result, rules, params)

	compareSpans(t, want.Spans, spans)
	compareMarkers(t, want.Markers, got)

	if len(got) == 0 {
		t.Fatal("confidence-free spans produced no markers at all")
	}
}
