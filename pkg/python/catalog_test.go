package python

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchPackagesMatchesCatalogNames(t *testing.T) {
	manager := NewManager(t.TempDir())
	t.Cleanup(func() {
		require.NoError(t, manager.Close())
	})

	db, err := manager.catalogDatabase()
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO projects(index_name, normalized_name, name) VALUES
		('pypi', 'six', 'six'),
		('mirror', 'six', 'six'),
		('pypi', 'six-tools', 'six-tools'),
		('pypi', 'unrelated', 'unrelated')`)
	require.NoError(t, err)

	indexes := []Index{
		{Name: "pypi", URL: "https://pypi.org/simple/", Default: true},
		{Name: "mirror", URL: "https://packages.example/simple/"},
	}
	results, err := manager.SearchPackages(context.Background(), "six", nil, 10, indexes)
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, "six", results[0].Name)
	assert.ElementsMatch(t, []string{"pypi", "mirror"}, results[0].Indexes)
	assert.Equal(t, "six-tools", results[1].Name)

	selected := "pypi"
	results, err = manager.SearchPackages(context.Background(), "six", &selected, 10, indexes)
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, []string{"pypi"}, results[0].Indexes)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestRefreshCatalogDoesNotReuseValidatorsForChangedURL(t *testing.T) {
	manager := NewManager(t.TempDir())
	t.Cleanup(func() {
		require.NoError(t, manager.Close())
	})

	requests := 0
	manager.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		assert.Empty(t, request.Header.Get("If-Modified-Since"))
		if request.URL.Host == "second.example" {
			assert.Empty(t, request.Header.Get("If-None-Match"))
		}

		project := "alpha"
		if request.URL.Host == "second.example" {
			project = "beta"
		}
		body := `{"projects":[{"name":"` + project + `"}]}`
		headers := http.Header{"Content-Type": []string{"application/vnd.pypi.simple.v1+json"}}
		if request.URL.Host == "first.example" {
			headers.Set("ETag", `"same"`)
			headers.Set("Last-Modified", "Wed, 21 Oct 2015 07:28:00 GMT")
		}
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Header:        headers,
			Body:          io.NopCloser(strings.NewReader(body)),
			ContentLength: int64(len(body)),
			Request:       request,
		}, nil
	})}

	index := Index{Name: "example", URL: "https://first.example/simple/", Default: true}
	require.NoError(t, manager.RefreshCatalog(context.Background(), index, nil))

	index.URL = "https://second.example/simple/"
	require.NoError(t, manager.RefreshCatalog(context.Background(), index, nil))
	assert.Equal(t, 2, requests)

	results, err := manager.SearchPackages(context.Background(), "beta", nil, 10, []Index{index})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "beta", results[0].Name)

	results, err = manager.SearchPackages(context.Background(), "alpha", nil, 10, []Index{index})
	require.NoError(t, err)
	assert.Empty(t, results)
}
