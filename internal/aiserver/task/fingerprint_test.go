package task

import (
	"context"
	"testing"
	"time"

	"github.com/stashapp/stash/internal/aiserver/action"
)

func ptr(s string) *string { return &s }

// The context key must reproduce Pydantic's
// exclude_none + exclude_defaults + by_alias behaviour exactly. Getting this
// wrong silently disables duplicate detection.
func TestContextFingerprintOmissionRules(t *testing.T) {
	cases := []struct {
		name string
		ctx  action.ContextInput
		want string
	}{
		{
			name: "library view carries only page",
			ctx:  action.ContextInput{Page: "scenes"},
			want: `{"page":"scenes"}`,
		},
		{
			// exclude_defaults: false is the declared default, so it vanishes.
			name: "isDetailView false is omitted",
			ctx:  action.ContextInput{Page: "scenes", IsDetailView: false},
			want: `{"page":"scenes"}`,
		},
		{
			name: "isDetailView true is present",
			ctx:  action.ContextInput{Page: "scenes", IsDetailView: true},
			want: `{"isDetailView":true,"page":"scenes"}`,
		},
		{
			// exclude_none: an absent optional disappears entirely.
			name: "nil entityId is omitted",
			ctx:  action.ContextInput{Page: "scenes", EntityID: nil},
			want: `{"page":"scenes"}`,
		},
		{
			name: "entityId present",
			ctx:  action.ContextInput{Page: "scenes", EntityID: ptr("42")},
			want: `{"entityId":"42","page":"scenes"}`,
		},
		{
			// An explicit empty array is NOT the default (nil), so it survives.
			// This is the distinction that makes nil-vs-empty matter.
			name: "empty selectedIds is present",
			ctx:  action.ContextInput{Page: "scenes", SelectedIDs: []string{}},
			want: `{"page":"scenes","selectedIds":[]}`,
		},
		{
			name: "nil selectedIds is omitted",
			ctx:  action.ContextInput{Page: "scenes", SelectedIDs: nil},
			want: `{"page":"scenes"}`,
		},
		{
			name: "populated selection",
			ctx:  action.ContextInput{Page: "scenes", SelectedIDs: []string{"1", "2"}},
			want: `{"page":"scenes","selectedIds":["1","2"]}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := fingerprint(tc.ctx, nil)
			if err != nil {
				t.Fatalf("fingerprint: %v", err)
			}
			if got != tc.want {
				t.Errorf("context key = %s, want %s", got, tc.want)
			}
		})
	}
}

// Keys must be sorted and whitespace-free so logically equal payloads collide
// regardless of how they were constructed.
func TestFingerprintIsCanonical(t *testing.T) {
	ctx := action.ContextInput{Page: "scenes"}

	a := map[string]any{"z": 1, "a": 2, "m": map[string]any{"y": true, "b": "x"}}
	b := map[string]any{"m": map[string]any{"b": "x", "y": true}, "a": 2, "z": 1}

	_, keyA, err := fingerprint(ctx, a)
	if err != nil {
		t.Fatalf("fingerprint a: %v", err)
	}
	_, keyB, err := fingerprint(ctx, b)
	if err != nil {
		t.Fatalf("fingerprint b: %v", err)
	}

	if keyA != keyB {
		t.Errorf("insertion order changed the key:\n  %s\n  %s", keyA, keyB)
	}
	if want := `{"a":2,"m":{"b":"x","y":true},"z":1}`; keyA != want {
		t.Errorf("params key = %s, want %s", keyA, want)
	}
}

// A task submitted with no params must match one submitted with {}.
func TestNilParamsMatchEmptyParams(t *testing.T) {
	ctx := action.ContextInput{Page: "scenes"}

	_, nilKey, err := fingerprint(ctx, nil)
	if err != nil {
		t.Fatalf("nil: %v", err)
	}
	_, emptyKey, err := fingerprint(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("empty: %v", err)
	}

	if nilKey != emptyKey || nilKey != "{}" {
		t.Errorf("nil=%s empty=%s, want both {}", nilKey, emptyKey)
	}
}

func TestFindDuplicateMatchesEquivalentSubmissions(t *testing.T) {
	gate := newFakeGate()
	gate.setReady("svc", false) // hold tasks queued so they stay active
	m := newManager(t, Options{Gate: gate})

	d := def("tag", "svc")
	ctx := action.ContextInput{Page: "scenes", SelectedIDs: []string{"1", "2"}}
	params := map[string]any{"threshold": 0.5}

	if _, ok := m.FindDuplicate(d, ctx, params); ok {
		t.Fatal("found a duplicate before anything was submitted")
	}

	rec, err := m.Submit(d, func(context.Context, action.ContextInput, map[string]any, action.Handle) (any, error) {
		return nil, nil
	}, ctx, params, PriorityNormal, SubmitOptions{})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	// Same payload, rebuilt independently.
	dup, ok := m.FindDuplicate(d, action.ContextInput{Page: "scenes", SelectedIDs: []string{"1", "2"}},
		map[string]any{"threshold": 0.5})
	if !ok {
		t.Fatal("equivalent submission was not detected as a duplicate")
	}
	if dup.ID != rec.ID {
		t.Errorf("duplicate id = %s, want %s", dup.ID, rec.ID)
	}

	// Differing in any component must not match.
	if _, ok := m.FindDuplicate(d, ctx, map[string]any{"threshold": 0.9}); ok {
		t.Error("different params matched")
	}
	if _, ok := m.FindDuplicate(d, action.ContextInput{Page: "scenes", SelectedIDs: []string{"1"}}, params); ok {
		t.Error("different selection matched")
	}
	if _, ok := m.FindDuplicate(def("other", "svc"), ctx, params); ok {
		t.Error("different action matched")
	}
	if _, ok := m.FindDuplicate(def("tag", "elsewhere"), ctx, params); ok {
		t.Error("different service matched")
	}
}

// A finished task must not block resubmission.
func TestCompletedTaskIsNotADuplicate(t *testing.T) {
	m := newManager(t, Options{Gate: newFakeGate()})

	d := def("tag", "svc")
	ctx := action.ContextInput{Page: "scenes"}

	rec, err := m.Submit(d, func(context.Context, action.ContextInput, map[string]any, action.Handle) (any, error) {
		return nil, nil
	}, ctx, nil, PriorityNormal, SubmitOptions{})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitStatus(t, m, rec.ID, StatusCompleted)

	if _, ok := m.FindDuplicate(d, ctx, nil); ok {
		t.Error("a completed task blocked resubmission")
	}
}

// Streaming counts as active for deduplication even though the core scheduler
// never sets it; plugins do.
func TestStreamingStatusCountsAsActive(t *testing.T) {
	if !StatusStreaming.Active() {
		t.Error("streaming should be active")
	}
	if StatusStreaming.Terminal() {
		t.Error("streaming should not be terminal")
	}
	for _, s := range []Status{StatusCompleted, StatusFailed, StatusCancelled} {
		if !s.Terminal() || s.Active() {
			t.Errorf("%q classified wrong", s)
		}
	}
}

func TestPriorityNamesRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		p    Priority
		name string
	}{{PriorityHigh, "high"}, {PriorityNormal, "normal"}, {PriorityLow, "low"}} {
		if tc.p.String() != tc.name {
			t.Errorf("%d.String() = %q, want %q", tc.p, tc.p.String(), tc.name)
		}
		got, ok := ParsePriority(tc.name)
		if !ok || got != tc.p {
			t.Errorf("ParsePriority(%q) = %v/%v", tc.name, got, ok)
		}
	}
	if _, ok := ParsePriority("urgent"); ok {
		t.Error("ParsePriority accepted an unknown name")
	}
	// Ordering is what the heap relies on.
	if !(PriorityHigh < PriorityNormal && PriorityNormal < PriorityLow) {
		t.Error("priority ordering is wrong")
	}
}

// The summary is the websocket contract: exact keys, snake_case.
func TestSummaryShape(t *testing.T) {
	started, finished := 1.0, 2.0
	rec := Record{
		ID: "abc", ActionID: "svc.tag", Service: "svc",
		Priority: PriorityHigh, Status: StatusCompleted,
		SubmittedAt: 0.5, StartedAt: &started, FinishedAt: &finished,
		Result: "yes", GroupID: "parent",
	}

	s := rec.Summary()
	wantKeys := []string{
		"id", "action_id", "service", "priority", "status", "submitted_at",
		"started_at", "finished_at", "error", "cancel_requested", "result", "group_id",
	}
	if len(s) != len(wantKeys) {
		t.Errorf("summary has %d keys, want %d: %v", len(s), len(wantKeys), s)
	}
	for _, k := range wantKeys {
		if _, ok := s[k]; !ok {
			t.Errorf("summary missing %q", k)
		}
	}
	if s["priority"] != "high" {
		t.Errorf("priority = %v, want the name high", s["priority"])
	}
	if s["result"] != "yes" {
		t.Errorf("result = %v", s["result"])
	}

	// A non-completed task hides its result.
	rec.Status = StatusRunning
	if rec.Summary()["result"] != nil {
		t.Error("a running task exposed a result")
	}
}

var _ = time.Second
