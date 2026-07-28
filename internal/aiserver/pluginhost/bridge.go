// Package pluginhost joins the supervised Python process to the rest of the AI
// server: it loads plugins into the host, publishes what they register into the
// Go registries, and serves the callbacks they make.
//
// The split it enforces is the one the whole design rests on. Go owns
// everything durable and network-facing - the database, settings, Stash's API,
// the scheduler. Python owns only the execution of plugin code. Every capability
// a plugin has is therefore a method on Bridge, and can be reasoned about here
// rather than audited across a Python codebase.
package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/stashapp/stash/internal/aiserver/action"
	"github.com/stashapp/stash/internal/aiserver/hostconn"
	"github.com/stashapp/stash/internal/aiserver/store"
	"github.com/stashapp/stash/internal/aiserver/task"
)

// ErrNoDatabase is returned when a plugin uses the database before the server
// has one open.
var ErrNoDatabase = errors.New("the AI database is not available")

// ErrNoGraphQL is returned when GraphQL access is attempted before Stash has
// registered its handler.
var ErrNoGraphQL = errors.New("Stash's GraphQL API is not available")

// bridge implements hostconn.Bridge.
//
// It reads its collaborators through the Manager rather than capturing them,
// because the database is replaced whenever the AI server restarts while the
// host process may outlive that.
type bridge struct{ m *Manager }

// Query runs a read-only statement inside the plugin's own table namespace.
func (b *bridge) Query(ctx context.Context, plugin, sql string, params []any) ([]map[string]any, error) {
	db := b.m.database()
	if db == nil {
		return nil, ErrNoDatabase
	}
	return db.PluginQuery(ctx, plugin, sql, params)
}

// Execute runs a write statement inside the plugin's own table namespace.
func (b *bridge) Execute(ctx context.Context, plugin, sql string, params []any) (int64, error) {
	db := b.m.database()
	if db == nil {
		return 0, ErrNoDatabase
	}
	return db.PluginExecute(ctx, plugin, sql, params)
}

// ExecuteBatch runs several writes in one transaction.
func (b *bridge) ExecuteBatch(ctx context.Context, plugin string, statements []hostconn.BatchStatement) (int64, error) {
	db := b.m.database()
	if db == nil {
		return 0, ErrNoDatabase
	}

	sqls := make([]string, len(statements))
	params := make([][]any, len(statements))
	for i, statement := range statements {
		sqls[i] = statement.SQL
		params[i] = statement.Params
	}
	return db.PluginExecuteBatch(ctx, plugin, sqls, params)
}

// MarkController releases a coordinating task's concurrency slot.
//
// The slot has to come back before the parent waits on its children, or a
// service with concurrency 1 deadlocks against itself: the parent holds the
// only slot while blocking on children that need it.
func (b *bridge) MarkController(_ context.Context, plugin, invocationID string) error {
	handle := b.m.handleFor(invocationID)
	if handle == nil {
		return fmt.Errorf("no running task %s", invocationID)
	}
	handle.MarkController()
	return nil
}

// GetSettings returns the plugin's settings as key to effective value.
//
// Effective, not stored: an unset setting resolves to its declared default, so
// plugins never have to reimplement that rule.
func (b *bridge) GetSettings(ctx context.Context, plugin string) (map[string]any, error) {
	db := b.m.database()
	if db == nil {
		return nil, ErrNoDatabase
	}

	settings, err := db.ListSettings(ctx, plugin)
	if err != nil {
		return nil, err
	}

	out := make(map[string]any, len(settings))
	for _, setting := range settings {
		out[setting.Key] = setting.Effective()
	}
	return out, nil
}

// SetSetting stores a plugin setting.
func (b *bridge) SetSetting(ctx context.Context, plugin, key string, value any) error {
	db := b.m.database()
	if db == nil {
		return ErrNoDatabase
	}

	// A plugin writing to the server's own settings would be a privilege
	// escalation: those include the paths and limits the server runs under.
	if plugin == store.SystemPluginName {
		return fmt.Errorf("plugins may not write server settings")
	}

	_, err := db.SetPluginSetting(ctx, plugin, key, value)
	return err
}

// StashGraphQL runs a query against Stash's own API on the plugin's behalf.
//
// The request is executed in-process against the registered handler, so it
// carries no credential and never touches a socket. This is strictly better
// than the standalone server, which was handed a real API key at bootstrap and
// passed it to every plugin.
func (b *bridge) StashGraphQL(ctx context.Context, plugin, query string, variables map[string]any) (map[string]any, error) {
	handler := b.m.graphQL()
	if handler == nil {
		return nil, ErrNoGraphQL
	}

	body, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": variables,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/graphql", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		return nil, fmt.Errorf("GraphQL request failed with status %d: %s",
			recorder.Code, truncate(recorder.Body.String(), 500))
	}

	var result map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("decode GraphQL response: %w", err)
	}
	return result, nil
}

// SubmitTask queues a child task on the plugin's behalf.
//
// This is how fan-out works now that the scheduler lives in Go: a plugin that
// wants to process 500 scenes submits 500 children and marks itself a
// controller, which releases its concurrency slot so it can wait on them
// without deadlocking its own service.
func (b *bridge) SubmitTask(ctx context.Context, plugin, actionID string, invocationContext, params map[string]any, parentInvocationID, priority string) (string, error) {
	manager := b.m.tasks()
	if manager == nil {
		return "", errors.New("the task scheduler is not running")
	}

	in := action.ContextFromMap(invocationContext)

	registration, ok := b.m.actions().Resolve(actionID, in)
	if !ok {
		return "", fmt.Errorf("unknown action: %s", actionID)
	}

	// An unrecognised priority falls back to normal rather than failing: a
	// typo in a plugin should not lose the work.
	parsed, _ := task.ParsePriority(priority)

	record, err := manager.Submit(registration.Definition, registration.Handler, in, params, parsed, task.SubmitOptions{
		GroupID: parentInvocationID,
	})
	if err != nil {
		return "", err
	}
	return record.ID, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
