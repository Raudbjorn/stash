package config

import (
	"path/filepath"
	"testing"

	"github.com/stashapp/stash/pkg/python"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPythonIndexesDefaultAndTransactionalUpdate(t *testing.T) {
	config := InitializeEmpty()
	assert.Equal(t, python.DefaultIndexes(), config.GetPythonIndexes())
	config.filePath = filepath.Join(t.TempDir(), "config.yml")

	err := config.UpdatePythonIndexes([]python.Index{
		{Name: "internal", URL: "HTTPS://EXAMPLE.COM/simple", Default: true},
		{Name: "pypi", URL: "https://pypi.org/simple"},
	})
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/simple/", config.GetPythonIndexes()[0].URL)

	previous := config.GetPythonIndexes()
	config.filePath = t.TempDir()
	err = config.UpdatePythonIndexes([]python.Index{{Name: "other", URL: "https://other.invalid/simple", Default: true}})
	require.Error(t, err)
	assert.Equal(t, previous, config.GetPythonIndexes())
}

func TestPythonSelectionWriteRollback(t *testing.T) {
	config := InitializeEmpty()
	config.SetString(PythonRuntimeID, "managed:old")
	config.SetString(PythonPath, "/old/python")
	config.filePath = t.TempDir()

	err := config.UpdatePythonSelection("managed:new", "/new/python")
	require.Error(t, err)
	assert.Equal(t, "managed:old", config.GetPythonRuntimeID())
	assert.Equal(t, "/old/python", config.GetPythonPath())

	config.SetPythonExternalPath("/external/python")
	assert.Empty(t, config.GetPythonRuntimeID())
	assert.Equal(t, "/external/python", config.GetPythonPath())
}
