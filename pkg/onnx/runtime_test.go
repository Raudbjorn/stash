package onnx

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// The wrapper is exercised against the same committed model the Phase 0 spike
// used, so the production code path is covered rather than only the library.
// AI_ORT_LIB points at a libonnxruntime shared object; without it these skip,
// which is the same condition the provider treats as "not installed".

func requireRuntime(t *testing.T) {
	t.Helper()

	lib := os.Getenv("AI_ORT_LIB")
	if lib == "" {
		lib = os.Getenv("AI_SPIKE_ORT_LIB")
	}
	if lib == "" {
		t.Skip("set AI_ORT_LIB to a libonnxruntime shared library to run the ONNX tests")
	}
	if _, err := os.Stat(lib); err != nil {
		t.Skipf("ONNX runtime library not found: %v", err)
	}

	if err := Initialize(lib); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
}

type reference struct {
	Batch   int         `json:"batch"`
	Side    int         `json:"side"`
	Classes int         `json:"classes"`
	Output  [][]float64 `json:"output"`
}

func loadReference(t *testing.T) reference {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", "tagging_spike_reference.json"))
	if err != nil {
		t.Fatalf("read reference: %v", err)
	}
	var ref reference
	if err := json.Unmarshal(data, &ref); err != nil {
		t.Fatalf("decode reference: %v", err)
	}
	return ref
}

// The numbers must match the reference, not merely be plausible: a wrong
// channel order or layout produces output of the right shape and the wrong
// values, which nothing else would catch.
func TestSessionMatchesTheReference(t *testing.T) {
	requireRuntime(t)
	ref := loadReference(t)

	session, err := NewSession("spike", filepath.Join("testdata", "tagging_spike.onnx"),
		[]string{"input"}, []string{"output"}, DefaultSessionOptions())
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer session.Close()

	// The reference was generated from a deterministic ramp; reproduced here
	// rather than stored, so the input is visibly what it claims to be.
	input := NewTensor(int64(ref.Batch), 3, int64(ref.Side), int64(ref.Side))
	for i := range input.Data {
		input.Data[i] = float32(i%255) / 255
	}

	outputs, err := session.Run([]*Tensor{input}, [][]int64{{int64(ref.Batch), int64(ref.Classes)}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	for row := 0; row < ref.Batch; row++ {
		for col := 0; col < ref.Classes; col++ {
			want := ref.Output[row][col]
			got := float64(outputs[0].Data[row*ref.Classes+col])
			if math.Abs(want-got) > 1e-4 {
				t.Errorf("output[%d][%d] = %v, reference %v", row, col, got, want)
			}
		}
	}
}

// Batch size is chosen from a memory budget at runtime, so a session pinned to
// one batch would be unusable. Phase 0 found an exporter that silently baked
// batch=1 into the graph, which is exactly what this catches.
func TestSessionAcceptsADynamicBatch(t *testing.T) {
	requireRuntime(t)
	ref := loadReference(t)

	session, err := NewSession("spike", filepath.Join("testdata", "tagging_spike.onnx"),
		[]string{"input"}, []string{"output"}, DefaultSessionOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	// Reused across batches: session creation costs 100ms to 1s, so a
	// per-batch session would dominate the runtime it enables.
	for _, batch := range []int64{1, 2, 5} {
		input := NewTensor(batch, 3, int64(ref.Side), int64(ref.Side))
		outputs, err := session.Run([]*Tensor{input}, [][]int64{{batch, int64(ref.Classes)}})
		if err != nil {
			t.Fatalf("batch %d: %v", batch, err)
		}
		if got := int64(outputs[0].Elements()); got != batch*int64(ref.Classes) {
			t.Errorf("batch %d produced %d values, want %d", batch, got, batch*int64(ref.Classes))
		}
	}
}

func TestSessionRejectsMismatchedArity(t *testing.T) {
	requireRuntime(t)

	session, err := NewSession("spike", filepath.Join("testdata", "tagging_spike.onnx"),
		[]string{"input"}, []string{"output"}, DefaultSessionOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	if _, err := session.Run(nil, [][]int64{{1, 1}}); err == nil {
		t.Error("running with no inputs was accepted")
	}
	if _, err := session.Run([]*Tensor{NewTensor(1, 3, 8, 8)}, nil); err == nil {
		t.Error("running with no output shapes was accepted")
	}
}

func TestClosedSessionRefusesToRun(t *testing.T) {
	requireRuntime(t)

	session, err := NewSession("spike", filepath.Join("testdata", "tagging_spike.onnx"),
		[]string{"input"}, []string{"output"}, DefaultSessionOptions())
	if err != nil {
		t.Fatal(err)
	}

	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	// Idempotent: a double close in a defer chain must not crash the process.
	if err := session.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if _, err := session.Run([]*Tensor{NewTensor(1, 3, 8, 8)}, [][]int64{{1, 4}}); err == nil {
		t.Error("a closed session ran")
	}
}

// Using the wrapper before the runtime is loaded must say so, rather than
// crashing in the C library.
func TestUninitialisedRuntimeIsReported(t *testing.T) {
	if Ready() {
		t.Skip("the runtime is already initialised in this process")
	}
	if _, err := NewSession("x", "nonexistent.onnx", nil, nil, SessionOptions{}); err != ErrNotInitialised {
		t.Errorf("NewSession = %v, want ErrNotInitialised", err)
	}
}

// Phase 0 measured 12 threads as 33% SLOWER than 6 on a 6-core machine: these
// kernels are bandwidth-bound, so hyperthread siblings contend rather than
// help. The default must therefore never exceed physical cores.
func TestDefaultThreadsDoNotExceedPhysicalCores(t *testing.T) {
	opts := DefaultSessionOptions()

	if opts.IntraOpThreads < 1 {
		t.Fatalf("IntraOpThreads = %d", opts.IntraOpThreads)
	}
	if opts.IntraOpThreads > PhysicalCores() {
		t.Errorf("IntraOpThreads = %d, above the %d physical cores", opts.IntraOpThreads, PhysicalCores())
	}
	if opts.InterOpThreads != 1 {
		t.Errorf("InterOpThreads = %d; one is right for a sequential model", opts.InterOpThreads)
	}
}

func TestNewTensorShape(t *testing.T) {
	tensor := NewTensor(2, 3, 4, 4)
	if got := tensor.Elements(); got != 2*3*4*4 {
		t.Errorf("Elements = %d, want %d", got, 2*3*4*4)
	}
	if len(tensor.Shape) != 4 {
		t.Errorf("Shape = %v", tensor.Shape)
	}
}
