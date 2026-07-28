package hostconn_test

// Plugin HTTP routes, end to end against the real host.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stashapp/stash/internal/aiserver/hostconn"
)

const routePlugin = `
from stash_ai.http import route, Response


@route("/hello")
def hello(req):
    return {"greeting": "hi", "query": req.query, "method": req.method}


@route("/items/{item_id}", methods=["GET", "POST"])
def item(req):
    return {"id": req.params["item_id"], "method": req.method}


@route("/echo", methods=["POST"])
def echo(req):
    return {"received": req.json()}


@route("/raw")
def raw(req):
    return Response(status=201, body=b"\x00\x01\x02\x03", content_type="application/octet-stream")


@route("/headers")
def headers(req):
    # Lower-cased so the assertion does not depend on Go's canonical casing.
    return {"seen": sorted(k.lower() for k in req.headers)}


@route("/boom")
def boom(req):
    raise RuntimeError("deliberate route failure")
`

func serveRoute(t *testing.T, conn *hostconn.Conn, req hostconn.HTTPRequest) hostconn.HTTPResponse {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	resp, err := conn.ServeHTTP(ctx, req)
	if err != nil {
		t.Fatalf("ServeHTTP %s %s: %v", req.Method, req.Path, err)
	}
	return resp
}

func decodeBody(t *testing.T, body []byte) map[string]any {
	t.Helper()

	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, body)
	}
	return out
}

func TestPluginRoutesAreServed(t *testing.T) {
	python := requirePyGObject(t)
	sup, base := newSupervisor(t, python, hostconn.Options{Bridge: &recordingBridge{}})
	waitReady(t, sup)

	conn := supervisorConn(t, sup, nil)
	result := loadPlugin(t, conn, base, "router", routePlugin)

	// The registry must tell Go which plugins serve routes, or it has no basis
	// for deciding whether to proxy.
	if len(result.Registry.Routes) != 6 {
		t.Fatalf("registered %d routes, want 6: %+v", len(result.Registry.Routes), result.Registry.Routes)
	}

	t.Run("simple GET", func(t *testing.T) {
		resp := serveRoute(t, conn, hostconn.HTTPRequest{
			Method: "GET", Path: "/router/hello", Query: "a=1&b=2",
		})
		if resp.Status != 200 {
			t.Fatalf("status = %d", resp.Status)
		}
		body := decodeBody(t, resp.Body)
		if body["greeting"] != "hi" {
			t.Errorf("greeting = %v", body["greeting"])
		}
		// The query string must survive: a plugin route that cannot read its
		// own parameters is useless.
		if body["query"] != "a=1&b=2" {
			t.Errorf("query = %v, want a=1&b=2", body["query"])
		}
		if resp.Headers["Content-Type"] != "application/json" {
			t.Errorf("Content-Type = %q", resp.Headers["Content-Type"])
		}
	})

	t.Run("path parameters", func(t *testing.T) {
		resp := serveRoute(t, conn, hostconn.HTTPRequest{Method: "GET", Path: "/router/items/42"})
		body := decodeBody(t, resp.Body)
		if body["id"] != "42" {
			t.Errorf("id = %v, want 42", body["id"])
		}
	})

	t.Run("request body", func(t *testing.T) {
		resp := serveRoute(t, conn, hostconn.HTTPRequest{
			Method:  "POST",
			Path:    "/router/echo",
			Headers: map[string]string{"Content-Type": "application/json"},
			Body:    []byte(`{"n": 7}`),
		})
		body := decodeBody(t, resp.Body)
		received, ok := body["received"].(map[string]any)
		if !ok || received["n"] != float64(7) {
			t.Errorf("received = %#v, want {n: 7}", body["received"])
		}
	})

	t.Run("binary response", func(t *testing.T) {
		resp := serveRoute(t, conn, hostconn.HTTPRequest{Method: "GET", Path: "/router/raw"})
		if resp.Status != 201 {
			t.Errorf("status = %d, want 201", resp.Status)
		}
		// Base64 in transit, bytes at both ends: a route returning an image
		// must not be mangled.
		want := []byte{0, 1, 2, 3}
		if len(resp.Body) != len(want) {
			t.Fatalf("body = %v, want %v", resp.Body, want)
		}
		for i := range want {
			if resp.Body[i] != want[i] {
				t.Fatalf("body = %v, want %v", resp.Body, want)
			}
		}
	})

	t.Run("unknown path is 404", func(t *testing.T) {
		resp := serveRoute(t, conn, hostconn.HTTPRequest{Method: "GET", Path: "/router/nope"})
		if resp.Status != 404 {
			t.Errorf("status = %d, want 404", resp.Status)
		}
	})

	// A registered path with the wrong verb must be 405, not 404: the
	// difference tells a plugin author whether their route registered at all.
	t.Run("wrong method is 405", func(t *testing.T) {
		resp := serveRoute(t, conn, hostconn.HTTPRequest{Method: "DELETE", Path: "/router/echo"})
		if resp.Status != 405 {
			t.Errorf("status = %d, want 405", resp.Status)
		}
	})

	// A plugin raising must become an HTTP error, not a transport failure: the
	// caller is an HTTP client and deserves an HTTP answer.
	t.Run("plugin exception is 500", func(t *testing.T) {
		resp := serveRoute(t, conn, hostconn.HTTPRequest{Method: "GET", Path: "/router/boom"})
		if resp.Status != 500 {
			t.Fatalf("status = %d, want 500", resp.Status)
		}
		body := decodeBody(t, resp.Body)
		if detail, _ := body["detail"].(string); detail == "" {
			t.Error("the 500 carried no explanation")
		}
	})

	// The host must survive a route that raised.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := conn.Ping(ctx); err != nil {
		t.Errorf("host unusable after a failing route: %v", err)
	}
}

// A plugin's routes must go with it, or the UI keeps offering an endpoint that
// can only fail.
func TestPluginRoutesGoAwayOnUnload(t *testing.T) {
	python := requirePyGObject(t)
	sup, base := newSupervisor(t, python, hostconn.Options{Bridge: &recordingBridge{}})
	waitReady(t, sup)

	conn := supervisorConn(t, sup, nil)
	loadPlugin(t, conn, base, "router", routePlugin)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := conn.Unload(ctx, "router"); err != nil {
		t.Fatalf("Unload: %v", err)
	}

	registry, err := conn.ListPlugins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.Routes) != 0 {
		t.Errorf("routes survived unload: %+v", registry.Routes)
	}

	resp := serveRoute(t, conn, hostconn.HTTPRequest{Method: "GET", Path: "/router/hello"})
	if resp.Status != 404 {
		t.Errorf("an unloaded plugin still served its route: status %d", resp.Status)
	}
}
