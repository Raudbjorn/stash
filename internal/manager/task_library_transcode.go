package manager

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"strconv"

	"sync"

	"github.com/remeh/sizedwaitgroup"
	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/file"
	filevideo "github.com/stashapp/stash/pkg/file/video"
	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/scene"
	"github.com/stashapp/stash/pkg/scene/librarytranscode"
	"github.com/stashapp/stash/pkg/sliceutil/stringslice"
)

type LibraryTranscodeProfile string

const (
	LibraryTranscodeHQ480  LibraryTranscodeProfile = "HQ_480"
	LibraryTranscodeLQ480  LibraryTranscodeProfile = "LQ_480"
	LibraryTranscodeMax360 LibraryTranscodeProfile = "MAX_360"
)

var AllLibraryTranscodeProfile = []LibraryTranscodeProfile{
	LibraryTranscodeHQ480,
	LibraryTranscodeLQ480,
	LibraryTranscodeMax360,
}

func (e LibraryTranscodeProfile) IsValid() bool {
	switch e {
	case LibraryTranscodeHQ480, LibraryTranscodeLQ480, LibraryTranscodeMax360:
		return true
	}
	return false
}

func (e LibraryTranscodeProfile) String() string {
	return string(e)
}

func (e *LibraryTranscodeProfile) UnmarshalGQL(v interface{}) error {
	str, ok := v.(string)
	if !ok {
		return fmt.Errorf("enums must be strings")
	}

	*e = LibraryTranscodeProfile(str)
	if !e.IsValid() {
		return fmt.Errorf("%s is not a valid LibraryTranscodeProfile", str)
	}
	return nil
}

func (e LibraryTranscodeProfile) MarshalGQL(w io.Writer) {
	fmt.Fprint(w, strconv.Quote(e.String()))
}

type LibraryTranscodeInput struct {
	SceneIDs []string                 `json:"sceneIDs"`
	Profile  *LibraryTranscodeProfile `json:"profile"`
}

type LibraryTranscodeJob struct {
	repository models.Repository
	input      LibraryTranscodeInput

	mu        sync.Mutex
	rewritten int
	failed    int
	skipped   int
}

func (j *LibraryTranscodeJob) addRewritten() {
	j.mu.Lock()
	j.rewritten++
	j.mu.Unlock()
}

func (j *LibraryTranscodeJob) addFailed() {
	j.mu.Lock()
	j.failed++
	j.mu.Unlock()
}

func (j *LibraryTranscodeJob) addSkipped() {
	j.mu.Lock()
	j.skipped++
	j.mu.Unlock()
}

func resolveLibraryTranscodeProfile(p *LibraryTranscodeProfile) LibraryTranscodeProfile {
	if p == nil || *p == "" {
		return LibraryTranscodeHQ480
	}
	return *p
}

func hasNVENC() bool {
	return instance.FFMpeg != nil && (instance.FFMpeg.HasHWCodec(ffmpeg.VideoCodecN264) || instance.FFMpeg.HasHWCodec(ffmpeg.VideoCodecN264H))
}

func (p LibraryTranscodeProfile) targetHeight() int {
	if p == LibraryTranscodeMax360 {
		return 360
	}
	return 480
}

func (p LibraryTranscodeProfile) nvencSpec(width, height int) librarytranscode.EncodeSpec {
	switch p {
	case LibraryTranscodeLQ480:
		return librarytranscode.NVENCSpec(480, 30, "64k", 0, width, height)
	case LibraryTranscodeMax360:
		return librarytranscode.NVENCSpec(360, 35, "48k", 24, width, height)
	default:
		return librarytranscode.NVENCSpec(480, 24, "96k", 0, width, height)
	}
}

func (j *LibraryTranscodeJob) Execute(ctx context.Context, progress *job.Progress) error {
	if len(j.input.SceneIDs) == 0 {
		return fmt.Errorf("sceneIDs required")
	}

	profile := resolveLibraryTranscodeProfile(j.input.Profile)
	if (profile == LibraryTranscodeHQ480 || profile == LibraryTranscodeLQ480) && !hasNVENC() {
		return fmt.Errorf("h264_nvenc not available")
	}

	ids, err := stringslice.StringSliceToIntSlice(j.input.SceneIDs)
	if err != nil {
		return fmt.Errorf("converting scene ids: %w", err)
	}

	var scenes []*models.Scene
	if err := j.repository.WithReadTxn(ctx, func(ctx context.Context) error {
		var err error
		scenes, err = j.repository.Scene.FindMany(ctx, ids)
		if err != nil {
			return err
		}
		for _, s := range scenes {
			if err := s.LoadPrimaryFile(ctx, j.repository.File); err != nil {
				logger.Errorf("Error loading primary file for scene %d: %v", s.ID, err)
			}
		}
		return nil
	}); err != nil {
		return err
	}

	progress.SetTotal(len(scenes))
	wg := sizedwaitgroup.New(1)

	for _, s := range scenes {
		if job.IsCancelled(ctx) {
			break
		}
		sc := s
		task := &LibraryTranscodeTask{
			job:     j,
			scene:   sc,
			profile: profile,
		}
		wg.Add()
		go func() {
			defer wg.Done()
			progress.ExecuteTask(task.GetDescription(), func() {
				task.Start(ctx)
			})
			progress.Increment()
		}()
	}
	wg.Wait()

	logger.Infof("%d rewritten, %d failed, %d skipped", j.rewritten, j.failed, j.skipped)
	if j.failed > 0 && j.rewritten == 0 {
		return fmt.Errorf("%d failed, %d skipped", j.failed, j.skipped)
	}
	return nil

}

