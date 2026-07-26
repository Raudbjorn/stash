//go:build darwin
// +build darwin

package file

import (
	"io/fs"
	"syscall"
	"time"
)

// BirthTime returns the file creation/birth time from the OS.
// On macOS, this comes from syscall.Stat_t.Birthtimespec.
// Truncated to seconds to match database precision.
func BirthTime(info fs.FileInfo) *time.Time {
	sys := info.Sys()
	if sys == nil {
		return nil
	}

	stat, ok := sys.(*syscall.Stat_t)
	if !ok {
		return nil
	}

	return birthTimeFromTimespec(stat.Birthtimespec.Sec, stat.Birthtimespec.Nsec)
}
