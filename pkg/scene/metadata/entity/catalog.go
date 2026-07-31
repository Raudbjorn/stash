package entity

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

const modelRootDirectory = "scene-metadata-models"

// Artifact is one immutable file in a pinned model bundle.
type Artifact struct {
	RemotePath string
	LocalPath  string
	Size       int64
	SHA256     string
}

// ModelSpec describes one immutable model bundle in the built-in catalog.
type ModelSpec struct {
	Key           string
	Family        string
	Display       string
	HuggingFaceID string
	Revision      string
	Precision     string
	ApproxSize    int64
	License       string
	LicenseURL    string
	Artifacts     []Artifact
	Labels        []string
}

var commonGLiNERArtifacts = []Artifact{
	{RemotePath: "tokenizer.json", LocalPath: "tokenizer.json", Size: 8657198, SHA256: "677203884d026e721115cf0daccf70ec4239545a13d6619e3e66d7151e0c9ce3"},
	{RemotePath: "spm.model", LocalPath: "spm.model", Size: 2464616, SHA256: "c679fbf93643d19aab7ee10c0b99e460bdbc02fedf34b92b05af343b4af586fd"},
	{RemotePath: "gliner_config.json", LocalPath: "gliner_config.json", Size: 731, SHA256: "8e8b59de124a256a3f3de67879d0da686fe3f73ecc05093506ba525e451b920d"},
	{RemotePath: "config.json", LocalPath: "config.json", Size: 28, SHA256: "8aece71b73ca0fbd6dd121ad755deb736e7757d053ced523c2e4959ff446d3f5"},
	{RemotePath: "special_tokens_map.json", LocalPath: "special_tokens_map.json", Size: 286, SHA256: "9463f61e1b109a8eb4688b829260d7c6b1e6dff04c98ff7269bb89e2b92369b9"},
	{RemotePath: "added_tokens.json", LocalPath: "added_tokens.json", Size: 86, SHA256: "0a4df7f7953a90443fa51b6ca723242f6cb29e7fc34b1f27fc320c8e60de45b7"},
	{RemotePath: "tokenizer_config.json", LocalPath: "tokenizer_config.json", Size: 1806, SHA256: "cef106fb5c03d234f0af80f7577f1fc90b4317f26c26888d625abba11331dc89"},
}

func glinerSpec(key, display, repository, revision string, modelSize int64, modelSHA256 string, overrides ...Artifact) ModelSpec {
	artifacts := make([]Artifact, 0, len(commonGLiNERArtifacts)+1)
	artifacts = append(artifacts, Artifact{
		RemotePath: "onnx/model_int8.onnx",
		LocalPath:  "model.onnx",
		Size:       modelSize,
		SHA256:     modelSHA256,
	})
	artifacts = append(artifacts, commonGLiNERArtifacts...)
	for _, override := range overrides {
		for index := range artifacts {
			if artifacts[index].LocalPath == override.LocalPath {
				artifacts[index] = override
				break
			}
		}
	}
	return ModelSpec{
		Key:           key,
		Family:        "gliner",
		Display:       display,
		HuggingFaceID: repository,
		Revision:      revision,
		Precision:     "int8",
		ApproxSize:    modelSize,
		License:       "Apache-2.0",
		LicenseURL:    "https://www.apache.org/licenses/LICENSE-2.0",
		Artifacts:     artifacts,
		Labels:        append([]string(nil), Labels...),
	}
}

