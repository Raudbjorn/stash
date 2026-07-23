package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/stashapp/stash/internal/manager"
	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/utils"
)

type ClipFinder interface {
	models.ClipGetter
}

type clipRoutes struct {
	routes
	clipFinder  ClipFinder
	sceneFinder SceneFinder
}

func (rs clipRoutes) Routes() chi.Router {
	r := chi.NewRouter()

	r.Route("/{clipId}", func(r chi.Router) {
		r.Use(rs.ClipCtx)
		r.Get("/stream", rs.Stream)
		r.Get("/preview", rs.Stream)
	})

	return r
}

// Stream serves the generated clip video file with range support. The stream
// and preview endpoints serve the same pre-rendered file. Returns 404 if the
// file has not been generated yet.
func (rs clipRoutes) Stream(w http.ResponseWriter, r *http.Request) {
	clip := r.Context().Value(clipKey).(*models.Clip)

	var scene *models.Scene
	_ = rs.withReadTxn(r, func(ctx context.Context) error {
		scene, _ = rs.sceneFinder.Find(ctx, clip.SceneID)
		return nil
	})
	if scene == nil {
		http.Error(w, http.StatusText(404), 404)
		return
	}

	sceneHash := scene.GetHash(config.GetInstance().GetVideoFileNamingAlgorithm())
	if sceneHash == "" {
		http.Error(w, http.StatusText(404), 404)
		return
	}

	filepath := manager.GetInstance().Paths.Clips.GetClipVideoPath(sceneHash, clip.ID)
	utils.ServeStaticFile(w, r, filepath)
}

func (rs clipRoutes) ClipCtx(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clipID, err := strconv.Atoi(chi.URLParam(r, "clipId"))
		if err != nil {
			http.Error(w, http.StatusText(404), 404)
			return
		}

		var clip *models.Clip
		_ = rs.withReadTxn(r, func(ctx context.Context) error {
			clip, _ = rs.clipFinder.Find(ctx, clipID)
			return nil
		})
		if clip == nil {
			http.Error(w, http.StatusText(404), 404)
			return
		}

		ctx := context.WithValue(r.Context(), clipKey, clip)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
