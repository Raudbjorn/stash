// Package onnx wraps ONNX Runtime for in-process inference.
//
// The library is loaded at RUNTIME with dlopen rather than linked, which is why
// this adds no build-time native dependency and the compiler image is untouched
// - the same arrangement Stash already uses for ffmpeg. The cost is that the
// binary must be dynamically linked: a static glibc build cannot dlopen at all,
// which is why the release matrix for this fork drops the static targets.
package onnx

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

// ErrNotInitialised reports use before the runtime was loaded.
var ErrNotInitialised = errors.New("the ONNX runtime is not initialised")

// ErrNoLibrary reports that no shared library was found.
var ErrNoLibrary = errors.New("no ONNX runtime library is available")

// runtimeState guards the process-wide ONNX environment.
//
// ONNX Runtime's environment is global and may be initialised exactly once, so
// it lives here rather than in each pipeline; several providers sharing one
// process must share it.
var runtimeState struct {
	sync.Mutex
	initialised bool
	libraryPath string
	version     string
}

// Initialize loads the ONNX runtime from a shared library path.
//
// Idempotent: a second call with the same path is a no-op, and with a different
// path it fails rather than silently keeping the first, because the loaded
// library determines the numerics of everything already running.
func Initialize(libraryPath string) error {
	runtimeState.Lock()
	defer runtimeState.Unlock()

	if runtimeState.initialised {
		if runtimeState.libraryPath != libraryPath && libraryPath != "" {
			return fmt.Errorf(
				"the ONNX runtime is already loaded from %s; restart Stash to use %s",
				runtimeState.libraryPath, libraryPath)
		}
		return nil
	}

	if libraryPath == "" {
		return ErrNoLibrary
	}
	if _, err := os.Stat(libraryPath); err != nil {
		return fmt.Errorf("%w: %s", ErrNoLibrary, libraryPath)
	}

	ort.SetSharedLibraryPath(libraryPath)
	if err := ort.InitializeEnvironment(); err != nil {
		return fmt.Errorf("load the ONNX runtime from %s: %w", libraryPath, err)
	}

	runtimeState.initialised = true
	runtimeState.libraryPath = libraryPath
	runtimeState.version = ort.GetVersion()
	return nil
}

// Shutdown releases the environment. Rarely needed: the runtime lives as long
// as the process, and tearing it down while a session exists would crash.
func Shutdown() error {
	runtimeState.Lock()
	defer runtimeState.Unlock()

	if !runtimeState.initialised {
		return nil
	}
	err := ort.DestroyEnvironment()
	runtimeState.initialised = false
	runtimeState.libraryPath = ""
	return err
}

// Ready reports whether the runtime is loaded.
func Ready() bool {
	runtimeState.Lock()
	defer runtimeState.Unlock()
	return runtimeState.initialised
}

// Info describes the loaded runtime, for diagnostics.
type Info struct {
	LibraryPath string `json:"library_path"`
	Version     string `json:"version"`
	Ready       bool   `json:"ready"`
}

// Describe returns what is loaded.
func Describe() Info {
	runtimeState.Lock()
	defer runtimeState.Unlock()
	return Info{
		LibraryPath: runtimeState.libraryPath,
		Version:     runtimeState.version,
		Ready:       runtimeState.initialised,
	}
}

// Execution providers.
//
// This build uses the CPU provider and only the CPU provider, deliberately:
//
//   - CUDA appears to have dropped Pascal (compute capability 6.1) after ONNX
//     Runtime 1.22.x. Release 1.23 crashes on cards 1.22.1 served, consistent
//     with the minimum moving to 7.5, and a community rebuild exists precisely
//     because stock builds no longer serve those GPUs. assets.RuntimeAsset
//     therefore pins 1.22.1, and a user who wants an older card should keep it
//     there rather than find out at their first analysis.
//   - OpenVINO is not in a stock libonnxruntime at all, so this dlopen wrapper
//     cannot enable it: it would need Intel's separately-built
//     onnxruntime-openvino shipped alongside. The evidence it would even help
//     is mixed - one published benchmark had it running 49% SLOWER on CPU than
//     the default provider.
//
// An AVX2 CPU running int8 SigLIP is the honest baseline. Treat a GPU as
// opportunistic, and measure before assuming it helps.

// SessionOptions configure how a model runs.
type SessionOptions struct {
	// IntraOpThreads is the parallelism within one operator.
	//
	// Set to PHYSICAL cores, not logical. Phase 0 measured 12 threads as 33%
	// SLOWER than 6 on a 6-core/12-thread machine: these kernels are
	// memory-bandwidth bound, so hyperthread siblings contend rather than help.
	// That result is clock-independent and the reason this is not simply
	// runtime.NumCPU().
	IntraOpThreads int
	// InterOpThreads parallelises independent operators. One is right for a
	// single sequential model; more only helps a branching graph.
	InterOpThreads int
}

