package assets

import (
	"fmt"
	"runtime"
)

// The model catalog.
//
// Nothing here ships in the binary. Every entry is downloaded on explicit
// opt-in, its licence is stated so the user sees what they are accepting before
// it is fetched, and every downloadable entry carries a SHA256 read from the
// publisher's own record - the downloader refuses anything without one.
//
// Two things are deliberately ABSENT:
//
//   - InsightFace buffalo_l. Its code is MIT but its weights are licensed for
//     non-commercial research only, which is not a free licence. YuNet (MIT)
//     and SFace (Apache-2.0) fill the same role.
//   - Any hosted chat VLM. Anthropic's usage policy prohibits this content and
//     its own documentation states Claude flags explicit material regardless of
//     prompt; no other hosted vendor's policy could be confirmed as permitting
//     it. The one policy-sanctioned remote option is OpenAI's moderation
//     endpoint, which lives in pkg/aitag/moderation and is a coarse gate rather
//     than a tagger.
//
// The load-bearing limitation to keep in view: NONE of these is a fine-grained
// multi-label scene tagger. The binary and three-class models are gates. The
// only route to a rich taxonomy is SigLIP embeddings plus a head trained on the
// user's own markers - which is what native.Train exists for, and which is why
// the embedding contract below (768 dimensions at 224x224) is load-bearing.

// Roles a model plays in the pipeline.
const (
	RoleEmbedding  = "embedding"
	RoleNSFWGate   = "nsfw_gate"
	RoleDetection  = "detection"
	RoleFaceDetect = "face_detect"
	RoleFaceEmbed  = "face_embed"
	RoleAudio      = "audio"
	RoleRuntime    = "runtime"
)

// ChannelOrder is the colour order a model's input expects.
//
// Explicit because it is invisible when wrong: feeding RGB to a BGR model
// produces detections in plausible places with slightly wrong boxes and scores,
// which nothing downstream would catch. Verified per model against a reference
// implementation rather than assumed.
type ChannelOrder string

const (
	// ChannelRGB is the usual convention, and what ffmpeg produces.
	ChannelRGB ChannelOrder = "rgb"
	// ChannelBGR is OpenCV's convention. YuNet was trained through OpenCV and
	// genuinely needs it.
	ChannelBGR ChannelOrder = "bgr"
)

// PixelRange is the numeric range a model's input expects.
type PixelRange string

const (
	// PixelUnit scales to [0,1] before applying mean and standard deviation.
	PixelUnit PixelRange = "unit"
	// PixelRaw passes 0-255 through untouched, for a graph with the
	// normalisation baked in.
	PixelRaw PixelRange = "raw"
)

// Model is a catalog entry: an asset plus what the pipeline needs to use it.
type Model struct {
	Asset

	// Role is what the model does.
	Role string

	// Inputs and Outputs are the graph's tensor names, verified against the
	// real artifact. A model with several outputs has no canonical order, and
	// choosing wrongly produces plausible nonsense rather than an error.
	Inputs  []string
	Outputs []string

	// InputSize is the square side length the model expects.
	InputSize int
	// Dim is the output width for an embedding model.
	Dim int

	// Channels and Pixels describe the input convention.
	Channels ChannelOrder
	Pixels   PixelRange

	// Mean and Std normalise each channel, applied after scaling to [0,1].
	// Ignored when Pixels is PixelRaw.
	Mean [3]float32
	Std  [3]float32

	// Resample names the interpolation the model was trained with. SigLIP's
	// published processor specifies bicubic; using bilinear is a small,
	// invisible accuracy loss rather than an error.
	Resample string

	// Labels are a classifier's output classes, in output order.
	Labels []string

	// Quantized marks a reduced-precision build. Phase 0 measured int8 at 1.37x
	// fp32 on this CPU, not the 2-4x usually claimed, so it is a size trade
	// rather than a speed one.
	Quantized bool

	// Variants are alternative precisions of the same model, so the UI can
	// offer the size trade rather than one being hard-coded.
	Variants []Model
}

const hfBase = "https://huggingface.co"

// hfURL builds a Hugging Face download URL.
//
// Pinned to a branch rather than a revision, which is safe here only because
// the SHA256 is the real guarantee: a repository that changes its file fails
// verification instead of being silently substituted.
func hfURL(repo, path string) string {
	return fmt.Sprintf("%s/%s/resolve/main/%s", hfBase, repo, path)
}

