package task

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/stashapp/stash/internal/aiserver/action"
	"github.com/stashapp/stash/pkg/logger"
)

// ErrNotFound is returned for an unknown task id.
var ErrNotFound = errors.New("task not found")

// ServiceGate lets the scheduler ask a service whether it can accept work.
//
// The scheduler holds no opinion about what a service is; the service registry
// supplies this. Returning false parks the whole service's queue for this tick.
type ServiceGate interface {
	// MaxConcurrency is how many tasks may run at once. Values below 1 are
	// treated as 1.
	MaxConcurrency(service string) int
	// Ready reports whether the service can accept work now. It is called with
	// a short timeout: a service that blocks must not stall the scheduler.
	Ready(ctx context.Context, service string) bool
}

// HistorySink persists terminal top-level tasks.
type HistorySink interface {
	RecordTask(ctx context.Context, rec Record, childCount int) error
}

// Options configure a Manager.
type Options struct {
	// LoopInterval bounds scheduling latency. It is a ceiling, not a poll
	// period: submissions and completions wake the scheduler immediately, and
	// this only ensures time-based conditions (service readiness backoff) are
	// re-evaluated.
	LoopInterval time.Duration

	// Debug enables verbose scheduling logs.
	Debug bool

	// Gate supplies per-service concurrency and readiness. Optional; without it
	// every service is always ready with a concurrency of 1.
	Gate ServiceGate

	// History persists terminal top-level tasks. Optional.
	History HistorySink

	// ReadyTimeout bounds a single readiness probe. The original used one
	// second and skipped the service on timeout rather than waiting.
	ReadyTimeout time.Duration
}

const (
	defaultLoopInterval = 50 * time.Millisecond
	defaultReadyTimeout = time.Second
)

// Manager schedules AI tasks across per-service priority queues.
//
// Concurrency model: one mutex guards all shared state. Records are stored by
// pointer internally but only ever handed out as copies, so no caller can
// observe a partially updated task.
type Manager struct {
	opts Options
	bus  *eventBus

	mu       sync.Mutex
	tasks    map[string]*Record
	queues   map[string]*priorityQueue
	running  map[string]int
	handlers map[string]action.Handler
	cancels  map[string]context.CancelFunc

	// wake carries scheduling nudges. Buffered depth 1: many submissions
	// collapse into one wakeup, which is all the loop needs.
	wake chan struct{}

	started  bool
	stopping bool
	stop     chan struct{}
	done     chan struct{}

	// inflight tracks running handler goroutines so Shutdown can wait for them.
	inflight sync.WaitGroup
}

