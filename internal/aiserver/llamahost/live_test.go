package llamahost_test

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	_ "image/png"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stashapp/stash/internal/aiserver/llamahost"
	"github.com/stashapp/stash/internal/aiserver/proc"
	"github.com/stashapp/stash/internal/aiserver/store"
	"github.com/stashapp/stash/internal/aiserver/tagging"
	"github.com/stashapp/stash/pkg/aitag"
	"github.com/stashapp/stash/pkg/aitag/assets"
	"github.com/stashapp/stash/pkg/aitag/llamaprov"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/models/mocks"
)

// TestLivePinnedVLM proves that the independently pinned server, model and
// projector still agree on their wire and multimodal formats. It deliberately
// says nothing about tag accuracy; marker-grounded evaluation owns that claim.
func TestLivePinnedVLM(t *testing.T) {
	if os.Getenv("AI_LIVE_FETCH") != "1" {
		t.Skip("set AI_LIVE_FETCH=1 to download and exercise pinned llama artifacts")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	cache := os.Getenv("AI_LIVE_CACHE")
	if cache == "" {
		userCache, err := os.UserCacheDir()
		if err != nil {
			t.Fatal(err)
		}
		cache = filepath.Join(userCache, "stash-ai-live")
	}
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	downloader := &assets.Downloader{Dir: cache}
	serverAsset, err := assets.ServerAsset("")
	if err != nil {
		t.Fatal(err)
	}
	executable, err := downloader.Fetch(ctx, serverAsset, nil)
	if err != nil {
		t.Fatalf("fetch llama-server: %v", err)
	}
	pair, ok := assets.FindPair(assets.DefaultVisionPair)
	if !ok {
		t.Fatalf("default vision pair %q is not catalogued", assets.DefaultVisionPair)
	}
	modelPath, projectorPath, err := downloader.FetchPair(ctx, pair, nil)
	if err != nil {
		t.Fatalf("fetch vision pair: %v", err)
	}
	runDir := filepath.Join(cache, "run")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}

	socket, err := proc.PrivateUnixSocket(runDir, "llama.sock")
	if err != nil {
		t.Fatal(err)
	}
	host, err := llamahost.New(llamahost.Config{
		RunDir: runDir, Executable: executable, ModelPath: modelPath, ProjectorPath: projectorPath,
		ContextTokens: pair.ContextTokens, Threads: 0, GPULayers: 0, StartupTimeout: 15 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	host.Start()
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopCancel()
		host.Stop(stopCtx)
	}()
	if err := host.WaitReady(ctx); err != nil {
		t.Fatalf("wait for pinned llama-server: %v (status=%+v)", err, host.Status())
	}
	ready := host.Status()
	if ready.State != proc.StateReady || ready.PID == 0 {
		t.Fatalf("ready status = %+v", ready)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://llama/props", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := host.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var props struct {
		Modalities struct {
			Vision bool `json:"vision"`
		} `json:"modalities"`
	}
	decodeErr := json.NewDecoder(response.Body).Decode(&props)
	response.Body.Close()
	if decodeErr != nil || response.StatusCode != http.StatusOK || !props.Modalities.Vision {
		t.Fatalf("props status=%d vision=%v decode=%v", response.StatusCode, props.Modalities.Vision, decodeErr)
	}

	labels := []string{"face", "person", "outdoors"}
	provider, err := llamaprov.New(llamaprov.Config{
		Client: host.Client(), BaseURL: "http://llama", Pair: pair, Labels: labels, FFmpegPath: "ffmpeg",
		Available: host.Available, WaitReady: host.WaitReady,
		CloseHost: func() {
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer stopCancel()
			host.Stop(stopCtx)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rgb, width, height := readLiveFixture(t, filepath.Join("..", "..", "..", "pkg", "aitag", "native", "testdata", "face.png"))
	decisions, err := provider.ClassifyFrame(ctx, rgb, width, height)
	if err != nil {
		t.Fatalf("classify committed fixture: %v", err)
	}
	if len(decisions) != len(labels) {
		t.Fatalf("decisions=%v, want every configured label", decisions)
	}
	for _, label := range labels {
		if _, exists := decisions[label]; !exists {
			t.Errorf("schema-valid response omitted %q: %v", label, decisions)
		}
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatalf("ffmpeg is required by AI_LIVE_FETCH: %v", err)
	}
	fixture := filepath.Join("..", "..", "..", "pkg", "aitag", "native", "testdata", "face.png")
	video := filepath.Join(t.TempDir(), "face.mp4")
	command := exec.CommandContext(ctx, ffmpeg,
		"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-loop", "1", "-i", fixture, "-t", "2", "-r", "1", "-pix_fmt", "yuv420p", video)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generate live scene: %v: %s", err, output)
	}
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "ai.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	sceneRepo := &liveSceneRepo{sceneID: 7, path: video}
	service := tagging.NewService(models.Repository{
		TxnManager: liveTxn{}, Scene: sceneRepo,
	}, database, tagging.Config{Provider: provider, FFmpegPath: ffmpeg})
	analysis, err := service.AnalyzeScene(ctx, tagging.AnalyzeRequest{
		SceneID: 7, Options: aitag.Options{FrameInterval: 2}, SkipWriteback: true,
	}, nil)
	if err != nil {
		t.Fatalf("analyze generated scene through live model: %v", err)
	}
	if analysis.RunID == 0 {
		t.Fatal("live analysis did not store a completed run")
	}
	stored, err := database.GetLatestSceneRun(ctx, llamaprov.ProviderName, 7)
	if err != nil || stored == nil || stored.CompletedAt == nil {
		t.Fatalf("stored live run=%+v err=%v", stored, err)
	}

	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	if stopped := host.Status(); stopped.State != proc.StateStopped || stopped.PID != 0 {
		t.Errorf("stopped status = %+v", stopped)
	}
	if _, err := os.Stat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("private socket remains after close: %v", err)
	}
}

func readLiveFixture(t *testing.T, path string) ([]byte, int, int) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoded, _, err := image.Decode(file)
	if err != nil {
		t.Fatal(err)
	}
	bounds := decoded.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	rgb := make([]byte, width*height*3)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			r, g, b, _ := decoded.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			offset := (y*width + x) * 3
			rgb[offset], rgb[offset+1], rgb[offset+2] = byte(r>>8), byte(g>>8), byte(b>>8)
		}
	}
	return rgb, width, height
}

type liveSceneRepo struct {
	mocks.SceneReaderWriter
	sceneID int
	path    string
}

func (r *liveSceneRepo) Find(context.Context, int) (*models.Scene, error) {
	return &models.Scene{ID: r.sceneID}, nil
}

func (r *liveSceneRepo) GetFiles(context.Context, int) ([]*models.VideoFile, error) {
	return []*models.VideoFile{{BaseFile: &models.BaseFile{Path: r.path}}}, nil
}

type liveTxn struct{}

func (liveTxn) Begin(ctx context.Context, _ bool) (context.Context, error) { return ctx, nil }
func (liveTxn) WithDatabase(ctx context.Context) (context.Context, error)  { return ctx, nil }
func (liveTxn) Commit(context.Context) error                               { return nil }
func (liveTxn) Rollback(context.Context) error                             { return nil }
func (liveTxn) IsLocked(error) bool                                        { return false }
