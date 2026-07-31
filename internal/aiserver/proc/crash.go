package proc

import "time"

// CrashReport records why one supervised process generation ended.
type CrashReport struct {
	Generation int       `json:"generation"`
	ExitCode   int       `json:"exit_code"`
	Error      string    `json:"error"`
	Stderr     []string  `json:"stderr,omitempty"`
	At         time.Time `json:"at"`
}
