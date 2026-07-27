package pluginhost

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/stashapp/stash/internal/aiserver/hostconn"
)

// Plugin HTTP routes.
//
// A plugin serves under /api/v1/plugins/<its own name>/, and only there. The
// Python server let a plugin pick its own FastAPI mount point, which meant a
// plugin could shadow the server's own endpoints - accidentally or otherwise.
// Namespacing by plugin name removes that entirely and costs nothing: a plugin
// that wants a prettier URL was never going to get one safely.

const (
	// maxRequestBody caps what is forwarded into the host. A plugin route is
	// not an upload endpoint; anything large belongs in the filesystem.
	maxRequestBody = 8 << 20

	// routeTimeout bounds one plugin request. Long enough for a route that does
	// real work, short enough that a wedged one frees the connection.
	routeTimeout = 60 * time.Second
)

// hopByHopHeaders must not be forwarded in either direction: they describe the
// connection between Stash and its client, not the message.
var hopByHopHeaders = map[string]bool{
	"connection":          true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"te":                  true,
	"trailer":             true,
	"transfer-encoding":   true,
	"upgrade":             true,
}

// HasRoutes reports whether a loaded plugin serves any HTTP routes.
func (m *Manager) HasRoutes(plugin string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.loaded[plugin]
	return ok && entry.Routes > 0
}

// ServeRoute proxies an HTTP request to a plugin.
//
// rest is the path below /plugins/, beginning with the plugin name. Returns
// false when no loaded plugin owns it, so the caller can fall through to its
// own 404 rather than this one masking a genuine routing mistake.
func (m *Manager) ServeRoute(w http.ResponseWriter, r *http.Request, rest string) bool {
	plugin, _, _ := strings.Cut(strings.TrimPrefix(rest, "/"), "/")
	if plugin == "" || !m.HasRoutes(plugin) {
		return false
	}

	conn, _, err := m.connection()
	if err != nil {
		writeRouteError(w, http.StatusServiceUnavailable,
			"The plugin host is not running, so this route cannot be served.")
		return true
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
	if err != nil {
		writeRouteError(w, http.StatusBadRequest, "could not read the request body")
		return true
	}
	if len(body) > maxRequestBody {
		writeRouteError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("request bodies to plugin routes are limited to %d bytes", maxRequestBody))
		return true
	}

	ctx, cancel := context.WithTimeout(r.Context(), routeTimeout)
	defer cancel()

	response, err := conn.ServeHTTP(ctx, hostconn.HTTPRequest{
		RequestID: r.Header.Get("X-Request-Id"),
		Method:    r.Method,
		Path:      "/" + strings.TrimPrefix(rest, "/"),
		Query:     r.URL.RawQuery,
		Headers:   forwardableHeaders(r.Header),
		Body:      body,
	})
	if err != nil {
		// A plugin's own exception already came back as a 500 response; an error
		// here means the transport failed, which is a different problem.
		writeRouteError(w, http.StatusBadGateway,
			fmt.Sprintf("the plugin host did not answer: %v", err))
		return true
	}

	for name, value := range response.Headers {
		if hopByHopHeaders[strings.ToLower(name)] {
			continue
		}
		w.Header().Set(name, value)
	}
	if response.Status == 0 {
		response.Status = http.StatusOK
	}
	w.WriteHeader(response.Status)
	_, _ = w.Write(response.Body)
	return true
}

// forwardableHeaders reduces the request headers to what a plugin may see.
//
// Cookie and Authorization are dropped deliberately: the plugin host runs
// downloaded code, and Stash's session cookie would be a credential handed to
// it for free. A plugin that needs Stash data uses the Bridge, where the call
// is made by Go and can be reasoned about.
func forwardableHeaders(in http.Header) map[string]string {
	out := make(map[string]string, len(in))
	for name, values := range in {
		lower := strings.ToLower(name)
		if hopByHopHeaders[lower] {
			continue
		}
		switch lower {
		case "cookie", "authorization", "proxy-authorization", "x-ai-api-key", "apikey":
			continue
		}
		if len(values) > 0 {
			out[name] = values[0]
		}
	}
	return out
}

func writeRouteError(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The same {"detail": ...} envelope the rest of the API uses, so a plugin's
	// frontend can parse failures uniformly.
	fmt.Fprintf(w, `{"detail":%q}`, detail)
}