// DefaultEmbedder names the embedding model used when configuration does not
// pick one.
//
// Named rather than left as "whichever entry comes first", because the embedding
// contract is what everything downstream is built on: a trained head is fitted
// to a specific model's 768-dimensional output and is meaningless against
// another's. Reordering the catalog must not silently change which model that
// is, so the default is a name and a test asserts it resolves.
const DefaultEmbedder = "siglip2-base-patch16-224-vision.onnx"

// Catalog lists every model this build knows how to use.
func Catalog() []Model {
	return []Model{siglipVision(), clipVision(), safetyClassifier(), yunet(), marqoGate(), sface(), yamnet()}
}

// siglipVision is the embedding backbone.
//
// SigLIP**2**, from onnx-community. An earlier draft of this file used SigLIP v1
// on the stated grounds that v2 had no published ONNX export - that was simply
// WRONG, and the export below was verified against the publisher's own record.
// v2 is the better model, so there is no reason to prefer v1.
//
// The vision tower alone, which is all that is needed: the text tower is only
// required for zero-shot prompting, and the conclusion carried through this
// whole workstream is that zero-shot will not separate fine-grained actions.
//
// Provenance is a community conversion rather than an author export, which is
// weaker than the rest of this catalog and accepted deliberately: the trained
// head is fitted on THIS embedder's outputs, so a conversion that drifts from
// the PyTorch original stays self-consistent with the head trained on it. The
// Phase 0 rule about not trusting third-party conversions was about accuracy
// claims against the proprietary reference, which this makes none of.
func siglipVision() Model {
	base := Model{
		Asset: Asset{
			Name:        "siglip2-base-patch16-224-vision.onnx",
			URL:         hfURL("onnx-community/siglip2-base-patch16-224-ONNX", "onnx/vision_model.onnx"),
			SHA256:      "c0573e3f4140c3a7c4e9cc5912bd6b26a033b46a6a8e8af26cbea262b163bcad",
			Size:        371807752,
			License:     LicenseApache2,
			LicenseURL:  "https://huggingface.co/google/siglip2-base-patch16-224",
			Description: "SigLIP2 vision tower: 768-dimensional image embeddings. The backbone of native tagging, and the contract a trained head must match.",
		},
		Role:      RoleEmbedding,
		Inputs:    []string{"pixel_values"},
		Outputs:   []string{"pooler_output"},
		InputSize: 224,
		Dim:       768,
		Channels:  ChannelRGB,
		Pixels:    PixelUnit,
		// [-1,1] rather than ImageNet statistics, read from the published
		// preprocessor_config.json. Getting it wrong does not error, it quietly
		// degrades every embedding.
		Mean:     [3]float32{0.5, 0.5, 0.5},
		Std:      [3]float32{0.5, 0.5, 0.5},
		Resample: "bicubic",
	}

	fp16 := base
	fp16.Name = "siglip2-base-patch16-224-vision-fp16.onnx"
	fp16.URL = hfURL("onnx-community/siglip2-base-patch16-224-ONNX", "onnx/vision_model_fp16.onnx")
	fp16.SHA256 = "a1959f7bd3993a607e48839f6d01e25b876fe76afda301b028b78eef68aabd95"
	fp16.Size = 186039516
	fp16.Quantized = true
	fp16.Description = "SigLIP2 vision tower, fp16: half the size, the same 768-dimensional output."

	int8 := base
	int8.Name = "siglip2-base-patch16-224-vision-int8.onnx"
	int8.URL = hfURL("onnx-community/siglip2-base-patch16-224-ONNX", "onnx/vision_model_int8.onnx")
	int8.SHA256 = "0dd31785a2713f1113ef2272472165c69d580473dae38d7b47568ac587795e70"
	int8.Size = 94553333
	int8.Quantized = true
	int8.Description = "SigLIP2 vision tower, int8: a quarter of the size. Measured at 1.37x fp32 on a 6-core AVX2 CPU, so this is a size trade rather than a speed one."

	base.Variants = []Model{fp16, int8}
	return base
}

