//go:build !windows && !darwin && !freebsd
// +build !windows,!darwin,!freebsd

package file

import (
	"io/fs"
	"time"
)

// BirthTime returns the file creation/birth time from the OS if available.
// Returns nil on platforms that do not expose birth time (Linux, etc.).
// Truncated to seconds to match database precision.
func BirthTime(info fs.FileInfo) *time.Time {
	return nil
}
