package recommend

import (
	"context"
	"fmt"
	"strconv"

	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/txn"
)

// Scene hydration for recommenders.
//
// The Python original was ~670 lines, almost all of it guessing at Stash's
// schema: probing for whichever of several table names existed, picking the
// first column that looked like a screenshot path, coping with duration being
// stored in three different units. None of that is needed in-process, where the
// repository is typed and the same code that owns the schema is calling it.

// Fetcher hydrates scenes for recommenders.
type Fetcher struct {
	repo models.Repository
}

// NewFetcher builds a fetcher over Stash's repository.
func NewFetcher(repo models.Repository) *Fetcher {
	return &Fetcher{repo: repo}
}

// FetchScenes hydrates the given scene ids.
//
// Every requested id appears in the result: ids Stash does not know about get a
// stub rather than being dropped, so a recommender working from stale AI
// results still returns a stable-length page instead of silently shrinking it.
func (f *Fetcher) FetchScenes(ctx context.Context, ids []int) (map[int]SceneModel, error) {
	out := make(map[int]SceneModel, len(ids))
	for _, id := range ids {
		out[id] = stubScene(id)
	}
	if len(ids) == 0 || f.repo.Scene == nil {
		return out, nil
	}

	err := txn.WithReadTxn(ctx, f.repo.TxnManager, func(ctx context.Context) error {
		scenes, err := f.repo.Scene.FindByIDs(ctx, ids)
		if err != nil {
			return fmt.Errorf("find scenes: %w", err)
		}

		// Collect related ids across all scenes so each lookup is one query
		// rather than one per scene.
		performerIDs := map[int]bool{}
		tagIDs := map[int]bool{}
		studioIDs := map[int]bool{}

		type loaded struct {
			scene      *models.Scene
			performers []int
			tags       []int
		}
		var rows []loaded

		for _, scene := range scenes {
			if err := scene.LoadPerformerIDs(ctx, f.repo.Scene); err != nil {
				return fmt.Errorf("load performer ids for scene %d: %w", scene.ID, err)
			}
			if err := scene.LoadTagIDs(ctx, f.repo.Scene); err != nil {
				return fmt.Errorf("load tag ids for scene %d: %w", scene.ID, err)
			}
			if err := scene.LoadFiles(ctx, f.repo.Scene); err != nil {
				return fmt.Errorf("load files for scene %d: %w", scene.ID, err)
			}

			row := loaded{scene: scene}
			if scene.PerformerIDs.Loaded() {
				row.performers = scene.PerformerIDs.List()
			}
			if scene.TagIDs.Loaded() {
				row.tags = scene.TagIDs.List()
			}
			for _, id := range row.performers {
				performerIDs[id] = true
			}
			for _, id := range row.tags {
				tagIDs[id] = true
			}
			if scene.StudioID != nil {
				studioIDs[*scene.StudioID] = true
			}
			rows = append(rows, row)
		}

		performers, err := f.loadPerformers(ctx, keys(performerIDs))
		if err != nil {
			return err
		}
		tags, err := f.loadTags(ctx, keys(tagIDs))
		if err != nil {
			return err
		}
		studios, err := f.loadStudios(ctx, keys(studioIDs))
		if err != nil {
			return err
		}

		for _, row := range rows {
			out[row.scene.ID] = buildSceneModel(row.scene, row.performers, row.tags, performers, tags, studios)
		}
		return nil
	})

	return out, err
}

func (f *Fetcher) loadPerformers(ctx context.Context, ids []int) (map[int]map[string]any, error) {
	out := map[int]map[string]any{}
	if len(ids) == 0 || f.repo.Performer == nil {
		return out, nil
	}
	found, err := f.repo.Performer.FindMany(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("find performers: %w", err)
	}
	for _, p := range found {
		if p == nil {
			continue
		}
		out[p.ID] = map[string]any{
			"id":         strconv.Itoa(p.ID),
			"name":       p.Name,
			"image_path": performerImagePath(p),
			"favorite":   p.Favorite,
			"gender":     genderString(p.Gender),
		}
	}
	return out, nil
}

