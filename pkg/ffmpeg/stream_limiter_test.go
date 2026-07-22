package ffmpeg

import (
	"testing"

	"github.com/stashapp/stash/pkg/models"
)

type fakeLimitConfig struct{ max int }

func (f fakeLimitConfig) GetMaxStreamingTranscodeSize() models.StreamingResolutionEnum {
	return models.StreamingResolutionEnumOriginal
}
func (fakeLimitConfig) GetLiveTranscodeInputArgs() []string    { return nil }
func (fakeLimitConfig) GetLiveTranscodeOutputArgs() []string   { return nil }
func (fakeLimitConfig) GetTranscodeHardwareAcceleration() bool { return false }
func (f fakeLimitConfig) GetMaxConcurrentStreamsPerScene() int { return f.max }

func newLimiterSM(max int) *StreamManager {
	return &StreamManager{
		config:       fakeLimitConfig{max: max},
		streamCounts: make(map[string]int),
	}
}

func TestStreamLimiterCapsPerKey(t *testing.T) {
	sm := newLimiterSM(2)

	if !sm.acquireStreamSlot("a") || !sm.acquireStreamSlot("a") {
		t.Fatal("expected first two acquisitions to succeed")
	}
	if sm.acquireStreamSlot("a") {
		t.Fatal("expected third acquisition to be refused")
	}

	// a different key has its own budget
	if !sm.acquireStreamSlot("b") {
		t.Fatal("expected acquisition for distinct key to succeed")
	}

	// releasing frees a slot
	sm.releaseStreamSlot("a")
	if !sm.acquireStreamSlot("a") {
		t.Fatal("expected acquisition to succeed after release")
	}
}

func TestStreamLimiterUnlimitedWhenZero(t *testing.T) {
	sm := newLimiterSM(0)
	for i := 0; i < 100; i++ {
		if !sm.acquireStreamSlot("x") {
			t.Fatalf("limit 0 should be unlimited, failed at %d", i)
		}
	}
}

func TestStreamLimiterReleaseCleansUp(t *testing.T) {
	sm := newLimiterSM(1)
	sm.acquireStreamSlot("a")
	sm.releaseStreamSlot("a")
	if _, ok := sm.streamCounts["a"]; ok {
		t.Fatal("expected key to be deleted once count returns to zero")
	}
	// over-release must not underflow
	sm.releaseStreamSlot("a")
	if sm.streamCounts["a"] != 0 {
		t.Fatal("over-release should not produce a negative/leftover count")
	}
}