// clipVision is the cheaper embedding preset.
//
// Half the output width of SigLIP2 and a correspondingly weaker signal, offered
// because a head fitted on 512 dimensions still works and the model is smaller.
// A head trained on one embedder CANNOT be used with the other: the trainer
// records which model produced its vectors precisely so that mismatch is caught.
func clipVision() Model {
	return Model{
		Asset: Asset{
			Name:        "clip-vit-base-patch32-vision.onnx",
			URL:         hfURL("Xenova/clip-vit-base-patch32", "onnx/vision_model.onnx"),
			SHA256:      "fd6e1402a588279d1723c7534d4bcba5bc0b14b47dfab0e46f8c47b8270d7d40",
			Size:        351685709,
			License:     LicenseMIT,
			LicenseURL:  "https://huggingface.co/openai/clip-vit-base-patch32",
			Description: "CLIP ViT-B/32 vision tower: 512-dimensional embeddings, the lower-cost preset.",
		},
		Role:      RoleEmbedding,
		Inputs:    []string{"pixel_values"},
		Outputs:   []string{"image_embeds"},
		InputSize: 224,
		Dim:       512,
		Channels:  ChannelRGB,
		Pixels:    PixelUnit,
		// CLIP's own statistics, not SigLIP's [-1,1].
		Mean:     [3]float32{0.48145466, 0.4578275, 0.40821073},
		Std:      [3]float32{0.26862954, 0.26130258, 0.27577711},
		Resample: "bicubic",
	}
}

// safetyClassifier is the ready-to-use content gate.
//
// Chosen over the more accurate alternatives because it is the only one that
// ships a usable ONNX export: preprocessing and softmax are inside the graph,
// so it takes raw 0-255 pixels and returns probabilities directly.
func safetyClassifier() Model {
	return Model{
		Asset: Asset{
			Name:   "image-safety-classifier-xs.onnx",
			URL:    hfURL("OwenElliott/image-safety-classifier-xs", "onnx/image-safety-classifier-xs.onnx"),
			SHA256: "8c28c49d9075f3ad15ebdc2961f02d5b3f99be944815b848b49c9f0e6f3fb689",
			Size:   13137569,
			// The model card advertises an fp16 variant that does not exist in
			// the repository. Listing it would produce a download that 404s.
			License:     LicenseMIT,
			LicenseURL:  "https://huggingface.co/OwenElliott/image-safety-classifier-xs",
			Description: "Three-class content gate (NSFL / NSFW / SFW), ~3M parameters, preprocessing and softmax inside the graph. A gate, not a tagger.",
		},
		Role:      RoleNSFWGate,
		Inputs:    []string{"image"},
		Outputs:   []string{"probabilities"},
		InputSize: 224,
		Channels:  ChannelRGB,
		// Verified empirically: 0-255 input gives a markedly more confident
		// answer than 0-1, because the normalisation is inside the graph.
		Pixels:   PixelRaw,
		Resample: "bicubic",
		Labels:   []string{"NSFL", "NSFW", "SFW"},
	}
}

// yunet is face detection.
//
// This contract was WRONG in the first draft of this catalog and is now read
// from the artifact: the input is 640x640, not 320, and the graph emits TWELVE
// outputs - classification, objectness, box and landmarks at each of three
// strides - not three. Decoding a single stride finds only the faces that
// happen to fall in its size band.
func yunet() Model {
	return Model{
		Asset: Asset{
			Name:   "face_detection_yunet_2023mar.onnx",
			URL:    hfURL("opencv/face_detection_yunet", "face_detection_yunet_2023mar.onnx"),
			SHA256: "8f2383e4dd3cfbb4553ea8718107fc0423210dc964f9f4280604804ed2552fa4",
			Size:   232589,
			// MIT via opencv_zoo's per-directory licence. Worth naming, because
			// the repository's top level is Apache-2.0 and the per-directory
			// file is the one that governs.
			License:     LicenseMIT,
			LicenseURL:  "https://github.com/opencv/opencv_zoo/tree/main/models/face_detection_yunet",
			Description: "Face detection with five landmarks. 233 KB, fast enough to run alongside the frame embedder.",
		},
		Role:   RoleFaceDetect,
		Inputs: []string{"input"},
		Outputs: []string{
			"cls_8", "cls_16", "cls_32",
			"obj_8", "obj_16", "obj_32",
			"bbox_8", "bbox_16", "bbox_32",
			"kps_8", "kps_16", "kps_32",
		},
		InputSize: 640,
		// BGR, established by differential test against OpenCV: feeding RGB
		// moves the box by twelve pixels and the score by 0.008 - close enough
		// to look right and far enough to be wrong.
		Channels: ChannelBGR,
		Pixels:   PixelRaw,
		Resample: "bilinear",
	}
}