func (f *Fetcher) loadTags(ctx context.Context, ids []int) (map[int]map[string]any, error) {
	out := map[int]map[string]any{}
	if len(ids) == 0 || f.repo.Tag == nil {
		return out, nil
	}
	found, err := f.repo.Tag.FindMany(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("find tags: %w", err)
	}
	for _, t := range found {
		if t == nil {
			continue
		}
		out[t.ID] = map[string]any{
			"id":   strconv.Itoa(t.ID),
			"name": t.Name,
		}
	}
	return out, nil
}

func (f *Fetcher) loadStudios(ctx context.Context, ids []int) (map[int]map[string]any, error) {
	out := map[int]map[string]any{}
	if len(ids) == 0 || f.repo.Studio == nil {
		return out, nil
	}
	found, err := f.repo.Studio.FindMany(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("find studios: %w", err)
	}
	for _, s := range found {
		if s == nil {
			continue
		}
		out[s.ID] = map[string]any{
			"id":   strconv.Itoa(s.ID),
			"name": s.Name,
		}
	}
	return out, nil
}

// buildSceneModel assembles the UI-facing shape.
func buildSceneModel(scene *models.Scene, performerIDs, tagIDs []int,
	performers, tags, studios map[int]map[string]any) SceneModel {

	model := SceneModel{
		ID:    scene.ID,
		Paths: scenePaths(scene.ID),
		// Never nil: the frontend maps over these directly.
		Performers: []map[string]any{},
		Tags:       []map[string]any{},
		Files:      []map[string]any{},
	}

	if scene.Title != "" {
		title := scene.Title
		model.Title = &title
	}

	// Stash stores ratings on a 1-100 scale already, so this passes straight
	// through. The Python read a legacy column and rounded it.
	if scene.Rating != nil {
		rating := *scene.Rating
		model.Rating100 = &rating
	}

	if scene.StudioID != nil {
		if studio, ok := studios[*scene.StudioID]; ok {
			model.Studio = studio
		}
	}

	for _, id := range performerIDs {
		if p, ok := performers[id]; ok {
			model.Performers = append(model.Performers, p)
		}
	}
	for _, id := range tagIDs {
		if t, ok := tags[id]; ok {
			model.Tags = append(model.Tags, t)
		}
	}

	if scene.Files.Loaded() {
		for _, file := range scene.Files.List() {
			if file == nil {
				continue
			}
			model.Files = append(model.Files, map[string]any{
				"id":       strconv.FormatInt(int64(file.ID), 10),
				"path":     file.Path,
				"size":     file.Size,
				"duration": file.Duration,
				"width":    file.Width,
				"height":   file.Height,
			})
		}
	}

	return model
}

// scenePaths builds the media URLs for a scene.
//
// These are Stash's own routes, so they are correct by construction. Note there
// is deliberately no API key appended: the request is same-origin and carries
// the session cookie, and embedding a key in markup would be a regression.
func scenePaths(sceneID int) map[string]any {
	id := strconv.Itoa(sceneID)
	return map[string]any{
		"screenshot": "/scene/" + id + "/screenshot",
		"preview":    "/scene/" + id + "/preview",
		"stream":     "/scene/" + id + "/stream",
		"webp":       "/scene/" + id + "/webp",
	}
}

// performerImagePath builds a performer's image URL with a cache-buster derived
// from when the performer was last updated.
func performerImagePath(p *models.Performer) string {
	path := "/performer/" + strconv.Itoa(p.ID) + "/image?default=true"
	if !p.UpdatedAt.IsZero() {
		path += "&t=" + strconv.FormatInt(p.UpdatedAt.Unix(), 10)
	}
	return path
}

func genderString(g *models.GenderEnum) string {
	if g == nil {
		return ""
	}
	return g.String()
}

// stubScene is the placeholder for an id Stash no longer knows about.
func stubScene(id int) SceneModel {
	title := "Scene " + strconv.Itoa(id)
	return SceneModel{
		ID:         id,
		Title:      &title,
		Paths:      scenePaths(id),
		Performers: []map[string]any{},
		Tags:       []map[string]any{},
		Files:      []map[string]any{},
	}
}

func keys(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
