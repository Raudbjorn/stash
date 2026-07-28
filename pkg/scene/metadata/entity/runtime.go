package entity

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"sync"

	"github.com/stashapp/stash/pkg/scene/metadata"
	ort "github.com/yalue/onnxruntime_go"
)

var (
	ErrClosed       = errors.New("GLiNER extractor is closed")
	ErrInputTooLong = errors.New("GLiNER tokenized input exceeds hard limit")
)

const (
	maxModelTokens   = 1024
	DefaultThreshold = 0.25
)

var (
	inputNames  = []string{"input_ids", "attention_mask", "words_mask", "text_lengths", "span_idx", "span_mask"}
	outputNames = []string{"logits"}

	environmentMu   sync.Mutex
	environmentRefs int
	environmentPath string
)

func acquireEnvironment(libraryPath string) error {
	environmentMu.Lock()
	defer environmentMu.Unlock()
	if environmentRefs != 0 {
		if environmentPath != libraryPath {
			return fmt.Errorf("onnxruntime environment already uses %q", environmentPath)
		}
		environmentRefs++
		return nil
	}
	ort.SetSharedLibraryPath(libraryPath)
	if err := ort.InitializeEnvironment(); err != nil {
		return fmt.Errorf("initialize onnxruntime environment: %w", err)
	}
	environmentPath = libraryPath
	environmentRefs = 1
	return nil
}

func releaseEnvironment() error {
	environmentMu.Lock()
	defer environmentMu.Unlock()
	if environmentRefs == 0 {
		return nil
	}
	environmentRefs--
	if environmentRefs != 0 {
		return nil
	}
	environmentPath = ""
	return ort.DestroyEnvironment()
}

func validateSignature(modelPath string) error {
	inputs, outputs, err := ort.GetInputOutputInfo(modelPath)
	if err != nil {
		return err
	}
	inputSet := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		inputSet[input.Name] = struct{}{}
	}
	for _, name := range inputNames {
		if _, ok := inputSet[name]; !ok {
			return fmt.Errorf("missing ONNX input %q", name)
		}
	}
	for _, output := range outputs {
		if output.Name == outputNames[0] {
			return nil
		}
	}
	return fmt.Errorf("missing ONNX output %q", outputNames[0])
}

// ValidateModelSignature opens the graph through the configured runtime and
// verifies the exact interface required by this extractor.
func ValidateModelSignature(libraryPath, modelPath string) (err error) {
	if err = acquireEnvironment(libraryPath); err != nil {
		return err
	}
	defer func() {
		if releaseErr := releaseEnvironment(); err == nil {
			err = releaseErr
		}
	}()
	return validateSignature(modelPath)
}

type tensorKey struct {
	tokens int
	words  int
}

type tensorSet struct {
	inputIDs      *ort.Tensor[int64]
	attentionMask *ort.Tensor[int64]
	wordsMask     *ort.Tensor[int64]
	textLengths   *ort.Tensor[int64]
	spanIdx       *ort.Tensor[int64]
	spanMask      *ort.Tensor[bool]
	logits        *ort.Tensor[float32]
}

func newTensorSet(key tensorKey) (*tensorSet, error) {
	set := &tensorSet{}
	var err error
	if set.inputIDs, err = ort.NewEmptyTensor[int64](ort.NewShape(1, int64(key.tokens))); err != nil {
		return nil, err
	}
	if set.attentionMask, err = ort.NewEmptyTensor[int64](ort.NewShape(1, int64(key.tokens))); err != nil {
		set.destroy()
		return nil, err
	}
	if set.wordsMask, err = ort.NewEmptyTensor[int64](ort.NewShape(1, int64(key.tokens))); err != nil {
		set.destroy()
		return nil, err
	}
	if set.textLengths, err = ort.NewEmptyTensor[int64](ort.NewShape(1, 1)); err != nil {
		set.destroy()
		return nil, err
	}
	spanCount := key.words * maxWidth
	if set.spanIdx, err = ort.NewEmptyTensor[int64](ort.NewShape(1, int64(spanCount), 2)); err != nil {
		set.destroy()
		return nil, err
	}
	if set.spanMask, err = ort.NewEmptyTensor[bool](ort.NewShape(1, int64(spanCount))); err != nil {
		set.destroy()
		return nil, err
	}
	if set.logits, err = ort.NewEmptyTensor[float32](ort.NewShape(1, int64(key.words), maxWidth, int64(len(Labels)))); err != nil {
		set.destroy()
		return nil, err
	}
	return set, nil
}