// marqoGate is the better-accuracy NSFW gate, which needs a local export.
//
// Deliberately has no URL: the repository ships safetensors only, so there is
// nothing to download and any checksum would be of an artifact the user
// produces. Fetch refuses an entry without one, so this describes what to build
// rather than something that can be silently downloaded.
func marqoGate() Model {
	return Model{
		Asset: Asset{
			Name:       "marqo-nsfw-image-detection-384.onnx",
			License:    LicenseApache2,
			LicenseURL: "https://huggingface.co/Marqo/nsfw-image-detection-384",
			Description: "Binary NSFW gate, 5.6M parameters at 384px, self-reported 98.56% on a proprietary set. " +
				"Ships safetensors only: export it with optimum and record your own checksum.",
		},
		Role:      RoleNSFWGate,
		Inputs:    []string{"pixel_values"},
		Outputs:   []string{"logits"},
		InputSize: 384,
		Channels:  ChannelRGB,
		Pixels:    PixelUnit,
		Mean:      [3]float32{0.5, 0.5, 0.5},
		Std:       [3]float32{0.5, 0.5, 0.5},
		Resample:  "bicubic",
		Labels:    []string{"NSFW", "SFW"},
	}
}

// sface embeds a detected face into a comparable vector.
//
// An earlier draft left this description-only, claiming the artifact was not
// checksum-verified. It is: the official OpenCV org publishes it with a
// Git-LFS digest, confirmed against their record.
//
// The graph is an old MXNet export in which every weight also appears as a
// graph input. Only "data" is fed; the runtime fills the rest from the model's
// own initialisers, which is why naming a single input is correct rather than
// an oversight.
func sface() Model {
	base := Model{
		Asset: Asset{
			Name:        "face_recognition_sface_2021dec.onnx",
			URL:         hfURL("opencv/face_recognition_sface", "face_recognition_sface_2021dec.onnx"),
			SHA256:      "0ba9fbfa01b5270c96627c4ef784da859931e02f04419c829e83484087c34e79",
			Size:        38696353,
			License:     LicenseApache2,
			LicenseURL:  "https://github.com/opencv/opencv_zoo/tree/main/models/face_recognition_sface",
			Description: "Face embeddings: 128-dimensional vectors for grouping and matching faces.",
		},
		Role:      RoleFaceEmbed,
		Inputs:    []string{"data"},
		Outputs:   []string{"fc1"},
		InputSize: 112,
		Dim:       128,
		// BGR at 0-255, like every OpenCV-trained model here.
		Channels: ChannelBGR,
		Pixels:   PixelRaw,
		Resample: "bilinear",
	}

	int8 := base
	int8.Name = "face_recognition_sface_2021dec_int8.onnx"
	int8.URL = hfURL("opencv/face_recognition_sface", "face_recognition_sface_2021dec_int8.onnx")
	int8.SHA256 = "2b0e941e6f16cc048c20aee0c8e31f569118f65d702914540f7bfdc14048d78a"
	int8.Size = 9896933
	int8.Quantized = true
	int8.Description = "Face embeddings, int8: a quarter of the size."

	base.Variants = []Model{int8}
	return base
}

// yamnet tags the audio track.
func yamnet() Model {
	return Model{
		Asset: Asset{
			Name:        "yamnet.onnx",
			License:     LicenseApache2,
			LicenseURL:  "https://tfhub.dev/google/yamnet/1",
			Description: "Audio tagging over 521 classes. Export from TensorFlow Hub with tf2onnx and record your own checksum.",
		},
		Role:    RoleAudio,
		Inputs:  []string{"waveform"},
		Outputs: []string{"scores", "embeddings"},
		Dim:     1024,
	}
}

// FindModel returns a catalog entry by name, variants included.
func FindModel(name string) (Model, bool) {
	for _, m := range Catalog() {
		if m.Name == name {
			return m, true
		}
		for _, v := range m.Variants {
			if v.Name == name {
				return v, true
			}
		}
	}
	return Model{}, false
}

