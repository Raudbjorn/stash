package tagging

import (
	"testing"

	"github.com/stashapp/stash/internal/aiserver/action"
	"github.com/stashapp/stash/internal/aiserver/service"
	"github.com/stashapp/stash/pkg/aitag/llamaprov"
	"github.com/stashapp/stash/pkg/models"
)

func TestDefaultFrameIntervalIsProviderSpecific(t *testing.T) {
	if got := DefaultFrameInterval(ProviderVLM, 2); got != llamaprov.DefaultFrameInterval {
		t.Errorf("VLM default = %v", got)
	}
	if got := DefaultFrameInterval(ProviderNative, 2); got != 2 {
		t.Errorf("native default = %v", got)
	}
	if got := DefaultFrameInterval(ProviderHTTP, 7); got != 7 {
		t.Errorf("HTTP configured default = %v", got)
	}
}

func TestAnalysisActionPublishesServiceFrameInterval(t *testing.T) {
	for name, interval := range map[string]float64{"dense": 2, "VLM": 30} {
		t.Run(name, func(t *testing.T) {
			tagger := NewService(models.Repository{}, nil, Config{DefaultFrameInterval: interval})
			actions := action.NewRegistry()
			tagger.Register(actions, service.NewRegistry())
			registrations := actions.Get(ActionID)
			if len(registrations) != 1 {
				t.Fatalf("registrations = %d", len(registrations))
			}
			properties := registrations[0].Definition.InputSchema["properties"].(map[string]any)
			frame := properties["frame_interval"].(map[string]any)
			if got := frame["default"]; got != interval {
				t.Errorf("schema default = %v, want %v", got, interval)
			}
		})
	}
}
