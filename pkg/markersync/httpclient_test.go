package markersync

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestRestClientRetriesOn429 asserts the client retries a 429 (honouring
// Retry-After: 0 for an immediate retry) and succeeds on the following 200.
func TestRestClientRetriesOn429(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer srv.Close()

	c := newRestClient("", 0)

	var out struct {
		OK bool `json:"ok"`
	}
	if err := c.getJSON(context.Background(), srv.URL, "", &out); err != nil {
		t.Fatalf("getJSON: %v", err)
	}
	if !out.OK {
		t.Errorf("out.OK = false, want true")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("server received %d calls, want 2 (one 429 + one 200)", got)
	}
}

// TestRestClientRetriesExhausted asserts that a server which always returns 429
// eventually fails with an HTTPStatusError after the bounded retries.
func TestRestClientRetriesExhausted(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "0")
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := newRestClient("", 0)

	err := c.getJSON(context.Background(), srv.URL, "", &struct{}{})
	if err == nil {
		t.Fatal("getJSON: expected an error after exhausting retries, got nil")
	}
	if !isRetryableStatus(err, http.StatusTooManyRequests) {
		t.Errorf("error = %v, want an HTTPStatusError with status 429", err)
	}
	// first attempt + maxRetries retries.
	if got := atomic.LoadInt32(&calls); got != int32(maxRetries+1) {
		t.Errorf("server received %d calls, want %d (1 + %d retries)", got, maxRetries+1, maxRetries)
	}
}

// TestParseRetryAfter covers the delta-seconds and HTTP-date forms plus the
// absent/invalid cases.
func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantOK  bool
		wantMin time.Duration
		wantMax time.Duration
	}{
		{name: "empty", in: "", wantOK: false},
		{name: "zero seconds", in: "0", wantOK: true, wantMin: 0, wantMax: 0},
		{name: "delta seconds", in: "5", wantOK: true, wantMin: 5 * time.Second, wantMax: 5 * time.Second},
		{name: "negative seconds", in: "-3", wantOK: true, wantMin: 0, wantMax: 0},
		{name: "garbage", in: "soon", wantOK: false},
		{name: "http date future", in: time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat), wantOK: true, wantMin: 20 * time.Second, wantMax: 31 * time.Second},
		{name: "http date past", in: time.Now().Add(-30 * time.Second).UTC().Format(http.TimeFormat), wantOK: true, wantMin: 0, wantMax: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, ok := parseRetryAfter(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("parseRetryAfter(%q) ok = %v, want %v", tt.in, ok, tt.wantOK)
			}
			if ok && (d < tt.wantMin || d > tt.wantMax) {
				t.Errorf("parseRetryAfter(%q) = %v, want in [%v, %v]", tt.in, d, tt.wantMin, tt.wantMax)
			}
		})
	}
}

// isRetryableStatus reports whether err is an HTTPStatusError with the given code.
func isRetryableStatus(err error, code int) bool {
	var se *HTTPStatusError
	if !errors.As(err, &se) {
		return false
	}
	return se.StatusCode == code
}