func (s *tensorSet) destroy() {
	if s.inputIDs != nil {
		s.inputIDs.Destroy()
	}
	if s.attentionMask != nil {
		s.attentionMask.Destroy()
	}
	if s.wordsMask != nil {
		s.wordsMask.Destroy()
	}
	if s.textLengths != nil {
		s.textLengths.Destroy()
	}
	if s.spanIdx != nil {
		s.spanIdx.Destroy()
	}
	if s.spanMask != nil {
		s.spanMask.Destroy()
	}
	if s.logits != nil {
		s.logits.Destroy()
	}
}

// Extractor owns one ONNX session. Calls and tensor reuse are serialized;
// Close waits for in-flight inference and is idempotent.
type Extractor struct {
	mu        sync.Mutex
	closed    bool
	threshold float64
	tokenizer *glinerTokenizer
	session   *ort.DynamicAdvancedSession
	tensors   map[tensorKey]*tensorSet
}

func Load(libraryPath, bundlePath string, threshold float64) (_ *Extractor, err error) {
	if err := ValidateBundle(bundlePath); err != nil {
		return nil, err
	}
	tokenizer, err := loadTokenizer(bundlePath)
	if err != nil {
		return nil, err
	}
	if err = acquireEnvironment(libraryPath); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = releaseEnvironment()
		}
	}()
	modelPath := ModelPathFromBundle(bundlePath)
	if err = validateSignature(modelPath); err != nil {
		return nil, fmt.Errorf("validate GLiNER model signature: %w", err)
	}
	options, err := ort.NewSessionOptions()
	if err != nil {
		return nil, fmt.Errorf("create GLiNER session options: %w", err)
	}
	defer options.Destroy()
	if err = options.SetGraphOptimizationLevel(ort.GraphOptimizationLevelEnableAll); err != nil {
		return nil, fmt.Errorf("configure GLiNER graph optimization: %w", err)
	}
	session, err := ort.NewDynamicAdvancedSession(modelPath, inputNames, outputNames, options)
	if err != nil {
		return nil, fmt.Errorf("create GLiNER session: %w", err)
	}
	if threshold <= 0 || threshold >= 1 {
		threshold = DefaultThreshold
	}
	return &Extractor{threshold: threshold, tokenizer: tokenizer, session: session, tensors: make(map[tensorKey]*tensorSet)}, nil
}

func ModelPathFromBundle(bundlePath string) string {
	return filepath.Join(bundlePath, "model.onnx")
}

func (e *Extractor) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	err := e.session.Destroy()
	for _, tensors := range e.tensors {
		tensors.destroy()
	}
	e.tensors = nil
	if environmentErr := releaseEnvironment(); err == nil {
		err = environmentErr
	}
	return err
}

func (e *Extractor) tensorSet(key tensorKey) (*tensorSet, error) {
	if tensors := e.tensors[key]; tensors != nil {
		return tensors, nil
	}
	tensors, err := newTensorSet(key)
	if err != nil {
		return nil, err
	}
	e.tensors[key] = tensors
	return tensors, nil
}

type scoredSpan struct {
	start int
	end   int
	class int
	score float64
}

func overlaps(left, right scoredSpan) bool {
	return left.start <= right.end && right.start <= left.end
}

