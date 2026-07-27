package task

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stashapp/stash/internal/aiserver/action"
)

// ---------------------------------------------------------------- helpers ---

type fakeGate struct {
	mu          sync.Mutex
	concurrency map[string]int
	notReady    map[string]bool
	readyDelay  time.Duration
	readyCalls  atomic.Int64
}

func newFakeGate() *fakeGate {
	return &fakeGate{concurrency: map[string]int{}, notReady: map[string]bool{}}
}

func (g *fakeGate) MaxConcurrency(service string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if n, ok := g.concurrency[service]; ok {
		return n
	}
	return 1
}

func (g *fakeGate) Ready(ctx context.Context, service string) bool {
	g.readyCalls.Add(1)
	g.mu.Lock()
	delay, blocked := g.readyDelay, g.notReady[service]
	g.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return false
		}
	}
	return !blocked
}

func (g *fakeGate) setConcurrency(service string, n int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.concurrency[service] = n
}

func (g *fakeGate) setReady(service string, ready bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.notReady[service] = !ready
}

type recordingHistory struct {
	mu      sync.Mutex
	entries []Record
	counts  []int
}

func (h *recordingHistory) RecordTask(_ context.Context, rec Record, childCount int) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries = append(h.entries, rec)
	h.counts = append(h.counts, childCount)
	return nil
}

func (h *recordingHistory) len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.entries)
}

func def(id, service string) action.Definition {
	return action.Definition{ID: id, Service: service, Label: id, DeduplicateSubmissions: true}
}

func newManager(t *testing.T, opts Options) *Manager {
	t.Helper()
	if opts.LoopInterval == 0 {
		opts.LoopInterval = 5 * time.Millisecond
	}
	m := NewManager(opts)
	m.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		m.Shutdown(ctx)
	})
	return m
}

// waitFor polls until cond holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func waitStatus(t *testing.T, m *Manager, id string, want Status) Record {
	t.Helper()
	var got Record
	waitFor(t, "task "+id+" to become "+string(want), func() bool {
		rec, err := m.Get(id)
		if err != nil {
			return false
		}
		got = rec
		return rec.Status == want
	})
	return got
}

// ------------------------------------------------------------------ tests ---

func TestPriorityOrdering(t *testing.T) {
	var mu sync.Mutex
	var order []string

	// Hold the service closed so every task is queued before any is dispatched.
	// Otherwise the first submission starts immediately - at concurrency 1 that
	// makes arrival order, not priority, decide who runs first.
	gate := newFakeGate()
	gate.setReady("svc", false)
	m := newManager(t, Options{Gate: gate})

	handler := func(ctx context.Context, _ action.ContextInput, params map[string]any, _ action.Handle) (any, error) {
		mu.Lock()
		order = append(order, params["name"].(string))
		mu.Unlock()
		return nil, nil
	}

	// Submit in the opposite order to the expected dispatch order, so a pass
	// cannot be an accident of arrival.
	for _, tc := range []struct {
		name string
		prio Priority
	}{
		{"low", PriorityLow},
		{"normal", PriorityNormal},
		{"high", PriorityHigh},
	} {
		if _, err := m.Submit(def("a", "svc"), handler, action.ContextInput{Page: "scenes"},
			map[string]any{"name": tc.name}, tc.prio, SubmitOptions{}); err != nil {
			t.Fatalf("submit %s: %v", tc.name, err)
		}
	}

	gate.setReady("svc", true)

	waitFor(t, "all three tasks to finish", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(order) == 3
	})

	mu.Lock()
	defer mu.Unlock()
	// Concurrency is 1, so execution order is dispatch order.
	want := []string{"high", "normal", "low"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("dispatch order = %v, want %v", order, want)
		}
	}
}

