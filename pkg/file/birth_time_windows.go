//go:build windows
// +build windows

package file

import (
	"io/fs"
	"syscall"
	"time"
)

// BirthTime returns the file creation time from the OS.
// On Windows, this comes from Win32FileAttributeData.CreationTime.
// Truncated to seconds to match database precision.
func BirthTime(info fs.FileInfo) *time.Time {
	sys := info.Sys()
	if sys == nil {
		return nil
	}

	data, ok := sys.(*syscall.Win32FileAttributeData)
	if !ok {
		return nil
	}

	t := time.Unix(0, data.CreationTime.Nanoseconds())
	t = t.Truncate(time.Second)
	return &t
}