// ModelsForRole lists the catalog entries that play a role, variants included.
func ModelsForRole(role string) []Model {
	var out []Model
	for _, m := range Catalog() {
		if m.Role != role {
			continue
		}
		out = append(out, m)
		out = append(out, m.Variants...)
	}
	return out
}

// Downloadable reports whether an entry can be fetched as published.
//
// An entry with no URL or no checksum describes something the user must export
// themselves, and the UI should say so rather than offering a download that
// cannot happen.
func (m Model) Downloadable() bool {
	return m.URL != "" && m.SHA256 != ""
}

// RuntimeAsset describes the ONNX runtime shared library for this platform.
//
// The version is set by the BINDING, not by preference. onnxruntime_go compiles
// against ORT_API_VERSION 26 and passes that number to OrtGetApiBase; a library
// that only implements an older API returns NULL rather than degrading, and
// initialisation fails with:
//
//	The requested API version [26] is not available, only API versions
//	[1, 22] are supported in this build.
//
// So the runtime must be at least as new as the binding's header. 1.28.0 is the
// release this was verified against - see TestRuntimeIsActuallyLoadable, which
// downloads it and runs a real graph through it. Lowering the pin without also
// lowering onnxruntime_go produces a build where native inference can never
// initialise, which was this file's state before the loadability test existed.
//
// CUDA on Pascal (compute capability 6.1) is therefore unreachable: it needs
// 1.22.x, which cannot satisfy API 26. That costs nothing here because the CPU
// provider is the shipped configuration and the only one wired up - but it does
// mean "pin the runtime low to keep the P2000 working" is not an option while
// the binding stays at v1.31.0. See LastPascalRuntimeVersion.
func RuntimeAsset(version string) (Asset, error) {
	if version == "" {
		version = DefaultRuntimeVersion
	}

	var platform, archive, library string
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64":
		platform, archive, library = "linux-x64", "tgz", "lib/libonnxruntime.so."+version
	case "linux/arm64":
		platform, archive, library = "linux-aarch64", "tgz", "lib/libonnxruntime.so."+version
	case "darwin/arm64":
		// Apple Silicon only. The universal2 build that served both Mac
		// architectures was discontinued after 1.22.x; from 1.23 the release
		// publishes osx-arm64 alone, so an Intel Mac has no published archive
		// and falls through to the error below rather than to a URL that 404s.
		// Note the library is named with the version BEFORE the extension.
		platform, archive, library = "osx-arm64", "tgz", "lib/libonnxruntime."+version+".dylib"
	case "windows/amd64":
		platform, archive, library = "win-x64", "zip", "lib/onnxruntime.dll"
	default:
		return Asset{}, fmt.Errorf(
			"no ONNX runtime build is published for %s/%s; use the AI server provider instead",
			runtime.GOOS, runtime.GOARCH)
	}

	asset := Asset{
		Name: "onnxruntime",
		URL: fmt.Sprintf("%s/v%s/onnxruntime-%s-%s.%s",
			ortReleases, version, platform, version, archive),
		Archive:     archive,
		ExtractPath: fmt.Sprintf("onnxruntime-%s-%s/%s", platform, version, library),
		License:     LicenseMIT,
		LicenseURL:  "https://github.com/microsoft/onnxruntime/blob/main/LICENSE",
		Description: "ONNX Runtime, loaded at runtime for native inference.",
	}
	pinRuntime(&asset, version, platform)
	return asset, nil
}

const ortReleases = "https://github.com/microsoft/onnxruntime/releases/download"

// DefaultRuntimeVersion is the ONNX Runtime release this build targets.
//
// See RuntimeAsset for why this exact version and not a newer one.
const DefaultRuntimeVersion = "1.28.0"

// runtimeChecksums pins the DefaultRuntimeVersion artifacts.
//
// Measured HERE, by downloading each archive and hashing it, because Microsoft
// publishes no digests alongside its releases. That is weaker than a publisher's
// own record - it pins what was served on the day it was measured rather than
// what the vendor attests - but it is strictly better than shipping a native
// library with no verification at all, which is what an unpinned entry would do.
//
// This is also what makes the runtime downloadable: Fetch REFUSES an asset with
// no checksum, so an unpinned RuntimeAsset is not merely unverified, it cannot
// be installed.
var runtimeChecksums = map[string]struct {
	sha256 string
	size   int64
}{
	"linux-x64":     {"a3e1b79d7bb1bf09696ce675f49e4064e6c81f6202b8225624fff0e93f8d6407", 9125960},
	"linux-aarch64": {"e15ff8b5d85afe6c144d97c6fd432254bf76a219daaf17658087d6ecb3e8f0bb", 8116278},
	"osx-arm64":     {"1268b359718099bde2cedb55787f182a130067bc4f31e8c88478c445b850d3d8", 32396562},
	"win-x64":       {"abef733dacbe2f571547a7150b479b5cb9cc0df22f96c24983a42cadb1b4f8bc", 78796801},
}

