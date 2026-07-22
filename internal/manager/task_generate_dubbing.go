package manager

import (
	"context"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/file/video"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
)

const dubbingPath = "/v1/dub"

// GenerateDubbingTask produces a dubbed video for a scene. It sends the scene's
// already-translated caption (e.g. movie.zh.srt, produced by
// GenerateSubtitlesTask) to the dub service, which synthesizes a dubbed audio
// track via CosyVoice. That track is then muxed over the original video into a
// sidecar "<name>.<lang>-dub.mp4". The original video is left untouched.
//
// This standalone task only handles scenes whose translated caption already
// exists. When subtitles and dubbing are generated together in one run, the
// caption does not exist at queue time, so the dub is instead chained onto the
// subtitle task (see GenerateSubtitlesTask.Dub); both paths share dubScene.
type GenerateDubbingTask struct {
	repository models.Repository
	Scene      models.Scene
	Overwrite  bool
}

func (t *GenerateDubbingTask) GetDescription() string {
	return fmt.Sprintf("Generating dubbing for %s", t.Scene.Path)
}

// required reports whether dubbing should run for this scene. It is called
// within the generate job's read transaction.
func (t *GenerateDubbingTask) required(ctx context.Context) bool {
	f := t.Scene.Files.Primary()
	if f == nil {
		return false
	}
	// dubbing is opt-in: with no dub service configured there is nothing to do.
	if config.GetInstance().GetDubbingURL() == "" {
		logger.Debugf("[dubbing] no dub service configured; skipping %s", f.Path)
		return false
	}
	if t.Overwrite {
		return true
	}
	target := config.GetInstance().GetSubtitleGenerationTranslateTo()
	// skip if a dubbed file already exists
	if _, err := os.Stat(dubOutputPath(f.Path, target)); err == nil {
		return false
	}
	// need a translated caption to dub from
	srtPath := video.GetCaptionPath(f.Path, target, "srt")
	if _, err := os.Stat(srtPath); err != nil {
		return false
	}
	return true
}

func (t *GenerateDubbingTask) Start(ctx context.Context) {
	if err := t.repository.WithReadTxn(ctx, func(ctx context.Context) error {
		return t.Scene.LoadPrimaryFile(ctx, t.repository.File)
	}); err != nil {
		logger.Errorf("[dubbing] error loading primary file for scene %d: %v", t.Scene.ID, err)
		return
	}

	f := t.Scene.Files.Primary()
	if f == nil {
		return
	}

	if err := dubScene(ctx, f.Path, f.Duration, t.Overwrite); err != nil {
		logger.Errorf("[dubbing] %v", err)
	}
}

