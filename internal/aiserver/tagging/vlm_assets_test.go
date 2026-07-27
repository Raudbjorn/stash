package tagging

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stashapp/stash/pkg/aitag/assets"
)

func TestResolveVLMArtifactsReportsExactMissingPaths(t *testing.T) {
	modelDir := t.TempDir()
	settings := Settings{ModelDir: modelDir, VLMModel: assets.DefaultVisionPair}
	_, err := resolveVLMArtifacts(settings)
	if err == nil {
		t.Fatal("missing llama-server was accepted")
	}
	serverAsset, assetErr := assets.ServerAsset("")
	if assetErr != nil {
		t.Skip(assetErr)
	}
	downloader := &assets.Downloader{Dir: modelDir}
	serverPath := downloader.Path(serverAsset)
	if !strings.Contains(err.Error(), serverPath) || !strings.Contains(err.Error(), serverAsset.License) {
		t.Errorf("server error does not name path and licence: %v", err)
	}

	if err := os.MkdirAll(filepath.Dir(serverPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(serverPath, []byte("server"), 0o755); err != nil {
		t.Fatal(err)
	}
	pair, _ := assets.FindPair(assets.DefaultVisionPair)
	_, err = resolveVLMArtifacts(settings)
	if err == nil {
		t.Fatal("missing VLM pair was accepted")
	}
	for _, path := range []string{downloader.Path(pair.Primary), downloader.Path(pair.Companion)} {
		if !strings.Contains(err.Error(), path) {
			t.Errorf("pair error does not name missing path %s: %v", path, err)
		}
	}
	if !strings.Contains(err.Error(), pair.Name) || !strings.Contains(err.Error(), pair.License) {
		t.Errorf("pair error does not name model and licence: %v", err)
	}
}

func TestResolveVLMArtifactsRejectsUnknownPair(t *testing.T) {
	_, err := resolveVLMArtifacts(Settings{ModelDir: t.TempDir(), VLMModel: "not-a-pair"})
	if err == nil || !strings.Contains(err.Error(), "unknown VLM model pair") {
		t.Errorf("unknown pair error = %v", err)
	}
}

func TestServerAssetForGPUSelectsExplicitBuild(t *testing.T) {
	cpu, err := serverAssetForGPU(0)
	if err != nil {
		t.Skip(err)
	}
	if strings.Contains(cpu.Name, "vulkan") {
		t.Errorf("CPU selection returned %s", cpu.Name)
	}
	gpu, err := serverAssetForGPU(1)
	if err != nil {
		// Unsupported platforms must report rather than silently falling back.
		if !strings.Contains(err.Error(), "unsupported") {
			t.Errorf("GPU selection error = %v", err)
		}
		return
	}
	if !strings.Contains(gpu.Name, "vulkan") && gpu.Name == cpu.Name {
		// macOS uses the normal Metal-capable archive; Linux must differ.
		if strings.Contains(strings.ToLower(gpu.URL), "ubuntu") {
			t.Errorf("Linux GPU selection silently reused CPU asset %s", gpu.Name)
		}
	}
}

func TestBuildVLMRejectsInvalidConfigurationWithRemediation(t *testing.T) {
	tests := []struct {
		name        string
		settings    Settings
		message     string
		remediation string
	}{
		{
			name:        "empty labels",
			settings:    Settings{VLMLabels: nil},
			message:     "ai_tagging_vlm_labels",
			remediation: "ai_tagging_vlm_labels",
		},
		{
			name:        "unknown pair",
			settings:    Settings{VLMModel: "not-a-pair", VLMLabels: []string{"tag"}},
			message:     "unknown VLM model pair",
			remediation: "ai_tagging_vlm_model",
		},
		{
			name:        "negative GPU layers",
			settings:    Settings{VLMLabels: []string{"tag"}, VLMGPULayers: -1},
			message:     "ai_tagging_vlm_gpu_layers cannot be negative",
			remediation: "ai_tagging_vlm_gpu_layers",
		},
		{
			name:        "negative context",
			settings:    Settings{VLMLabels: []string{"tag"}, VLMContext: -1},
			message:     "ai_tagging_vlm_context cannot be negative",
			remediation: "ai_tagging_vlm_context",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.settings.Provider = ProviderVLM
			test.settings.FFmpegPath = "ffmpeg"
			test.settings.ModelDir = t.TempDir()
			provider, _, status := Build(context.Background(), test.settings)
			if provider != nil {
				provider.Close()
				t.Fatal("invalid VLM configuration returned a provider")
			}
			if !strings.Contains(status.Message, test.message) ||
				!strings.Contains(status.Remediation, test.remediation) {
				t.Errorf("status = %+v", status)
			}
		})
	}
}

func TestUnknownProviderRemediationIncludesVLM(t *testing.T) {
	_, _, status := Build(context.Background(), Settings{Provider: "unknown"})
	if !strings.Contains(status.Remediation, ProviderVLM) {
		t.Errorf("remediation does not offer %q: %s", ProviderVLM, status.Remediation)
	}
}
