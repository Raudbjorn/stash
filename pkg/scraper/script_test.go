package scraper

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/models"
	"github.com/stretchr/testify/assert"
)

func TestScraperCommandResultPreservesFailureWhenCancellationLosesRace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	processErr := errors.New("exit status 7")
	cancel()

	err := scraperCommandResult(ctx, processErr, false)

	assert.ErrorIs(t, err, ErrScraperScript)
	assert.NotErrorIs(t, err, context.Canceled)
}

func TestScraperCommandResultReturnsCancellationWhenItKilledCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := scraperCommandResult(ctx, errors.New("signal: killed"), true)

	assert.ErrorIs(t, err, context.Canceled)
	assert.NotErrorIs(t, err, ErrScraperScript)
}

func TestRunScraperScriptReturnsCancellationWhenContextKillsProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell")
	}

	startedPath := filepath.Join(t.TempDir(), "started")
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		var out map[string]interface{}
		s := &scriptScraper{}
		result <- s.runScraperScript(
			ctx,
			[]string{"sh", "-c", `printf '{}'; : > "$1"; exec sleep 30`, "sh", startedPath},
			"{}",
			&out,
		)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(startedPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scraper helper did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-result:
		assert.ErrorIs(t, err, context.Canceled)
		assert.NotErrorIs(t, err, ErrScraperScript)
	case <-time.After(5 * time.Second):
		t.Fatal("scraper did not return after cancellation")
	}
}

func Test_imageInputFromImage_worksWithMultipleFiles(t *testing.T) {

	date, _ := models.ParseDate("2020-01-01")
	model := models.Image{
		ID:           1,
		Title:        "Test Image",
		URLs:         models.NewRelatedStrings([]string{"https://example.com/image.png"}),
		Date:         &date,
		Code:         "Code",
		Photographer: "Photographer",
		Files: models.NewRelatedFiles([]models.File{
			makeImageFile(1),
			makeImageFile(2),
		}),
	}

	input := imageInputFromImage(&model)

	assert.Equal(t, "1", input.ID)
	assert.Equal(t, "Test Image", input.Title)
	assert.Equal(t, "https://example.com/image.png", input.Urls[0])
	assert.Equal(t, "2020-01-01", *input.Date)
	assert.Equal(t, "Code", input.Code)
	assert.Equal(t, "Photographer", input.Photographer)
	assert.Equal(t, "/data/images/image_0001_.png", input.Files[0].Path)
	assert.Equal(t, "/data/images/image_0002_.png", input.Files[1].Path)
}

func TestRunScraperScriptReturnsCanceledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var output []models.ScrapedPerformer
	s := &scriptScraper{}
	err := s.runScraperScript(ctx, []string{os.Args[0]}, `{"name":"candidate"}`, &output)

	assert.ErrorIs(t, err, context.Canceled)
}

func TestRunScraperScriptWaitsForProcessAfterDecodeError(t *testing.T) {
	t.Setenv("STASH_SCRAPER_TEST_HELPER", "1")
	marker := t.TempDir() + "/process-finished"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var output []models.ScrapedPerformer
	s := &scriptScraper{}
	err := s.runScraperScript(
		ctx,
		[]string{os.Args[0], "-test.run=^TestScraperScriptDecodeErrorHelper$", "--", marker},
		`{"name":"candidate"}`,
		&output,
	)

	assert.ErrorContains(t, err, "could not unmarshal json from script output")
	assert.FileExists(t, marker, "runScraperScript returned before reaping the scraper process")
}

func TestScraperScriptDecodeErrorHelper(t *testing.T) {
	if os.Getenv("STASH_SCRAPER_TEST_HELPER") != "1" {
		return
	}

	marker := os.Args[len(os.Args)-1]
	_, _ = fmt.Fprint(os.Stdout, strings.Repeat("x", 1<<20))
	time.Sleep(50 * time.Millisecond)
	assert.NoError(t, os.WriteFile(marker, nil, 0o600))
}

func getImageStringValue(index int, field string) string {
	return fmt.Sprintf("image_%04d_%s", index, field)
}

func makeImageFile(i int) *models.ImageFile {
	return &models.ImageFile{
		BaseFile: &models.BaseFile{
			Path:     "/data/images/" + getImageStringValue(i, ".png"),
			Basename: getImageStringValue(i, ".png"),
		},
		Height: 200,
		Width:  300,
	}
}
