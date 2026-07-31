package hostconn

import (
	"context"
	"encoding/json"
	"time"

	"github.com/godbus/dbus/v5"
)

// The Bridge is exported by Go on the same connection the host serves its own
// interfaces on. That bidirectionality is the point of the transport: a plugin
// can reach Stash without the host process holding any credential of its own.

// bridgeExport adapts a Bridge to D-Bus.
//
// Every method returns (result, *dbus.Error). Errors are namespaced so the
// Python side can distinguish a rejected request from a transport failure.
type bridgeExport struct {
	bridge Bridge
	// timeout bounds a single bridge call so a wedged handler cannot hold a
	// host worker thread indefinitely.
	timeout time.Duration
}

const (
	errBridgeFailed  = "dev.stash.ai.Error.BridgeFailed"
	errBridgeDecode  = "dev.stash.ai.Error.BadRequest"
	defaultBridgeTTL = 30 * time.Second
	// batchTTL is longer because a batch is a whole transaction; a plugin
	// writing tens of thousands of rows should not be cut off mid-way.
	batchTTL = 5 * time.Minute
)

func (c *Conn) exportBridge(bridge Bridge) error {
	export := &bridgeExport{bridge: bridge, timeout: defaultBridgeTTL}
	return c.conn.Export(export, dbus.ObjectPath(bridgePath), ifaceBridge)
}

func (b *bridgeExport) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), b.timeout)
}

func decodeParams(raw string) ([]any, *dbus.Error) {
	if raw == "" {
		return nil, nil
	}
	var params []any
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return nil, dbus.NewError(errBridgeDecode, []any{"invalid params: " + err.Error()})
	}
	return params, nil
}

// Query runs a read query against the AI database on a plugin's behalf.
func (b *bridgeExport) Query(plugin, sql, paramsJSON string) (string, *dbus.Error) {
	params, derr := decodeParams(paramsJSON)
	if derr != nil {
		return "", derr
	}

	ctx, cancel := b.ctx()
	defer cancel()

	rows, err := b.bridge.Query(ctx, plugin, sql, params)
	if err != nil {
		return "", dbus.NewError(errBridgeFailed, []any{err.Error()})
	}

	// Never null: the Python side iterates the result directly.
	if rows == nil {
		rows = []map[string]any{}
	}
	encoded, err := json.Marshal(rows)
	if err != nil {
		return "", dbus.NewError(errBridgeFailed, []any{err.Error()})
	}
	return string(encoded), nil
}

// Execute runs a write statement on a plugin's behalf.
func (b *bridgeExport) Execute(plugin, sql, paramsJSON string) (uint64, *dbus.Error) {
	params, derr := decodeParams(paramsJSON)
	if derr != nil {
		return 0, derr
	}

	ctx, cancel := b.ctx()
	defer cancel()

	affected, err := b.bridge.Execute(ctx, plugin, sql, params)
	if err != nil {
		return 0, dbus.NewError(errBridgeFailed, []any{err.Error()})
	}
	if affected < 0 {
		affected = 0
	}
	return uint64(affected), nil
}

// ExecuteBatch runs several statements in one transaction.
func (b *bridgeExport) ExecuteBatch(plugin, batchJSON string) (uint64, *dbus.Error) {
	var statements []BatchStatement
	if batchJSON != "" {
		if err := json.Unmarshal([]byte(batchJSON), &statements); err != nil {
			return 0, dbus.NewError(errBridgeDecode, []any{"invalid batch: " + err.Error()})
		}
	}
	if len(statements) == 0 {
		return 0, nil
	}

	// A batch holds a write transaction open, so it gets its own budget rather
	// than the per-call default.
	ctx, cancel := context.WithTimeout(context.Background(), batchTTL)
	defer cancel()

	affected, err := b.bridge.ExecuteBatch(ctx, plugin, statements)
	if err != nil {
		return 0, dbus.NewError(errBridgeFailed, []any{err.Error()})
	}
	if affected < 0 {
		affected = 0
	}
	return uint64(affected), nil
}

// MarkController releases the calling task's concurrency slot.
func (b *bridgeExport) MarkController(plugin, invocationID string) *dbus.Error {
	ctx, cancel := b.ctx()
	defer cancel()

	if err := b.bridge.MarkController(ctx, plugin, invocationID); err != nil {
		return dbus.NewError(errBridgeFailed, []any{err.Error()})
	}
	return nil
}

// GetSettings returns a plugin's settings as key to effective value.
func (b *bridgeExport) GetSettings(plugin string) (string, *dbus.Error) {
	ctx, cancel := b.ctx()
	defer cancel()

	settings, err := b.bridge.GetSettings(ctx, plugin)
	if err != nil {
		return "", dbus.NewError(errBridgeFailed, []any{err.Error()})
	}
	if settings == nil {
		settings = map[string]any{}
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return "", dbus.NewError(errBridgeFailed, []any{err.Error()})
	}
	return string(encoded), nil
}

// SetSetting stores a plugin setting.
func (b *bridgeExport) SetSetting(plugin, key, valueJSON string) *dbus.Error {
	var value any
	if valueJSON != "" {
		if err := json.Unmarshal([]byte(valueJSON), &value); err != nil {
			return dbus.NewError(errBridgeDecode, []any{"invalid value: " + err.Error()})
		}
	}

	ctx, cancel := b.ctx()
	defer cancel()

	if err := b.bridge.SetSetting(ctx, plugin, key, value); err != nil {
		return dbus.NewError(errBridgeFailed, []any{err.Error()})
	}
	return nil
}

// StashGraphQL runs a query against Stash's own API.
//
// Executed in-process by Go, so no API key ever exists in the Python process to
// be read or leaked - strictly better than the standalone server, which was
// handed a shared key.
func (b *bridgeExport) StashGraphQL(plugin, query, varsJSON string) (string, *dbus.Error) {
	var variables map[string]any
	if varsJSON != "" {
		if err := json.Unmarshal([]byte(varsJSON), &variables); err != nil {
			return "", dbus.NewError(errBridgeDecode, []any{"invalid variables: " + err.Error()})
		}
	}

	ctx, cancel := b.ctx()
	defer cancel()

	result, err := b.bridge.StashGraphQL(ctx, plugin, query, variables)
	if err != nil {
		return "", dbus.NewError(errBridgeFailed, []any{err.Error()})
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", dbus.NewError(errBridgeFailed, []any{err.Error()})
	}
	return string(encoded), nil
}

// SubmitTask queues a child task.
//
// Fan-out has to round-trip through here because the scheduler lives in Go now;
// the SDK's submit_child_tasks preserves the shape plugins already expect.
func (b *bridgeExport) SubmitTask(plugin, actionID, contextJSON, paramsJSON, parentInvocationID, priority string) (string, *dbus.Error) {
	var invocationContext, params map[string]any
	if contextJSON != "" {
		if err := json.Unmarshal([]byte(contextJSON), &invocationContext); err != nil {
			return "", dbus.NewError(errBridgeDecode, []any{"invalid context: " + err.Error()})
		}
	}
	if paramsJSON != "" {
		if err := json.Unmarshal([]byte(paramsJSON), &params); err != nil {
			return "", dbus.NewError(errBridgeDecode, []any{"invalid params: " + err.Error()})
		}
	}

	ctx, cancel := b.ctx()
	defer cancel()

	id, err := b.bridge.SubmitTask(ctx, plugin, actionID, invocationContext, params, parentInvocationID, priority)
	if err != nil {
		return "", dbus.NewError(errBridgeFailed, []any{err.Error()})
	}
	return id, nil
}