func TestPerServiceConcurrencyLimit(t *testing.T) {
	gate := newFakeGate()
	gate.setConcurrency("limited", 2)
	m := newManager(t, Options{Gate: gate})

	var running, peak atomic.Int64
	release := make(chan struct{})

	handler := func(ctx context.Context, _ action.ContextInput, _ map[string]any, _ action.Handle) (any, error) {
		cur := running.Add(1)
		for {
			p := peak.Load()
			if cur <= p || peak.CompareAndSwap(p, cur) {
				break
			}
		}
		<-release
		running.Add(-1)
		return nil, nil
	}

	for i := 0; i < 6; i++ {
		if _, err := m.Submit(def("a", "limited"), handler, action.ContextInput{Page: "scenes"},
			nil, PriorityNormal, SubmitOptions{}); err != nil {
			t.Fatalf("submit: %v", err)
		}
	}

	waitFor(t, "two tasks to be running", func() bool { return running.Load() == 2 })
	time.Sleep(50 * time.Millisecond) // give a third a chance to wrongly start
	if p := peak.Load(); p > 2 {
		t.Errorf("peak concurrency = %d, want at most 2", p)
	}
	close(release)

	waitFor(t, "all tasks to drain", func() bool {
		return len(m.List(ListFilter{Status: StatusCompleted})) == 6
	})
}

// A controller releases its slot so it can wait on children without
// deadlocking its own service.
func TestMarkControllerReleasesSlot(t *testing.T) {
	gate := newFakeGate()
	gate.setConcurrency("svc", 1)
	m := newManager(t, Options{Gate: gate})

	childRan := make(chan struct{})
	childDone := make(chan struct{})

	child := func(ctx context.Context, _ action.ContextInput, _ map[string]any, _ action.Handle) (any, error) {
		close(childRan)
		<-childDone
		return "child", nil
	}

	parent := func(ctx context.Context, _ action.ContextInput, _ map[string]any, h action.Handle) (any, error) {
		// Without releasing the slot this would deadlock: concurrency is 1 and
		// the parent is holding the only one.
		h.MarkController()
		if _, err := m.Submit(def("child", "svc"), child, action.ContextInput{Page: "scenes"},
			nil, PriorityHigh, SubmitOptions{GroupID: h.ID()}); err != nil {
			return nil, err
		}
		select {
		case <-childRan:
		case <-time.After(3 * time.Second):
			return nil, errors.New("child never ran - controller did not release its slot")
		}
		close(childDone)
		return "parent", nil
	}

	rec, err := m.Submit(def("parent", "svc"), parent, action.ContextInput{Page: "scenes"},
		nil, PriorityNormal, SubmitOptions{})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	got := waitStatus(t, m, rec.ID, StatusCompleted)
	if got.Result != "parent" {
		t.Errorf("result = %v, want parent", got.Result)
	}
	if !got.SkipConcurrency {
		t.Error("controller flag not set")
	}
}