func decode(text string, inputs modelInputs, logits []float32, wordBucket int, threshold float64, labels []string) []metadata.EntitySpan {
	spans := make([]scoredSpan, 0, len(inputs.Words))
	for start := range inputs.Words {
		for width := range maxWidth {
			end := start + width
			if end >= len(inputs.Words) {
				break
			}
			for class := range labels {
				index := ((start*maxWidth+width)*len(labels) + class)
				if index >= len(logits) || start >= wordBucket {
					continue
				}
				score := 1 / (1 + math.Exp(-float64(logits[index])))
				if score > threshold {
					spans = append(spans, scoredSpan{start: start, end: end, class: class, score: score})
				}
			}
		}
	}
	sort.SliceStable(spans, func(i, j int) bool { return spans[i].score > spans[j].score })
	accepted := make([]scoredSpan, 0, len(spans))
	for _, span := range spans {
		conflict := false
		for _, existing := range accepted {
			if overlaps(span, existing) {
				conflict = true
				break
			}
		}
		if !conflict {
			accepted = append(accepted, span)
		}
	}
	sort.SliceStable(accepted, func(i, j int) bool { return accepted[i].start < accepted[j].start })
	ret := make([]metadata.EntitySpan, 0, len(accepted))
	for _, span := range accepted {
		first, last := inputs.Words[span.start], inputs.Words[span.end]
		ret = append(ret, metadata.EntitySpan{
			ByteStart: first.ByteStart, ByteEnd: last.ByteEnd,
			Text: text[first.ByteStart:last.ByteEnd], Label: labels[span.class], Score: span.score,
		})
	}
	return ret
}

func (e *Extractor) Extract(ctx context.Context, text string, labels []string) ([]metadata.EntitySpan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(labels) == 0 {
		labels = Labels
	}
	if len(labels) != len(Labels) {
		return nil, fmt.Errorf("GLiNER requires the stable %d-label prompt", len(Labels))
	}
	for index := range labels {
		if labels[index] != Labels[index] {
			return nil, fmt.Errorf("GLiNER label %d is %q, expected %q", index, labels[index], Labels[index])
		}
	}
	inputs, err := e.tokenizer.build(text, labels)
	if err != nil {
		return nil, err
	}
	if len(inputs.InputIDs) > maxModelTokens {
		return nil, fmt.Errorf("%w: %d tokens", ErrInputTooLong, len(inputs.InputIDs))
	}
	if len(inputs.Words) == 0 {
		return nil, nil
	}
	key := tensorKey{
		tokens: len(inputs.InputIDs),
		words:  len(inputs.Words),
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tensors, err := e.tensorSet(key)
	if err != nil {
		return nil, fmt.Errorf("create GLiNER tensors: %w", err)
	}
	clear(tensors.inputIDs.GetData())
	clear(tensors.attentionMask.GetData())
	clear(tensors.wordsMask.GetData())
	clear(tensors.spanIdx.GetData())
	clear(tensors.spanMask.GetData())
	copy(tensors.inputIDs.GetData(), inputs.InputIDs)
	copy(tensors.attentionMask.GetData(), inputs.AttentionMask)
	copy(tensors.wordsMask.GetData(), inputs.WordsMask)
	copy(tensors.textLengths.GetData(), inputs.TextLengths)
	copy(tensors.spanIdx.GetData(), inputs.SpanIdx)
	copy(tensors.spanMask.GetData(), inputs.SpanMask)

	values := []ort.Value{tensors.inputIDs, tensors.attentionMask, tensors.wordsMask, tensors.textLengths, tensors.spanIdx, tensors.spanMask}
	if err := e.session.Run(values, []ort.Value{tensors.logits}); err != nil {
		return nil, fmt.Errorf("run GLiNER inference: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return decode(text, inputs, tensors.logits.GetData(), key.words, e.threshold, labels), nil
}
