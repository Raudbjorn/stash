package recommend

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/stashapp/stash/internal/aiserver/store"
	"github.com/stashapp/stash/pkg/aitag/taxonomy"
)

// VoyageProviderName identifies direct Voyage multimodal retrieval runs.
const VoyageProviderName = "voyage_multimodal"

const (
	voyageSegmentService         = VoyageProviderName
	defaultVoyageSegmentModel    = "voyage-multimodal-3.5"
	defaultVoyageSegmentEndpoint = "https://api.voyageai.com/v1/multimodalembeddings"
	defaultVoyageSegmentSecs     = 30.0
	defaultVoyageDimension       = 1024
	maxVoyageMediaBytes          = 20 << 20
)

var sceneTimeRE = regexp.MustCompile(`pts_time:([0-9]+(?:\.[0-9]+)?)`)

// VoyageSegmentIndex builds read-only recommendation vectors for video
// segments. It never writes tags or markers.
type VoyageSegmentIndex struct {
	APIKey, Model string
	Endpoint      string
	SegmentSecs   float64
	Dimension     int
	MaxSceneTags  int
	DB            *store.DB
	FFmpegPath    string
	Client        *http.Client
	Taxonomy      *taxonomy.Client
	Categories    []string
	ExtractVideo  func(context.Context, string, float64, float64) ([]byte, error)
}

type SegmentCache struct {
	SceneID int
	Start   float64
	End     float64
	Vector  []float32
}

// Build returns cached or newly embedded segments for one scene.
func (v *VoyageSegmentIndex) Build(ctx context.Context, sceneID int, videoPath, transcript string, duration float64) ([]SegmentCache, error) {
	if v == nil || strings.TrimSpace(v.APIKey) == "" {
		return nil, errors.New("voyage segment index disabled: no api key")
	}
	if v.DB == nil {
		return nil, errors.New("voyage segment index database is required")
	}
	if duration <= 0 {
		return nil, errors.New("voyage segment duration must be positive")
	}
	model := v.cacheModel()
	stored, err := v.DB.GetEmbeddingSegments(ctx, voyageSegmentService, sceneID, model)
	if err != nil {
		return nil, err
	}
	sourceFingerprint, err := v.sourceFingerprint(videoPath, transcript, duration)
	if err != nil {
		return nil, err
	}
	if ret, ok := cachedVoyageSegments(sceneID, stored, sourceFingerprint, duration); ok {
		return ret, nil
	}

	segments := v.segments(ctx, videoPath, duration)
	cached := make(map[float64]store.StoredEmbeddings, len(stored))
	for _, item := range stored {
		cached[item.SegmentStart] = item
	}

	ret := make([]SegmentCache, 0, len(segments))
	for _, segment := range segments {
		inputHash := voyageSegmentInputHash(sourceFingerprint, segment.Start, segment.End)
		if item, ok := cached[segment.Start]; ok &&
			item.SegmentEnd == segment.End &&
			item.InputHash == inputHash &&
			len(item.Vectors) > 0 {
			ret = append(ret, SegmentCache{SceneID: sceneID, Start: segment.Start, End: segment.End, Vector: append([]float32(nil), item.Vectors...)})
			continue
		}
		video, err := v.segmentVideo(ctx, videoPath, segment.Start, segment.End)
		if err != nil {
			return nil, err
		}
		vector, err := v.embed(ctx, transcript, video)
		if err != nil {
			return nil, fmt.Errorf("embed scene %d segment %.3f-%.3f: %w", sceneID, segment.Start, segment.End, err)
		}
		if err := v.DB.StoreEmbeddings(ctx, voyageSegmentService, store.StoredEmbeddings{
			SceneID: sceneID, Model: model, Dim: len(vector), FrameInterval: segment.End - segment.Start,
			Times: []float64{segment.Start}, Vectors: vector, SegmentStart: segment.Start, SegmentEnd: segment.End,
			InputHash: inputHash,
		}); err != nil {
			return nil, err
		}
		item := SegmentCache{SceneID: sceneID, Start: segment.Start, End: segment.End, Vector: vector}
		ret = append(ret, item)
	}
	return ret, nil
}

// sourceFingerprint is deliberately computable from filesystem metadata. A
// cache hit must not decode the source merely to prove that it is unchanged.
// The version prefix invalidates content-hash entries written by older builds.
func (v *VoyageSegmentIndex) sourceFingerprint(videoPath, transcript string, duration float64) (string, error) {
	absolutePath, err := filepath.Abs(videoPath)
	if err != nil {
		return "", fmt.Errorf("resolve Voyage video source %q: %w", videoPath, err)
	}
	info, err := os.Stat(absolutePath)
	if err != nil {
		return "", fmt.Errorf("stat Voyage video source %q: %w", videoPath, err)
	}
	segmentSecs := v.SegmentSecs
	if segmentSecs <= 0 {
		segmentSecs = defaultVoyageSegmentSecs
	}

	hash := sha256.New()
	_, _ = hash.Write([]byte("voyage-source-v2"))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(absolutePath))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(strconv.FormatInt(info.Size(), 10)))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(strconv.FormatInt(info.ModTime().UnixNano(), 10)))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(strconv.FormatFloat(segmentSecs, 'g', -1, 64)))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(strconv.FormatFloat(duration, 'g', -1, 64)))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(strconv.Itoa(len(transcript))))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(transcript))
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func voyageSegmentInputHash(sourceFingerprint string, start, end float64) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("voyage-segment-v2"))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(sourceFingerprint))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(strconv.FormatFloat(start, 'g', -1, 64)))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(strconv.FormatFloat(end, 'g', -1, 64)))
	return hex.EncodeToString(hash.Sum(nil))
}

