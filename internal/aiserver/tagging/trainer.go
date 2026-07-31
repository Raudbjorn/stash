package tagging

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/stashapp/stash/internal/aiserver/store"
	"github.com/stashapp/stash/pkg/aitag/native"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/txn"
)

// Building a training set from the user's own markers.
//
// This is what makes the trained head practical: the labels already exist. A
// user who has been marking their library has, without meaning to, been
// building a labelled dataset - every marker says "this stretch of this scene
// is this tag", which is exactly a per-frame label once the frames are lined up
// against the cached embeddings.

// TrainRequest configures a training run.
type TrainRequest struct {
	// Service and Model select which cached embeddings to use. Vectors from
	// different models are not comparable, so both are required.
	Service string
	Model   string

	// Name is what the fitted head is stored as.
	Name string

	// IncludeTags restricts training to these tag names. Empty uses every tag
	// that appears on enough markers.
	IncludeTags []string

	// ExcludeGenerated skips markers this server created, which is on by
	// default and load-bearing: training on your own output teaches the head to
	// reproduce its own mistakes rather than the user's judgement.
	ExcludeGenerated bool

	Options native.TrainOptions
}

// TrainResult reports what a training run produced.
type TrainResult struct {
	Name    string               `json:"name"`
	Model   string               `json:"embed_model"`
	Dim     int                  `json:"dim"`
	Labels  []string             `json:"labels"`
	Metrics *native.TrainMetrics `json:"metrics"`
	// ScenesUsed is how many scenes contributed, which is the number a user
	// should judge the result by: a head fitted from three scenes will not
	// generalise however good its validation score looks.
	ScenesUsed int `json:"scenes_used"`
}

// ErrNoTrainingData reports that nothing has been analysed yet.
//
// A normal state for a fresh installation rather than a failure, so it is
// distinguishable: the API answers 422 with an explanation instead of a 500
// that reads like something broke.
var ErrNoTrainingData = errors.New("no training data is available")

// Trainer fits heads from cached embeddings and Stash markers.
type Trainer struct {
	repo models.Repository
	db   *store.DB
}

// NewTrainer builds a trainer.
func NewTrainer(repo models.Repository, db *store.DB) *Trainer {
	return &Trainer{repo: repo, db: db}
}

// DefaultTrainRequest is the safe configuration.
func DefaultTrainRequest(service, model string) TrainRequest {
	return TrainRequest{
		Service:          service,
		Model:            model,
		Name:             "user-head",
		ExcludeGenerated: true,
		Options:          native.DefaultTrainOptions(),
	}
}

// Train fits a head and stores it.
func (t *Trainer) Train(ctx context.Context, req TrainRequest) (*TrainResult, error) {
	if t.db == nil {
		return nil, fmt.Errorf("the AI database is not available")
	}
	if req.Service == "" || req.Model == "" {
		return nil, fmt.Errorf("training needs a service and an embedding model")
	}
	if req.Name == "" {
		req.Name = "user-head"
	}

	scenes, err := t.db.EmbeddedScenes(ctx, req.Service, req.Model)
	if err != nil {
		return nil, err
	}
	if len(scenes) == 0 {
		return nil, fmt.Errorf(
			"%w: no scenes have cached %s embeddings; analyse some scenes with embeddings enabled first",
			ErrNoTrainingData, req.Model)
	}

	// Markers we generated are excluded by default. Training on them would fit
	// the head to its own predecessor's output rather than to anything the user
	// actually decided.
	generated := map[int]bool{}
	if req.ExcludeGenerated {
		for _, sceneID := range scenes {
			ids, err := t.db.GeneratedMarkerIDs(ctx, sceneID)
			if err != nil {
				return nil, err
			}
			for _, id := range ids {
				generated[id] = true
			}
		}
	}

	include := map[string]bool{}
	for _, name := range req.IncludeTags {
		include[strings.ToLower(strings.TrimSpace(name))] = true
	}

	var (
		labelIndex = map[string]int{}
		labels     []string
		samples    []native.Sample
		used       int
		dim        int
	)

	for _, sceneID := range scenes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		embeddings, err := t.db.GetEmbeddings(ctx, req.Service, sceneID, req.Model)
		if err != nil {
			return nil, err
		}
		if embeddings == nil || embeddings.Dim == 0 {
			continue
		}
		if dim == 0 {
			dim = embeddings.Dim
		}
		if embeddings.Dim != dim {
			// Two models under one name would silently mix incomparable
			// vectors into one training set.
			return nil, fmt.Errorf(
				"scene %d has %d-dimensional embeddings but others have %d; re-analyse with one model",
				sceneID, embeddings.Dim, dim)
		}

		intervals, err := markerIntervals(ctx, t.repo, sceneID, generated, include)
		if err != nil {
			return nil, err
		}
		if len(intervals) == 0 {
			// A scene with no markers contributes no labels, but it is not an
			// error: most of a library is unmarked.
			continue
		}

		before := len(samples)
		for frame := 0; frame < embeddings.FrameCount(); frame++ {
			at := 0.0
			if frame < len(embeddings.Times) {
				at = embeddings.Times[frame]
			}

			var frameLabels []int
			for _, interval := range intervals {
				if at < interval.start || at >= interval.end {
					continue
				}
				index, ok := labelIndex[interval.tag]
				if !ok {
					index = len(labels)
					labelIndex[interval.tag] = index
					labels = append(labels, interval.tag)
				}
				frameLabels = append(frameLabels, index)
			}

			// Frames inside no marker are kept as negatives. Without them the
			// head sees only positives and learns to answer yes to everything.
			vector := make([]float32, dim)
			copy(vector, embeddings.Vectors[frame*dim:(frame+1)*dim])
			native.NormalizeInPlace(vector, dim)

			samples = append(samples, native.Sample{Vector: vector, Labels: frameLabels})
		}

		if len(samples) > before {
			used++
		}
	}

	if len(samples) == 0 {
		return nil, fmt.Errorf("%w: no marked scenes have cached embeddings", ErrNoTrainingData)
	}

	head, metrics, err := native.Train(ctx, labels, dim, samples, req.Options)
	if err != nil {
		return nil, err
	}

	if err := t.db.StoreTrainedHead(ctx, store.TrainedHead{
		Name:        req.Name,
		EmbedModel:  req.Model,
		Dim:         dim,
		Labels:      head.Labels,
		Weights:     head.Weights,
		Metrics:     metricsMap(metrics),
		SampleCount: len(samples),
	}); err != nil {
		return nil, fmt.Errorf("store the trained head: %w", err)
	}

	logger.Infof("Trained AI head %q from %d scenes, %d frames, %d labels (macro F1 %.3f)",
		req.Name, used, len(samples), len(head.Labels), metrics.MacroF1)

	return &TrainResult{
		Name:       req.Name,
		Model:      req.Model,
		Dim:        dim,
		Labels:     head.Labels,
		Metrics:    metrics,
		ScenesUsed: used,
	}, nil
}