// NewManager builds a scheduler. Call Start to begin dispatching.
func NewManager(opts Options) *Manager {
	if opts.LoopInterval <= 0 {
		opts.LoopInterval = defaultLoopInterval
	}
	if opts.ReadyTimeout <= 0 {
		opts.ReadyTimeout = defaultReadyTimeout
	}

	return &Manager{
		opts:     opts,
		bus:      newEventBus(),
		tasks:    make(map[string]*Record),
		queues:   make(map[string]*priorityQueue),
		running:  make(map[string]int),
		handlers: make(map[string]action.Handler),
		cancels:  make(map[string]context.CancelFunc),
		wake:     make(chan struct{}, 1),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Start launches the dispatch loop. Idempotent.
func (m *Manager) Start() {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.mu.Unlock()

	go m.loop()
}

// Shutdown stops dispatching, cancels everything outstanding, and waits for
// running handlers to return.
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	if !m.started || m.stopping {
		m.mu.Unlock()
		return
	}
	m.stopping = true
	ids := make([]string, 0, len(m.tasks))
	for id := range m.tasks {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	close(m.stop)

	for _, id := range ids {
		m.Cancel(id)
	}

	// Wait for the dispatch loop, then for handlers still winding down.
	select {
	case <-m.done:
	case <-ctx.Done():
	}

	waited := make(chan struct{})
	go func() {
		m.inflight.Wait()
		close(waited)
	}()
	select {
	case <-waited:
	case <-ctx.Done():
		logger.Warn("AI task manager shut down with handlers still running")
	}
}

// Subscribe returns a stream of task events. Callers must Close it.
func (m *Manager) Subscribe(buffer int) *Subscription { return m.bus.subscribe(buffer) }

// SubscriberCount reports active subscribers, for diagnostics.
func (m *Manager) SubscriberCount() int { return m.bus.count() }

// SubmitOptions adjust a submission.
type SubmitOptions struct {
	// GroupID marks this task as a child of another, for fan-out batches.
	// Children are excluded from history and cancelled with their parent.
	GroupID string
}

// Submit queues a task and returns a snapshot of it.
func (m *Manager) Submit(def action.Definition, handler action.Handler, ctx action.ContextInput, params map[string]any, priority Priority, opts SubmitOptions) (Record, error) {
	service := def.Service
	if service == "" {
		return Record{}, errors.New("cannot determine service name for task")
	}

	// A fingerprint failure must not block execution; deduplication is a
	// nicety, running the task is not.
	ctxKey, paramsKey, err := fingerprint(ctx, params)
	if err != nil {
		ctxKey, paramsKey = "", ""
	}

	rec := &Record{
		ID:               uuid.NewString(),
		ActionID:         def.ID,
		Service:          service,
		Priority:         priority,
		Status:           StatusQueued,
		SubmittedAt:      nowSeconds(),
		Context:          ctx,
		Params:           params,
		GroupID:          opts.GroupID,
		dedupeContextKey: ctxKey,
		dedupeParamsKey:  paramsKey,
	}

	m.mu.Lock()
	if m.stopping {
		m.mu.Unlock()
		return Record{}, errors.New("task manager is shutting down")
	}
	m.tasks[rec.ID] = rec
	m.handlers[rec.ID] = handler
	q, ok := m.queues[service]
	if !ok {
		q = newPriorityQueue()
		m.queues[service] = q
	}
	q.Push(priority, rec.ID)
	if _, ok := m.running[service]; !ok {
		m.running[service] = 0
	}
	snapshot := *rec
	m.mu.Unlock()

	if m.opts.Debug {
		logger.Debugf("AI task submit service=%s id=%s priority=%s group=%s",
			service, rec.ID, priority, opts.GroupID)
	}

	m.publish(EventQueued, snapshot, nil)
	m.nudge()
	return snapshot, nil
}

// FindDuplicate returns an active task with the same action, service, context
// and parameters, if one exists.
//
// "Active" means queued, running or streaming - a completed task does not block
// a resubmission.
func (m *Manager) FindDuplicate(def action.Definition, ctx action.ContextInput, params map[string]any) (Record, bool) {
	if def.Service == "" {
		return Record{}, false
	}
	wantCtx, wantParams, err := fingerprint(ctx, params)
	if err != nil {
		return Record{}, false
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Iterate deterministically so that with several matches the same one is
	// always reported; map order would make the 409 payload flap.
	for _, rec := range m.sortedTasksLocked() {
		if rec.ActionID != def.ID || rec.Service != def.Service {
			continue
		}
		if !rec.Status.Active() {
			continue
		}
		if rec.dedupeContextKey == wantCtx && rec.dedupeParamsKey == wantParams {
			return *rec, true
		}
	}
	return Record{}, false
}

// Get returns a copy of a task's state.
func (m *Manager) Get(id string) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	rec, ok := m.tasks[id]
	if !ok {
		return Record{}, ErrNotFound
	}
	return *rec, nil
}

// ListFilter narrows List.
type ListFilter struct {
	Service string
	Status  Status
}

// List returns tasks ordered by submission time, oldest first.
func (m *Manager) List(f ListFilter) []Record {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := []Record{}
	for _, rec := range m.sortedTasksLocked() {
		if f.Service != "" && rec.Service != f.Service {
			continue
		}
		if f.Status != "" && rec.Status != f.Status {
			continue
		}
		out = append(out, *rec)
	}
	return out
}

// sortedTasksLocked returns every task ordered by submission time then id.
// Caller must hold the mutex.
func (m *Manager) sortedTasksLocked() []*Record {
	out := make([]*Record, 0, len(m.tasks))
	for _, rec := range m.tasks {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SubmittedAt != out[j].SubmittedAt {
			return out[i].SubmittedAt < out[j].SubmittedAt
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Cancel cancels a task and cascades to its children.
//
// A queued task is cancelled immediately. A running task is flagged and its
// context cancelled; it becomes cancelled once the handler returns. Terminal
// tasks report false, which the API surfaces as 404.
func (m *Manager) Cancel(id string) bool {
	type pending struct {
		rec Record
	}
	var toPublish []pending

	m.mu.Lock()

	rec, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return false
	}

	// Collect the whole subtree first so cancellation is applied under one lock
	// acquisition and cannot interleave with new children being added.
	var cancelled bool
	var walk func(taskID string)
	seen := map[string]bool{}
	walk = func(taskID string) {
		if seen[taskID] {
			return
		}
		seen[taskID] = true

		t, exists := m.tasks[taskID]
		if !exists {
			return
		}

		switch t.Status {
		case StatusQueued:
			if q := m.queues[t.Service]; q != nil {
				q.Remove(taskID)
			}
			t.Status = StatusCancelled
			t.CancelRequested = true
			f := nowSeconds()
			t.FinishedAt = &f
			delete(m.handlers, taskID)
			if cancel := m.cancels[taskID]; cancel != nil {
				cancel()
				delete(m.cancels, taskID)
			}
			toPublish = append(toPublish, pending{rec: *t})
			cancelled = true

		case StatusRunning, StatusStreaming:
			t.CancelRequested = true
			if cancel := m.cancels[taskID]; cancel != nil {
				cancel()
			}
			cancelled = true
		}

		for childID, child := range m.tasks {
			if child.GroupID == taskID {
				walk(childID)
			}
		}
	}
	walk(rec.ID)

	m.mu.Unlock()

	for _, p := range toPublish {
		m.publish(EventCancelled, p.rec, nil)
		m.persistHistory(p.rec)
	}
	if cancelled {
		m.nudge()
	}
	return cancelled
}

// nudge wakes the dispatch loop without blocking.
func (m *Manager) nudge() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// loop dispatches ready work.
//
// It is condition-driven with a ticker floor rather than a busy poll: the
// ticker exists only because service readiness backoff is time-based and needs
// re-evaluating without an external event.
func (m *Manager) loop() {
	defer close(m.done)

	ticker := time.NewTicker(m.opts.LoopInterval)
	defer ticker.Stop()

	for {
		m.dispatchReady()

		select {
		case <-m.stop:
			return
		case <-m.wake:
		case <-ticker.C:
		}
	}
}

// dispatchReady starts as many tasks as capacity and readiness allow.
func (m *Manager) dispatchReady() {
	for {
		if !m.dispatchOne() {
			return
		}
	}
}

// dispatchOne starts at most one task, reporting whether it did.
func (m *Manager) dispatchOne() bool {
	m.mu.Lock()

	if m.stopping {
		m.mu.Unlock()
		return false
	}

	// Deterministic service order keeps scheduling reproducible under test.
	services := make([]string, 0, len(m.queues))
	for svc, q := range m.queues {
		if q.Len() > 0 {
			services = append(services, svc)
		}
	}
	sort.Strings(services)

	for _, svc := range services {
		q := m.queues[svc]

		limit := 1
		if m.opts.Gate != nil {
			limit = max(1, m.opts.Gate.MaxConcurrency(svc))
		}

		// The concurrency check happens BEFORE popping, so a controller task
		// that would not consume a slot still waits behind a saturated service.
		// That is arguably a flaw, but changing it changes scheduling order, so
		// it is preserved deliberately.
		if m.running[svc] >= limit {
			if m.opts.Debug {
				logger.Debugf("AI task skip service=%s running=%d limit=%d queued=%d",
					svc, m.running[svc], limit, q.Len())
			}
			continue
		}

		// Readiness is probed without the lock: a service that blocks must not
		// freeze the scheduler or every other service's queue.
		gate := m.opts.Gate
		m.mu.Unlock()

		if gate != nil && !m.serviceReady(gate, svc) {
			m.mu.Lock()
			continue
		}

		m.mu.Lock()
		if m.stopping {
			m.mu.Unlock()
			return false
		}

		// Re-check under the lock: capacity may have gone while probing.
		q = m.queues[svc]
		if q == nil || q.Len() == 0 || m.running[svc] >= limit {
			continue
		}

		id := q.Pop()
		if id == "" {
			continue
		}
		rec, ok := m.tasks[id]
		if !ok || rec.Status != StatusQueued {
			continue
		}

		handler := m.handlers[id]
		if handler == nil {
			rec.Status = StatusFailed
			rec.Error = "Action no longer available"
			f := nowSeconds()
			rec.FinishedAt = &f
			snapshot := *rec
			m.mu.Unlock()
			m.publish(EventFailed, snapshot, nil)
			m.persistHistory(snapshot)
			return true
		}

		if !rec.SkipConcurrency {
			m.running[svc]++
		}
		rec.Status = StatusRunning
		s := nowSeconds()
		rec.StartedAt = &s

		taskCtx, cancel := context.WithCancel(context.Background())
		m.cancels[id] = cancel

		snapshot := *rec
		m.inflight.Add(1)
		m.mu.Unlock()

		m.publish(EventStarted, snapshot, nil)
		go m.run(taskCtx, id, handler)
		return true
	}

	m.mu.Unlock()
	return false
}

// serviceReady probes a service with a bounded timeout.
func (m *Manager) serviceReady(gate ServiceGate, service string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), m.opts.ReadyTimeout)
	defer cancel()

	// Run the probe in its own goroutine so a gate that ignores ctx cannot
	// outlast the timeout from the scheduler's point of view.
	result := make(chan bool, 1)
	go func() { result <- gate.Ready(ctx, service) }()

	select {
	case ready := <-result:
		return ready
	case <-ctx.Done():
		if m.opts.Debug {
			logger.Debugf("AI task readiness probe timed out service=%s", service)
		}
		return false
	}
}

// run executes a handler and records the outcome.
func (m *Manager) run(ctx context.Context, id string, handler action.Handler) {
	defer m.inflight.Done()

	handle := &taskHandle{manager: m, id: id, ctx: ctx}

	m.mu.Lock()
	rec, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	inCtx, params, service := rec.Context, rec.Params, rec.Service
	m.mu.Unlock()

	var (
		result any
		err    error
	)

	func() {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("panic: %v", p)
				logger.Errorf("panic in AI task %s: %v\n%s", id, p, debug.Stack())
			}
		}()
		result, err = handler(ctx, inCtx, params, handle)
	}()

	m.mu.Lock()
	rec, ok = m.tasks[id]
	if !ok {
		m.releaseSlotLocked(service, id)
		m.mu.Unlock()
		m.nudge()
		return
	}

	f := nowSeconds()
	rec.FinishedAt = &f

	var event EventType
	switch {
	case rec.CancelRequested || errors.Is(err, context.Canceled):
		// Cancellation wins over an error: a handler that returns
		// context.Canceled was cancelled, not broken.
		rec.Status = StatusCancelled
		event = EventCancelled
	case err != nil:
		rec.Status = StatusFailed
		rec.Error = err.Error()
		event = EventFailed
	default:
		rec.Status = StatusCompleted
		rec.Result = result
		event = EventCompleted
	}

	m.releaseSlotLocked(service, id)
	snapshot := *rec
	m.mu.Unlock()

	m.publish(event, snapshot, nil)
	m.persistHistory(snapshot)
	m.nudge()
}

