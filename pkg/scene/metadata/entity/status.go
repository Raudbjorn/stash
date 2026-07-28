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
	ModelID   string
	Version   string
	State     ModelState
	CachePath string
	LastError string
}

var lifecycleStatus struct {
	sync.RWMutex
	loading   bool
	lastError string
}

func setLoading(loading bool) {
	lifecycleStatus.Lock()
	lifecycleStatus.loading = loading
	lifecycleStatus.Unlock()
}

func setLastError(err error) {
	lifecycleStatus.Lock()
	if err == nil {
		lifecycleStatus.lastError = ""
	} else {
		lifecycleStatus.lastError = err.Error()
	}
	lifecycleStatus.Unlock()
}

func Status(cachePath string) ModelStatus {
	path := BundlePath(cachePath)
	status := ModelStatus{ModelID: ModelID, Version: ModelVersion, CachePath: path}
	lifecycleStatus.RLock()
	loading, lastError := lifecycleStatus.loading, lifecycleStatus.lastError
	lifecycleStatus.RUnlock()
	status.LastError = lastError
	if loading {
		status.State = ModelLoading
		return status
	}
	if err := ValidateBundle(path); err != nil {
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
