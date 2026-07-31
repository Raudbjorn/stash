package entity

import (
	"errors"
	"sync"
)

type ModelState string

const (
	ModelMissing ModelState = "missing"
	ModelInvalid ModelState = "invalid"
	ModelLoading ModelState = "loading"
	ModelReady   ModelState = "ready"
)

type ModelStatus struct {
	Key       string
	State     ModelState
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
	if err := validateBundle(path, spec); err != nil {
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
