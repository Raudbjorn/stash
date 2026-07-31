package tagging

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stashapp/stash/internal/aiserver/llamahost"
	"github.com/stashapp/stash/pkg/aitag"
	"github.com/stashapp/stash/pkg/aitag/assets"
	"github.com/stashapp/stash/pkg/aitag/httpprov"
	"github.com/stashapp/stash/pkg/aitag/llamaprov"
	"github.com/stashapp/stash/pkg/aitag/moderation"
	"github.com/stashapp/stash/pkg/aitag/native"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/onnx"
)

// Building a provider from configuration.
//
// Every failure here is a REPORTED STATE rather than an error that stops the AI
// server: a missing model or an unreachable inference server disables tagging
// and leaves the scheduler, the interactions pipeline and the recommenders
// running. Stash must boot on a machine with none of this installed.

// Settings are what the server knows about tagging.
type Settings struct {
	// Provider selects the backend. Empty is the default: analysis needs either
	// a model download or a running server, and starting one unrequested would
	// be a surprise.
	Provider string

	// ServerURL is the remote server for the HTTP provider.
	ServerURL string

	// ModelDir holds downloaded models and the ONNX runtime.
	ModelDir string
	// RulesDir holds the per-category marker rules.
	RulesDir string

	// EmbedModel names which catalog entry to use for embeddings.
	EmbedModel string
	// HeadName is the trained head to classify with, if any.
	HeadName string

	// OpenAIKey authenticates the moderation provider.
	OpenAIKey string

	// VLMModel selects a pinned vision-language pair.
	VLMModel string
	// VLMLabels is the open-vocabulary action taxonomy.
	VLMLabels []string
	// VLMGPULayers enables explicit llama.cpp GPU offload when positive.
	VLMGPULayers int
	// VLMContext overrides the pair's default context window when positive.
	VLMContext int

	FFmpegPath          string
	FrameInterval       float64
	Threshold           float64
	MaxSpanMergeSeconds float64
}

// Status describes what tagging can currently do.
//
// Returned rather than logged, so the settings page can explain why analysis is
// unavailable instead of the user having to read the log.
type Status struct {
	Provider string `json:"provider"`
	// Available reports whether an analysis would run.
	Available bool `json:"available"`
	// Message explains the current state in one line.
	Message string `json:"message,omitempty"`
	// Remediation says what the user can do about it.
	Remediation string `json:"remediation,omitempty"`

	// Runtime describes the loaded ONNX runtime, when there is one.
	Runtime *onnx.Info `json:"runtime,omitempty"`
	// Models lists what would run.
	Models []aitag.ModelInfo `json:"models,omitempty"`
	// RulesLoaded counts the marker rule categories found.
	RulesLoaded int `json:"rules_loaded"`

	live func() (available bool, message, remediation string)
}

// Current combines the setup snapshot with a supervised provider's live state.
func (s Status) Current(provider aitag.Provider) Status {
	if s.live == nil {
		return s
	}
	s.Available, s.Message, s.Remediation = s.live()
	if provider != nil {
		if models, err := provider.Models(context.Background()); err == nil {
			s.Models = models
		}
	}
	return s
}

// Provider names.
const (
	ProviderNative = native.ProviderName
	ProviderHTTP   = httpprov.ProviderName
	// ProviderModeration is OpenAI's moderation endpoint: the one hosted option
	// whose usage policy permits this content. A coarse gate over six
	// categories, not a tagger - see pkg/aitag/moderation.
	ProviderModeration = moderation.ProviderName
	ProviderVLM        = llamaprov.ProviderName
)

// DefaultFrameInterval keeps the sparse VLM cadence independent of dense
// embedding and remote-server sampling.
func DefaultFrameInterval(provider string, configured float64) float64 {
	if provider == ProviderVLM {
		return llamaprov.DefaultFrameInterval
	}
	if configured > 0 {
		return configured
	}
	return 2
}

