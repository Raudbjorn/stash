package manager

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/file/video"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/txn"
)

const (
	subtitleTranscribePath = "/v1/audio/transcriptions"
	subtitleTranslatePath  = "/v1/translate"
)

// GenerateSubtitlesTask generates a caption (subtitle) file for a scene's
// primary video file by sending its audio to an external ASR service
// (parakeet-api). It only targets videos that do not already have captions,
// so already-subtitled content (e.g. Chinese videos with sidecar subs) is
// skipped. parakeet does not support Chinese; the configured language
// (default "en") is sent as the transcription prompt.
type GenerateSubtitlesTask struct {
	repository models.Repository
	Scene      models.Scene
	Overwrite  bool
	// Language overrides the configured default for this run (Parakeet prompt + caption label).
	// Empty means fall back to the configured GetSubtitleGenerationLanguage.
	Language string
	// Dub, when set, dubs the scene from the translated caption produced by this
	// task, right after it is written. This is how Subtitles+Dubbing run in a
	// single generate pass: the translated caption does not exist at queue time,
	// so a standalone GenerateDubbingTask would be skipped. No-op if no
	// translated caption ends up being produced.
	Dub bool
}

func (t *GenerateSubtitlesTask) GetDescription() string {
	return fmt.Sprintf("Generating subtitles for %s", t.Scene.Path)
}

// required reports whether subtitles should be generated for this scene.
// It is called within the generate job's read transaction.
func (t *GenerateSubtitlesTask) required(ctx context.Context) bool {
	f := t.Scene.Files.Primary()
	if f == nil {
		return false
	}

	// subtitle generation is opt-in: with no ASR service configured there is
	// nothing to do, so skip cleanly without contacting any host.
	if config.GetInstance().GetSubtitleGenerationURL() == "" {
		logger.Debugf("[subtitles] no ASR service configured; skipping %s", f.Path)
		return false
	}

	if t.Overwrite {
		return true
	}

	// skip scenes that already have captions
	captions, err := t.repository.File.GetCaptions(ctx, f.Base().ID)
	if err != nil {
		logger.Errorf("[subtitles] error getting captions for %s: %v", f.Path, err)
		return false
	}

	return len(captions) == 0
}

