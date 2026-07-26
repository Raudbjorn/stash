//go:build darwin || freebsd
// +build darwin freebsd

package file

import (
	"time"
)

// birthTimeFromTimespec converts a Unix timestamp from seconds and nanoseconds,
// truncates to second precision, and returns a pointer.
func birthTimeFromTimespec(sec, nsec int64) *time.Time {
	t := time.Unix(sec, nsec)
	t = t.Truncate(time.Second)
	return &t
}