// DefaultSessionOptions returns thread counts suited to this machine.
func DefaultSessionOptions() SessionOptions {
	return SessionOptions{IntraOpThreads: PhysicalCores(), InterOpThreads: 1}
}

// PhysicalCores estimates the physical core count.
//
// Go exposes only logical CPUs, so on a hyperthreaded machine NumCPU is double
// what these kernels want. Halving is a heuristic, but it errs in the direction
// the measurement supports; anything above physical cores was actively slower.
func PhysicalCores() int {
	logical := runtime.NumCPU()
	if logical <= 2 {
		return logical
	}
	if hyperthreaded() {
		return logical / 2
	}
	return logical
}

// Session is a loaded model.
//
// Sessions are expensive to create - a hundred milliseconds to a second - and
// cheap to reuse, so one is built per model and shared across batches. Reusing
// one across goroutines is safe for Run; the dynamic-shape variant allocates
// its own tensors per call.
type Session struct {
	name    string
	session *ort.DynamicAdvancedSession

	inputs  []string
	outputs []string

	mu     sync.Mutex
	closed bool
}

// NewSession loads a model from disk.
//
// The inputs and outputs are named explicitly rather than discovered, because a
// model with several outputs has no canonical order and picking the wrong one
// produces plausible nonsense rather than an error.
func NewSession(name, modelPath string, inputs, outputs []string, opts SessionOptions) (*Session, error) {
	if !Ready() {
		return nil, ErrNotInitialised
	}
	if _, err := os.Stat(modelPath); err != nil {
		return nil, fmt.Errorf("model %s: %w", name, err)
	}

	sessionOpts, err := ort.NewSessionOptions()
	if err != nil {
		return nil, err
	}
	defer sessionOpts.Destroy()

	if opts.IntraOpThreads > 0 {
		if err := sessionOpts.SetIntraOpNumThreads(opts.IntraOpThreads); err != nil {
			return nil, err
		}
	}
	if opts.InterOpThreads > 0 {
		if err := sessionOpts.SetInterOpNumThreads(opts.InterOpThreads); err != nil {
			return nil, err
		}
	}

	session, err := ort.NewDynamicAdvancedSession(modelPath, inputs, outputs, sessionOpts)
	if err != nil {
		return nil, fmt.Errorf("load model %s: %w", name, err)
	}

	return &Session{name: name, session: session, inputs: inputs, outputs: outputs}, nil
}

// Name identifies the model.
func (s *Session) Name() string { return s.name }

// Close releases the session. Safe to call more than once.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.session.Destroy()
	return nil
}

// Tensor is an input or output buffer with its shape.
type Tensor struct {
	Shape []int64
	Data  []float32
}

// NewTensor allocates a tensor of the given shape.
func NewTensor(shape ...int64) *Tensor {
	total := int64(1)
	for _, d := range shape {
		total *= d
	}
	return &Tensor{Shape: shape, Data: make([]float32, total)}
}

// Elements is the tensor's value count.
func (t *Tensor) Elements() int { return len(t.Data) }

// Run executes the model over one set of inputs.
//
// Shapes are supplied per call rather than fixed at load, because batch size is
// chosen from a memory budget at runtime: a model pinned to a static batch could
// not adapt, and Phase 0 found an exporter that silently baked batch=1 into the
// graph for exactly this reason.
func (s *Session) Run(inputs []*Tensor, outputShapes [][]int64) ([]*Tensor, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil, fmt.Errorf("session %s is closed", s.name)
	}
	if len(inputs) != len(s.inputs) {
		return nil, fmt.Errorf("model %s expects %d inputs, got %d", s.name, len(s.inputs), len(inputs))
	}
	if len(outputShapes) != len(s.outputs) {
		return nil, fmt.Errorf("model %s produces %d outputs, got %d shapes",
			s.name, len(s.outputs), len(outputShapes))
	}

	inputValues := make([]ort.Value, len(inputs))
	for i, in := range inputs {
		tensor, err := ort.NewTensor(ort.NewShape(in.Shape...), in.Data)
		if err != nil {
			destroyAll(inputValues[:i])
			return nil, fmt.Errorf("build input %d for %s: %w", i, s.name, err)
		}
		inputValues[i] = tensor
	}
	defer destroyAll(inputValues)

	outputs := make([]*Tensor, len(outputShapes))
	outputValues := make([]ort.Value, len(outputShapes))
	for i, shape := range outputShapes {
		out := NewTensor(shape...)
		tensor, err := ort.NewTensor(ort.NewShape(shape...), out.Data)
		if err != nil {
			destroyAll(outputValues[:i])
			return nil, fmt.Errorf("build output %d for %s: %w", i, s.name, err)
		}
		outputs[i] = out
		outputValues[i] = tensor
	}
	defer destroyAll(outputValues)

	if err := s.session.Run(inputValues, outputValues); err != nil {
		return nil, fmt.Errorf("run %s: %w", s.name, err)
	}
	return outputs, nil
}

func destroyAll(values []ort.Value) {
	for _, v := range values {
		if v != nil {
			v.Destroy()
		}
	}
}
