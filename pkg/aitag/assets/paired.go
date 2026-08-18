package assets

import (
	"context"
	"fmt"
)

// Paired artifacts, for models that are not one file.
//
// A llama.cpp vision model is TWO GGUF files: the language model and a separate
// multimodal projector. Neither is usable alone, and the two must come from the
// same export or the projector's output does not match what the model expects -
// a mismatch that produces fluent, confident, wrong descriptions rather than an
// error.
//
// This exists whether or not a llama.cpp binding is wired up, because the gap it
// closes is a downloading gap: yzma's own model fetcher performs no checksum
// verification, so anything built on it needs its downloads wrapped by something
// that does. That is this.

// Pair is a model that needs several artifacts to be usable.
type Pair struct {
	// Name identifies the pair.
	Name string
	// Description explains what it is for.
	Description string
	License     string
	LicenseURL  string

	// Primary is the main artifact - the language model for a VLM.
	Primary Asset
	// Companion is what the primary cannot work without: the multimodal
	// projector for a VLM.
	Companion Asset

	// ContextTokens is the context window the pair needs, which for a vision
	// model has to hold the encoded image as well as the prompt.
	ContextTokens int
}

// Assets returns both parts in catalog order.
func (p Pair) Assets() []Asset { return []Asset{p.Primary, p.Companion} }

// Downloadable reports whether BOTH parts can be fetched as published.
//
// Both, deliberately: a pair with one verifiable half is not usable, and
// reporting it as downloadable would produce a half-installed model that fails
// at load time rather than at download time.
func (p Pair) Downloadable() bool {
	verifiable := func(a Asset) bool { return a.URL != "" && a.SHA256 != "" }
	return verifiable(p.Primary) && verifiable(p.Companion)
}

// FetchPair downloads both halves, returning their paths.
//
// The companion is fetched FIRST. If the download is interrupted the result is
// a projector with no model - which reports as not installed - rather than a
// model whose projector is missing, which is the combination that looks
// installed and then fails.
func (d *Downloader) FetchPair(ctx context.Context, pair Pair, progress Progress) (primary, companion string, err error) {
	if !pair.Downloadable() {
		return "", "", fmt.Errorf(
			"%s cannot be downloaded: both the model and its projector need a published checksum", pair.Name)
	}

	companion, err = d.Fetch(ctx, pair.Companion, progress)
	if err != nil {
		return "", "", fmt.Errorf("download the projector for %s: %w", pair.Name, err)
	}

	primary, err = d.Fetch(ctx, pair.Primary, progress)
	if err != nil {
		return "", "", fmt.Errorf("download %s: %w", pair.Name, err)
	}
	return primary, companion, nil
}

// PairInstalled reports whether both halves are present and verified.
func (d *Downloader) PairInstalled(pair Pair) bool {
	return d.Installed(pair.Primary) && d.Installed(pair.Companion)
}

