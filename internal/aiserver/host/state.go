// Package host supervises the Python plugin process.
package host

import (
	"time"

	"github.com/stashapp/stash/internal/aiserver/proc"
)

const (
	readyTimeout = 30 * time.Second
	drainTimeout = 30 * time.Second
	stopGrace    = 5 * time.Second
	pingInterval = 10 * time.Second
	pingTimeout  = 5 * time.Second
	missedPings  = 3
)

// CrashReport adds Python plugin attribution to the generic process report.
type CrashReport struct {
	proc.CrashReport
	RunningPlugin string `json:"running_plugin,omitempty"`
}
