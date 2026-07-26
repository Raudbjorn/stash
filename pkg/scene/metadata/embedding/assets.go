package embedding

import (
	_ "embed"
)

// modelBytes and vocabBytes are the quantized all-MiniLM-L6-v2 sentence-
// embedding model (ONNX, int8 dynamic quantization, ~23MB) and its BERT
// wordpiece vocabulary, embedded directly into the stash binary - same
// pattern as ui/ui.go's embedded UI assets. This keeps the feature fully
// offline with no lazy download: the model ships with the binary, only the
// ONNX Runtime shared library itself is a runtime (not build-time)
// dependency. See https://huggingface.co/Xenova/all-MiniLM-L6-v2.
var (
	//go:embed model.onnx
	modelBytes []byte

	//go:embed vocab.txt
	vocabBytes []byte
)
