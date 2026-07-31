// Package hostconn is the transport between Stash and the Python plugin host.
//
// Everything is behind the Conn interface so the transport stays replaceable:
// D-Bus buys real introspection tooling and a language-neutral IDL, at the cost
// of a PyGObject dependency. If that trade ever stops paying, swapping in
// JSON-RPC over stdio is a change to this package alone.
package hostconn

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// Object paths and interface names, matching interfaces.xml.
const (
	hostPath   = "/dev/stash/ai/Host"
	bridgePath = "/dev/stash/ai/Bridge"

	ifaceHost     = "dev.stash.ai.Host1"
	ifacePlugins  = "dev.stash.ai.Plugins1"
	ifaceDispatch = "dev.stash.ai.Dispatch1"
	ifaceBridge   = "dev.stash.ai.Bridge1"

	// Protocol is bumped on any breaking change to the IDL; a mismatch fails
	// the handshake rather than producing confusing errors later.
	Protocol uint32 = 1
)

// Bridge is what the host may call back into. Implemented by the AI server.
//
// This is the reason no Stash credential exists in the Python process: every
// privileged operation is performed here, by Go, on the host's behalf.
type Bridge interface {
	Query(ctx context.Context, plugin, sql string, params []any) ([]map[string]any, error)
	Execute(ctx context.Context, plugin, sql string, params []any) (int64, error)
	ExecuteBatch(ctx context.Context, plugin string, statements []BatchStatement) (int64, error)
	GetSettings(ctx context.Context, plugin string) (map[string]any, error)
	SetSetting(ctx context.Context, plugin, key string, value any) error
	StashGraphQL(ctx context.Context, plugin, query string, variables map[string]any) (map[string]any, error)
	SubmitTask(ctx context.Context, plugin, actionID string, invocationContext, params map[string]any, parentInvocationID, priority string) (string, error)
	MarkController(ctx context.Context, plugin, invocationID string) error
}

// BatchStatement is one statement in a batched write.
type BatchStatement struct {
	SQL    string `json:"sql"`
	Params []any  `json:"params"`
}

// ProgressFunc receives progress signals from running plugin handlers.
type ProgressFunc func(invocationID string, fraction float64, message string, detail map[string]any)

// Options configure a connection.
type Options struct {
	// Bridge handles callbacks from the host. Required.
	Bridge Bridge
	// OnProgress receives progress signals. Optional.
	OnProgress ProgressFunc
}

// Conn is an authenticated connection to a plugin host.
type Conn struct {
	conn *dbus.Conn
	obj  dbus.BusObject

	// HostVersion is what the host reported at handshake.
	HostVersion string

	closeOnce sync.Once
	signals   chan *dbus.Signal
	stop      chan struct{}
}

// Dial connects to a host, authenticates, and exports the Bridge.
//
// Note what is deliberately absent: Hello() in the bus-name sense. Peer-to-peer
// D-Bus has no bus daemon to hand out unique names, and calling it would hang.
// The authentication here is the host's own Hello method with the shared token.
func Dial(ctx context.Context, address, token string, opts Options) (*Conn, error) {
	conn, err := dbus.Dial(address)
	if err != nil {
		return nil, fmt.Errorf("dial host: %w", err)
	}

	// EXTERNAL: the kernel vouches for the peer's uid. ANONYMOUS is never used.
	if err := conn.Auth([]dbus.Auth{dbus.AuthExternal(strconv.Itoa(os.Getuid()))}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("authenticate with host: %w", err)
	}

	c := &Conn{
		conn:    conn,
		signals: make(chan *dbus.Signal, 64),
		stop:    make(chan struct{}),
	}
	// An empty destination: peer-to-peer has no bus names, the connection is
	// the peer.
	c.obj = conn.Object("", dbus.ObjectPath(hostPath))

	if opts.Bridge != nil {
		if err := c.exportBridge(opts.Bridge); err != nil {
			conn.Close()
			return nil, err
		}
	}

	conn.Signal(c.signals)
	go c.consumeSignals(opts.OnProgress)

	if err := c.hello(ctx, token); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// hello performs the authenticated handshake.
func (c *Conn) hello(ctx context.Context, token string) error {
	var version string
	var protocol uint32

	err := c.obj.CallWithContext(ctx, ifaceHost+".Hello", 0, token, Protocol).
		Store(&version, &protocol)
	if err != nil {
		return fmt.Errorf("host handshake: %w", err)
	}
	if protocol != Protocol {
		return fmt.Errorf("host speaks protocol %d, this build speaks %d", protocol, Protocol)
	}

	c.HostVersion = version
	return nil
}

// Ping verifies the host is responsive.
func (c *Conn) Ping(ctx context.Context) error {
	var monotonic uint64
	return c.obj.CallWithContext(ctx, ifaceHost+".Ping", 0).Store(&monotonic)
}

// Info returns the host's self-report.
func (c *Conn) Info(ctx context.Context) (map[string]any, error) {
	var raw string
	if err := c.obj.CallWithContext(ctx, ifaceHost+".GetInfo", 0).Store(&raw); err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("decode host info: %w", err)
	}
	return out, nil
}