// releaseSlotLocked frees a task's concurrency slot and per-task bookkeeping.
// Caller must hold the mutex.
func (m *Manager) releaseSlotLocked(service, id string) {
	if rec, ok := m.tasks[id]; ok && !rec.SkipConcurrency {
		if n := m.running[service]; n > 0 {
			m.running[service] = n - 1
		}
	}
	delete(m.handlers, id)
	if cancel := m.cancels[id]; cancel != nil {
		cancel()
		delete(m.cancels, id)
	}
}

// publish emits an event to subscribers.
func (m *Manager) publish(t EventType, rec Record, extra map[string]any) {
	m.bus.publish(Event{Type: t, Task: rec, Extra: extra})
}

// PublishProgress emits a progress event for a task by id.
//
// This exists for work whose body runs outside the handler's goroutine - a
// plugin in the host process signals progress asynchronously, so there is no
// Handle in scope to call. An unknown id is ignored rather than an error: a
// signal for a task that has just finished is a race, not a fault.
func (m *Manager) PublishProgress(id string, payload map[string]any) {
	m.mu.Lock()
	rec, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	snapshot := *rec
	m.mu.Unlock()

	m.publish(EventProgress, snapshot, payload)
}

// persistHistory records a terminal top-level task.
//
// Children are excluded to keep history readable: a fan-out of 500 chunks
// should appear as one row, not 501.
func (m *Manager) persistHistory(rec Record) {
	if m.opts.History == nil || rec.GroupID != "" || !rec.Status.Terminal() {
		return
	}

	m.mu.Lock()
	childCount := 0
	for _, t := range m.tasks {
		if t.GroupID == rec.ID {
			childCount++
		}
	}
	m.mu.Unlock()

	if err := m.opts.History.RecordTask(context.Background(), rec, childCount); err != nil {
		// History is diagnostic; losing a row must not disturb scheduling.
		logger.Errorf("recording AI task history for %s: %v", rec.ID, err)
	}
}