// Build assembles a provider and rules from settings.
//
// Returns a nil provider with a Status explaining why, rather than an error,
// whenever the reason is a configuration or installation state. A real error is
// reserved for something genuinely broken.
func Build(ctx context.Context, settings Settings) (aitag.Provider, *aitag.Rules, Status) {
	status := Status{Provider: settings.Provider}

	rules := loadRules(settings.RulesDir, &status)

	switch settings.Provider {
	case "":
		status.Message = "No AI tagging provider is selected."
		status.Remediation = "Choose the native pipeline or point Stash at an AI server in the AI settings."
		return nil, rules, status

	case ProviderHTTP:
		provider, ok := buildHTTP(ctx, settings, &status)
		if !ok {
			return nil, rules, status
		}
		return provider, rules, status

	case ProviderNative:
		provider, ok := buildNative(ctx, settings, &status)
		if !ok {
			return nil, rules, status
		}
		return provider, rules, status

	case ProviderModeration:
		provider, ok := buildModeration(ctx, settings, &status)
		if !ok {
			return nil, rules, status
		}
		return provider, rules, status

	case ProviderVLM:
		provider, ok := buildVLM(ctx, settings, &status)
		if !ok {
			return nil, rules, status
		}
		return provider, rules, status

	default:
		status.Message = fmt.Sprintf("Unknown AI tagging provider %q.", settings.Provider)
		status.Remediation = fmt.Sprintf("Set ai_tagging_provider to %q, %q, %q or %q.",
			ProviderNative, ProviderHTTP, ProviderModeration, ProviderVLM)
		return nil, rules, status
	}
}

func buildHTTP(ctx context.Context, settings Settings, status *Status) (aitag.Provider, bool) {
	if settings.ServerURL == "" {
		status.Message = "No AI server URL is configured."
		status.Remediation = "Set ai_tagging_server_url to your AI server's address."
		return nil, false
	}

	provider := httpprov.New(httpprov.Config{
		ServerURL:     settings.ServerURL,
		FrameInterval: settings.FrameInterval,
		Threshold:     settings.Threshold,
	})

	// Probed once at startup so the settings page can say whether it worked.
	// A failure here does not prevent the provider being used: the server may
	// simply be starting, and every analysis re-checks.
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if err := provider.Available(probeCtx); err != nil {
		status.Message = err.Error()
		status.Remediation = "Start the AI server, or check the URL."
		// Returned anyway: it may recover, and a queued task will find out.
		return provider, true
	}

	if models, err := provider.Models(probeCtx); err == nil {
		status.Models = models
	}

	status.Available = true
	status.Message = fmt.Sprintf("Connected to the AI server at %s.", settings.ServerURL)
	return provider, true
}

// buildModeration assembles the OpenAI moderation provider.
//
// Kept deliberately blunt about what it is: six coarse categories against the
// dozens of fine-grained labels a trained head produces. A user who selects it
// expecting a tagger should be told immediately, not after a library-wide run.
func buildModeration(ctx context.Context, settings Settings, status *Status) (aitag.Provider, bool) {
	if settings.OpenAIKey == "" {
		status.Message = "No OpenAI API key is configured."
		status.Remediation = "Set ai_tagging_openai_key, or choose the native pipeline."
		return nil, false
	}

	provider := moderation.New(moderation.Config{
		APIKey:        settings.OpenAIKey,
		FFmpegPath:    settings.FFmpegPath,
		FrameInterval: settings.FrameInterval,
		Threshold:     settings.Threshold,
	})

	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	if err := provider.Available(probeCtx); err != nil {
		status.Message = err.Error()
		status.Remediation = "Check the API key and that the account has moderation access."
		return provider, true
	}

	if models, err := provider.Models(probeCtx); err == nil {
		status.Models = models
	}

	status.Available = true
	status.Message = "Connected to OpenAI's moderation endpoint. This is a coarse six-category gate, " +
		"not a fine-grained tagger: use it as a pre-filter alongside the native pipeline."
	return provider, true
}