// pinRuntime fills in the checksum for a runtime artifact, where one is known.
//
// Only DefaultRuntimeVersion is pinned. A caller asking for another version gets
// an asset with no checksum, which Fetch refuses - so the failure is a clear
// message rather than an unverified native library loaded into the process.
func pinRuntime(asset *Asset, version, platform string) {
	if version != DefaultRuntimeVersion {
		return
	}
	if pin, ok := runtimeChecksums[platform]; ok {
		asset.SHA256 = pin.sha256
		asset.Size = pin.size
	}
}

// ------------------------------------------------------------ llama.cpp --

// ServerAsset describes the llama.cpp `llama-server` build for this platform.
//
// A vision-language model runs as a supervised CHILD PROCESS rather than in
// this one, which is why this fetches a server binary rather than a library.
// The reasoning is recorded here because the alternative keeps looking cheaper
// than it is:
//
//   - A crash in llama.cpp inside this process kills Stash. Everything else in
//     this tree degrades instead - a missing model, an unreachable server and a
//     dead Python host are all reported states. An in-process VLM would be the
//     one component that can take the whole server down with it.
//   - libmtmd's C API is documented as experimental with breaking changes
//     expected. The HTTP API does not move, so tracking llama.cpp becomes a
//     checksum bump rather than a rewrite.
//   - The in-process route saves no download: libmtmd and libllama ship in this
//     same archive.
//
// The archive carries the server AND its shared libraries, so the caller runs
// the returned binary with the containing directory on the library path.
//
// NOTE this is exactly the archive whose SONAME symlink chains the extractor
// used to discard - see writeLink. Without that fix every entry here downloads,
// verifies, and then fails at exec.
func ServerAsset(version string) (Asset, error) {
	if version == "" {
		version = DefaultServerVersion
	}

	var platform, archive, binary string
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64":
		platform, archive, binary = "ubuntu-x64", "tgz", "llama-server"
	case "linux/arm64":
		platform, archive, binary = "ubuntu-arm64", "tgz", "llama-server"
	case "darwin/arm64":
		platform, archive, binary = "macos-arm64", "tgz", "llama-server"
	case "darwin/amd64":
		// Unlike ONNX Runtime, which dropped Intel Mac builds after 1.22.x,
		// llama.cpp still publishes one.
		platform, archive, binary = "macos-x64", "tgz", "llama-server"
	case "windows/amd64":
		platform, archive, binary = "win-cpu-x64", "zip", "llama-server.exe"
	default:
		return Asset{}, fmt.Errorf(
			"no llama.cpp build is published for %s/%s; use a different tagging provider",
			runtime.GOOS, runtime.GOARCH)
	}
	return serverAssetFor(version, platform, archive, binary)
}

