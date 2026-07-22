package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/99designs/gqlgen/graphql"
	"github.com/stashapp/stash/pkg/file/video"
	"github.com/stashapp/stash/pkg/models"
)

type SceneUploadCaptionInput struct {
	SceneID      string         `json:"scene_id"`
	CaptionFile  graphql.Upload `json:"caption_file"`
	LanguageCode *string        `json:"language_code"`
}

func (r *mutationResolver) SceneUploadCaption(ctx context.Context, input SceneUploadCaptionInput) (*models.Scene, error) {
	sceneID, err := strconv.Atoi(input.SceneID)
	if err != nil {
		return nil, fmt.Errorf("invalid scene ID: %w", err)
	}

	if input.CaptionFile.File == nil {
		return nil, errors.New("no caption file provided")
	}

	filename := input.CaptionFile.Filename
	if filename == "" {
		return nil, errors.New("caption file has no filename")
	}

	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		return nil, errors.New("caption file has no extension")
	}
	ext = ext[1:] // remove the leading dot

	isValidExt := false
	for _, validExt := range video.CaptionExts {
		if ext == validExt {
			isValidExt = true
			break
		}
	}
	if !isValidExt {
		return nil, fmt.Errorf("invalid caption file type: %s (must be .srt or .vtt)", ext)
	}

	var scene *models.Scene
	var videoFile *models.VideoFile

	if err := r.withReadTxn(ctx, func(ctx context.Context) error {
		var err error
		scene, err = r.repository.Scene.Find(ctx, sceneID)
		if err != nil {
			return fmt.Errorf("finding scene: %w", err)
		}
		if scene == nil {
			return errors.New("scene not found")
		}

		if err := scene.LoadPrimaryFile(ctx, r.repository.File); err != nil {
			return fmt.Errorf("loading scene files: %w", err)
		}

		videoFile = scene.Files.Primary()
		if videoFile == nil {
			return errors.New("scene has no primary file")
		}

		return nil
	}); err != nil {
		return nil, err
	}

	languageCode := "en" // default to English
	if input.LanguageCode != nil && *input.LanguageCode != "" {
		languageCode = *input.LanguageCode
	} else if extractedLang := extractLanguageFromFilename(filename); extractedLang != "" {
		languageCode = extractedLang
	}

	if languageCode != video.LangUnknown && !video.IsValidLanguage(languageCode) {
		return nil, fmt.Errorf("invalid language code: %s (must be ISO 639-1 format)", languageCode)
	}

	videoPath := videoFile.Path
	videoBasename := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))

	var captionFilename string
	if languageCode == video.LangUnknown {
		captionFilename = fmt.Sprintf("%s.%s", videoBasename, ext)
	} else {
		captionFilename = fmt.Sprintf("%s.%s.%s", videoBasename, languageCode, ext)
	}

	captionPath := filepath.Join(filepath.Dir(videoPath), captionFilename)

	var existingCaptions []*models.VideoCaption
	if err := r.withReadTxn(ctx, func(ctx context.Context) error {
		var err error
		existingCaptions, err = r.repository.File.GetCaptions(ctx, videoFile.ID)
		return err
	}); err != nil {
		return nil, fmt.Errorf("getting existing captions: %w", err)
	}

	captionExists := video.IsLangInCaptions(languageCode, ext, existingCaptions)

	outFile, err := os.Create(captionPath)
	if err != nil {
		return nil, fmt.Errorf("creating caption file: %w", err)
	}
	defer outFile.Close()

	if _, err := io.Copy(outFile, input.CaptionFile.File); err != nil {
		os.Remove(captionPath)
		return nil, fmt.Errorf("saving caption file: %w", err)
	}

	if _, err := video.ReadSubs(captionPath); err != nil {
		os.Remove(captionPath)
		return nil, fmt.Errorf("invalid caption file format: %w", err)
	}

	var updatedCaptions []*models.VideoCaption
	if captionExists {
		for _, existing := range existingCaptions {
			if existing.LanguageCode == languageCode && existing.CaptionType == ext {
				existing.Filename = captionFilename
			}
			updatedCaptions = append(updatedCaptions, existing)
		}
	} else {
		updatedCaptions = existingCaptions
		updatedCaptions = append(updatedCaptions, &models.VideoCaption{
			LanguageCode: languageCode,
			Filename:     captionFilename,
			CaptionType:  ext,
		})
	}

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		return r.repository.File.UpdateCaptions(ctx, videoFile.ID, updatedCaptions)
	}); err != nil {
		os.Remove(captionPath)
		return nil, fmt.Errorf("updating captions in database: %w", err)
	}

	updatedScene, err := r.getScene(ctx, sceneID)
	if err != nil {
		return nil, fmt.Errorf("retrieving updated scene: %w", err)
	}

	return updatedScene, nil
}

// extractLanguageFromFilename attempts to extract language code from filename
// e.g., "subtitle.en.srt" -> "en"
func extractLanguageFromFilename(filename string) string {
	basename := strings.TrimSuffix(filename, filepath.Ext(filename))
	langExt := filepath.Ext(basename)

	if len(langExt) > 1 {
		lang := langExt[1:] // remove the leading dot
		if video.IsValidLanguage(lang) {
			return lang
		}
	}

	return ""
}