// Forget drops terminal tasks from memory. The Python server never did this and
// grew without bound; history persistence covers the long-term record.
func (m *Manager) Forget(olderThan time.Duration) int {
	cutoff := nowSeconds() - olderThan.Seconds()

	m.mu.Lock()
	defer m.mu.Unlock()

	removed := 0
	for id, rec := range m.tasks {
		if !rec.Status.Terminal() || rec.FinishedAt == nil || *rec.FinishedAt > cutoff {
			continue
		}
		delete(m.tasks, id)
		delete(m.handlers, id)
		delete(m.cancels, id)
		removed++
	}
	return removed
}

func nowSeconds() float64 {
	return float64(time.Now().UnixNano()) / float64(time.Second)
}

// taskHandle is the narrow view of a task handed to its handler.
type taskHandle struct {
	manager *Manager
	id      string
	ctx     context.Context
}

func (h *taskHandle) ID() string { return h.id }

func (h *taskHandle) Params() map[string]any {
	h.manager.mu.Lock()
	defer h.manager.mu.Unlock()
	if rec, ok := h.manager.tasks[h.id]; ok {
		return rec.Params
	}
	return nil
}

func (h *taskHandle) Progress(payload map[string]any) {
	h.manager.mu.Lock()
	rec, ok := h.manager.tasks[h.id]
	if !ok {
		h.manager.mu.Unlock()
		return
	}
	snapshot := *rec
	h.manager.mu.Unlock()

	h.manager.publish(EventProgress, snapshot, payload)
}

func (h *taskHandle) Cancelled() bool {
	if h.ctx.Err() != nil {
		return true
	}
	h.manager.mu.Lock()
	defer h.manager.mu.Unlock()
	if rec, ok := h.manager.tasks[h.id]; ok {
		return rec.CancelRequested
	}
	return false
}

// MarkController releases the task's concurrency slot so a coordinator can wait
// on its children without deadlocking its own service.
//
// The slot is only returned if the task is actually running - marking a queued
// task must not decrement the counter, or it goes negative.
func (h *taskHandle) MarkController() {
	h.manager.mu.Lock()
	rec, ok := h.manager.tasks[h.id]
	if !ok || rec.SkipConcurrency {
		h.manager.mu.Unlock()
		return
	}

	wasRunning := rec.Status == StatusRunning || rec.Status == StatusStreaming
	rec.SkipConcurrency = true
	if wasRunning {
		if n := h.manager.running[rec.Service]; n > 0 {
			h.manager.running[rec.Service] = n - 1
		}
	}
	h.manager.mu.Unlock()

	h.manager.nudge()
}