// Marking a queued task must not decrement the counter below zero.
func TestMarkControllerOnNonRunningTaskIsSafe(t *testing.T) {
	gate := newFakeGate()
	m := newManager(t, Options{Gate: gate})

	done := make(chan struct{})
	handler := func(ctx context.Context, _ action.ContextInput, _ map[string]any, h action.Handle) (any, error) {
		h.MarkController()
		h.MarkController() // second call must be a no-op
		close(done)
		return nil, nil
	}

	if _, err := m.Submit(def("a", "svc"), handler, action.ContextInput{Page: "scenes"},
		nil, PriorityNormal, SubmitOptions{}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	<-done

	// A later task must still be dispatchable, which it would not be if the
	// counter had gone negative or been double-decremented.
	rec2, err := m.Submit(def("b", "svc"), func(context.Context, action.ContextInput, map[string]any, action.Handle) (any, error) {
		return "ok", nil
	}, action.ContextInput{Page: "scenes"}, nil, PriorityNormal, SubmitOptions{})
	if err != nil {
		t.Fatalf("submit second: %v", err)
	}
	waitStatus(t, m, rec2.ID, StatusCompleted)
}

func TestCancelQueuedTask(t *testing.T) {
	gate := newFakeGate()
	gate.setReady("svc", false) // keep everything queued
	m := newManager(t, Options{Gate: gate})

	rec, err := m.Submit(def("a", "svc"), func(context.Context, action.ContextInput, map[string]any, action.Handle) (any, error) {
		t.Error("handler ran for a cancelled queued task")
		return nil, nil
	}, action.ContextInput{Page: "scenes"}, nil, PriorityNormal, SubmitOptions{})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	if !m.Cancel(rec.ID) {
		t.Fatal("Cancel returned false for a queued task")
	}

	got, err := m.Get(rec.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != StatusCancelled {
		t.Errorf("status = %q, want cancelled", got.Status)
	}
	if !got.CancelRequested {
		t.Error("cancel_requested not set")
	}
	if got.FinishedAt == nil {
		t.Error("finished_at not set")
	}

	// Terminal tasks cannot be cancelled again; the API turns this into a 404.
	if m.Cancel(rec.ID) {
		t.Error("Cancel succeeded on an already-cancelled task")
	}
}

func TestCancelRunningTaskCascadesToChildren(t *testing.T) {
	gate := newFakeGate()
	gate.setConcurrency("svc", 4)
	m := newManager(t, Options{Gate: gate})

	childStarted := make(chan struct{})
	var once sync.Once

	child := func(ctx context.Context, _ action.ContextInput, _ map[string]any, h action.Handle) (any, error) {
		once.Do(func() { close(childStarted) })
		<-ctx.Done() // wait to be cancelled
		return nil, ctx.Err()
	}

	parentReady := make(chan string, 1)
	parent := func(ctx context.Context, _ action.ContextInput, _ map[string]any, h action.Handle) (any, error) {
		h.MarkController()
		for i := 0; i < 3; i++ {
			if _, err := m.Submit(def("child", "svc"), child, action.ContextInput{Page: "scenes"},
				nil, PriorityHigh, SubmitOptions{GroupID: h.ID()}); err != nil {
				return nil, err
			}
		}
		parentReady <- h.ID()
		<-ctx.Done()
		return nil, ctx.Err()
	}

	rec, err := m.Submit(def("parent", "svc"), parent, action.ContextInput{Page: "scenes"},
		nil, PriorityNormal, SubmitOptions{})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	<-parentReady
	<-childStarted

	if !m.Cancel(rec.ID) {
		t.Fatal("Cancel returned false")
	}

	waitFor(t, "parent and all children to be cancelled", func() bool {
		all := m.List(ListFilter{})
		if len(all) != 4 {
			return false
		}
		for _, r := range all {
			if r.Status != StatusCancelled {
				return false
			}
		}
		return true
	})
}

func TestFailedHandlerRecordsError(t *testing.T) {
	m := newManager(t, Options{Gate: newFakeGate()})

	rec, err := m.Submit(def("a", "svc"), func(context.Context, action.ContextInput, map[string]any, action.Handle) (any, error) {
		return nil, errors.New("boom")
	}, action.ContextInput{Page: "scenes"}, nil, PriorityNormal, SubmitOptions{})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	got := waitStatus(t, m, rec.ID, StatusFailed)
	if got.Error != "boom" {
		t.Errorf("error = %q, want boom", got.Error)
	}
	// result is only surfaced for completed tasks.
	if got.Summary()["result"] != nil {
		t.Error("a failed task reported a result")
	}
}

// A panicking handler must fail its task, not take down the process.
func TestPanicInHandlerFailsTask(t *testing.T) {
	m := newManager(t, Options{Gate: newFakeGate()})

	rec, err := m.Submit(def("a", "svc"), func(context.Context, action.ContextInput, map[string]any, action.Handle) (any, error) {
		panic("handler exploded")
	}, action.ContextInput{Page: "scenes"}, nil, PriorityNormal, SubmitOptions{})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	got := waitStatus(t, m, rec.ID, StatusFailed)
	if got.Error == "" {
		t.Error("panic did not record an error")
	}
}

func TestServiceReadinessGatesDispatch(t *testing.T) {
	gate := newFakeGate()
	gate.setReady("svc", false)
	m := newManager(t, Options{Gate: gate})

	ran := make(chan struct{})
	rec, err := m.Submit(def("a", "svc"), func(context.Context, action.ContextInput, map[string]any, action.Handle) (any, error) {
		close(ran)
		return nil, nil
	}, action.ContextInput{Page: "scenes"}, nil, PriorityNormal, SubmitOptions{})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	select {
	case <-ran:
		t.Fatal("task ran while its service was not ready")
	case <-time.After(80 * time.Millisecond):
	}

	gate.setReady("svc", true)
	waitStatus(t, m, rec.ID, StatusCompleted)
}

// A gate that hangs must not stall the scheduler.
func TestSlowReadinessProbeTimesOut(t *testing.T) {
	gate := newFakeGate()
	gate.readyDelay = time.Hour
	m := newManager(t, Options{Gate: gate, ReadyTimeout: 30 * time.Millisecond})

	if _, err := m.Submit(def("a", "slow"), func(context.Context, action.ContextInput, map[string]any, action.Handle) (any, error) {
		t.Error("task ran despite a hanging readiness probe")
		return nil, nil
	}, action.ContextInput{Page: "scenes"}, nil, PriorityNormal, SubmitOptions{}); err != nil {
		t.Fatalf("submit: %v", err)
	}

	// The scheduler must remain responsive: the probe is bounded and retried.
	waitFor(t, "readiness to be probed more than once", func() bool {
		return gate.readyCalls.Load() > 1
	})
}

func TestHistoryRecordsOnlyTopLevelTerminalTasks(t *testing.T) {
	gate := newFakeGate()
	gate.setConcurrency("svc", 4)
	hist := &recordingHistory{}
	m := newManager(t, Options{Gate: gate, History: hist})

	child := func(context.Context, action.ContextInput, map[string]any, action.Handle) (any, error) {
		return "c", nil
	}
	parent := func(ctx context.Context, _ action.ContextInput, _ map[string]any, h action.Handle) (any, error) {
		h.MarkController()
		for i := 0; i < 3; i++ {
			if _, err := m.Submit(def("child", "svc"), child, action.ContextInput{Page: "scenes"},
				nil, PriorityHigh, SubmitOptions{GroupID: h.ID()}); err != nil {
				return nil, err
			}
		}
		waitForChildren := time.Now().Add(2 * time.Second)
		for time.Now().Before(waitForChildren) {
			done := 0
			for _, r := range m.List(ListFilter{}) {
				if r.GroupID == h.ID() && r.Status.Terminal() {
					done++
				}
			}
			if done == 3 {
				break
			}
			time.Sleep(time.Millisecond)
		}
		return "p", nil
	}

	rec, err := m.Submit(def("parent", "svc"), parent, action.ContextInput{Page: "scenes"},
		nil, PriorityNormal, SubmitOptions{})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitStatus(t, m, rec.ID, StatusCompleted)

	waitFor(t, "history to be written", func() bool { return hist.len() == 1 })

	hist.mu.Lock()
	defer hist.mu.Unlock()
	if hist.entries[0].ID != rec.ID {
		t.Errorf("recorded %s, want the parent %s", hist.entries[0].ID, rec.ID)
	}
	if hist.counts[0] != 3 {
		t.Errorf("child count = %d, want 3", hist.counts[0])
	}
}

func TestSubscribeReceivesLifecycleEvents(t *testing.T) {
	m := newManager(t, Options{Gate: newFakeGate()})

	sub := m.Subscribe(32)
	defer sub.Close()

	rec, err := m.Submit(def("a", "svc"), func(_ context.Context, _ action.ContextInput, _ map[string]any, h action.Handle) (any, error) {
		h.Progress(map[string]any{"pct": 50})
		return "done", nil
	}, action.ContextInput{Page: "scenes"}, nil, PriorityNormal, SubmitOptions{})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	seen := map[EventType]bool{}
	deadline := time.After(3 * time.Second)
	for len(seen) < 4 {
		select {
		case ev := <-sub.Events:
			if ev.Task.ID == rec.ID {
				seen[ev.Type] = true
			}
		case <-deadline:
			t.Fatalf("only saw events %v", seen)
		}
	}

	for _, want := range []EventType{EventQueued, EventStarted, EventProgress, EventCompleted} {
		if !seen[want] {
			t.Errorf("missing event %q", want)
		}
	}
}

// A subscriber that stops reading must not wedge the scheduler.
func TestSlowSubscriberDoesNotBlockScheduler(t *testing.T) {
	m := newManager(t, Options{Gate: newFakeGate()})

	sub := m.Subscribe(1) // deliberately tiny, never drained
	defer sub.Close()

	var last string
	for i := 0; i < 20; i++ {
		rec, err := m.Submit(def("a", "svc"), func(context.Context, action.ContextInput, map[string]any, action.Handle) (any, error) {
			return nil, nil
		}, action.ContextInput{Page: "scenes"}, nil, PriorityNormal, SubmitOptions{})
		if err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
		last = rec.ID
	}

	waitStatus(t, m, last, StatusCompleted)
}

func TestListIsOrderedAndFilterable(t *testing.T) {
	gate := newFakeGate()
	gate.setReady("blocked", false)
	m := newManager(t, Options{Gate: gate})

	noop := func(context.Context, action.ContextInput, map[string]any, action.Handle) (any, error) {
		return nil, nil
	}

	for i := 0; i < 3; i++ {
		if _, err := m.Submit(def("a", "blocked"), noop, action.ContextInput{Page: "scenes"},
			nil, PriorityNormal, SubmitOptions{}); err != nil {
			t.Fatalf("submit: %v", err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	other, err := m.Submit(def("a", "other"), noop, action.ContextInput{Page: "scenes"},
		nil, PriorityNormal, SubmitOptions{})
	if err != nil {
		t.Fatalf("submit other: %v", err)
	}
	// Let the unblocked service drain, so the queued count below is stable.
	waitStatus(t, m, other.ID, StatusCompleted)

	all := m.List(ListFilter{})
	if len(all) != 4 {
		t.Fatalf("listed %d tasks, want 4", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].SubmittedAt > all[i].SubmittedAt {
			t.Error("list is not ordered by submission time")
		}
	}

	if got := m.List(ListFilter{Service: "blocked"}); len(got) != 3 {
		t.Errorf("service filter returned %d, want 3", len(got))
	}
	if got := m.List(ListFilter{Status: StatusQueued}); len(got) != 3 {
		t.Errorf("status filter returned %d, want 3", len(got))
	}
}

func TestGetUnknownTask(t *testing.T) {
	m := newManager(t, Options{Gate: newFakeGate()})
	if _, err := m.Get("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
	if m.Cancel("nope") {
		t.Error("Cancel succeeded for an unknown task")
	}
}

func TestForgetDropsOldTerminalTasks(t *testing.T) {
	m := newManager(t, Options{Gate: newFakeGate()})

	rec, err := m.Submit(def("a", "svc"), func(context.Context, action.ContextInput, map[string]any, action.Handle) (any, error) {
		return nil, nil
	}, action.ContextInput{Page: "scenes"}, nil, PriorityNormal, SubmitOptions{})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitStatus(t, m, rec.ID, StatusCompleted)

	if n := m.Forget(time.Hour); n != 0 {
		t.Errorf("Forget(1h) dropped %d recent tasks", n)
	}
	if n := m.Forget(0); n != 1 {
		t.Errorf("Forget(0) dropped %d tasks, want 1", n)
	}
	if _, err := m.Get(rec.ID); !errors.Is(err, ErrNotFound) {
		t.Error("task survived Forget")
	}
}

func TestSubmitWithoutServiceIsRejected(t *testing.T) {
	m := newManager(t, Options{Gate: newFakeGate()})
	if _, err := m.Submit(def("a", ""), nil, action.ContextInput{Page: "scenes"},
		nil, PriorityNormal, SubmitOptions{}); err == nil {
		t.Error("submitting an action with no service should fail")
	}
}
