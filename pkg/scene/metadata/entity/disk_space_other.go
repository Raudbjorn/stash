//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package entity

import "math"

func diskAvailableBytes(string) (int64, error) {
	return math.MaxInt64, nil
}