// dubScene produces the sidecar dubbed video for videoPath from its translated
// caption (e.g. movie.zh.srt). It is shared by GenerateDubbingTask and the
// inline dub step that runs right after GenerateSubtitlesTask writes the
// translated caption, so a single generate run can produce captions and then
// dub from them. The original video is left untouched. A missing caption,
// unknown duration, or already-existing dub is treated as a skip (nil error).
func dubScene(ctx context.Context, videoPath string, duration float64, overwrite bool) error {
	cfg := config.GetInstance()
	// opt-in guard: without a configured dub service, skip cleanly before
	// reading captions, extracting audio, or contacting any host.
	if cfg.GetDubbingURL() == "" {
		logger.Debugf("[dubbing] no dub service configured; skipping %s", videoPath)
		return nil
	}
	target := cfg.GetSubtitleGenerationTranslateTo()

	outPath := dubOutputPath(videoPath, target)
	if !overwrite {
		if _, err := os.Stat(outPath); err == nil {
			return nil
		}
	}

	// the dub source is the tagged dub script when present (speaker + emotion
	// routing), else the display caption (older runs, single-speaker content)
	captionPath := video.GetCaptionPath(videoPath, target, "srt")
	srtPath := dubScriptPath(videoPath, target)
	if _, err := os.Stat(srtPath); err != nil {
		srtPath = captionPath
	}
	srtBytes, err := os.ReadFile(srtPath)
	if err != nil {
		logger.Warnf("[dubbing] no %s caption for %s (%v); skipping", target, videoPath, err)
		return nil
	}

	if duration <= 0 {
		logger.Warnf("[dubbing] unknown/zero duration for %s; skipping", videoPath)
		return nil
	}

	// fetch the dubbed audio track from the dub service
	tmp, err := os.CreateTemp("", "stash-dub-*.wav")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	dubAudioPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(dubAudioPath)

	// hand the dub service the scene's original audio so it separates and remixes
	// the music/SFX under the dubbed voice. extraction or upload failures degrade
	// gracefully to a voice-only dub.
	var bgAudioPath string
	bgTmp, berr := os.CreateTemp("", "stash-dubbg-*.wav")
	if berr != nil {
		logger.Warnf("[dubbing] could not create background temp for %s (%v); dubbing voice-only", videoPath, berr)
	} else {
		bgAudioPath = bgTmp.Name()
		_ = bgTmp.Close()
		defer os.Remove(bgAudioPath)
		if berr := extractDubBackground(ctx, videoPath, bgAudioPath); berr != nil {
			logger.Warnf("[dubbing] could not extract background audio from %s (%v); dubbing voice-only", videoPath, berr)
			bgAudioPath = ""
		}
	}

	recut, refit, err := requestDub(ctx, string(srtBytes), duration, cfg.GetDubbingVoice(), bgAudioPath, dubAudioPath)
	if err != nil {
		return fmt.Errorf("dub service error for %s: %w", videoPath, err)
	}
	// The dub re-cut the unit-level Chinese into clause-level display lines timed
	// to the synthesized audio; it is the display caption (and the muxed soft-sub).
	if strings.Contains(recut, "-->") {
		if werr := os.WriteFile(captionPath, []byte(recut), 0644); werr != nil {
			logger.Warnf("[dubbing] could not write re-cut caption for %s: %v", videoPath, werr)
		}
	}

	// One-shot pace convergence: the dub measured this scene's cloned voices
	// speaking at a rate that disagrees with the budget the translation
	// assumed. The dub service already wrote the corrected calibration, so a
	// single re-translate + re-dub lands the converged result inside this
	// same generate run instead of over the next few scenes.
	if refit {
		srcScript := dubScriptPath(videoPath, cfg.GetSubtitleGenerationLanguage())
		if enTagged, rerr := os.ReadFile(srcScript); rerr == nil {
			logger.Infof("[dubbing] pace calibration shifted; re-translating %s with the measured rate", videoPath)
			retrans, terr := translateSRT(ctx, string(enTagged), target)
			if terr == nil && strings.Contains(retrans, "-->") {
				_ = os.WriteFile(dubScriptPath(videoPath, target), []byte(retrans), 0644)
				_ = os.WriteFile(captionPath, []byte(stripSpeakerTags(retrans)), 0644)
				recut2, _, derr := requestDub(ctx, retrans, duration, cfg.GetDubbingVoice(), bgAudioPath, dubAudioPath)
				if derr != nil {
					return fmt.Errorf("dub service error (refit) for %s: %w", videoPath, derr)
				}
				if strings.Contains(recut2, "-->") {
					_ = os.WriteFile(captionPath, []byte(recut2), 0644)
				}
			} else if terr != nil {
				logger.Warnf("[dubbing] refit re-translation failed (%v); keeping the first dub", terr)
			}
		}
	}

	// mux the dubbed audio over the original video into the sidecar file;
	// the soft-sub track is always the CLEAN caption, never the tagged script
	if err := muxDub(ctx, videoPath, dubAudioPath, captionPath, target, outPath); err != nil {
		return fmt.Errorf("muxing dubbed video for %s: %w", videoPath, err)
	}
	logger.Infof("[dubbing] generated dubbed video %s", outPath)
	return nil
}

// dubOutputPath returns "<dir>/<name>.<lang>-dub.mp4".
func dubOutputPath(videoPath, lang string) string {
	ext := filepath.Ext(videoPath)
	return strings.TrimSuffix(videoPath, ext) + "." + lang + "-dub.mp4"
}

// dubScriptPath returns "<dir>/<name>.<lang>.srt.dub" — the tagged dub script
// ("[Speaker N|emotion]: " prefixes for voice/emotion routing) kept beside the
// clean display caption; deliberately not ".srt" so caption scans ignore it.
func dubScriptPath(videoPath, lang string) string {
	return video.GetCaptionPath(videoPath, lang, "srt") + ".dub"
}

