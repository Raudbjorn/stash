//go:build linux

package manager

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syncthing/notify"
)

type fakeEvent struct {
	ev   notify.Event
	path string
}

func (f fakeEvent) Event() notify.Event { return f.ev }
func (f fakeEvent) Path() string        { return f.path }
func (f fakeEvent) Sys() interface{}    { return nil }

// On Linux, InCreate for a plain file fires before the write finishes, so it
// must be ignored (InCloseWrite covers files); InCreate for a directory is kept.
func TestNotifyShouldScanEvent_Linux(t *testing.T) {
	dir := t.TempDir()

	file := filepath.Join(dir, "a.mp4")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0644))
	fi, err := os.Stat(file)
	require.NoError(t, err)

	sub := filepath.Join(dir, "sub")
	require.NoError(t, os.Mkdir(sub, 0755))
	di, err := os.Stat(sub)
	require.NoError(t, err)

	assert.False(t, notifyShouldScanEvent(fi, fakeEvent{notify.InCreate, file}),
		"InCreate on a file should be ignored (may still be writing)")
	assert.True(t, notifyShouldScanEvent(di, fakeEvent{notify.InCreate, sub}),
		"InCreate on a directory should be handled")
	assert.True(t, notifyShouldScanEvent(fi, fakeEvent{notify.InCloseWrite, file}),
		"InCloseWrite on a file should be handled")
}
