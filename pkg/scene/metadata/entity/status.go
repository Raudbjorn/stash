package entity

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type ModelLifecycleState string

const (
	ModelMissing ModelLifecycleState = "missing"
	ModelInvalid ModelLifecycleState = "invalid"
	ModelLoading ModelLifecycleState = "loading"
	ModelReady   ModelLifecycleState = "ready"
)

type ModelStatus struct {
	Key       string
	State     ModelLifecycleState
	CachePath string
	LastError string
}

type modelLifecycle struct {
	loading   bool
	lastError string
}

var lifecycleStatus = struct {
	sync.RWMutex
	models map[string]modelLifecycle
}{
	models: make(map[string]modelLifecycle),
}

func setLoading(key string, loading bool) {
	lifecycleStatus.Lock()
	status := lifecycleStatus.models[key]
	status.loading = loading
	lifecycleStatus.models[key] = status
	lifecycleStatus.Unlock()
}

func setLastError(key string, err error) {
	lifecycleStatus.Lock()
	status := lifecycleStatus.models[key]
	if err == nil {
		status.lastError = ""
	} else {
		status.lastError = err.Error()
	}
	lifecycleStatus.models[key] = status
	lifecycleStatus.Unlock()
}

func Status(cachePath, key string) ModelStatus {
	path := BundlePath(cachePath, key)
	status := ModelStatus{Key: key, CachePath: path}
	spec, ok := FindModel(key)
	if !ok {
		status.State = ModelInvalid
		status.LastError = "unknown scene metadata model key"
		return status
	}
	lifecycleStatus.RLock()
	lifecycle := lifecycleStatus.models[key]
	lifecycleStatus.RUnlock()
	status.LastError = lifecycle.lastError
	if lifecycle.loading {
		status.State = ModelLoading
		return status
	}
	if err := cachedValidateBundle(path, spec); err != nil {
		if errors.Is(err, ErrBundleMissing) {
			status.State = ModelMissing
		} else {
			status.State = ModelInvalid
		}
		if status.LastError == "" {
			status.LastError = err.Error()
		}
		return status
	}
	status.State = ModelReady
	return status
}

// bundleValidation is the outcome of the last full checksum pass over a
// bundle, tagged with the cheap filesystem signature it was computed from.
type bundleValidation struct {
	signature string
	err       error
}

var validationCache = struct {
	sync.RWMutex
	models map[string]bundleValidation
}{
	models: make(map[string]bundleValidation),
}

// bundleSignature is a cheap (stat-only) fingerprint of a bundle's artifacts,
// used to detect whether a full checksum re-validation is needed.
func bundleSignature(path string, spec ModelSpec) (string, error) {
	var signature strings.Builder
	for _, artifact := range spec.Artifacts {
		info, err := os.Stat(filepath.Join(path, artifact.LocalPath))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return "", fmt.Errorf("%w: %s", ErrBundleMissing, artifact.LocalPath)
			}
			return "", fmt.Errorf("stat %s: %w", artifact.LocalPath, err)
		}
		fmt.Fprintf(&signature, "%s:%d:%d;", artifact.LocalPath, info.Size(), info.ModTime().UnixNano())
	}
	return signature.String(), nil
}

// cachedValidateBundle re-hashes a bundle only when its cheap filesystem
// signature (artifact sizes and modification times) has changed since the
// last full validation, so repeated status polls stay cheap while installs,
// reinstalls, and out-of-band bundle changes are still caught.
func cachedValidateBundle(path string, spec ModelSpec) error {
	signature, err := bundleSignature(path, spec)
	if err != nil {
		return err
	}

	validationCache.RLock()
	cached, ok := validationCache.models[path]
	validationCache.RUnlock()
	if ok && cached.signature == signature {
		return cached.err
	}

	validated := validateBundle(path, spec)
	validationCache.Lock()
	validationCache.models[path] = bundleValidation{signature: signature, err: validated}
	validationCache.Unlock()
	return validated
}
