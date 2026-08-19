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

	sourceFingerprint, err := v.sourceFingerprint(videoPath, transcript, duration)
	if err != nil {
		return nil, err
	}
	model := v.cacheModel()
	stored, err := v.DB.GetEmbeddingSegments(ctx, voyageSegmentService, sceneID, model)
	if err != nil {
		return nil, err
	}
	if cached, ok := cachedVoyageSegments(sceneID, sourceFingerprint, duration, v.dimension(), stored); ok {
		return cached, nil
	}

	// A complete cache hit returns above, before shot detection or segment
	// transcoding. Clear incomplete or stale rows so an interrupted rebuild
	// cannot masquerade as a complete cache on the next run.
	if len(stored) > 0 {
		if err := v.DB.DeleteEmbeddingSegments(ctx, voyageSegmentService, sceneID, model); err != nil {
			return nil, err
		}
	}

	segments := v.segments(ctx, videoPath, duration)
	ret := make([]SegmentCache, 0, len(segments))
	for _, segment := range segments {
		video, err := v.segmentVideo(ctx, videoPath, segment.Start, segment.End)
		if err != nil {
			return nil, err
		}
		vector, err := v.embed(ctx, transcript, video)
		if err != nil {
			return nil, fmt.Errorf("embed scene %d segment %.3f-%.3f: %w", sceneID, segment.Start, segment.End, err)
		}
		inputHash := voyageSegmentInputHash(sourceFingerprint, segment.Start, segment.End)
		if err := v.DB.StoreEmbeddings(ctx, voyageSegmentService, store.StoredEmbeddings{
			SceneID: sceneID, Model: model, Dim: len(vector), FrameInterval: segment.End - segment.Start,
			Times: []float64{segment.Start}, Vectors: vector, SegmentStart: segment.Start, SegmentEnd: segment.End,
			InputHash: inputHash,
		}); err != nil {
			return nil, err
		}
		ret = append(ret, SegmentCache{SceneID: sceneID, Start: segment.Start, End: segment.End, Vector: vector})
	}
	return ret, nil
}

func (v *VoyageSegmentIndex) sourceFingerprint(videoPath, transcript string, duration float64) (string, error) {
	info, err := os.Stat(videoPath)
	if err != nil {
		return "", fmt.Errorf("stat Voyage source video: %w", err)
	}
	absolutePath, err := filepath.Abs(videoPath)
	if err != nil {
		return "", fmt.Errorf("resolve Voyage source video path: %w", err)
	}
	segmentSecs := v.SegmentSecs
	if segmentSecs <= 0 {
		segmentSecs = defaultVoyageSegmentSecs
	}
	transcriptHash := sha256.Sum256([]byte(transcript))
	payload, err := json.Marshal(struct {
		Version        int     `json:"version"`
		Path           string  `json:"path"`
		Size           int64   `json:"size"`
		ModifiedNanos  int64   `json:"modified_nanos"`
		Duration       float64 `json:"duration"`
		SegmentSeconds float64 `json:"segment_seconds"`
		TranscriptHash string  `json:"transcript_hash"`
	}{
		Version:        2,
		Path:           filepath.Clean(absolutePath),
		Size:           info.Size(),
		ModifiedNanos:  info.ModTime().UnixNano(),
		Duration:       duration,
		SegmentSeconds: segmentSecs,
		TranscriptHash: hex.EncodeToString(transcriptHash[:]),
	})
	if err != nil {
		return "", fmt.Errorf("encode Voyage source fingerprint: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func voyageSegmentInputHash(sourceFingerprint string, start, end float64) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(sourceFingerprint))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(strconv.FormatFloat(start, 'g', -1, 64)))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(strconv.FormatFloat(end, 'g', -1, 64)))
	return hex.EncodeToString(hash.Sum(nil))
}

func cachedVoyageSegments(sceneID int, sourceFingerprint string, duration float64, dimension int, stored []store.StoredEmbeddings) ([]SegmentCache, bool) {
	if len(stored) == 0 {
		return nil, false
	}
	ret := make([]SegmentCache, 0, len(stored))
	nextStart := 0.0
	for _, item := range stored {
		if item.SegmentStart != nextStart ||
			item.SegmentEnd <= item.SegmentStart ||
			item.SegmentEnd > duration ||
			item.Dim != dimension ||
			len(item.Vectors) != dimension ||
			item.InputHash != voyageSegmentInputHash(sourceFingerprint, item.SegmentStart, item.SegmentEnd) {
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
	return ret, nextStart == duration
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