type LibraryTranscodeTask struct {
	job     *LibraryTranscodeJob
	scene   *models.Scene
	profile LibraryTranscodeProfile
}

func (t *LibraryTranscodeTask) GetDescription() string {
	return fmt.Sprintf("Library transcode %s", t.scene.DisplayName())
}

func (t *LibraryTranscodeTask) Start(ctx context.Context) {
	if job.IsCancelled(ctx) {
		return
	}
	if !t.scene.Files.PrimaryLoaded() {
		logger.Infof("skipping scene %d: no primary file", t.scene.ID)
		t.job.addSkipped()
		return
	}
	primary := t.scene.Files.Primary()
	if primary == nil {
		logger.Infof("skipping scene %d: no primary file", t.scene.ID)
		t.job.addSkipped()
		return
	}

	target := t.profile.targetHeight()
	if models.GetMinResolution(primary) <= target {
		logger.Infof("skipping scene %d: min-side %d <= %d", t.scene.ID, models.GetMinResolution(primary), target)
		t.job.addSkipped()
		return
	}

	srcPath := primary.Path
	if _, err := os.Stat(srcPath); err != nil {
		logger.Infof("skipping scene %d: path missing %s", t.scene.ID, srcPath)
		t.job.addSkipped()
		return
	}

	if err := t.rewrite(ctx, primary); err != nil {
		logger.Errorf("library transcode scene %d: %v", t.scene.ID, err)
		t.job.addFailed()
		return
	}
	t.job.addRewritten()
}

