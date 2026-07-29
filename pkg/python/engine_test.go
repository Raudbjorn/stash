package python

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEngineStatusTreatsMissingManagedEngineAsUninstalled(t *testing.T) {
	manager := NewManager(t.TempDir())
	manager.platform = func() (string, string, bool) {
		return "linux", "amd64", false
	}

	status := manager.EngineStatus(context.Background())

	assert.False(t, status.Installed)
	assert.Nil(t, status.Error)
	require.NotNil(t, status.Path)
	assert.Equal(t, manager.uvPath, *status.Path)
}
