//go:build linux

package manager

import (
	"os"

	"github.com/syncthing/notify"
)

func notifyEvents() []notify.Event {
	return []notify.Event{
		notify.InCloseWrite,
		notify.InCreate,
		notify.InMovedTo,
		notify.Rename,
	}
}

func notifyShouldScanEvent(fi os.FileInfo, ev notify.EventInfo) bool {
	// On Linux, InCreate for a file fires before the write completes; rely on
	// InCloseWrite for files. Only act on InCreate for directories.
	if ev.Event()&notify.InCreate != 0 {
		return fi.IsDir()
	}
	return true
}
