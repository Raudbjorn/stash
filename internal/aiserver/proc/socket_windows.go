//go:build windows

package proc

import "os"

func socketOwnerKey() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return dir
	}
	return os.Getenv("USERNAME")
}
