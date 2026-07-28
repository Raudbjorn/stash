package pluginhost

import (
	"net/http"
	"testing"
)

// The plugin host runs downloaded code. Forwarding Stash's session cookie into
// it would hand every plugin a working credential for free - exactly what the
// Bridge design exists to avoid. This is a unit test rather than an end-to-end
// one because the property is about what is NOT sent, and an integration test
// can only observe what arrives.
func TestCredentialHeadersAreNotForwarded(t *testing.T) {
	in := http.Header{}
	in.Set("Content-Type", "application/json")
	in.Set("Accept", "application/json")
	in.Set("X-Custom", "kept")
	in.Set("Cookie", "session=secret")
	in.Set("Authorization", "Bearer secret")
	in.Set("X-Ai-Api-Key", "secret")
	in.Set("ApiKey", "secret")
	in.Set("Connection", "keep-alive")
	in.Set("Transfer-Encoding", "chunked")

	out := forwardableHeaders(in)

	for _, name := range []string{"Content-Type", "Accept", "X-Custom"} {
		if _, ok := out[name]; !ok {
			t.Errorf("%s was dropped but should have been forwarded", name)
		}
	}

	// Credentials.
	for _, name := range []string{"Cookie", "Authorization", "X-Ai-Api-Key", "Apikey"} {
		if value, ok := out[name]; ok {
			t.Errorf("%s was forwarded to the plugin host: %q", name, value)
		}
	}

	// Hop-by-hop headers describe the connection to Stash's client, not the
	// message, so forwarding them would be wrong even if it were harmless.
	for _, name := range []string{"Connection", "Transfer-Encoding"} {
		if _, ok := out[name]; ok {
			t.Errorf("hop-by-hop header %s was forwarded", name)
		}
	}
}

// A request for a plugin that is not loaded must fall through rather than being
// answered with this layer's 404, which would mask a genuine routing mistake.
func TestServeRouteDeclinesUnknownPlugins(t *testing.T) {
	m := New(Deps{}, nil)

	for _, rest := range []string{"", "/", "nosuch/thing", "/nosuch/thing"} {
		if m.HasRoutes("nosuch") {
			t.Fatal("an unloaded plugin reported routes")
		}
		recorder := &discardWriter{header: http.Header{}}
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/plugins/"+rest, nil)

		if m.ServeRoute(recorder, req, rest) {
			t.Errorf("ServeRoute claimed %q for an unloaded plugin", rest)
		}
	}
}

// A loaded plugin with no routes must also decline, or every plugin would
// swallow the whole /plugins/ namespace.
func TestServeRouteDeclinesPluginsWithoutRoutes(t *testing.T) {
	m := New(Deps{}, nil)
	m.loaded["demo"] = loadedPlugin{Name: "demo", Routes: 0}

	if m.HasRoutes("demo") {
		t.Fatal("a plugin with no routes reported routes")
	}

	recorder := &discardWriter{header: http.Header{}}
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/plugins/demo/thing", nil)
	if m.ServeRoute(recorder, req, "demo/thing") {
		t.Error("ServeRoute claimed a path for a plugin serving no routes")
	}
}

// A plugin that does serve routes, with the host down, must get a 503 that
// blames the host - not a 404 suggesting the route does not exist.
func TestServeRouteReportsAMissingHost(t *testing.T) {
	m := New(Deps{}, nil)
	m.loaded["demo"] = loadedPlugin{Name: "demo", Routes: 1}

	recorder := &discardWriter{header: http.Header{}}
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/plugins/demo/thing", nil)

	if !m.ServeRoute(recorder, req, "demo/thing") {
		t.Fatal("ServeRoute declined a path belonging to a route-serving plugin")
	}
	if recorder.status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", recorder.status)
	}
}

// discardWriter records the status without buffering the body.
type discardWriter struct {
	header http.Header
	status int
}

func (w *discardWriter) Header() http.Header         { return w.header }
func (w *discardWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *discardWriter) WriteHeader(status int)      { w.status = status }