// VulkanServerAsset is the GPU build, offered separately because choosing it is
// a decision rather than a default.
//
// Vulkan and not CUDA: llama.cpp publishes NO Linux CUDA archive at all, so on
// Linux this is the only prebuilt GPU option. It is also the one that serves
// Pascal, which the ONNX path cannot reach at all (see LastPascalRuntimeVersion)
// - so if a GPU is going to be used for anything here, this is it.
//
// Windows CUDA archives do exist but are ~247 MB and need a matching CUDA
// runtime; they are deliberately not pinned until someone needs one.
func VulkanServerAsset(version string) (Asset, error) {
	if version == "" {
		version = DefaultServerVersion
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return Asset{}, fmt.Errorf(
			"no Vulkan llama.cpp build is pinned for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	return serverAssetFor(version, "ubuntu-vulkan-x64", "tgz", "llama-server")
}

func serverAssetFor(version, platform, archive, binary string) (Asset, error) {
	// The Windows zip has no top-level directory; the Unix tarballs unpack into
	// llama-<version>/. Getting this wrong produces "archive does not contain"
	// at the very end of a download.
	inside := binary
	if archive != "zip" {
		inside = fmt.Sprintf("llama-%s/%s", version, binary)
	}

	asset := Asset{
		Name: "llama-server-" + platform,
		URL: fmt.Sprintf("%s/%s/llama-%s-bin-%s.%s",
			llamaReleases, version, version, platform, archiveExt(archive)),
		Archive:     archive,
		ExtractPath: inside,
		License:     LicenseMIT,
		LicenseURL:  "https://github.com/ggml-org/llama.cpp/blob/master/LICENSE",
		Description: "llama.cpp server, run as a child process for vision-language tagging.",
	}
	pinServer(&asset, version, platform)
	return asset, nil
}

func archiveExt(archive string) string {
	if archive == "zip" {
		return "zip"
	}
	return "tar.gz"
}

const llamaReleases = "https://github.com/ggml-org/llama.cpp/releases/download"

// DefaultServerVersion is the llama.cpp release this build targets.
//
// Unlike the ONNX runtime pin, nothing in Go links against this, so the
// constraint is not an ABI - it is that the multimodal server API and the
// mmproj format must match what the pinned model pairs were exported for.
// Moving it means re-measuring the checksums and re-running the live test.
const DefaultServerVersion = "b10107"

// serverChecksums pins the DefaultServerVersion archives.
//
// The GitHub release API publishes digest and size values for these archives;
// the pins below match that publisher-provided metadata.
var serverChecksums = map[string]struct {
	sha256 string
	size   int64
}{
	"ubuntu-x64":        {"afe1ae0b706c4a0830b218a9249037b7a6cc723f81deb78825662128b25453e6", 16275561},
	"ubuntu-vulkan-x64": {"28f86dfce8c3723d4e9fd971b8456d946e09324708880533091399d284fe9add", 32239108},
	"ubuntu-arm64":      {"1f93c35122865287824ef0dc040e24190b18edc6e163152be9ac10b8aaeafeef", 13173138},
	"macos-arm64":       {"b9554ab4c9f6e91199f48387cb4ab27466fb1d724881f81463ef03f6370cfa32", 10804162},
	"macos-x64":         {"6f35c90a6e9f33c905d09694946b82a29b4ab530a358226d95d832262f526ea2", 11075592},
	"win-cpu-x64":       {"52133a0a5a8f6035b1bdd2f89c3425ea8b742413d9bdb9a2dee30e3a1681b18c", 18213827},
}

// pinServer fills in the checksum for a pinned version, leaving any other
// version unverifiable so Fetch refuses it rather than running an unchecked
// binary. This one spawns a process, so the rule matters more here than
// anywhere else in the catalog.
func pinServer(asset *Asset, version, platform string) {
	if version != DefaultServerVersion {
		return
	}
	if pin, ok := serverChecksums[platform]; ok {
		asset.SHA256 = pin.sha256
		asset.Size = pin.size
	}
}

// LastPascalRuntimeVersion is the newest release whose CUDA provider still
// serves compute capability 6.1.
//
// Recorded but NOT used, and the gap is the point: it is older than the oldest
// runtime this binding can load (see RuntimeAsset), so supporting Pascal CUDA
// would mean downgrading onnxruntime_go as well, not just moving this pin. It is
// kept so that decision is visible if anyone goes looking for a GPU path, rather
// than discovered by pinning 1.22.x and finding nothing initialises.
//
// It is also worth noting the pin is moot on this machine for a second,
// independent reason: GP106 runs fp16 at 1/64 fp32, so the P2000 would want the
// fp32 graph anyway.
const LastPascalRuntimeVersion = "1.22.1"

// OpenVINO is deliberately not offered as an execution provider.
//
// It is not present in a stock libonnxruntime, so the dlopen wrapper cannot
// simply enable it - it would need Intel's separately-built onnxruntime-openvino
// shipped alongside. The evidence that it would help is also mixed: one
// published benchmark had the OpenVINO provider on CPU running 49% SLOWER than
// the default CPU provider, and the integrated-GPU gains come from a dated
// source. An AVX2 CPU running int8 SigLIP is the honest baseline; revisit this
// only if that proves inadequate.
const openVINONotSupported = "the OpenVINO execution provider needs a different ONNX Runtime build"