// VisionPairs lists the vision-language models this build offers.
//
// Checksums come from the publisher's own Git-LFS metadata, not from a local
// download, because these are multi-gigabyte artifacts. Both halves of every
// pair are pinned, which is what FetchPair requires.
//
// A VLM here is a SPARSE tagger and the defaults say so. One frame takes
// seconds, not the tens of milliseconds an embedding takes, so this samples at
// thirty-second intervals rather than the native pipeline's two.
//
// # On model choice, and why the obvious one is missing
//
// Every pair here is Apache-2.0 UPSTREAM, verified against the original weights
// rather than the GGUF repository's own tag - the two disagree, and the
// disagreement is not academic. `ggml-org/Qwen2.5-VL-3B-Instruct-GGUF` is
// tagged apache-2.0 while `Qwen/Qwen2.5-VL-3B-Instruct` it was converted from is
// released under the Qwen RESEARCH licence. That is the same objection that
// already excluded InsightFace buffalo_l from the model catalog: freely
// downloadable is not the same as freely licensed. Curiously the 2B and 7B
// Qwen2.5-VL releases ARE Apache-2.0; only the 3B is restricted, which is
// exactly the sort of trap that a per-repository glance would miss.
//
// Gemma-3 is likewise absent. It is capable and well-quantized, but the Gemma
// Terms carry a prohibited-use policy, which is the same reason hosted Claude
// is not an option for this workload.
//
// # On accuracy, measured rather than assumed
//
// A 500M VLM was measured against a known fixture during design: it described
// the image correctly in free text, and its yes/no label discrimination was
// near chance. Small is not merely worse here, it is unusable, which is why the
// smallest pair offered is 2B. Even so, NONE of these has been measured against
// real content - run the evaluation harness before trusting the output.
func VisionPairs() []Pair {
	return []Pair{
		{
			Name: "internvl3-2b",
			Description: "InternVL3 2B: the small option, ~1.8 GB for both halves. " +
				"Fits a 4 GB card alongside its context. Start here.",
			License:    LicenseApache2,
			LicenseURL: "https://huggingface.co/OpenGVLab/InternVL3-2B-Instruct",
			Primary: Asset{
				Name:        "internvl3-2b-instruct-q4_k_m.gguf",
				URL:         hfURL("ggml-org/InternVL3-2B-Instruct-GGUF", "InternVL3-2B-Instruct-Q4_K_M.gguf"),
				SHA256:      "dc36eddc05ff1db5e11e0aa38efe7a5063b045aa5350c3b1a2510f3ff9107179",
				Size:        1116758816,
				Description: "The language model, 4-bit.",
				License:     LicenseApache2,
				LicenseURL:  "https://huggingface.co/OpenGVLab/InternVL3-2B-Instruct",
			},
			Companion: Asset{
				Name:        "internvl3-2b-mmproj-f16.gguf",
				URL:         hfURL("ggml-org/InternVL3-2B-Instruct-GGUF", "mmproj-InternVL3-2B-Instruct-f16.gguf"),
				SHA256:      "e4e6f0663d591169e8ddd2f415262cef2a5a994705658841e56c09401a54deb3",
				Size:        628237600,
				Description: "The multimodal projector. MUST come from the same export as the model: a mismatched pair produces fluent, confident, wrong answers rather than an error.",
				License:     LicenseApache2,
				LicenseURL:  "https://huggingface.co/OpenGVLab/InternVL3-2B-Instruct",
			},
			ContextTokens: 4096,
		},
		{
			Name: "smolvlm2-2.2b",
			Description: "SmolVLM2 2.2B: a video-trained option, ~1.7 GB for both halves. " +
				"Uses the publisher's Q4_K_M model and Q8 projector to fit a 4 GB card.",
			License:    LicenseApache2,
			LicenseURL: "https://huggingface.co/HuggingFaceTB/SmolVLM2-2.2B-Instruct",
			Primary: Asset{
				Name:        "smolvlm2-2.2b-instruct-q4_k_m.gguf",
				URL:         hfURL("ggml-org/SmolVLM2-2.2B-Instruct-GGUF", "SmolVLM2-2.2B-Instruct-Q4_K_M.gguf"),
				SHA256:      "0cf76814555b8665149075b74ab6b5c1d428ea1d3d01c1918c12012e8d7c9f58",
				Size:        1112602656,
				Description: "The language model, 4-bit.",
				License:     LicenseApache2,
				LicenseURL:  "https://huggingface.co/HuggingFaceTB/SmolVLM2-2.2B-Instruct",
			},
			Companion: Asset{
				Name:        "smolvlm2-2.2b-mmproj-q8_0.gguf",
				URL:         hfURL("ggml-org/SmolVLM2-2.2B-Instruct-GGUF", "mmproj-SmolVLM2-2.2B-Instruct-Q8_0.gguf"),
				SHA256:      "ae07ea1facd07dd3230c4483b63e8cda96c6944ad2481f33d531f79e892dd024",
				Size:        592523200,
				Description: "The Q8 multimodal projector from the same publisher export.",
				License:     LicenseApache2,
				LicenseURL:  "https://huggingface.co/HuggingFaceTB/SmolVLM2-2.2B-Instruct",
			},
			ContextTokens: 8192,
		},
		{
			Name: "qwen2.5-vl-7b",
			Description: "Qwen2.5-VL 7B: the quality option, ~6 GB for both halves. " +
				"CPU or a card with room to spare; too large for 4 GB of VRAM.",
			License:    LicenseApache2,
			LicenseURL: "https://huggingface.co/Qwen/Qwen2.5-VL-7B-Instruct",
			Primary: Asset{
				Name:        "qwen2.5-vl-7b-instruct-q4_k_m.gguf",
				URL:         hfURL("ggml-org/Qwen2.5-VL-7B-Instruct-GGUF", "Qwen2.5-VL-7B-Instruct-Q4_K_M.gguf"),
				SHA256:      "9258bf05b12686d097ff3b6b18d968ab393649780aa2b3cd67fec43d50554392",
				Size:        4683072032,
				Description: "The language model, 4-bit.",
				License:     LicenseApache2,
				LicenseURL:  "https://huggingface.co/Qwen/Qwen2.5-VL-7B-Instruct",
			},
			Companion: Asset{
				Name:        "qwen2.5-vl-7b-mmproj-f16.gguf",
				URL:         hfURL("ggml-org/Qwen2.5-VL-7B-Instruct-GGUF", "mmproj-Qwen2.5-VL-7B-Instruct-f16.gguf"),
				SHA256:      "c24a7f5fcfc68286f0a217023b6738e73bea4f11787a43e8238d4bb1b8604cde",
				Size:        1354162912,
				Description: "The multimodal projector. MUST come from the same export as the model.",
				License:     LicenseApache2,
				LicenseURL:  "https://huggingface.co/Qwen/Qwen2.5-VL-7B-Instruct",
			},
			ContextTokens: 8192,
		},
	}
}

// DefaultVisionPair is the pair used when configuration names none.
//
// The smaller one: it is the only pinned pair that fits a 4 GB card with room
// for its context, and a model that runs is worth more than one that swaps.
const DefaultVisionPair = "internvl3-2b"

// FindPair returns a vision pair by name.
func FindPair(name string) (Pair, bool) {
	for _, p := range VisionPairs() {
		if p.Name == name {
			return p, true
		}
	}
	return Pair{}, false
}
