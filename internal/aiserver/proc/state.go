// Package proc provides process-agnostic child supervision primitives.
package proc

// State is a supervisor lifecycle state.
type State string

const (
	StateDisabled    State = "disabled"
	StateProbing     State = "probing"
	StateUnavailable State = "unavailable"
	StateStarting    State = "starting"
	StateHandshaking State = "handshaking"
	StateReady       State = "ready"
	StateDraining    State = "draining"
	StateStopping    State = "stopping"
	StateStopped     State = "stopped"
	StateCrashed     State = "crashed"
	StateBackoff     State = "backoff"
	StateFailed      State = "failed"
)

// Terminal reports whether the state requires intervention to leave.
func (s State) Terminal() bool {
	return s == StateFailed || s == StateUnavailable || s == StateDisabled
}

// Running reports whether a process should exist in this state.
func (s State) Running() bool {
	switch s {
	case StateStarting, StateHandshaking, StateReady, StateDraining, StateStopping:
		return true
	default:
		return false
	}
}
