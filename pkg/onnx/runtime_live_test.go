package onnx

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/aitag/assets"
)

// The pinned runtime must actually LOAD, which is a stronger claim than that it
// downloads and matches its checksum.
//
// This exists because a pin once passed every other test in the tree and could
// not initialise: onnxruntime_go compiles against ORT_API_VERSION 26 and calls
// OrtGetApiBase with that number, and a runtime implementing an older API
// returns NULL rather than falling back. The catalog cannot express that
// constraint - only running it can - so the check is here, downloading exactly
// what a user would get and putting a real graph through it.
//
// Network-gated, because it fetches ~10 MB. Run it whenever the pin moves:
//
//	AI_LIVE_FETCH=1 go test ./pkg/onnx/ -run TestPinnedRuntimeIsActuallyLoadable
func TestPinnedRuntimeIsActuallyLoadable(t *testing.T) {
	if os.Getenv("AI_LIVE_FETCH") == "" {
		t.Skip("set AI_LIVE_FETCH=1 to download and load the pinned runtime")
	}
	if Ready() {
		t.Skip("a runtime is already initialised in this process; ORT cannot be re-pointed")
	}

	asset, err := assets.RuntimeAsset("")
	if err != nil {
		t.Skipf("no runtime published for this platform: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	d := &assets.Downloader{Dir: t.TempDir()}
	path, err := d.Fetch(ctx, asset, nil)
	if err != nil {
		t.Fatalf("fetch the pinned runtime: %v", err)
	}

	if err := Initialize(path); err != nil {
		t.Fatalf("the PINNED runtime does not load: %v\n\n"+
			"This is the failure the pin exists to prevent. If the message mentions an "+
			"API version, %s is older than onnxruntime_go's ORT_API_VERSION and the pin "+
			"must move UP, not down.", err, assets.DefaultRuntimeVersion)
	}
	defer func() {
		if err := Shutdown(); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	}()

	// Loading is not enough - run the committed graph against its reference, so
	// a runtime that initialises and then computes differently still fails here.
	ref := loadReference(t)

	sess, err := NewSession("live", filepath.Join("testdata", "tagging_spike.onnx"),
		[]string{"input"}, []string{"output"}, DefaultSessionOptions())
	if err != nil {
		t.Fatalf("the pinned runtime loaded but could not build a session: %v", err)
	}
	defer sess.Close()

	input := NewTensor(int64(ref.Batch), 3, int64(ref.Side), int64(ref.Side))
	for i := range input.Data {
		input.Data[i] = float32(i%255) / 255
	}

	out, err := sess.Run([]*Tensor{input}, [][]int64{{int64(ref.Batch), int64(ref.Classes)}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for row := 0; row < ref.Batch; row++ {
		for col := 0; col < ref.Classes; col++ {
			want := ref.Output[row][col]
			if got := float64(out[0].Data[row*ref.Classes+col]); math.Abs(want-got) > 1e-4 {
				t.Errorf("output[%d][%d] = %v, reference %v", row, col, got, want)
			}
		}
	}
	t.Logf("ONNX Runtime %s downloaded, verified, loaded, and matched the reference",
		assets.DefaultRuntimeVersion)
}
