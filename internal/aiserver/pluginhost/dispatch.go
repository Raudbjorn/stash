package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/stashapp/stash/internal/aiserver/action"
	"github.com/stashapp/stash/internal/aiserver/hostconn"
	"github.com/stashapp/stash/internal/aiserver/recommend"
)

// cancelTimeout bounds the out-of-band cancel call. Short on purpose: the task
// is already cancelled from the user's point of view, and this is only telling
// the host so it can stop sooner.
const cancelTimeout = 5 * time.Second

// Dispatch: a Go task, scheduled by Go, whose body happens to run in Python.
//
// The scheduler is unaware of any of this. Plugin actions are ordinary
// action.Handlers, so priority, per-service concurrency, deduplication and
// cancellation all behave identically whether the work is native or hosted.

// actionHandler returns a handler that runs an action in the plugin host.
//
// generation is captured deliberately: if the host has been recycled since the
// action was registered, the registration is stale and the task must fail
// rather than run against a host that never imported the plugin.
func (m *Manager) actionHandler(generation int, plugin, actionID string) action.Handler {
	return func(ctx context.Context, in action.ContextInput, params map[string]any, handle action.Handle) (any, error) {
		conn, current, err := m.connection()
		if err != nil {
			return nil, err
		}
		if current != generation {
			return nil, fmt.Errorf("%w: %s was registered by an earlier host", ErrHostUnavailable, actionID)
		}

		// The invocation is keyed by task id so progress signals coming back
		// from Python land on the right task with no correlation table.
		invocationID := handle.ID()

		ctx, cancel := context.WithCancelCause(ctx)
		defer cancel(nil)

		m.beginInvocation(invocationID, &invocation{
			plugin:     plugin,
			actionID:   actionID,
			generation: generation,
			cancel:     cancel,
			handle:     handle,
		})
		defer m.endInvocation(invocationID)

		m.markRunning(plugin)
		defer m.markRunning("")

		// Cancellation has to travel as its own call: the invocation occupies a
		// reply slot for its whole duration, and a plugin that ignores its
		// cancel flag would otherwise never see the request at all.
		done := make(chan struct{})
		defer close(done)
		go m.forwardCancel(ctx, conn, invocationID, done)

		result, err := conn.InvokeAction(ctx, invocationID, actionID, contextToMap(in), params)
		if err != nil {
			return nil, m.explain(ctx, generation, actionID, err)
		}
		return result, nil
	}
}

// recommenderHandler returns a handler that runs a recommender in the host.
func (m *Manager) recommenderHandler(generation int, plugin, recommenderID string) recommend.Handler {
	return func(ctx context.Context, req recommend.Request) (recommend.Result, error) {
		conn, current, err := m.connection()
		if err != nil {
			return recommend.Result{}, err
		}
		if current != generation {
			return recommend.Result{}, fmt.Errorf("%w: %s was registered by an earlier host", ErrHostUnavailable, recommenderID)
		}

		// Recommendations are synchronous requests rather than scheduled tasks,
		// so there is no task id to key on; a request-scoped id keeps the host's
		// bookkeeping uniform.
		invocationID := "rec-" + recommenderID

		payload := map[string]any{
			"context":       string(req.Context),
			"recommenderId": req.RecommenderID,
			"config":        req.Config,
			"seedSceneIds":  req.SeedSceneIDs,
			"offset":        req.Offset,
		}
		if req.Limit != nil {
			payload["limit"] = *req.Limit
		}

		raw, err := conn.InvokeRecommender(ctx, invocationID, recommenderID, payload)
		if err != nil {
			return recommend.Result{}, m.explain(ctx, generation, recommenderID, err)
		}

		result, err := recommendResult(raw)
		if err != nil {
			return recommend.Result{}, fmt.Errorf("recommender %s returned an unusable result: %w", recommenderID, err)
		}
		return result, nil
	}
}

// forwardCancel tells the host to stop an invocation when its context ends.
//
// Best-effort by design: a plugin that never checks its cancel flag will run to
// completion regardless, and the task is already marked cancelled from the
// user's point of view.
func (m *Manager) forwardCancel(ctx context.Context, conn *hostconn.Conn, invocationID string, done <-chan struct{}) {
	select {
	case <-done:
		return
	case <-ctx.Done():
	}

	// A fresh context: the one that just expired cannot carry the cancel.
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cancelTimeout)
	defer cancel()

	if err := conn.Cancel(cancelCtx, invocationID); err != nil {
		// Nothing useful to do: the host may already be gone, which is itself a
		// form of the cancellation succeeding.
		return
	}
}

// explain turns a transport error into something worth showing a user.
//
// A dead host and a plugin that raised are very different problems, and the
// generic message from the transport conflates them.
func (m *Manager) explain(ctx context.Context, generation int, id string, err error) error {
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return cause
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}

	if sup := m.supervisor(); sup != nil && sup.Status().Generation != generation {
		return fmt.Errorf("%w: it stopped while %s was running", ErrHostUnavailable, id)
	}
	return fmt.Errorf("%s failed: %w", id, err)
}

func (m *Manager) beginInvocation(id string, inv *invocation) {
	m.mu.Lock()
	m.inflight[id] = inv
	m.mu.Unlock()
}

func (m *Manager) endInvocation(id string) {
	m.mu.Lock()
	delete(m.inflight, id)
	m.mu.Unlock()
}

// handleFor returns the task handle of a running invocation, or nil.
//
// This is how a plugin declares itself a coordinator from inside its own
// handler: the Bridge has no handle in scope, so it looks the invocation up.
func (m *Manager) handleFor(invocationID string) action.Handle {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if inv, ok := m.inflight[invocationID]; ok {
		return inv.handle
	}
	return nil
}

// InFlight reports how many invocations are running, for diagnostics.
func (m *Manager) InFlight() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.inflight)
}