func (t *GenerateSubtitlesTask) Start(ctx context.Context) {
	// the scene may not have its primary file loaded yet (e.g. when invoked
	// from a scan), so load it before use.
	if err := t.repository.WithReadTxn(ctx, func(ctx context.Context) error {
		return t.Scene.LoadPrimaryFile(ctx, t.repository.File)
	}); err != nil {
		logger.Errorf("[subtitles] error loading primary file for scene %d: %v", t.Scene.ID, err)
		return
	}

	f := t.Scene.Files.Primary()
	if f == nil {
		return
	}

	// opt-in guard: without a configured ASR service, skip before extracting
	// any audio or contacting any host.
	if config.GetInstance().GetSubtitleGenerationURL() == "" {
		logger.Debugf("[subtitles] no ASR service configured; skipping %s", f.Path)
		return
	}

	fileID := f.Base().ID
	videoPath := f.Path
	// marker written next to the video when a previous run produced no usable
	// subtitles (e.g. Chinese / unsupported language). lets rescans skip the
	// file instead of re-running detection+ASR every time.
	subskipPath := videoPath + ".subskip"

	// determine the language to send to the ASR service. an explicit per-run
	// Language wins; otherwise fall back to the configured default language.
	cfg := config.GetInstance()
	lang := t.Language
	if lang == "" {
		lang = cfg.GetSubtitleGenerationLanguage()
	}

	// re-check captions here in case they were added since queuing
	if !t.Overwrite {
		var existing []*models.VideoCaption
		if err := t.repository.WithReadTxn(ctx, func(ctx context.Context) error {
			var err error
			existing, err = t.repository.File.GetCaptions(ctx, fileID)
			return err
		}); err != nil {
			logger.Errorf("[subtitles] error getting captions for %s: %v", videoPath, err)
			return
		}
		if len(existing) > 0 {
			return
		}

		// skip files a previous run already determined have no usable
		// subtitles (e.g. Chinese), so rescans don't reprocess them.
		if _, err := os.Stat(subskipPath); err == nil {
			logger.Debugf("[subtitles] skipping %s (previously marked no-subtitles)", videoPath)
			return
		}
	} else {
		// a forced (overwrite) run should retry: clear any stale skip marker.
		_ = os.Remove(subskipPath)
	}

	// extract audio to a temporary 16kHz mono wav for transcription.
	// CreateTemp guarantees a unique name so parallel tasks don't collide.
	tmp, err := os.CreateTemp("", "stash-asr-*.wav")
	if err != nil {
		logger.Errorf("[subtitles] error creating temp file: %v", err)
		return
	}
	audioPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(audioPath)

	if err := t.extractAudio(ctx, videoPath, audioPath); err != nil {
		logger.Errorf("[subtitles] error extracting audio from %s: %v", videoPath, err)
		return
	}

	srt, detected, err := t.transcribe(ctx, audioPath, lang)
	if err != nil {
		logger.Errorf("[subtitles] error transcribing %s: %v", videoPath, err)
		return
	}

	// guard against empty or non-SRT responses. this also covers the case
	// where the service detected an unsupported language (e.g. Chinese) and
	// returned no transcription.
	if !strings.Contains(srt, "-->") {
		logger.Warnf("[subtitles] ASR service returned no usable subtitles for %s (detected language %q)", videoPath, detected)
		// record a skip marker so future rescans don't reprocess this file.
		// transcription errors return earlier and are intentionally not marked,
		// so transient failures can be retried; a forced overwrite run clears it.
		marker := detected
		if marker == "" {
			marker = "none"
		}
		if err := os.WriteFile(subskipPath, []byte(marker+"\n"), 0644); err != nil {
			logger.Debugf("[subtitles] could not write skip marker %s: %v", subskipPath, err)
		}
		return
	}

	// source language: prefer what the service reports, falling back to the
	// requested language. "default" is the service's multilingual model key,
	// not a real language, so resolve it to the configured default.
	srcLang := detected
	if srcLang == "" || srcLang == "default" {
		srcLang = lang
	}
	if srcLang == "default" {
		srcLang = cfg.GetSubtitleGenerationLanguage()
	}

	// Write the original-language caption with a language suffix
	// (e.g. movie.ja.srt) and associate it under its language code, so it
	// shows up as a selectable track. Viewers get the tag-free text; the
	// "[Speaker N]: " prefixes are dub-routing metadata, not subtitle content.
	if err := t.writeAndAssociate(ctx, fileID, videoPath, srcLang, stripSpeakerTags(srt)); err != nil {
		logger.Errorf("[subtitles] error writing %s caption for %s: %v", srcLang, videoPath, err)
		return
	}
	// the tagged source is kept for dub-time re-translation (pace refit)
	if strings.Contains(srt, "[Speaker") {
		if err := os.WriteFile(dubScriptPath(videoPath, srcLang), []byte(srt), 0644); err != nil {
			logger.Warnf("[subtitles] could not write %s dub script for %s: %v", srcLang, videoPath, err)
		}
	}
	logger.Infof("[subtitles] generated %s caption for %s", srcLang, videoPath)

	// Additionally translate captions whose language differs from the target
	// (default Chinese) and keep it as a separate track (e.g. movie.zh.srt).
	// A translation failure does not discard the original caption above.
	if cfg.GetSubtitleGenerationTranslate() {
		target := cfg.GetSubtitleGenerationTranslateTo()
		if srcLang != "" && srcLang != target {
			translated, terr := t.translate(ctx, srt, target)
			if terr != nil {
				logger.Errorf("[subtitles] error translating %s->%s for %s: %v", srcLang, target, videoPath, terr)
				return
			}
			if !strings.Contains(translated, "-->") {
				logger.Warnf("[subtitles] translation produced no usable subtitles for %s", videoPath)
				return
			}
			if err := t.writeAndAssociate(ctx, fileID, videoPath, target, stripSpeakerTags(translated)); err != nil {
				logger.Errorf("[subtitles] error writing %s translation for %s: %v", target, videoPath, err)
				return
			}
			// The tagged original ("[Speaker N|emotion]: ...") is what the dub
			// service needs for voice and emotion routing; keep it beside the
			// clean caption under a non-caption extension so scans ignore it.
			if err := os.WriteFile(dubScriptPath(videoPath, target), []byte(translated), 0644); err != nil {
				logger.Warnf("[subtitles] could not write dub script for %s: %v", videoPath, err)
			}
			logger.Infof("[subtitles] generated %s translation for %s", target, videoPath)
		}
	}

	// when chained from the generate job (Subtitles+Dubbing in one run), dub
	// straight from the translated caption we just produced. dubScene no-ops if
	// no translated caption exists (e.g. translation disabled or src==target
	// with no target caption).
	if t.Dub {
		if err := dubScene(ctx, videoPath, f.Duration, t.Overwrite); err != nil {
			logger.Errorf("[dubbing] %v", err)
		}
	}
}

