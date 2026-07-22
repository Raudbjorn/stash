package ffmpeg

// The concurrent-stream limiter caps the number of simultaneous live-transcode
// streams served for a single file. This protects the server from runaway or
// duplicate transcodes (for example when an external app repeatedly re-requests
// a stream). Direct (non-transcoded) streams are not limited, since they are
// cheap and rely on HTTP range requests that legitimately open several
// connections. A configured limit of 0 disables the cap.

// acquireStreamSlot attempts to reserve a concurrent-stream slot for the given
// key (the file path). It returns false if the per-file limit has been reached.
func (sm *StreamManager) acquireStreamSlot(key string) bool {
	limit := sm.config.GetMaxConcurrentStreamsPerScene()

	sm.streamCountMu.Lock()
	defer sm.streamCountMu.Unlock()

	if limit > 0 && sm.streamCounts[key] >= limit {
		return false
	}

	sm.streamCounts[key]++
	return true
}

// releaseStreamSlot releases a previously acquired slot for the given key.
func (sm *StreamManager) releaseStreamSlot(key string) {
	sm.streamCountMu.Lock()
	defer sm.streamCountMu.Unlock()

	if sm.streamCounts[key] > 0 {
		sm.streamCounts[key]--
		if sm.streamCounts[key] == 0 {
			delete(sm.streamCounts, key)
		}
	}
}