func buildNative(ctx context.Context, settings Settings, status *Status) (aitag.Provider, bool) {
	if settings.FFmpegPath == "" {
		status.Message = "ffmpeg is not available."
		status.Remediation = "Let Stash download ffmpeg, or set its path in the system settings."
		return nil, false
	}

	downloader := &assets.Downloader{Dir: settings.ModelDir}

	// The runtime first: without it nothing else can even be loaded.
	runtimeAsset, err := assets.RuntimeAsset("")
	if err != nil {
		status.Message = err.Error()
		status.Remediation = "Use the AI server provider instead."
		return nil, false
	}

	runtimePath := downloader.Path(runtimeAsset)
	if _, err := os.Stat(runtimePath); err != nil {
		status.Message = "The ONNX runtime is not installed."
		status.Remediation = "Download it from the AI settings, or place it at " + runtimePath + "."
		return nil, false
	}

	if err := onnx.Initialize(runtimePath); err != nil {
		status.Message = "The ONNX runtime could not be loaded: " + err.Error()
		status.Remediation = "Re-download it, or check that this build is dynamically linked."
		return nil, false
	}
	info := onnx.Describe()
	status.Runtime = &info

	modelName := settings.EmbedModel
	if modelName == "" {
		// Naming the default in the catalog rather than taking whichever entry
		// happens to be first: a trained head is fitted to one model's embedding
		// space, so a reordering that quietly changed the embedder would leave
		// every existing head producing confident nonsense.
		modelName = assets.DefaultEmbedder
		if _, ok := assets.FindModel(modelName); !ok {
			candidates := assets.ModelsForRole(assets.RoleEmbedding)
			if len(candidates) == 0 {
				status.Message = "No embedding model is available."
				return nil, false
			}
			modelName = candidates[0].Name
		}
	}

	model, ok := assets.FindModel(modelName)
	if !ok {
		status.Message = fmt.Sprintf("Unknown embedding model %q.", modelName)
		status.Remediation = "Pick one of the models listed in the AI settings."
		return nil, false
	}

	modelPath := filepath.Join(settings.ModelDir, model.Name)
	if _, err := os.Stat(modelPath); err != nil {
		status.Message = fmt.Sprintf("The %s model is not installed.", model.Name)
		status.Remediation = "Export it with the documented command and place it at " + modelPath + "."
		return nil, false
	}

	embedder, err := native.NewEmbedder(model, modelPath, onnx.DefaultSessionOptions())
	if err != nil {
		status.Message = "The embedding model could not be loaded: " + err.Error()
		status.Remediation = "Re-export the model; a truncated or mis-exported graph will not load."
		return nil, false
	}

	provider, err := native.New(native.Config{
		FFmpegPath:    settings.FFmpegPath,
		Embedder:      embedder,
		Category:      "actions",
		FrameInterval: settings.FrameInterval,
		Threshold:     settings.Threshold,
	})
	if err != nil {
		embedder.Close()
		status.Message = err.Error()
		return nil, false
	}

	if models, err := provider.Models(ctx); err == nil {
		status.Models = models
	}

	// A provider with no head produces embeddings but no labels. That is a
	// legitimate and useful state - it is how a library is prepared before a
	// head is trained from it - so it is reported rather than treated as a
	// failure.
	status.Message = fmt.Sprintf(
		"Native pipeline ready with %s. No tagging head is loaded, so analysis produces embeddings only.",
		model.Name)
	status.Remediation = "Train a head from your own markers to start generating tags."
	return provider, true
}

