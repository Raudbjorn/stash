package tagging

import (
	"fmt"
	"os"
	"runtime"

	"github.com/stashapp/stash/pkg/aitag/assets"
)

type vlmArtifacts struct {
	pair                              assets.Pair
	serverPath, modelPath, mmprojPath string
	contextTokens                     int
}

func resolveVLMArtifacts(settings Settings) (vlmArtifacts, error) {
	pairName := settings.VLMModel
	if pairName == "" {
		pairName = assets.DefaultVisionPair
	}
	pair, ok := assets.FindPair(pairName)
	if !ok {
		return vlmArtifacts{}, fmt.Errorf(
			"unknown VLM model pair %q; choose a checksum-pinned pair from the AI settings", pairName)
	}
	if !pair.Downloadable() || pair.License == "" || pair.LicenseURL == "" {
		return vlmArtifacts{}, fmt.Errorf(
			"VLM pair %s has incomplete checksum or licence metadata and cannot be executed", pair.Name)
	}
	for _, asset := range pair.Assets() {
		if len(asset.SHA256) != 64 || asset.Size <= 0 || asset.License == "" {
			return vlmArtifacts{}, fmt.Errorf(
				"VLM artifact %s has incomplete checksum, size, or licence metadata", asset.Name)
		}
	}

	contextTokens := settings.VLMContext
	if contextTokens == 0 {
		contextTokens = pair.ContextTokens
	}
	if contextTokens < 0 {
		return vlmArtifacts{}, fmt.Errorf("ai_tagging_vlm_context cannot be negative")
	}

	serverAsset, err := serverAssetForGPU(settings.VLMGPULayers)
	if err != nil {
		return vlmArtifacts{}, err
	}
	if len(serverAsset.SHA256) != 64 || serverAsset.Size <= 0 || serverAsset.License == "" {
		return vlmArtifacts{}, fmt.Errorf("llama-server has incomplete checksum, size, or licence metadata")
	}

	downloader := &assets.Downloader{Dir: settings.ModelDir}
	serverPath := downloader.Path(serverAsset)
	if !downloader.Installed(serverAsset) {
		return vlmArtifacts{}, fmt.Errorf(
			"llama-server %s (%s) is not installed or verified at %s; download the pinned %s asset from the AI settings",
			assets.DefaultServerVersion, serverAsset.License, serverPath, serverAsset.Name)
	}
	if err := requireVLMPair(downloader, pair); err != nil {
		return vlmArtifacts{}, err
	}
	modelPath := downloader.Path(pair.Primary)
	mmprojPath := downloader.Path(pair.Companion)

	return vlmArtifacts{
		pair: pair, serverPath: serverPath, modelPath: modelPath,
		mmprojPath: mmprojPath, contextTokens: contextTokens,
	}, nil
}

func requireVLMPair(downloader *assets.Downloader, pair assets.Pair) error {
	modelPath := downloader.Path(pair.Primary)
	mmprojPath := downloader.Path(pair.Companion)
	if downloader.PairInstalled(pair) {
		return nil
	}
	missing := make([]string, 0, 2)
	for _, path := range []string{modelPath, mmprojPath} {
		if _, statErr := os.Stat(path); statErr != nil {
			missing = append(missing, path)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf(
			"VLM pair %s (%s) is not installed; missing %v; download both pinned GGUF files from the AI settings",
			pair.Name, pair.License, missing)
	}
	return fmt.Errorf(
		"VLM pair %s (%s) failed checksum verification at %s and %s; re-download both pinned GGUF files",
		pair.Name, pair.License, modelPath, mmprojPath)
}

func serverAssetForGPU(gpuLayers int) (assets.Asset, error) {
	if gpuLayers < 0 {
		return assets.Asset{}, fmt.Errorf("ai_tagging_vlm_gpu_layers cannot be negative")
	}
	if gpuLayers == 0 {
		return assets.ServerAsset("")
	}
	switch {
	case runtime.GOOS == "linux" && runtime.GOARCH == "amd64":
		return assets.VulkanServerAsset("")
	case runtime.GOOS == "darwin" && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64"):
		return assets.ServerAsset("")
	default:
		return assets.Asset{}, fmt.Errorf(
			"GPU llama-server is unsupported on %s/%s; set ai_tagging_vlm_gpu_layers to 0 or install a pinned supported build",
			runtime.GOOS, runtime.GOARCH)
	}
}
