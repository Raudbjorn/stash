package entity

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	ModelID       = "onnx-community/gliner_small-v2.1"
	ModelRevision = "8142fb00740ccea973e64b1272949ff48653df5e"
	ModelVersion  = "gliner-small-v2.1-int8-8142fb0"
	modelRootDir  = "scene-metadata-models"
)

// Artifact is one immutable file in the pinned model bundle.
type Artifact struct {
	RemotePath string
	LocalPath  string
	Size       int64
	SHA256     string
}

// Artifacts is the complete pinned model bundle. Checksums were computed from
// ModelRevision; no moving branch or unverified sidecar is accepted.
var Artifacts = []Artifact{
	{RemotePath: "onnx/model_int8.onnx", LocalPath: "model.onnx", Size: 183403734, SHA256: "c76c90920547fd937aaf505e7f2de5ec73168bf1c25abbb55a298104cb061400"},
	{RemotePath: "tokenizer.json", LocalPath: "tokenizer.json", Size: 8657198, SHA256: "677203884d026e721115cf0daccf70ec4239545a13d6619e3e66d7151e0c9ce3"},
	{RemotePath: "spm.model", LocalPath: "spm.model", Size: 2464616, SHA256: "c679fbf93643d19aab7ee10c0b99e460bdbc02fedf34b92b05af343b4af586fd"},
	{RemotePath: "gliner_config.json", LocalPath: "gliner_config.json", Size: 731, SHA256: "8e8b59de124a256a3f3de67879d0da686fe3f73ecc05093506ba525e451b920d"},
	{RemotePath: "config.json", LocalPath: "config.json", Size: 28, SHA256: "8aece71b73ca0fbd6dd121ad755deb736e7757d053ced523c2e4959ff446d3f5"},
	{RemotePath: "special_tokens_map.json", LocalPath: "special_tokens_map.json", Size: 286, SHA256: "9463f61e1b109a8eb4688b829260d7c6b1e6dff04c98ff7269bb89e2b92369b9"},
	{RemotePath: "added_tokens.json", LocalPath: "added_tokens.json", Size: 86, SHA256: "0a4df7f7953a90443fa51b6ca723242f6cb29e7fc34b1f27fc320c8e60de45b7"},
	{RemotePath: "tokenizer_config.json", LocalPath: "tokenizer_config.json", Size: 1806, SHA256: "cef106fb5c03d234f0af80f7577f1fc90b4317f26c26888d625abba11331dc89"},
}

func BundlePath(cachePath string) string {
	return filepath.Join(cachePath, modelRootDir, ModelVersion)
}

func ModelPath(cachePath string) string {
	return filepath.Join(BundlePath(cachePath), "model.onnx")
}

var ErrBundleMissing = errors.New("scene metadata entity model is not installed")

// ValidateBundle verifies every pinned byte before a bundle can be loaded.
func ValidateBundle(path string) error {
	for _, artifact := range Artifacts {
		filename := filepath.Join(path, artifact.LocalPath)
		file, err := os.Open(filename)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("%w: %s", ErrBundleMissing, artifact.LocalPath)
			}
			return fmt.Errorf("open %s: %w", artifact.LocalPath, err)
		}
		hash := sha256.New()
		count, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return fmt.Errorf("hash %s: %w", artifact.LocalPath, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close %s: %w", artifact.LocalPath, closeErr)
		}
		if count != artifact.Size {
			return fmt.Errorf("%s has size %d, expected %d", artifact.LocalPath, count, artifact.Size)
		}
		actual := hex.EncodeToString(hash.Sum(nil))
		if actual != artifact.SHA256 {
			return fmt.Errorf("%s checksum %s, expected %s", artifact.LocalPath, actual, artifact.SHA256)
		}
	}
	return nil
}