func buildVLM(ctx context.Context, settings Settings, status *Status) (aitag.Provider, bool) {
	if settings.FFmpegPath == "" {
		status.Message = "ffmpeg is not available."
		status.Remediation = "Let Stash download ffmpeg, or set its path in the system settings."
		return nil, false
	}

	if err := llamaprov.ValidateLabels(settings.VLMLabels); err != nil {
		status.Message = err.Error()
		status.Remediation = "Set ai_tagging_vlm_labels to a non-empty list of unique action labels."
		return nil, false
	}

	artifacts, err := resolveVLMArtifacts(settings)
	if err != nil {
		status.Message = err.Error()
		switch {
		case strings.Contains(err.Error(), "unknown VLM model pair"):
			status.Remediation = "Set ai_tagging_vlm_model to a pinned pair listed in the AI settings."
		case strings.Contains(err.Error(), "ai_tagging_vlm_gpu_layers cannot be negative"):
			status.Remediation = "Set ai_tagging_vlm_gpu_layers to 0 for CPU or a positive layer count for explicit GPU offload."
		case strings.Contains(err.Error(), "ai_tagging_vlm_context cannot be negative"):
			status.Remediation = "Set ai_tagging_vlm_context to 0 or a positive token count."
		case strings.Contains(err.Error(), "GPU llama-server is unsupported"):
			status.Remediation = "Set ai_tagging_vlm_gpu_layers to 0, or install on a platform with a pinned GPU build."
		default:
			status.Remediation = "Download the named llama-server and model pair from the AI settings, or install them at the reported paths."
		}
		return nil, false
	}

	supervisor, err := llamahost.New(llamahost.Config{
		RunDir:        filepath.Join(settings.ModelDir, "llama-server-run"),
		Executable:    artifacts.serverPath,
		ModelPath:     artifacts.modelPath,
		ProjectorPath: artifacts.mmprojPath,
		ContextTokens: artifacts.contextTokens,
		GPULayers:     settings.VLMGPULayers,
		Threads:       onnx.PhysicalCores(),
	})
	if err != nil {
		status.Message = err.Error()
		status.Remediation = "Check the installed llama-server and selected model pair."
		return nil, false
	}
	stopHost := func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		supervisor.Stop(stopCtx)
	}
	provider, err := llamaprov.New(llamaprov.Config{
		Client:          supervisor.Client(),
		BaseURL:         "http://llama",
		Pair:            artifacts.pair,
		Labels:          settings.VLMLabels,
		Category:        "actions",
		FFmpegPath:      settings.FFmpegPath,
		DefaultInterval: llamaprov.DefaultFrameInterval,
		MaxMergeSeconds: settings.MaxSpanMergeSeconds,
		Available:       supervisor.Available,
		WaitReady:       supervisor.WaitReady,
		CloseHost:       stopHost,
	})
	if err != nil {
		stopHost()
		status.Message = err.Error()
		status.Remediation = "Set ai_tagging_vlm_labels to a non-empty list of unique action labels."
		return nil, false
	}
	status.live = func() (bool, string, string) {
		hostStatus := supervisor.Status()
		available := supervisor.Available() == nil
		remediation := hostStatus.Remediation
		if !available && remediation == "" {
			remediation = "Wait for llama-server to finish loading, or inspect its crash diagnostics."
		}
		return available, hostStatus.Message, remediation
	}
	supervisor.Start()
	*status = status.Current(provider)
	return provider, true
}

// AttachHead loads a trained head into a native provider.
//
// Separate from Build because the head lives in the AI database, which is not
// open when the provider is first assembled.
func AttachHead(provider aitag.Provider, trainer *Trainer, name string) error {
	if name == "" {
		return nil
	}

	nativeProvider, ok := provider.(*native.Provider)
	if !ok {
		// Only the native pipeline has a head; the HTTP provider's model is
		// the remote server's business.
		return nil
	}

	head, err := trainer.LoadHead(context.Background(), name)
	if err != nil {
		return err
	}

	nativeProvider.SetHead(head)
	return nil
}

// loadRules reads the marker rules, tolerating their absence.
func loadRules(dir string, status *Status) *aitag.Rules {
	if dir == "" {
		return nil
	}
	if _, err := os.Stat(dir); err != nil {
		// No rules means no markers, which is the correct behaviour for an
		// installation that has not configured any: generating markers from
		// invented defaults would be worse.
		return nil
	}

	rules, err := aitag.LoadRulesDir(dir)
	if err != nil {
		logger.Errorf("could not read AI marker rules from %s: %v", dir, err)
		return nil
	}

	status.RulesLoaded = len(rules.Categories)
	return rules
}
