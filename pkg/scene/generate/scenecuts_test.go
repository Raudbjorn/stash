package generate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/ffmpeg/transcoder"
	"github.com/stashapp/stash/pkg/fsutil"
)

func TestParseSceneCutTimestamps(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    []float64
		wantErr bool
	}{
		{
			name:   "no cuts",
			output: "",
			want:   []float64{},
		},
		{
			name: "single cut",
			output: "frame:42   pts:12345    pts_time:12.345\n" +
				"lavfi.scene_score=0.512340\n",
			want: []float64{12.345},
		},
		{
			name: "multiple cuts, out of order in output",
			output: "frame:100  pts:54321   pts_time:54.321\n" +
				"lavfi.scene_score=0.612340\n" +
				"frame:10   pts:1234    pts_time:1.234\n" +
				"lavfi.scene_score=0.512340\n",
			want: []float64{1.234, 54.321},
		},
		{
			name:    "malformed pts_time",
			output:  "frame:1 pts:1 pts_time:notanumber\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSceneCutTimestamps([]byte(tt.output))
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseSceneCutTimestamps() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseSceneCutTimestamps() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestDetectSceneCutsReturnsContextCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell script")
	}

	scriptPath := filepath.Join(t.TempDir(), "ffmpeg")
	script := `#!/bin/sh
if [ "$1" = "-version" ]; then
	echo "ffmpeg version 7.0.0"
	exit 0
fi
: > "$0.started"
exec sleep 30
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatalf("writing fake ffmpeg: %v", err)
	}

	g := Generator{
		Encoder:     ffmpeg.NewEncoder(scriptPath),
		LockManager: fsutil.NewReadLockManager(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := g.DetectSceneCuts(ctx, "input.mp4", transcoder.SceneDetectOptions{})
		result <- err
	}()

	startedPath := scriptPath + ".started"
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(startedPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake ffmpeg did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("DetectSceneCuts() error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("DetectSceneCuts() did not return after cancellation")
	}
}
