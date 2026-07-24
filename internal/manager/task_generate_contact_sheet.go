package manager

import (
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/webp"

	"github.com/disintegration/imaging"
	"github.com/remeh/sizedwaitgroup"

	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/gallery"
	galgen "github.com/stashapp/stash/pkg/gallery/generate"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
)

type GenerateGalleryContactSheetTask struct {
	GalleryID  int
	ImageIDs   []int
	Overwrite  bool
	repository models.Repository
}

func (t *GenerateGalleryContactSheetTask) GetDescription() string {
	return fmt.Sprintf("Generating gallery contact sheet for gallery %d", t.GalleryID)
}

func (t *GenerateGalleryContactSheetTask) Start(ctx context.Context) {
	hash := gallery.ContactSheetHash(t.GalleryID)
	outPath := instance.Paths.Generated.GetGalleryContactSheetPath(hash)
	if !t.Overwrite {
		if exists, _ := fsutil.FileExists(outPath); exists {
			return
		}
	}

	var images []*models.Image
	r := t.repository
	if err := r.WithReadTxn(ctx, func(ctx context.Context) error {
		var err error
		if len(t.ImageIDs) > 0 {
			images, err = r.Image.FindMany(ctx, t.ImageIDs)
			if err != nil {
				return fmt.Errorf("finding images %v: %w", t.ImageIDs, err)
			}
			sort.SliceStable(images, func(i, j int) bool {
				return images[i].Path < images[j].Path
			})
		} else {
			images, err = r.Image.FindByGalleryID(ctx, t.GalleryID)
			if err != nil {
				return fmt.Errorf("finding images for gallery %d: %w", t.GalleryID, err)
			}
		}
		return nil
	}); err != nil {
		if ctx.Err() == nil {
			logger.Errorf("contact sheet: gallery %d: %v", t.GalleryID, err)
		}
		return
	}

	usable := make([]*models.Image, 0, len(images))
	for _, img := range images {
		if img.Path != "" {
			usable = append(usable, img)
		}
	}
	if len(usable) < 2 {
		logger.Warnf("contact sheet: gallery %d has %d usable images (<2), skipping", t.GalleryID, len(usable))
		return
	}

	picked := galgen.PickEvenIndices(len(usable))
	if len(picked) < 2 {
		return
	}

	// Cap inner workers so outer parallelTasks × inner doesn't over-subscribe.
	workers := len(picked)
	outer := config.GetInstance().GetParallelTasksWithAutoDetection()
	if outer < 1 {
		outer = 1
	}
	if per := runtime.NumCPU() / outer; per > 0 && per < workers {
		workers = per
	}
	if workers < 1 {
		workers = 1
	}

	aspects := make([]float64, len(picked))
	wg := sizedwaitgroup.New(workers)
	for i, idx := range picked {
		if ctx.Err() != nil {
			break
		}
		wg.Add()
		i, src := i, usable[idx]
		go func() {
			defer wg.Done()
			a, err := readImageAspect(src.Path)
			if err != nil {
				logger.Debugf("contact sheet: gallery %d: aspect probe failed for %s: %v (falling back to 1.0)", t.GalleryID, src.Path, err)
				a = 1.0
			}
			aspects[i] = a
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return
	}

	cells, canvasH := galgen.ComputeCellGeometry(aspects)
	if canvasH <= 0 || len(cells) != len(picked) {
		logger.Errorf("contact sheet: gallery %d: invalid cell geometry (len=%d canvasH=%d)", t.GalleryID, len(cells), canvasH)
		return
	}

	preResized := make([]image.Image, len(picked))
	wg = sizedwaitgroup.New(workers)
	for i, idx := range picked {
		if ctx.Err() != nil {
			break
		}
		wg.Add()
		i, src := i, usable[idx]
		cell := cells[i]
		go func() {
			defer wg.Done()
			resized, err := decodeAndResize(src.Path, cell.W, cell.H)
			if err != nil {
				logger.Warnf("contact sheet: gallery %d: decode/resize failed for %s: %v (using placeholder)", t.GalleryID, src.Path, err)
				resized = imaging.New(cell.W, cell.H, galgen.ContactSheetBG)
			}
			preResized[i] = resized
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return
	}

	sheet := galgen.Compose(cells, preResized, canvasH)

	if err := fsutil.EnsureDirAll(filepath.Dir(outPath)); err != nil {
		logger.Errorf("contact sheet: gallery %d: creating output dir: %v", t.GalleryID, err)
		return
	}

	tmpFile, err := instance.Paths.Generated.TempFile("gallery_contact_*" + galgen.ContactSheetFileExt)
	if err != nil {
		logger.Errorf("contact sheet: gallery %d: creating temp file: %v", t.GalleryID, err)
		return
	}
	tmpName := tmpFile.Name()
	_ = tmpFile.Close()
	defer func() { _ = os.Remove(tmpName) }()

	if err := writeJPEG(tmpName, sheet, galgen.ContactSheetJPEGQuality); err != nil {
		logger.Errorf("contact sheet: gallery %d: encoding JPEG: %v", t.GalleryID, err)
		return
	}

	if err := fsutil.SafeMove(tmpName, outPath); err != nil {
		logger.Errorf("contact sheet: gallery %d: moving %s -> %s: %v", t.GalleryID, tmpName, outPath, err)
		return
	}

	if err := r.WithTxn(ctx, func(ctx context.Context) error {
		if err := r.Gallery.SetHasGeneratedCover(ctx, t.GalleryID, true); err != nil {
			return fmt.Errorf("setting has_generated_cover: %w", err)
		}
		if instance.GalleryService != nil {
			if err := instance.GalleryService.Updated(ctx, t.GalleryID); err != nil {
				return fmt.Errorf("bumping updated_at: %w", err)
			}
		}
		return nil
	}); err != nil && ctx.Err() == nil {
		logger.Errorf("contact sheet: gallery %d: updating flag/timestamp: %v", t.GalleryID, err)
		return
	}

	logger.Debugf("contact sheet: gallery %d: wrote %s (%dx%d)", t.GalleryID, outPath, galgen.ContactSheetCanvasWidth, canvasH)
}

func readImageAspect(path string) (float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, err
	}
	if cfg.Height <= 0 {
		return 1, nil
	}
	return float64(cfg.Width) / float64(cfg.Height), nil
}

func decodeAndResize(path string, w, h int) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	src, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	return imaging.Fill(src, w, h, imaging.Center, imaging.Lanczos), nil
}

func writeJPEG(path string, img image.Image, quality int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return jpeg.Encode(f, img, &jpeg.Options{Quality: quality})
}
