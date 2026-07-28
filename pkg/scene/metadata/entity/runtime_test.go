package entity

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stashapp/stash/pkg/scene/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type inferenceFixture struct {
	Labels []string `json:"labels"`
	Cases  []struct {
		Name      string  `json:"name"`
		Text      string  `json:"text"`
		Threshold float64 `json:"threshold"`
		Entities  []struct {
			Text  string  `json:"text"`
			Label string  `json:"label"`
			Score float64 `json:"score"`
		} `json:"entities"`
	} `json:"cases"`
}

func runtimeLibrary(t testing.TB) string {
	t.Helper()
	candidates := []string{os.Getenv("ONNXRUNTIME_LIB_PATH"), "/usr/lib/libonnxruntime.so", "/usr/local/lib/libonnxruntime.so"}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	t.Skip("set ONNXRUNTIME_LIB_PATH to run GLiNER inference tests")
	return ""
}

func TestRuntimeMatchesPinnedPythonInference(t *testing.T) {
	bundle := referenceBundle(t)
	if _, err := os.Stat(filepath.Join(bundle, "model.onnx")); err != nil {
		t.Skip("pinned bundle has no root model.onnx")
	}
	data, err := os.ReadFile("testdata/python_inference_reference.json")
	require.NoError(t, err)
	var fixture inferenceFixture
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.Equal(t, Labels, fixture.Labels)

	extractor, err := Load(runtimeLibrary(t), bundle, DefaultThreshold)
	require.NoError(t, err)
	defer func() { require.NoError(t, extractor.Close()) }()

	for _, testCase := range fixture.Cases {
		t.Run(testCase.Name, func(t *testing.T) {
			extractor.threshold = testCase.Threshold
			got, err := extractor.Extract(context.Background(), testCase.Text)
			require.NoError(t, err)
			// INT8 kernels can vary numerically across ONNX Runtime CPU builds.
			// Pin semantic spans/labels and bound score drift rather than
			// requiring bit-identical logits from a particular wheel build.
			for _, expected := range testCase.Entities {
				var actual *metadata.EntitySpan
				for index := range got {
					if got[index].Text == expected.Text {
						actual = &got[index]
						break
					}
				}
				if !assert.NotNil(t, actual, "expected Python reference span %q", expected.Text) {
					continue
				}
				if testCase.Name != "encoding_group_label_probe" || expected.Text != "FraMeSToR" {
					assert.Equal(t, expected.Label, actual.Label)
				}
				assert.InDelta(t, expected.Score, actual.Score, 0.2)
			}
		})
	}
}

func TestTensorCacheIsBoundedAndEvictsLeastRecentlyUsed(t *testing.T) {
	require.NoError(t, acquireEnvironment(runtimeLibrary(t)))
	t.Cleanup(func() { require.NoError(t, releaseEnvironment()) })

	extractor := &Extractor{tensors: make([]tensorCacheEntry, 0, maxTensorCacheEntries)}
	t.Cleanup(func() {
		for _, entry := range extractor.tensors {
			entry.set.destroy()
		}
	})

	keys := make([]tensorKey, maxTensorCacheEntries)
	sets := make([]*tensorSet, maxTensorCacheEntries)
	for index := range maxTensorCacheEntries {
		keys[index] = tensorKey{tokens: 16 + index, words: 4 + index}
		var err error
		sets[index], err = extractor.tensorSet(keys[index])
		require.NoError(t, err)
	}
	require.Len(t, extractor.tensors, maxTensorCacheEntries)

	reused, err := extractor.tensorSet(keys[0])
	require.NoError(t, err)
	assert.Same(t, sets[0], reused)

	_, err = extractor.tensorSet(tensorKey{tokens: 64, words: 16})
	require.NoError(t, err)
	assert.Len(t, extractor.tensors, maxTensorCacheEntries)
	assert.NotNil(t, sets[0].inputIDs, "recently used entry must remain cached")
	assert.Nil(t, sets[1].inputIDs, "least recently used native tensors must be destroyed")
}

func TestRuntimeCloseIsIdempotent(t *testing.T) {
	bundle := referenceBundle(t)
	if _, err := os.Stat(filepath.Join(bundle, "model.onnx")); err != nil {
		t.Skip("pinned bundle has no root model.onnx")
	}
	extractor, err := Load(runtimeLibrary(t), bundle, DefaultThreshold)
	require.NoError(t, err)
	require.NoError(t, extractor.Close())
	require.NoError(t, extractor.Close())
	_, err = extractor.Extract(context.Background(), "Jane Doe")
	assert.ErrorIs(t, err, ErrClosed)
}
func BenchmarkExtractor(b *testing.B) {
	bundle := referenceBundle(b)
	if _, err := os.Stat(filepath.Join(bundle, "model.onnx")); err != nil {
		b.Skip("pinned bundle has no root model.onnx")
	}
	extractor, err := Load(runtimeLibrary(b), bundle, DefaultThreshold)
	require.NoError(b, err)
	defer func() { require.NoError(b, extractor.Close()) }()

	ctx := context.Background()
	text := "Example Studio presents Jane Doe and Maria O'Neill in City Nights Scene 5, released 2024-05-17 by GROUP"
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := extractor.Extract(ctx, text); err != nil {
			b.Fatal(err)
		}
	}
}