// cachedVoyageSegments accepts only a contiguous set covering the full video.
// That proof lets Build return before both shot detection and transcoding.
func cachedVoyageSegments(sceneID int, stored []store.StoredEmbeddings, sourceFingerprint string, duration float64) ([]SegmentCache, bool) {
	if len(stored) == 0 {
		return nil, false
	}
	ret := make([]SegmentCache, 0, len(stored))
	nextStart := 0.0
	for _, item := range stored {
		if item.InputHash != voyageSegmentInputHash(sourceFingerprint, item.SegmentStart, item.SegmentEnd) ||
			len(item.Vectors) == 0 {
			continue
		}
		if item.SegmentStart != nextStart ||
			item.SegmentEnd <= item.SegmentStart ||
			item.SegmentEnd > duration {
			return nil, false
		}
		ret = append(ret, SegmentCache{
			SceneID: sceneID,
			Start:   item.SegmentStart,
			End:     item.SegmentEnd,
			Vector:  append([]float32(nil), item.Vectors...),
		})
		nextStart = item.SegmentEnd
	}
	return ret, len(ret) > 0 && nextStart == duration
}

// IndexScene populates the read-only segment cache after scene analysis.
func (v *VoyageSegmentIndex) IndexScene(ctx context.Context, sceneID int, videoPath string, duration float64) (int, error) {
	segments, err := v.Build(ctx, sceneID, videoPath, "", duration)
	return len(segments), err
}

func (v *VoyageSegmentIndex) segments(ctx context.Context, videoPath string, duration float64) []SegmentCache {
	if cuts := v.shotBoundaries(ctx, videoPath, duration); len(cuts) > 0 {
		ret := make([]SegmentCache, 0, len(cuts)+1)
		start := 0.0
		for _, end := range append(cuts, duration) {
			if end > start {
				ret = append(ret, SegmentCache{Start: start, End: end})
				start = end
			}
		}
		return ret
	}
	window := v.SegmentSecs
	if window <= 0 {
		window = defaultVoyageSegmentSecs
	}
	ret := make([]SegmentCache, 0, int(duration/window)+1)
	for start := 0.0; start < duration; start += window {
		ret = append(ret, SegmentCache{Start: start, End: min(start+window, duration)})
	}
	return ret
}

func (v *VoyageSegmentIndex) shotBoundaries(ctx context.Context, videoPath string, duration float64) []float64 {
	ffmpeg := v.FFmpegPath
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	command := exec.CommandContext(ctx, ffmpeg,
		"-nostdin", "-hide_banner", "-i", videoPath,
		"-vf", "select='gt(scene,0.3)',showinfo", "-an", "-f", "null", "-")
	output, err := command.CombinedOutput()
	if err != nil {
		return nil
	}
	matches := sceneTimeRE.FindAllStringSubmatch(string(output), -1)
	cuts := make([]float64, 0, len(matches))
	for _, match := range matches {
		at, err := strconv.ParseFloat(match[1], 64)
		if err == nil && at > 0 && at < duration {
			cuts = append(cuts, at)
		}
	}
	sort.Float64s(cuts)
	return cuts
}

func (v *VoyageSegmentIndex) segmentVideo(ctx context.Context, videoPath string, start, end float64) ([]byte, error) {
	var video []byte
	var err error
	if v.ExtractVideo != nil {
		video, err = v.ExtractVideo(ctx, videoPath, start, end)
	} else {
		video, err = v.extractVideo(ctx, videoPath, start, end)
	}
	if err != nil {
		return nil, fmt.Errorf("extract video %.3f-%.3f: %w", start, end, err)
	}
	if len(video) == 0 {
		return nil, errors.New("Voyage multimodal video is empty")
	}
	if len(video) > maxVoyageMediaBytes {
		return nil, fmt.Errorf("Voyage multimodal video is %d bytes; limit is %d", len(video), maxVoyageMediaBytes)
	}
	return video, nil
}

func (v *VoyageSegmentIndex) extractVideo(ctx context.Context, videoPath string, start, end float64) ([]byte, error) {
	ffmpeg := v.FFmpegPath
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	duration := end - start
	if duration <= 0 {
		return nil, errors.New("Voyage video segment duration must be positive")
	}
	command := exec.CommandContext(ctx, ffmpeg,
		"-nostdin", "-hide_banner", "-loglevel", "error",
		"-ss", strconv.FormatFloat(start, 'f', 3, 64),
		"-t", strconv.FormatFloat(duration, 'f', 3, 64),
		"-i", videoPath, "-an",
		"-vf", "fps=1/6,scale=448:-2",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "28",
		"-pix_fmt", "yuv420p", "-movflags", "frag_keyframe+empty_moov",
		"-f", "mp4", "pipe:1")
	return command.Output()
}

func (v *VoyageSegmentIndex) embed(ctx context.Context, transcript string, video []byte) ([]float32, error) {
	type contentItem struct {
		Type        string `json:"type"`
		Text        string `json:"text,omitempty"`
		VideoBase64 string `json:"video_base64,omitempty"`
	}
	content := make([]contentItem, 0, 2)
	if transcript != "" {
		content = append(content, contentItem{Type: "text", Text: transcript})
	}
	content = append(content, contentItem{
		Type: "video_base64", VideoBase64: "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(video),
	})
	payload, err := json.Marshal(map[string]any{
		"inputs": []any{map[string]any{"content": content}},
		"model":  v.model(), "input_type": "document", "truncation": false,
		"output_dimension": v.dimension(),
	})
	if err != nil {
		return nil, err
	}
	vectors, err := v.embeddingRequest(ctx, payload, 1)
	if err != nil {
		return nil, err
	}
	return v.prepareVector(vectors[0])
}