// Shutdown asks the host to exit cleanly.
func (c *Conn) Shutdown(ctx context.Context, grace time.Duration) error {
	ms := uint32(grace.Milliseconds())
	if ms == 0 {
		ms = 1
	}
	return c.obj.CallWithContext(ctx, ifaceHost+".Shutdown", 0, ms).Store()
}

// Close releases the connection. Safe to call more than once.
func (c *Conn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.stop)
		err = c.conn.Close()
	})
	return err
}

// ---------------------------------------------------------------- plugins ---

// LoadResult is what loading a plugin produced.
type LoadResult struct {
	Status   string
	Error    string
	Registry Registry
}

// Registry is what a host reports its plugins have registered.
type Registry struct {
	Services     []map[string]any `json:"services"`
	Actions      []map[string]any `json:"actions"`
	Recommenders []map[string]any `json:"recommenders"`
	// Routes are the HTTP paths plugins serve. Patterns and handlers stay in
	// the host; Go only needs to know which plugins to proxy to.
	Routes []map[string]any `json:"routes"`
}

// Load imports a plugin in the host.
//
// The manifest is passed already parsed: it is read once, in Go, so there is a
// single interpretation of a plugin.yml rather than two that can drift.
func (c *Conn) Load(ctx context.Context, name, dir string, manifest any) (LoadResult, error) {
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return LoadResult{}, fmt.Errorf("encode manifest: %w", err)
	}

	var status, errText, registryJSON string
	if err := c.obj.CallWithContext(ctx, ifacePlugins+".Load", 0,
		name, dir, string(manifestJSON)).Store(&status, &errText, &registryJSON); err != nil {
		return LoadResult{}, fmt.Errorf("load plugin %s: %w", name, err)
	}

	result := LoadResult{Status: status, Error: errText}
	if registryJSON != "" {
		if err := json.Unmarshal([]byte(registryJSON), &result.Registry); err != nil {
			return result, fmt.Errorf("decode registry: %w", err)
		}
	}
	return result, nil
}

// Unload drops a plugin's registrations.
func (c *Conn) Unload(ctx context.Context, name string) error {
	return c.obj.CallWithContext(ctx, ifacePlugins+".Unload", 0, name).Store()
}

// ListPlugins returns the host's current registry.
func (c *Conn) ListPlugins(ctx context.Context) (Registry, error) {
	var raw string
	if err := c.obj.CallWithContext(ctx, ifacePlugins+".List", 0).Store(&raw); err != nil {
		return Registry{}, err
	}
	var registry Registry
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &registry); err != nil {
			return Registry{}, fmt.Errorf("decode registry: %w", err)
		}
	}
	return registry, nil
}

// --------------------------------------------------------------- dispatch ---

// InvokeAction runs an action in the host.
//
// This is a long call - a plugin may analyse an entire video - and D-Bus has no
// wire-level timeout, so the deadline is entirely the caller's context. Cancel
// travels separately on the same multiplexed connection, so it is never queued
// behind the invocation it is cancelling.
func (c *Conn) InvokeAction(ctx context.Context, invocationID, actionID string, invocationContext, params map[string]any) (any, error) {
	contextJSON, err := json.Marshal(orEmpty(invocationContext))
	if err != nil {
		return nil, err
	}
	paramsJSON, err := json.Marshal(orEmpty(params))
	if err != nil {
		return nil, err
	}

	var resultJSON string
	if err := c.obj.CallWithContext(ctx, ifaceDispatch+".InvokeAction", 0,
		invocationID, actionID, string(contextJSON), string(paramsJSON)).Store(&resultJSON); err != nil {
		return nil, err
	}

	var result any
	if resultJSON != "" {
		if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
			return nil, fmt.Errorf("decode action result: %w", err)
		}
	}
	return result, nil
}