// requestDub posts the SRT to the dub service /v1/dub and streams the returned
// wav to outPath. When bgAudioPath is non-empty, the request is sent as
// multipart with the original audio attached as bg_audio so the service remixes
// the background; otherwise a plain url-encoded form is used. recut=1 asks the
// service to also return a clause-level display caption re-cut from the dub
// audio; the response is then multipart/mixed (audio part + recut SRT part).
//
// Returns the re-cut display SRT (empty when the service is an old build that
// returns plain audio) and the X-Dub-Cps-Refit flag (this scene's measured
// speaking rate disagreed with the translation's budget assumption).
func requestDub(ctx context.Context, srt string, duration float64, voice, bgAudioPath, outPath string) (string, bool, error) {
	serviceURL := config.GetInstance().GetDubbingURL() + dubbingPath
	durStr := strconv.FormatFloat(duration, 'f', 3, 64)

	var req *http.Request
	var err error
	if bgAudioPath == "" {
		form := url.Values{}
		form.Set("text", srt)
		form.Set("duration", durStr)
		form.Set("voice", voice)
		form.Set("recut", "1")
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, serviceURL, strings.NewReader(form.Encode()))
		if err != nil {
			return "", false, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		pr, pw := io.Pipe()
		mw := multipart.NewWriter(pw)
		go func() {
			werr := func() error {
				if err := mw.WriteField("text", srt); err != nil {
					return err
				}
				if err := mw.WriteField("duration", durStr); err != nil {
					return err
				}
				if err := mw.WriteField("voice", voice); err != nil {
					return err
				}
				if err := mw.WriteField("recut", "1"); err != nil {
					return err
				}
				bf, err := os.Open(bgAudioPath)
				if err != nil {
					return err
				}
				defer bf.Close()
				part, err := mw.CreateFormFile("bg_audio", filepath.Base(bgAudioPath))
				if err != nil {
					return err
				}
				if _, err := io.Copy(part, bf); err != nil {
					return err
				}
				return mw.Close()
			}()
			_ = pw.CloseWithError(werr)
		}()
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, serviceURL, pr)
		if err != nil {
			return "", false, err
		}
		req.Header.Set("Content-Type", mw.FormDataContentType())
	}

	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", false, fmt.Errorf("dub service returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	refit := resp.Header.Get("X-Dub-Cps-Refit") == "1"

	mediaType, params, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if strings.HasPrefix(mediaType, "multipart/") {
		recut, err := streamMultipartDub(resp.Body, params["boundary"], outPath)
		if err != nil {
			return "", refit, err
		}
		if recut == "" {
			logger.Warnf("[dubbing] multipart dub response carried no re-cut caption")
		}
		return recut, refit, nil
	}

	// legacy / recut-off path: the whole body is the wav, streamed to disk.
	out, err := os.Create(outPath)
	if err != nil {
		return "", false, err
	}
	defer out.Close()
	if _, err := io.Copy(out, resp.Body); err != nil {
		return "", false, err
	}
	return "", refit, nil
}

// streamMultipartDub splits a multipart/mixed dub response: the audio part is
// streamed to outPath (never buffered), the text part is read into the returned
// re-cut SRT string.
func streamMultipartDub(body io.Reader, boundary, outPath string) (string, error) {
	if boundary == "" {
		return "", fmt.Errorf("multipart dub response missing boundary")
	}
	mr := multipart.NewReader(body, boundary)
	var recut string
	wroteAudio := false
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		ct, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
		if strings.HasPrefix(ct, "audio/") {
			out, cerr := os.Create(outPath)
			if cerr != nil {
				p.Close()
				return "", cerr
			}
			_, cerr = io.Copy(out, p)
			out.Close()
			p.Close()
			if cerr != nil {
				return "", cerr
			}
			wroteAudio = true
		} else if strings.HasPrefix(ct, "text/") || strings.Contains(ct, "subrip") || p.FormName() == "recut_srt" {
			b, rerr := io.ReadAll(p)
			p.Close()
			if rerr != nil {
				return "", rerr
			}
			recut = string(b)
		} else {
			p.Close()
		}
	}
	if !wroteAudio {
		return "", fmt.Errorf("multipart dub response carried no audio part")
	}
	return recut, nil
}

// extractDubBackground extracts the scene's original audio to a stereo 44.1kHz
// wav at outPath, suitable for the dub service to source-separate and remix the
// music/SFX under the dubbed voice.
func extractDubBackground(ctx context.Context, videoPath, outPath string) error {
	args := ffmpeg.Args{}.LogLevel(ffmpeg.LogLevelError)
	args = args.Input(videoPath)
	args = append(args, "-vn")
	args = append(args, "-ac", "2", "-ar", "44100", "-c:a", "pcm_s16le", "-f", "wav")
	args = args.Overwrite()
	args = args.Output(outPath)
	return instance.FFMpeg.Generate(ctx, args)
}

// muxDub writes a sidecar mp4 with the original video stream, the dubbed audio,
// and the translated caption as a soft subtitle track.
func muxDub(ctx context.Context, videoPath, dubAudioPath, srtPath, lang, outPath string) error {
	args := ffmpeg.Args{}.LogLevel(ffmpeg.LogLevelError)
	args = args.Input(videoPath)
	args = args.Input(dubAudioPath)
	args = args.Input(srtPath)
	args = append(args,
		"-map", "0:v:0", "-map", "1:a:0", "-map", "2:0",
		"-c:v", "copy", "-c:a", "aac", "-b:a", "192k",
		"-c:s", "mov_text", "-metadata:s:s:0", "language="+lang,
		"-shortest",
	)
	args = args.Overwrite()
	args = args.Output(outPath)
	return instance.FFMpeg.Generate(ctx, args)
}