// LoadHead reconstructs a stored head for inference.
func (t *Trainer) LoadHead(ctx context.Context, name string) (*native.Head, error) {
	if t.db == nil {
		return nil, fmt.Errorf("the AI database is not available")
	}

	stored, err := t.db.GetTrainedHead(ctx, name)
	if err != nil {
		return nil, err
	}
	if stored == nil {
		return nil, fmt.Errorf("no trained head named %q", name)
	}
	return native.NewHead(stored.Labels, stored.Dim, stored.Weights)
}

// interval is one marker's coverage of a scene.
type interval struct {
	tag   string
	start float64
	end   float64
}

// markerIntervals returns a scene's markers as labelled [start,end) ranges.
func markerIntervals(ctx context.Context, repo models.Repository, sceneID int, generated map[int]bool, include map[string]bool) ([]interval, error) {
	if repo.SceneMarker == nil {
		return nil, nil
	}

	var out []interval

	err := txn.WithReadTxn(ctx, repo.TxnManager, func(ctx context.Context) error {
		markers, err := repo.SceneMarker.FindBySceneID(ctx, sceneID)
		if err != nil {
			return err
		}

		for _, marker := range markers {
			if marker == nil || generated[marker.ID] {
				continue
			}

			tag, err := repo.Tag.Find(ctx, marker.PrimaryTagID)
			if err != nil || tag == nil {
				continue
			}
			if len(include) > 0 && !include[strings.ToLower(strings.TrimSpace(tag.Name))] {
				continue
			}

			end := marker.Seconds
			if marker.EndSeconds != nil {
				end = *marker.EndSeconds
			}
			if end <= marker.Seconds {
				// A marker with no end covers an instant. Giving it a nominal
				// window is what makes it usable as a label at all; without one
				// it would match no sampled frame and contribute nothing.
				end = marker.Seconds + defaultMarkerWindow
			}

			out = append(out, interval{tag: tag.Name, start: marker.Seconds, end: end})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(out, func(i, j int) bool { return out[i].start < out[j].start })
	return out, nil
}

// defaultMarkerWindow is how long a marker with no end is taken to cover.
//
// Ten seconds: long enough to catch several sampled frames at a typical
// interval, short enough not to mislabel a scene's worth of them.
const defaultMarkerWindow = 10.0

func metricsMap(metrics *native.TrainMetrics) map[string]any {
	if metrics == nil {
		return nil
	}

	perLabel := make(map[string]any, len(metrics.PerLabel))
	for label, m := range metrics.PerLabel {
		perLabel[label] = map[string]any{
			"positives": m.Positives,
			"precision": m.Precision,
			"recall":    m.Recall,
			"f1":        m.F1,
		}
	}

	return map[string]any{
		"samples":            metrics.Samples,
		"training_samples":   metrics.TrainingSamples,
		"validation_samples": metrics.ValidationCount,
		"epochs":             metrics.Epochs,
		"final_loss":         metrics.FinalLoss,
		"macro_f1":           metrics.MacroF1,
		"per_label":          perLabel,
		"skipped_labels":     metrics.SkippedLabels,
	}
}
