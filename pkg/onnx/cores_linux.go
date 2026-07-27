//go:build linux

package onnx

import (
	"os"
	"strings"
)

// hyperthreaded reports whether logical CPUs outnumber physical cores.
//
// Read from sysfs rather than guessed: the halving heuristic is only correct on
// an SMT machine, and applying it to a machine without SMT would leave half the
// cores idle. Every sibling list on an SMT system names more than one CPU.
func hyperthreaded() bool {
	data, err := os.ReadFile("/sys/devices/system/cpu/cpu0/topology/thread_siblings_list")
	if err != nil {
		// Containers and unusual kernels may not expose this. Assuming SMT is
		// the safer default: too few threads is a modest slowdown, too many was
		// measured at 33% slower.
		return true
	}

	list := strings.TrimSpace(string(data))
	// The format is either "0,6" or "0-1"; both mean more than one sibling.
	return strings.ContainsAny(list, ",-")
}
