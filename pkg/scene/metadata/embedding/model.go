package embedding

import (
	"bytes"
	"fmt"
	"math"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

// maxSeqLen bounds tokenized input length. Candidates handed to this
// package are always short (title-case word pairs from
// metadata.ExtractNameCandidates), so this is generous headroom, not a
// tight fit.
const maxSeqLen = 16

// embeddingDim is all-MiniLM-L6-v2's fixed output width.
const embeddingDim = 384

// Model wraps a loaded MiniLM ONNX Runtime session for producing sentence
// embeddings. Sessions are not safe for concurrent Run calls, so all access
// goes through mu.
type Model struct {
	mu        sync.Mutex
	tokenizer *Tokenizer
	session   *ort.AdvancedSession

	inputIDsTensor   *ort.Tensor[int64]
	attentionTensor  *ort.Tensor[int64]
	tokenTypesTensor *ort.Tensor[int64]
	outputTensor     *ort.Tensor[float32]
}

// Load initializes the ONNX Runtime environment against the shared library
// at libraryPath and creates a session for the embedded MiniLM model.
// Returns an error (never panics) if the library can't be loaded or the
// session can't be created - callers are expected to fall back to a
// non-embedding NamePlausibilityScorer in that case.
func Load(libraryPath string) (*Model, error) {
	tokenizer, err := NewTokenizer(bytes.NewReader(vocabBytes))
	if err != nil {
		return nil, fmt.Errorf("loading embedded vocab: %w", err)
	}

	ort.SetSharedLibraryPath(libraryPath)
	if err := ort.InitializeEnvironment(); err != nil {
		return nil, fmt.Errorf("initializing onnxruntime environment: %w", err)
	}

	shape := ort.NewShape(1, maxSeqLen)

	inputIDsTensor, err := ort.NewEmptyTensor[int64](shape)
	if err != nil {
		ort.DestroyEnvironment()
		return nil, fmt.Errorf("creating input_ids tensor: %w", err)
	}
	attentionTensor, err := ort.NewEmptyTensor[int64](shape)
	if err != nil {
		inputIDsTensor.Destroy()
		ort.DestroyEnvironment()
		return nil, fmt.Errorf("creating attention_mask tensor: %w", err)
	}
	tokenTypesTensor, err := ort.NewEmptyTensor[int64](shape)
	if err != nil {
		inputIDsTensor.Destroy()
		attentionTensor.Destroy()
		ort.DestroyEnvironment()
		return nil, fmt.Errorf("creating token_type_ids tensor: %w", err)
	}
	outputTensor, err := ort.NewEmptyTensor[float32](ort.NewShape(1, maxSeqLen, embeddingDim))
	if err != nil {
		inputIDsTensor.Destroy()
		attentionTensor.Destroy()
		tokenTypesTensor.Destroy()
		ort.DestroyEnvironment()
		return nil, fmt.Errorf("creating output tensor: %w", err)
	}

	session, err := ort.NewAdvancedSessionWithONNXData(
		modelBytes,
		[]string{"input_ids", "attention_mask", "token_type_ids"},
		[]string{"last_hidden_state"},
		[]ort.Value{inputIDsTensor, attentionTensor, tokenTypesTensor},
		[]ort.Value{outputTensor},
		nil,
	)
	if err != nil {
		inputIDsTensor.Destroy()
		attentionTensor.Destroy()
		tokenTypesTensor.Destroy()
		outputTensor.Destroy()
		ort.DestroyEnvironment()
		return nil, fmt.Errorf("creating onnxruntime session: %w", err)
	}

	return &Model{
		tokenizer:        tokenizer,
		session:          session,
		inputIDsTensor:   inputIDsTensor,
		attentionTensor:  attentionTensor,
		tokenTypesTensor: tokenTypesTensor,
		outputTensor:     outputTensor,
	}, nil
}

// Close releases the session, tensors, and the process-wide onnxruntime
// environment. Only one Model is ever loaded per process (see
// internal/manager/task_analyze_scene_metadata.go), so owning the
// environment lifecycle here is safe.
func (m *Model) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	err := m.session.Destroy()
	m.inputIDsTensor.Destroy()
	m.attentionTensor.Destroy()
	m.tokenTypesTensor.Destroy()
	m.outputTensor.Destroy()
	if envErr := ort.DestroyEnvironment(); err == nil {
		err = envErr
	}
	return err
}

// Embed tokenizes text and returns its mean-pooled, L2-normalized 384-dim
// sentence embedding, following all-MiniLM-L6-v2's documented pooling
// recipe (mean over token embeddings, weighted by the attention mask, then
// normalized to unit length).
func (m *Model) Embed(text string) ([]float32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	inputIDs, attentionMask, tokenTypeIDs := m.tokenizer.Encode(text, maxSeqLen)

	copy(m.inputIDsTensor.GetData(), inputIDs)
	copy(m.attentionTensor.GetData(), attentionMask)
	copy(m.tokenTypesTensor.GetData(), tokenTypeIDs)

	if err := m.session.Run(); err != nil {
		return nil, fmt.Errorf("running inference: %w", err)
	}

	return meanPoolAndNormalize(m.outputTensor.GetData(), attentionMask, maxSeqLen, embeddingDim), nil
}

// meanPoolAndNormalize implements sentence-transformers' mean-pooling: the
// per-token hidden states are averaged, weighted by the attention mask (so
// padding tokens don't contribute), then the result is scaled to unit
// length.
func meanPoolAndNormalize(hiddenStates []float32, attentionMask []int64, seqLen, dim int) []float32 {
	pooled := make([]float32, dim)
	var weightSum float32

	for pos := 0; pos < seqLen; pos++ {
		if attentionMask[pos] == 0 {
			continue
		}
		weightSum++
		base := pos * dim
		for d := 0; d < dim; d++ {
			pooled[d] += hiddenStates[base+d]
		}
	}

	if weightSum > 0 {
		for d := range pooled {
			pooled[d] /= weightSum
		}
	}

	var norm float64
	for _, v := range pooled {
		norm += float64(v) * float64(v)
	}
	norm = math.Sqrt(norm)
	if norm > 0 {
		for d := range pooled {
			pooled[d] = float32(float64(pooled[d]) / norm)
		}
	}

	return pooled
}
