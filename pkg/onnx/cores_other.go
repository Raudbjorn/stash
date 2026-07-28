//go:build !linux

package onnx

// hyperthreaded is assumed on platforms with no cheap way to ask.
//
// Erring toward SMT halves the thread count on a machine that does not have it,
// which is a modest slowdown; erring the other way was measured at 33% slower,
// so the asymmetry decides the default.
func hyperthreaded() bool { return true }
