package markersync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

// defaultHTTPTimeout is the per-request timeout used by the REST client.
const defaultHTTPTimeout = 30 * time.Second

// defaultUserAgent is used when a source does not supply its own User-Agent.
const defaultUserAgent = "stash"

// errorBodySnippetLen bounds how many bytes of a non-2xx response body are
// included in the returned error.
const errorBodySnippetLen = 512

// HTTPStatusError is returned by the client when a request completes with a
// non-2xx status. It lets callers distinguish an expected outcome (for example a
// 404 meaning "no such record") from a genuine failure such as a 5xx. Transport
// errors (network, timeout, context cancellation) are returned unwrapped and are
// NOT represented by this type.
type HTTPStatusError struct {
	StatusCode int
	Method     string
	URL        string
	Body       string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("%s %s: unexpected status %d: %s", e.Method, e.URL, e.StatusCode, e.Body)
}

// restClient is a small JSON-over-HTTP client shared by the marker sync
// sources. It mirrors the shape of pkg/stashbox/client.go (bearer auth header,
// User-Agent header, optional per-source rate limiting) but speaks plain REST
// rather than gqlgen.
type restClient struct {
	httpClient *http.Client
	userAgent  string
	// limiter is an optional per-source rate limiter. It may be nil.
	limiter *rate.Limiter
}

// newRestClient constructs a restClient. A userAgent of "" defaults to "stash".
// A requestsPerMinute of <= 0 disables rate limiting.
func newRestClient(userAgent string, requestsPerMinute int) *restClient {
	ua := userAgent
	if ua == "" {
		ua = defaultUserAgent
	}

	var limiter *rate.Limiter
	if requestsPerMinute > 0 {
		perSec := float64(requestsPerMinute) / 60
		limiter = rate.NewLimiter(rate.Limit(perSec), 1)
	}

	return &restClient{
		httpClient: &http.Client{Timeout: defaultHTTPTimeout},
		userAgent:  ua,
		limiter:    limiter,
	}
}

// getJSON performs a GET request and decodes the JSON response into out. If
// bearer is non-empty an Authorization: Bearer header is set.
func (c *restClient) getJSON(ctx context.Context, url, bearer string, out any) error {
	return c.do(ctx, http.MethodGet, url, bearer, nil, out)
}

// postJSON marshals body to JSON, performs a POST request and decodes the JSON
// response into out. out may be nil to discard the response body. If bearer is
// non-empty an Authorization: Bearer header is set.
func (c *restClient) postJSON(ctx context.Context, url, bearer string, body, out any) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshalling request body: %w", err)
		}
		reader = bytes.NewReader(buf)
	}
	return c.do(ctx, http.MethodPost, url, bearer, reader, out)
}

func (c *restClient) do(ctx context.Context, method, url, bearer string, body io.Reader, out any) error {
	if c.limiter != nil {
		if err := c.limiter.Wait(ctx); err != nil {
			// Only happens when the context is cancelled.
			return err
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodySnippetLen))
		return &HTTPStatusError{
			StatusCode: resp.StatusCode,
			Method:     method,
			URL:        url,
			Body:       strings.TrimSpace(string(snippet)),
		}
	}

	if out == nil {
		// Drain the body so the connection can be reused, then discard.
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding response from %s: %w", url, err)
	}

	return nil
}