func (t *LibraryTranscodeTask) rewrite(ctx context.Context, primary *models.VideoFile) error {
	srcPath := primary.Path
	oldID := primary.ID
	algo := instance.Config.GetVideoFileNamingAlgorithm()
	oldHash := t.scene.GetHash(algo)

	KillRunningStreams(t.scene, algo)

	if err := instance.Paths.Generated.EnsureTmpDir(); err != nil {
		return fmt.Errorf("ensuring tmp dir: %w", err)
	}
	tempPath := instance.Paths.Generated.GetTmpPath(fmt.Sprintf("libtranscode-%d.mp4", t.scene.ID))
	if _, err := os.Stat(tempPath); err == nil {
		_ = os.Remove(tempPath)
	}

	srcDuration := primary.Duration
	target := t.profile.targetHeight()

	specs := []librarytranscode.EncodeSpec{t.profile.nvencSpec(primary.Width, primary.Height)}
	if t.profile == LibraryTranscodeMax360 {
		specs = append(specs,
			librarytranscode.SoftwareX265Spec(primary.Width, primary.Height),
			librarytranscode.SoftwareX264Spec(primary.Width, primary.Height),
		)
	}

	var encodeErr error
	encoded := false
	for i, spec := range specs {
		if spec.UseCUDA && !hasNVENC() {
			encodeErr = fmt.Errorf("h264_nvenc not available")
			continue
		}
		if err := librarytranscode.Encode(ctx, instance.FFMpeg, srcPath, tempPath, spec); err != nil {
			encodeErr = err
			_ = os.Remove(tempPath)
			logger.Warnf("library transcode attempt %d failed for scene %d: %v", i+1, t.scene.ID, err)
			continue
		}
		if err := librarytranscode.Validate(ctx, instance.FFMpeg, instance.FFProbe, tempPath, target, srcDuration); err != nil {
			encodeErr = err
			logger.Warnf("library transcode validate attempt %d failed for scene %d: %v", i+1, t.scene.ID, err)
			continue
		}
		encoded = true
		encodeErr = nil
		break
	}
	if !encoded {
		_ = os.Remove(tempPath)
		if encodeErr == nil {
			encodeErr = fmt.Errorf("encode failed")
		}
		return encodeErr
	}

	finalPath := librarytranscode.FinalPath(srcPath, target)
	if err := librarytranscode.MoveFileNoReplace(tempPath, finalPath); err != nil {
		return fmt.Errorf("moving temp output: %w", err)
	}

	committed := false
	var newFileID models.FileID
	var copiedSidecars []string
	defer func() {
		if committed {
			return
		}
		_ = os.Remove(finalPath)
		for _, p := range copiedSidecars {
			_ = os.Remove(p)
		}
		if newFileID != 0 {
			_ = t.job.repository.WithTxn(ctx, func(ctx context.Context) error {
				return t.job.repository.File.Destroy(ctx, newFileID)
			})
		}
	}()

	scanned, err := scanLibraryOutput(ctx, t.job.repository, finalPath)
	if err != nil {
		return fmt.Errorf("scanning output: %w", err)
	}
	if scanned == nil || scanned.File == nil {
		return fmt.Errorf("scanning output produced no file")
	}
	if scanned.Renamed {
		return fmt.Errorf("scan treated output as rename of existing file")
	}

	newFile := scanned.File
	newFileID = newFile.Base().ID
	newHash := scene.GetHash(newFile, algo)

	var oldCaps []*models.VideoCaption
	if err := t.job.repository.WithReadTxn(ctx, func(ctx context.Context) error {
		var err error
		oldCaps, err = t.job.repository.File.GetCaptions(ctx, oldID)
		return err
	}); err != nil {
		return fmt.Errorf("loading captions: %w", err)
	}

	var newCaps []*models.VideoCaption
	for _, cap := range oldCaps {
		if cap == nil {
			continue
		}
		relocated, dest, created, err := librarytranscode.RelocateCaption(srcPath, finalPath, *cap)
		if err != nil {
			return fmt.Errorf("copying caption %s: %w", cap.Filename, err)
		}
		if created {
			copiedSidecars = append(copiedSidecars, dest)
		}
		if relocated != nil {
			newCaps = append(newCaps, relocated)
		}
	}

	if err := t.job.repository.WithTxn(ctx, func(ctx context.Context) error {
		if err := instance.SceneService.AssignFile(ctx, t.scene.ID, newFileID); err != nil {
			return fmt.Errorf("assigning file: %w", err)
		}
		partial := models.NewScenePartial()
		partial.PrimaryFileID = &newFileID
		partial.TranscodeBenefit = models.NewOptionalString(string(models.TranscodeBenefitLow))
		if _, err := t.job.repository.Scene.UpdatePartial(ctx, t.scene.ID, partial); err != nil {
			return fmt.Errorf("updating primary file: %w", err)
		}
		if len(newCaps) > 0 {
			if err := t.job.repository.File.UpdateCaptions(ctx, newFileID, newCaps); err != nil {
				return fmt.Errorf("attaching captions: %w", err)
			}
		}
		return nil
	}); err != nil {
		return err
	}

	if oldHash != "" && newHash != "" && oldHash != newHash {
		scene.MigrateHash(instance.Paths, oldHash, newHash)
	}

	committed = true

	trashPath := instance.Config.GetDeleteTrashPath()
	fileDeleter := file.NewDeleterWithTrash(trashPath)
	if err := t.job.repository.WithTxn(ctx, func(ctx context.Context) error {
		fileDeleter.RegisterHooks(ctx)
		isPrimary, err := t.job.repository.File.IsPrimary(ctx, oldID)
		if err != nil {
			return fmt.Errorf("checking if file is primary: %w", err)
		}
		if isPrimary {
			return fmt.Errorf("cannot delete primary file %s", srcPath)
		}
		oldFiles, err := t.job.repository.File.Find(ctx, oldID)
		if err != nil {
			return err
		}
		if len(oldFiles) == 0 {
			return fmt.Errorf("old file %d not found", oldID)
		}
		const deleteFile = true
		return file.Destroy(ctx, t.job.repository.File, oldFiles[0], fileDeleter, deleteFile)
	}); err != nil {
		logger.Errorf("library transcode scene %d: new file is primary but source delete failed: %v", t.scene.ID, err)
		return fmt.Errorf("deleting source file: %w", err)
	}

	return nil
}

func scanLibraryOutput(ctx context.Context, repo models.Repository, path string) (*file.ScanFileResult, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	osfs := &file.OsFS{}
	size, err := file.GetFileSize(osfs, path, info)
	if err != nil {
		return nil, err
	}

	scanner := &file.Scanner{
		Repository: file.NewRepository(repo),
		FileDecorators: []file.Decorator{
			&filevideo.Decorator{FFProbe: instance.FFProbe},
		},
		FingerprintCalculator: &fingerprintCalculator{instance.Config},
		FS:                    osfs,
		RootPaths:             instance.Config.GetStashPaths().Paths(),
	}

	return scanner.ScanFile(ctx, file.ScannedFile{
		BaseFile: &models.BaseFile{
			DirEntry: models.DirEntry{
				ModTime:   file.ModTime(info),
				BirthTime: file.BirthTime(info),
			},
			Path:     path,
			Basename: filepath.Base(path),
			Size:     size,
		},
		FS:   osfs,
		Info: info,
	})
}