// Catalog contains every model that may be downloaded. Repositories and
// revisions are pinned; arbitrary Hub models are deliberately not accepted.
var Catalog = []ModelSpec{
	glinerSpec(
		"gliner-small-v2.1-int8",
		"GLiNER small v2.1 (INT8)",
		"onnx-community/gliner_small-v2.1",
		"8142fb00740ccea973e64b1272949ff48653df5e",
		183403734,
		"c76c90920547fd937aaf505e7f2de5ec73168bf1c25abbb55a298104cb061400",
	),
	glinerSpec(
		"gliner-medium-v2.1-int8",
		"GLiNER medium v2.1 (INT8)",
		"onnx-community/gliner_medium-v2.1",
		"959437589dc623d4c0a93f6e2828213567929cde",
		255347355,
		"3107f08ce7c5263503a23b18c0b26287b2bd49eba24635f5d44da2d27a27cbd6",
		Artifact{RemotePath: "gliner_config.json", LocalPath: "gliner_config.json", Size: 730, SHA256: "7a45664d36377a1cd8893424dedb61b466a1541213c9d47324ed5bb32a5c6aeb"},
	),
	glinerSpec(
		"gliner-large-v2.1-int8",
		"GLiNER large v2.1 (INT8)",
		"onnx-community/gliner_large-v2.1",
		"cb194c5f7353ddb7e0eb38967b609f57f2620c99",
		653199597,
		"23e6ca25f7889744fcd3edc44bf018a1b279c313e5e3dc5bbca54dc99942f1bd",
		Artifact{RemotePath: "gliner_config.json", LocalPath: "gliner_config.json", Size: 731, SHA256: "ed6a6099d4f85a104214b83b91d48246d2c18b1b3f0288880ffe01a321d31f9c"},
	),
	glinerSpec(
		"gliner-multi-v2.1-int8",
		"GLiNER multi v2.1 (INT8)",
		"onnx-community/gliner_multi-v2.1",
		"6ddaeb9413b0e71ad8457da1aab378a165b24058",
		349120924,
		"995058c82c5f570601dd8a0ba74ee60a392f764268bc5f628455e44dd3b476ec",
		Artifact{RemotePath: "tokenizer.json", LocalPath: "tokenizer.json", Size: 16331948, SHA256: "914bd3c8fb7b525af9e23b60d0ec7b1248ddb2b99014efd9c02ebeb022f8cab7"},
		Artifact{RemotePath: "spm.model", LocalPath: "spm.model", Size: 4305025, SHA256: "13c8d666d62a7bc4ac8f040aab68e942c861f93303156cc28f5c7e885d86d6e3"},
		Artifact{RemotePath: "gliner_config.json", LocalPath: "gliner_config.json", Size: 731, SHA256: "1ef59c57fe6816a155697a5670ca8d0faf0babe13f199a2c2b5b65113399ed72"},
		Artifact{RemotePath: "added_tokens.json", LocalPath: "added_tokens.json", Size: 86, SHA256: "030e747c4ca7992a3ac794c6fda9919352c88ae722e85178217cd083b450078d"},
		Artifact{RemotePath: "tokenizer_config.json", LocalPath: "tokenizer_config.json", Size: 1806, SHA256: "78f866883daf7ee2bc400200a155cdbe9116ed0a6ed597ff573ea2c9862a89a6"},
	),
}

func cloneSpec(spec ModelSpec) ModelSpec {
	spec.Artifacts = append([]Artifact(nil), spec.Artifacts...)
	spec.Labels = append([]string(nil), spec.Labels...)
	return spec
}

// FindModel returns a copy of the pinned spec for key.
func FindModel(key string) (ModelSpec, bool) {
	for _, spec := range Catalog {
		if spec.Key == key {
			return cloneSpec(spec), true
		}
	}
	return ModelSpec{}, false
}

func ModelRootPath(cachePath string) string {
	return filepath.Join(cachePath, modelRootDirectory)
}

func BundlePath(cachePath, key string) string {
	return filepath.Join(ModelRootPath(cachePath), key)
}

func ModelPath(cachePath, key string) string {
	return filepath.Join(BundlePath(cachePath, key), "model.onnx")
}

func modelForBundle(path string) (ModelSpec, bool) {
	if spec, ok := FindModel(filepath.Base(filepath.Clean(path))); ok {
		return spec, true
	}
	modelInfo, err := os.Stat(filepath.Join(path, "model.onnx"))
	if err != nil {
		return ModelSpec{}, false
	}
	for _, spec := range Catalog {
		for _, artifact := range spec.Artifacts {
			if artifact.LocalPath == "model.onnx" && artifact.Size == modelInfo.Size() {
				return cloneSpec(spec), true
			}
		}
	}
	return ModelSpec{}, false
}

var ErrBundleMissing = errors.New("scene metadata entity model is not installed")

// ValidateBundle verifies every pinned byte before a bundle can be loaded.
func ValidateBundle(path string) error {
	spec, ok := modelForBundle(path)
	if !ok {
		return fmt.Errorf("unknown scene metadata model key %q", filepath.Base(filepath.Clean(path)))
	}
	return validateBundle(path, spec)
}

func validateBundle(path string, spec ModelSpec) error {
	for _, artifact := range spec.Artifacts {
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

// InstalledKeys lists catalog bundle directories present under the model root.
func InstalledKeys(cachePath string) []string {
	entries, err := os.ReadDir(ModelRootPath(cachePath))
	if err != nil {
		return nil
	}
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, ok := FindModel(entry.Name()); ok {
			keys = append(keys, entry.Name())
		}
	}
	sort.Strings(keys)
	return keys
}
