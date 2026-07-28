package python

import (
	"context"
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
