package metadata

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveExactNamed(t *testing.T) {
	records := []NamedAliases{
		{ID: 1, Name: "Example Movie", Aliases: []string{"Example.Movie", "Movie One"}},
		{ID: 2, Name: "Another Movie", Aliases: []string{"Shared Alias"}},
		{ID: 3, Name: "Third Movie", Aliases: []string{"Shared_Alias"}},
	}

	id, ok := ResolveExactNamed("example-movie", records)
	assert.True(t, ok)
	assert.Equal(t, 1, id)

	id, ok = ResolveExactNamed("Movie One", records)
	assert.True(t, ok)
	assert.Equal(t, 1, id)

	_, ok = ResolveExactNamed("shared alias", records)
	assert.False(t, ok)

	_, ok = ResolveExactNamed("Example", records)
	assert.False(t, ok, "partial matches must not link")
}
