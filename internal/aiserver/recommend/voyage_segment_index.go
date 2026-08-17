package recommend

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stashapp/stash/internal/aiserver/store"
)

const (
	voyageSegmentService         = "voyage_multimodal"
	defaultVoyageSegmentModel    = "voyage-multimodal-3.5"
	defaultVoyageSegmentEndpoint = "https://api.voyageai.com/v1/multimodalembeddings"
	defaultVoyageSegmentSecs     = 30.0
	defaultVoyageDimension       = 256
	maxVoyageMediaBytes          = 20 << 20
	maxVoyageInputTokens         = 32_000
)

var sceneTimeRE = regexp.MustCompile(`pts_time:([0-9]+(?:\.[0-9]+)?)`)

// VoyageSegmentIndex builds read-only recommendation vectors for video
// segments. It never writes tags or markers.
type VoyageSegmentIndex struct {
	APIKey, Model string
	Endpoint      string
	SegmentSecs   float64
	Dimension     int
	DB            *store.DB
	Cache         *SegmentCache
	FFmpegPath    string
	Client        *http.Client
	ExtractFrame  func(context.Context, string, float64) ([]byte, error)
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
	model := strings.TrimSpace(v.Model)
	if model == "" {
		model = defaultVoyageSegmentModel
	}
	segments := v.segments(ctx, videoPath, duration)
	stored, err := v.DB.GetEmbeddingSegments(ctx, voyageSegmentService, sceneID, model)
	if err != nil {
		return nil, err
	}
	cached := make(map[float64]store.StoredEmbeddings, len(stored))
	for _, item := range stored {
		cached[item.SegmentStart] = item
	}

	ret := make([]SegmentCache, 0, len(segments))
	for _, segment := range segments {
		if item, ok := cached[segment.Start]; ok && item.SegmentEnd == segment.End && len(item.Vectors) > 0 {
			ret = append(ret, SegmentCache{SceneID: sceneID, Start: segment.Start, End: segment.End, Vector: append([]float32(nil), item.Vectors...)})
			continue
		}
		images, err := v.segmentFrames(ctx, videoPath, segment.Start, segment.End)
		if err != nil {
			return nil, err
		}
		vector, err := v.embed(ctx, transcript, images)
		if err != nil {
			return nil, fmt.Errorf("embed scene %d segment %.3f-%.3f: %w", sceneID, segment.Start, segment.End, err)
		}
		if err := v.DB.StoreEmbeddings(ctx, voyageSegmentService, store.StoredEmbeddings{
			SceneID: sceneID, Model: model, Dim: len(vector), FrameInterval: segment.End - segment.Start,
			Times: []float64{segment.Start}, Vectors: vector, SegmentStart: segment.Start, SegmentEnd: segment.End,
		}); err != nil {
			return nil, err
		}
		item := SegmentCache{SceneID: sceneID, Start: segment.Start, End: segment.End, Vector: vector}
		v.Cache = &item
		ret = append(ret, item)
	}
	return ret, nil
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

func (v *VoyageSegmentIndex) segmentFrames(ctx context.Context, videoPath string, start, end float64) ([][]byte, error) {
	ret := make([][]byte, 0, 5)
	for _, fraction := range []float64{0, 0.25, 0.5, 0.75, 1} {
		at := start + (end-start)*fraction
		if at == end && end > start {
			at = max(start, end-0.001)
		}
		var image []byte
		var err error
		if v.ExtractFrame != nil {
			image, err = v.ExtractFrame(ctx, videoPath, at)
		} else {
			image, err = v.extractFrame(ctx, videoPath, at)
		}
		if err != nil {
			return nil, fmt.Errorf("extract frame %.3f: %w", at, err)
		}
		if err := validateVoyageImage(image); err != nil {
			return nil, err
		}
		ret = append(ret, image)
	}
	return ret, nil
}

func (v *VoyageSegmentIndex) extractFrame(ctx context.Context, videoPath string, at float64) ([]byte, error) {
	ffmpeg := v.FFmpegPath
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	command := exec.CommandContext(ctx, ffmpeg,
		"-nostdin", "-hide_banner", "-loglevel", "error", "-ss", strconv.FormatFloat(at, 'f', 3, 64),
		"-i", videoPath, "-frames:v", "1", "-vf", "scale=448:-2", "-f", "image2pipe", "-vcodec", "mjpeg", "pipe:1")
	return command.Output()
}

func (v *VoyageSegmentIndex) embed(ctx context.Context, transcript string, images [][]byte) ([]float32, error) {
	type contentItem struct {
		Type        string `json:"type"`
		Text        string `json:"text,omitempty"`
		ImageBase64 string `json:"image_base64,omitempty"`
	}
	content := make([]contentItem, 0, len(images)+1)
	if transcript != "" {
		content = append(content, contentItem{Type: "text", Text: transcript})
	}
	tokens := (len(transcript) + 3) / 4
	for _, image := range images {
		config, err := jpeg.DecodeConfig(bytes.NewReader(image))
		if err != nil {
			return nil, fmt.Errorf("decode JPEG dimensions: %w", err)
		}
		tokens += config.Width * config.Height / 560
		content = append(content, contentItem{Type: "image_base64", ImageBase64: "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(image)})
	}
	if tokens > maxVoyageInputTokens {
		return nil, fmt.Errorf("voyage multimodal input is approximately %d tokens; limit is %d", tokens, maxVoyageInputTokens)
	}
	model := v.Model
	if model == "" {
		model = defaultVoyageSegmentModel
	}
	payload, err := json.Marshal(map[string]any{
		"inputs": []any{map[string]any{"content": content}},
		"model":  model, "input_type": "document", "truncation": false,
	})
	if err != nil {
		return nil, err
	}
	endpoint := v.Endpoint
	if endpoint == "" {
		endpoint = defaultVoyageSegmentEndpoint
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+v.APIKey)
	req.Header.Set("Content-Type", "application/json")
	client := v.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var decoded struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&decoded); err != nil {
		return nil, err
	}
	if len(decoded.Data) != 1 || len(decoded.Data[0].Embedding) == 0 {
		return nil, errors.New("voyage multimodal response contained no embedding")
	}
	dimension := v.Dimension
	if dimension <= 0 {
		dimension = defaultVoyageDimension
	}
	if len(decoded.Data[0].Embedding) != dimension {
		return nil, fmt.Errorf("voyage embedding dimension %d, want %d", len(decoded.Data[0].Embedding), dimension)
	}
	return decoded.Data[0].Embedding, nil
}

func validateVoyageImage(image []byte) error {
	if len(image) == 0 {
		return errors.New("voyage multimodal image is empty")
	}
	if len(image) > maxVoyageMediaBytes {
		return fmt.Errorf("voyage multimodal image is %d bytes; limit is %d", len(image), maxVoyageMediaBytes)
	}
	return nil
}
