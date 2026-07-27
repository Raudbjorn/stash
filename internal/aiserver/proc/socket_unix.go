//go:build !windows

package proc

import (
	"fmt"
	"os"
)

func socketOwnerKey() string { return fmt.Sprint(os.Getuid()) }