// speakerTagRE matches the "[Speaker N]: " / "[Speaker N|emotion]: " line prefixes
// the ASR and translation services emit on multi-speaker content. They route
// per-speaker voices and emotion in the dub service and must survive in the
// dub script, but are noise in viewer-facing captions.
var speakerTagRE = regexp.MustCompile(`(?m)^\[Speaker[^\]]*\]:\s*`)

func stripSpeakerTags(srt string) string {
	return speakerTagRE.ReplaceAllString(srt, "")
}

// writeAndAssociate writes an SRT body to a language-suffixed caption file
// (e.g. movie.ja.srt) and associates it with the file under that language code.
func (t *GenerateSubtitlesTask) writeAndAssociate(ctx context.Context, fileID models.FileID, videoPath, langCode, srt string) error {
	captionPath := video.GetCaptionPath(videoPath, langCode, "srt")
	if err := os.WriteFile(captionPath, []byte(srt), 0644); err != nil {
		return err
	}
	return t.associateCaption(ctx, fileID, captionPath, langCode)
}

// translate sends an SRT body to the service /v1/translate and returns the
// translated SRT.
func (t *GenerateSubtitlesTask) translate(ctx context.Context, srt, target string) (string, error) {
	return translateSRT(ctx, srt, target)
}

// translateSRT is the package-level translation call, shared with the dub
// task's pace-refit re-translation.
func translateSRT(ctx context.Context, srt, target string) (string, error) {
	serviceURL := config.GetInstance().GetSubtitleGenerationURL() + subtitleTranslatePath
	form := url.Values{}
	form.Set("text", srt)
	form.Set("target", target)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serviceURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 15 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("translate service returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return string(body), nil
}

// extractAudio extracts the audio track of videoPath to a 16kHz mono wav at
// audioPath, matching the input parakeet expects.
func (t *GenerateSubtitlesTask) extractAudio(ctx context.Context, videoPath, audioPath string) error {
	cfg := config.GetInstance()
	args := ffmpeg.Args{}.LogLevel(ffmpeg.LogLevelError)
	args = args.Input(videoPath)
	args = append(args, "-vn")

	if cfg.GetSubtitleGenerationDenoise() {
		filter := cfg.GetSubtitleGenerationAudioFilter()
		if filter != "" {
			args = append(args, "-af", filter)
		}
	}

	args = append(args, "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", "-f", "wav")
	args = args.Overwrite()
	args = args.Output(audioPath)

	return instance.FFMpeg.Generate(ctx, args)
}

// transcribe uploads the audio file to the ASR service and returns the SRT
// body along with the language the service reports via the X-Detected-Language
// header (empty if not provided).
func (t *GenerateSubtitlesTask) transcribe(ctx context.Context, audioPath, lang string) (string, string, error) {
	serviceURL := config.GetInstance().GetSubtitleGenerationURL() + subtitleTranscribePath

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	contentType := mw.FormDataContentType()

	go func() {
		werr := func() error {
			af, err := os.Open(audioPath)
			if err != nil {
				return err
			}
			defer af.Close()

			part, err := mw.CreateFormFile("file", filepath.Base(audioPath))
			if err != nil {
				return err
			}
			if _, err := io.Copy(part, af); err != nil {
				return err
			}
			if err := mw.WriteField("prompt", lang); err != nil {
				return err
			}
			return mw.Close()
		}()
		_ = pw.CloseWithError(werr)
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serviceURL, pr)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", contentType)

	client := &http.Client{Timeout: 20 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("service returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return string(body), resp.Header.Get("X-Detected-Language"), nil
}

// associateCaption records the generated caption against the video file so it
// is served to the player.
func (t *GenerateSubtitlesTask) associateCaption(ctx context.Context, fileID models.FileID, captionPath, lang string) error {
	return txn.WithTxn(ctx, t.repository.TxnManager, func(ctx context.Context) error {
		captions, err := t.repository.File.GetCaptions(ctx, fileID)
		if err != nil {
			return err
		}

		if video.IsLangInCaptions(lang, "srt", captions) {
			return nil
		}

		captions = append(captions, &models.VideoCaption{
			LanguageCode: lang,
			Filename:     filepath.Base(captionPath),
			CaptionType:  "srt",
		})

		return t.repository.File.UpdateCaptions(ctx, fileID, captions)
	})
}