// InvokeRecommender runs a recommender in the host.
func (c *Conn) InvokeRecommender(ctx context.Context, invocationID, recommenderID string, request map[string]any) (any, error) {
	requestJSON, err := json.Marshal(orEmpty(request))
	if err != nil {
		return nil, err
	}

	var resultJSON string
	if err := c.obj.CallWithContext(ctx, ifaceDispatch+".InvokeRecommender", 0,
		invocationID, recommenderID, string(requestJSON)).Store(&resultJSON); err != nil {
		return nil, err
	}

	var result any
	if resultJSON != "" {
		if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
			return nil, fmt.Errorf("decode recommender result: %w", err)
		}
	}
	return result, nil
}

// HTTPRequest is a request routed to a plugin.
type HTTPRequest struct {
	RequestID string
	Method    string
	// Path is the full path below the plugin mount, beginning with the plugin
	// name - the host uses that first segment to decide which plugin owns it.
	Path    string
	Query   string
	Headers map[string]string
	Body    []byte
}

// HTTPResponse is what a plugin route returned.
type HTTPResponse struct {
	Status  int
	Headers map[string]string
	Body    []byte
}

// ServeHTTP asks the host to serve a plugin route.
//
// Bodies travel base64-encoded in a string rather than as a D-Bus byte array:
// a plugin route may return an image, and marshalling 'ay' element by element
// through PyGObject is markedly slower than decoding one string.
func (c *Conn) ServeHTTP(ctx context.Context, req HTTPRequest) (HTTPResponse, error) {
	headersJSON, err := json.Marshal(req.Headers)
	if err != nil {
		return HTTPResponse{}, err
	}

	var (
		status      uint16
		respHeaders string
		respBody    string
	)
	err = c.obj.CallWithContext(ctx, ifaceDispatch+".HandleHTTP", 0,
		req.RequestID, req.Method, req.Path, req.Query,
		string(headersJSON), base64.StdEncoding.EncodeToString(req.Body)).
		Store(&status, &respHeaders, &respBody)
	if err != nil {
		return HTTPResponse{}, err
	}

	out := HTTPResponse{Status: int(status)}
	if respHeaders != "" {
		if err := json.Unmarshal([]byte(respHeaders), &out.Headers); err != nil {
			return HTTPResponse{}, fmt.Errorf("decode plugin response headers: %w", err)
		}
	}
	if respBody != "" {
		decoded, err := base64.StdEncoding.DecodeString(respBody)
		if err != nil {
			return HTTPResponse{}, fmt.Errorf("decode plugin response body: %w", err)
		}
		out.Body = decoded
	}
	return out, nil
}

// Cancel asks the host to stop an invocation.
func (c *Conn) Cancel(ctx context.Context, invocationID string) error {
	return c.obj.CallWithContext(ctx, ifaceDispatch+".Cancel", 0, invocationID).Store()
}

// ---------------------------------------------------------------- signals ---

func (c *Conn) consumeSignals(onProgress ProgressFunc) {
	for {
		select {
		case <-c.stop:
			return
		case sig, ok := <-c.signals:
			if !ok {
				return
			}
			if sig == nil || sig.Name != ifaceDispatch+".Progress" || onProgress == nil {
				continue
			}
			if len(sig.Body) != 4 {
				continue
			}

			invocationID, _ := sig.Body[0].(string)
			fraction, _ := sig.Body[1].(float64)
			message, _ := sig.Body[2].(string)

			var detail map[string]any
			if raw, ok := sig.Body[3].(string); ok && raw != "" {
				_ = json.Unmarshal([]byte(raw), &detail)
			}

			onProgress(invocationID, fraction, message, detail)
		}
	}
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}
