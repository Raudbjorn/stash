package entity

import (
	"fmt"

	"github.com/stashapp/stash/pkg/fsutil"
)

var availableDiskBytes = diskAvailableBytes

func preflightDiskSpace(path string, required int64) error {
	available, err := availableDiskBytes(path)
	if err != nil {
		return fmt.Errorf("inspect model cache free space: %w", err)
	}
	if available >= required {
		return nil
	}
	network, networkErr := fsutil.IsNetworkFS(path)
	if networkErr != nil {
		return fmt.Errorf("inspect model cache filesystem: %w", networkErr)
	}
	return &ErrInsufficientDisk{Required: required, Available: available, Network: network}
}
