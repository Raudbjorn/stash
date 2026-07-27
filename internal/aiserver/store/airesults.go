package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"
)

// AI analysis results: which models ran over which scene or image, the
// per-timespan detections they produced, and the aggregates the recommenders
// read.
//
// This schema is the contract between whatever produces detections - a remote
// inference server today, a native pipeline later - and everything downstream,
// so both providers write through here and neither can tell the other apart.

// defaultFrameInterval is assumed when a run does not report one.
//
// It matters more than it looks: a timespan's end is derived by adding the
// frame interval to the last frame that matched, so an absent interval would
// otherwise collapse every detection to zero length.
const defaultFrameInterval = 2.0

// AIModel is one model that has produced results.
type AIModel struct {
	ID         int64
	Service    string
	PluginName *string
	// ModelID is the provider's own identifier, which may be absent.
	ModelID    *int
	Name       string
	Version    *float64
	ModelType  *string
	Categories []string
	Extra      map[string]any
}

// ModelInput describes a model as reported by a provider.
type ModelInput struct {
	ModelID       *int           `json:"model_id"`
	Name          string         `json:"name"`
	Version       *float64       `json:"version"`
	Type          *string        `json:"type"`
	Categories    []string       `json:"categories"`
	FrameInterval *float64       `json:"frame_interval"`
	Extra         map[string]any `json:"extra"`
}

// Timespan is one detection over a range of a scene.
type Timespan struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	// Confidence is absent when the provider did not report one.
	Confidence *float64 `json:"confidence"`
}

// SceneTimespanBuckets groups timespans by category then by tag id.
//
// The tag id is the map key as a string because it identifies a Stash tag and
// callers join on it; labels that never resolved to a tag are dropped, since
// nothing downstream can use them.
type SceneTimespanBuckets map[string]map[string][]Timespan

// FrameDetection is one raw detection as posted by a provider.
type FrameDetection struct {
	Start float64        `json:"start"`
	End   *float64       `json:"end"`
	Extra map[string]any `json:"-"`
}

// SceneRunInput is a completed analysis of one scene.
type SceneRunInput struct {
	Service     string
	PluginName  *string
	SceneID     int
	InputParams map[string]any
	// Timespans is category -> label -> detections.
	Timespans map[string]map[string][]FrameDetection
	// FrameInterval is how far apart sampled frames were.
	FrameInterval *float64
	Duration      *float64
	SchemaVersion any
	Models        []ModelInput
	// ResolveReference maps a provider's label to a Stash tag id. Labels it
	// cannot resolve still produce timespans, but no aggregate - see
	// StoreSceneRun.
	ResolveReference func(label, category string) *int
}

// ImageRunInput is a completed analysis of one image.
type ImageRunInput struct {
	Service     string
	PluginName  *string
	ImageID     int
	InputParams map[string]any
	// TagsByCategory maps a category to the resolved tag ids present.
	TagsByCategory map[string][]int
	Models         []ModelInput
}

// StoredRun summarises the most recent analysis of an entity.
type StoredRun struct {
	RunID       int64
	CompletedAt *time.Time
	InputParams map[string]any
	// Aggregates is "category:tagID" -> total seconds, or just "tagID" when
	// the category is empty.
	Aggregates map[string]float64
	Models     []AIModel
}

// StoreSceneRun records a completed scene analysis and returns the run id.
//
// Two rules here are subtle and deliberate:
//
//  1. A timespan's end is (end ?? start) + frame_interval. A detection that
//     matched a single sampled frame covers the interval up to the next sample,
//     not an instant.
//  2. Aggregates are written ONLY for labels that resolve to a Stash tag id.
//     An unresolved label still gets timespans - they are the raw record - but
//     no aggregate, because aggregates exist to be joined against Stash tags
//     and a row with no tag could never participate.
func (db *DB) StoreSceneRun(ctx context.Context, in SceneRunInput) (int64, error) {
	var runID int64

	err := db.InTx(ctx, func(tx *sql.Tx) error {
		models, err := upsertModels(ctx, tx, in.Service, in.PluginName, in.Models)
		if err != nil {
			return err
		}

		interval := defaultFrameInterval
		if in.FrameInterval != nil {
			interval = *in.FrameInterval
		} else if v, ok := numberFrom(in.InputParams, "frame_interval"); ok {
			interval = v
		}

		metadata := map[string]any{
			"schema_version": in.SchemaVersion,
			"duration":       in.Duration,
			"frame_interval": in.FrameInterval,
		}

		now := NowMillis()
		runID, err = insertRun(ctx, tx, runRow{
			Service:     in.Service,
			PluginName:  in.PluginName,
			EntityType:  "scene",
			EntityID:    in.SceneID,
			InputParams: in.InputParams,
			Metadata:    metadata,
			CompletedAt: &now,
		})
		if err != nil {
			return err
		}

		if err := assignRunModels(ctx, tx, runID, models, in.Models, in.InputParams, &interval); err != nil {
			return err
		}

		totals, err := insertSceneTimespans(ctx, tx, runID, in, interval)
		if err != nil {
			return err
		}
		return insertSceneAggregates(ctx, tx, runID, in, totals)
	})

	return runID, err
}

// totalKey identifies a (category, label) pair while accumulating durations.
type totalKey struct {
	category string
	label    string
}

func insertSceneTimespans(ctx context.Context, tx *sql.Tx, runID int64, in SceneRunInput, interval float64) (map[totalKey]float64, error) {
	totals := map[totalKey]float64{}

	// Deterministic order so a run's rows are reproducible.
	categories := sortedKeys(in.Timespans)
	for _, category := range categories {
		labels := in.Timespans[category]
		for _, label := range sortedKeys(labels) {
			var resolved *int
			if in.ResolveReference != nil {
				resolved = in.ResolveReference(label, category)
			}

			for _, frame := range labels[label] {
				end := frame.Start
				if frame.End != nil {
					end = *frame.End
				}
				end += interval

				valueJSON, err := MarshalArg(nonEmptyMap(frame.Extra))
				if err != nil {
					return nil, err
				}

				if _, err := tx.ExecContext(ctx,
					// str_value carries the PROVIDER'S OWN LABEL, not NULL.
					// Without it an unresolved detection leaves a row that
					// records a time range and nothing about what was detected -
					// so the "raw record" a label with no Stash tag is supposed
					// to leave behind would be unreadable, and a user could not
					// see which tag they need to create.
					`INSERT INTO ai_result_timespans
					 (run_id, entity_type, entity_id, payload_type, category, str_value,
					  value_id, start_s, end_s, value_json)
					 VALUES (?,'scene',?,'tag',?,?,?,?,?,?)`,
					runID, in.SceneID, nullIfEmptyString(category), label, intArg(resolved),
					frame.Start, end, valueJSON); err != nil {
					return nil, fmt.Errorf("insert timespan: %w", err)
				}

				span := end - frame.Start
				if span < 0 {
					span = 0
				}
				totals[totalKey{category: category, label: label}] += span
			}
		}
	}
	return totals, nil
}

func insertSceneAggregates(ctx context.Context, tx *sql.Tx, runID int64, in SceneRunInput, totals map[totalKey]float64) error {
	keys := make([]totalKey, 0, len(totals))
	for k := range totals {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].category != keys[j].category {
			return keys[i].category < keys[j].category
		}
		return keys[i].label < keys[j].label
	})

	for _, key := range keys {
		var resolved *int
		if in.ResolveReference != nil {
			resolved = in.ResolveReference(key.label, key.category)
		}
		if resolved == nil {
			// Deliberate: an aggregate with no tag id cannot be joined against
			// Stash, so it would only ever be dead weight.
			continue
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO ai_result_aggregates
			 (run_id, entity_type, entity_id, payload_type, category, str_value,
			  value_id, metric, value_float, value_json, created_at)
			 VALUES (?,'scene',?,'tag',?,NULL,?,'duration_s',?,NULL,?)`,
			runID, in.SceneID, nullIfEmptyString(key.category), *resolved,
			totals[key], NowMillis()); err != nil {
			return fmt.Errorf("insert aggregate: %w", err)
		}
	}
	return nil
}

// StoreImageRun records a completed image analysis and returns the run id.
//
// Images have no timeline, so results are presence aggregates rather than
// timespans. Re-analysing an image supersedes earlier results for the same
// categories: the stale aggregates are removed so a tag the model no longer
// detects does not linger.
func (db *DB) StoreImageRun(ctx context.Context, in ImageRunInput) (int64, error) {
	var runID int64

	err := db.InTx(ctx, func(tx *sql.Tx) error {
		models, err := upsertModels(ctx, tx, in.Service, in.PluginName, in.Models)
		if err != nil {
			return err
		}

		now := NowMillis()
		runID, err = insertRun(ctx, tx, runRow{
			Service:     in.Service,
			PluginName:  in.PluginName,
			EntityType:  "image",
			EntityID:    in.ImageID,
			InputParams: in.InputParams,
			CompletedAt: &now,
		})
		if err != nil {
			return err
		}

		if err := clearStaleImageAggregates(ctx, tx, in, runID); err != nil {
			return err
		}

		if err := assignRunModels(ctx, tx, runID, models, in.Models, in.InputParams, nil); err != nil {
			return err
		}

		for _, category := range sortedKeys(in.TagsByCategory) {
			for _, tagID := range in.TagsByCategory[category] {
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO ai_result_aggregates
					 (run_id, entity_type, entity_id, payload_type, category, str_value,
					  value_id, metric, value_float, value_json, created_at)
					 VALUES (?,'image',?,'tag',?,NULL,?,'presence',NULL,NULL,?)`,
					runID, in.ImageID, nullIfEmptyString(category), tagID, NowMillis()); err != nil {
					return fmt.Errorf("insert image aggregate: %w", err)
				}
			}
		}
		return nil
	})

	return runID, err
}

// clearStaleImageAggregates drops aggregates from earlier runs for the same
// categories, so re-analysis replaces rather than accumulates.
//
// The empty-category case needs its own statement because SQL equality never
// matches NULL.
func clearStaleImageAggregates(ctx context.Context, tx *sql.Tx, in ImageRunInput, runID int64) error {
	var named []string
	clearNull := false
	for category := range in.TagsByCategory {
		if category == "" {
			clearNull = true
			continue
		}
		named = append(named, category)
	}
	sort.Strings(named)

	const staleRuns = `SELECT id FROM ai_model_runs
	                   WHERE service = ? AND entity_type = 'image' AND entity_id = ? AND id != ?`

	if len(named) > 0 {
		args := []any{in.Service, in.ImageID, runID}
		for _, c := range named {
			args = append(args, c)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM ai_result_aggregates
			 WHERE run_id IN (`+staleRuns+`) AND category IN (`+Placeholders(len(named))+`)`,
			args...); err != nil {
			return fmt.Errorf("clear stale image aggregates: %w", err)
		}
	}

	if clearNull {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM ai_result_aggregates
			 WHERE run_id IN (`+staleRuns+`) AND category IS NULL`,
			in.Service, in.ImageID, runID); err != nil {
			return fmt.Errorf("clear stale null-category aggregates: %w", err)
		}
	}
	return nil
}

// ------------------------------------------------------------------ runs ---

type runRow struct {
	Service     string
	PluginName  *string
	EntityType  string
	EntityID    int
	InputParams map[string]any
	Metadata    map[string]any
	CompletedAt *int64
}

func insertRun(ctx context.Context, tx *sql.Tx, r runRow) (int64, error) {
	params, err := MarshalArg(nonEmptyMap(r.InputParams))
	if err != nil {
		return 0, err
	}
	metadata, err := MarshalArg(nonEmptyMap(r.Metadata))
	if err != nil {
		return 0, err
	}

	var id int64
	err = tx.QueryRowContext(ctx,
		`INSERT INTO ai_model_runs
		 (service, plugin_name, entity_type, entity_id, status, input_params,
		  started_at, completed_at, result_metadata)
		 VALUES (?,?,?,?,'completed',?,?,?,?) RETURNING id`,
		r.Service, NullString(r.PluginName), r.EntityType, r.EntityID,
		params, NowMillis(), int64Arg(r.CompletedAt), metadata,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert model run: %w", err)
	}
	return id, nil
}

// upsertModels records the models a run used, keyed by (service, model_id,
// name). A provider that reports no numeric id gets its own branch, since NULL
// never compares equal.
func upsertModels(ctx context.Context, tx *sql.Tx, service string, pluginName *string, models []ModelInput) (map[string]int64, error) {
	out := map[string]int64{}

	for _, m := range models {
		name := m.Name
		if name == "" && m.ModelID != nil {
			name = strconv.Itoa(*m.ModelID)
		}
		if name == "" {
			name = "unknown"
		}

		categories, err := MarshalArg(m.Categories)
		if err != nil {
			return nil, err
		}
		extra, err := MarshalArg(nonEmptyMap(m.Extra))
		if err != nil {
			return nil, err
		}

		var id int64
		var lookupErr error
		if m.ModelID != nil {
			lookupErr = tx.QueryRowContext(ctx,
				`SELECT id FROM ai_models WHERE service = ? AND model_id = ? AND name = ?`,
				service, *m.ModelID, name).Scan(&id)
		} else {
			lookupErr = tx.QueryRowContext(ctx,
				`SELECT id FROM ai_models WHERE service = ? AND model_id IS NULL AND name = ?`,
				service, name).Scan(&id)
		}

		switch {
		case errors.Is(lookupErr, sql.ErrNoRows):
			if err := tx.QueryRowContext(ctx,
				`INSERT INTO ai_models
				 (service, plugin_name, model_id, name, version, model_type, categories, extra, created_at)
				 VALUES (?,?,?,?,?,?,?,?,?) RETURNING id`,
				service, NullString(pluginName), intArg(m.ModelID), name,
				floatArgValue(m.Version), NullString(m.Type), categories, extra, NowMillis(),
			).Scan(&id); err != nil {
				return nil, fmt.Errorf("insert model %q: %w", name, err)
			}

		case lookupErr != nil:
			return nil, fmt.Errorf("look up model %q: %w", name, lookupErr)

		default:
			if _, err := tx.ExecContext(ctx,
				`UPDATE ai_models
				 SET plugin_name = ?, name = ?, version = ?, model_type = ?, categories = ?, extra = ?
				 WHERE id = ?`,
				NullString(pluginName), name, floatArgValue(m.Version),
				NullString(m.Type), categories, extra, id); err != nil {
				return nil, fmt.Errorf("update model %q: %w", name, err)
			}
		}

		out[modelKey(m.ModelID, name)] = id
	}
	return out, nil
}

func modelKey(modelID *int, name string) string {
	if modelID == nil {
		return "nil|" + name
	}
	return strconv.Itoa(*modelID) + "|" + name
}

// assignRunModels links a run to the models it used, recording the frame
// interval in force for each.
func assignRunModels(ctx context.Context, tx *sql.Tx, runID int64, records map[string]int64, models []ModelInput, inputParams map[string]any, fallbackInterval *float64) error {
	for _, m := range models {
		name := m.Name
		if name == "" && m.ModelID != nil {
			name = strconv.Itoa(*m.ModelID)
		}
		if name == "" {
			name = "unknown"
		}

		modelDBID, ok := records[modelKey(m.ModelID, name)]
		var modelArg any
		if ok {
			modelArg = modelDBID
		}

		interval := m.FrameInterval
		if interval == nil {
			interval = fallbackInterval
		}
		if interval == nil {
			if v, ok := numberFrom(inputParams, "frame_interval"); ok {
				interval = &v
			}
		}

		params, err := MarshalArg(nonEmptyMap(m.Extra))
		if err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO ai_model_run_models (run_id, model_id, input_params, frame_interval, created_at)
			 VALUES (?,?,?,?,?)`,
			runID, modelArg, params, floatArgValue(interval), NowMillis()); err != nil {
			return fmt.Errorf("assign run model: %w", err)
		}
	}
	return nil
}

// ---------------------------------------------------------------- readers ---

// GetSceneTimespans returns every stored timespan for a scene, bucketed by
// category and tag id and ordered so callers can merge them directly.
//
// Timespans whose label never resolved to a tag are omitted: they cannot be
// joined against Stash, so no caller can act on them.
func (db *DB) GetSceneTimespans(ctx context.Context, service string, sceneID int) (SceneTimespanBuckets, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT t.category, t.value_id, t.start_s, t.end_s, t.value_json
		 FROM ai_result_timespans t
		 JOIN ai_model_runs r ON r.id = t.run_id
		 WHERE r.service = ? AND r.entity_type = 'scene' AND r.entity_id = ?
		   AND t.payload_type = 'tag'
		 ORDER BY t.category, t.value_id, t.start_s`, service, sceneID)
	if err != nil {
		return nil, fmt.Errorf("get scene timespans: %w", err)
	}
	defer rows.Close()

	out := SceneTimespanBuckets{}
	for rows.Next() {
		var category sql.NullString
		var valueID sql.NullInt64
		var start float64
		var end sql.NullFloat64
		var payload JSONText[map[string]any]

		if err := rows.Scan(&category, &valueID, &start, &end, &payload); err != nil {
			return nil, fmt.Errorf("scan timespan: %w", err)
		}
		if !valueID.Valid {
			continue
		}

		entry := Timespan{Start: start, End: start}
		if end.Valid {
			entry.End = end.Float64
		}
		if payload.Valid {
			if c, ok := payload.Data["confidence"]; ok {
				if f, ok := floatFromAny(c); ok {
					entry.Confidence = &f
				}
			}
		}

		bucket, ok := out[category.String]
		if !ok {
			bucket = map[string][]Timespan{}
			out[category.String] = bucket
		}
		label := strconv.FormatInt(valueID.Int64, 10)
		bucket[label] = append(bucket[label], entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate timespans: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// LabelledSpan is one stored detection, keyed by the provider's own label
// rather than by a resolved Stash tag id.
type LabelledSpan struct {
	Start      float64  `json:"start"`
	End        float64  `json:"end"`
	Confidence *float64 `json:"confidence,omitempty"`
	// TagID is the Stash tag the label resolved to, or nil when it resolved to
	// nothing. Reported rather than used to filter: a label with no tag is
	// precisely what a user needs to see in order to create one.
	TagID *int `json:"tag_id,omitempty"`
}

// GetSceneSpansByLabel returns a scene's raw stored spans, keyed by category
// and then by the PROVIDER'S LABEL.
//
// Distinct from GetSceneTimespans, which keys by resolved tag id and drops
// anything unresolved. That filtering is right for the recommenders, which join
// on tag ids and can do nothing with a label - but wrong for showing a user what
// was detected: an analysis whose labels have no matching tags would appear to
// have found nothing at all, which is the opposite of what they need to know.
//
// runID restricts the read to a single analysis. Pass 0 for every run, which is
// what a "show me everything ever detected" view wants - but NOT what marker
// regeneration wants: analysing a scene three times would otherwise feed
// three overlapping copies of every span into the clustering and shift every
// marker boundary.
func (db *DB) GetSceneSpansByLabel(ctx context.Context, service string, sceneID int, runID int64) (map[string]map[string][]LabelledSpan, error) {
	query := `SELECT t.category, t.str_value, t.value_id, t.start_s, t.end_s, t.value_json
		 FROM ai_result_timespans t
		 JOIN ai_model_runs r ON r.id = t.run_id
		 WHERE r.service = ? AND r.entity_type = 'scene' AND r.entity_id = ?
		   AND t.payload_type = 'tag'`
	args := []any{service, sceneID}

	if runID > 0 {
		query += ` AND t.run_id = ?`
		args = append(args, runID)
	}
	query += ` ORDER BY t.category, t.str_value, t.start_s`

	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("get scene spans: %w", err)
	}
	defer rows.Close()

	// Never nil: the caller serialises this to a UI that maps over it.
	out := map[string]map[string][]LabelledSpan{}

	for rows.Next() {
		var (
			category sql.NullString
			label    sql.NullString
			valueID  sql.NullInt64
			start    float64
			end      sql.NullFloat64
			payload  JSONText[map[string]any]
		)
		if err := rows.Scan(&category, &label, &valueID, &start, &end, &payload); err != nil {
			return nil, fmt.Errorf("scan span: %w", err)
		}

		entry := LabelledSpan{Start: start, End: start}
		if end.Valid {
			entry.End = end.Float64
		}
		if valueID.Valid {
			id := int(valueID.Int64)
			entry.TagID = &id
		}
		if payload.Valid {
			if c, ok := payload.Data["confidence"]; ok {
				if f, ok := floatFromAny(c); ok {
					entry.Confidence = &f
				}
			}
		}

		bucket, ok := out[category.String]
		if !ok {
			bucket = map[string][]LabelledSpan{}
			out[category.String] = bucket
		}
		bucket[label.String] = append(bucket[label.String], entry)
	}

	return out, rows.Err()
}

// GetSceneTagTotals returns total detected seconds per tag id, summed across
// every run for the scene.
func (db *DB) GetSceneTagTotals(ctx context.Context, service string, sceneID int) (map[int]float64, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT a.value_id, SUM(a.value_float)
		 FROM ai_result_aggregates a
		 JOIN ai_model_runs r ON r.id = a.run_id
		 WHERE r.service = ? AND r.entity_type = 'scene' AND r.entity_id = ?
		   AND a.payload_type = 'tag' AND a.metric = 'duration_s' AND a.value_id IS NOT NULL
		 GROUP BY a.value_id`, service, sceneID)
	if err != nil {
		return nil, fmt.Errorf("get scene tag totals: %w", err)
	}
	defer rows.Close()

	totals := map[int]float64{}
	for rows.Next() {
		var tagID int
		var total sql.NullFloat64
		if err := rows.Scan(&tagID, &total); err != nil {
			return nil, fmt.Errorf("scan tag total: %w", err)
		}
		totals[tagID] = total.Float64
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tag totals: %w", err)
	}
	return totals, nil
}

// GetImageTagIDs returns the tag ids detected on an image.
func (db *DB) GetImageTagIDs(ctx context.Context, service string, imageID int) ([]int, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT DISTINCT a.value_id
		 FROM ai_result_aggregates a
		 JOIN ai_model_runs r ON r.id = a.run_id
		 WHERE r.service = ? AND r.entity_type = 'image' AND r.entity_id = ?
		   AND a.payload_type = 'tag' AND a.value_id IS NOT NULL
		 ORDER BY a.value_id`, service, imageID)
	if err != nil {
		return nil, fmt.Errorf("get image tag ids: %w", err)
	}
	defer rows.Close()

	var out []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan image tag id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// GetLatestSceneRun returns the most recent analysis of a scene.
//
// Ordering uses (completed_at IS NULL) rather than NULLS LAST, which is not
// portable: an unfinished run must never outrank a completed one.
func (db *DB) GetLatestSceneRun(ctx context.Context, service string, sceneID int) (*StoredRun, error) {
	var run StoredRun
	var completedAt NullTime
	var params JSONText[map[string]any]

	err := db.sql.QueryRowContext(ctx,
		`SELECT id, completed_at, input_params FROM ai_model_runs
		 WHERE service = ? AND entity_type = 'scene' AND entity_id = ?
		 ORDER BY (completed_at IS NULL) ASC, completed_at DESC, id DESC LIMIT 1`,
		service, sceneID).Scan(&run.RunID, &completedAt, &params)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get latest scene run: %w", err)
	}

	run.CompletedAt = completedAt.TimePtr()
	run.InputParams = params.Data

	aggregates, err := db.runAggregates(ctx, run.RunID)
	if err != nil {
		return nil, err
	}
	run.Aggregates = aggregates

	models, err := db.ModelHistory(ctx, service, "scene", sceneID)
	if err != nil {
		return nil, err
	}
	run.Models = models

	return &run, nil
}

// runAggregates keys duration aggregates as "category:tagID", or bare "tagID"
// when there is no category.
func (db *DB) runAggregates(ctx context.Context, runID int64) (map[string]float64, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT category, value_id, str_value, value_float FROM ai_result_aggregates
		 WHERE run_id = ? AND metric = 'duration_s'`, runID)
	if err != nil {
		return nil, fmt.Errorf("read run aggregates: %w", err)
	}
	defer rows.Close()

	out := map[string]float64{}
	for rows.Next() {
		var category, strValue sql.NullString
		var valueID sql.NullInt64
		var value sql.NullFloat64

		if err := rows.Scan(&category, &valueID, &strValue, &value); err != nil {
			return nil, fmt.Errorf("scan run aggregate: %w", err)
		}

		// Prefer the numeric tag id; fall back to the label so a partially
		// resolved run is still legible.
		label := ""
		switch {
		case valueID.Valid:
			label = strconv.FormatInt(valueID.Int64, 10)
		case strValue.Valid:
			label = strValue.String
		}

		key := label
		if category.Valid && category.String != "" {
			key = category.String + ":" + label
		}
		out[key] = value.Float64
	}
	return out, rows.Err()
}

// ModelHistory lists the models that have analysed an entity, most recent first.
func (db *DB) ModelHistory(ctx context.Context, service, entityType string, entityID int) ([]AIModel, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT DISTINCT m.id, m.service, m.plugin_name, m.model_id, m.name,
		        m.version, m.model_type, m.categories, m.extra
		 FROM ai_models m
		 JOIN ai_model_run_models rm ON rm.model_id = m.id
		 JOIN ai_model_runs r ON r.id = rm.run_id
		 WHERE r.service = ? AND r.entity_type = ? AND r.entity_id = ?
		 ORDER BY m.id DESC`, service, entityType, entityID)
	if err != nil {
		return nil, fmt.Errorf("get model history: %w", err)
	}
	defer rows.Close()

	var out []AIModel
	for rows.Next() {
		var m AIModel
		var pluginName, modelType sql.NullString
		var modelID sql.NullInt64
		var version sql.NullFloat64
		var categories JSONText[[]string]
		var extra JSONText[map[string]any]

		if err := rows.Scan(&m.ID, &m.Service, &pluginName, &modelID, &m.Name,
			&version, &modelType, &categories, &extra); err != nil {
			return nil, fmt.Errorf("scan model: %w", err)
		}

		m.PluginName = StringPtr(pluginName)
		if modelID.Valid {
			v := int(modelID.Int64)
			m.ModelID = &v
		}
		m.Version = Float64Ptr(version)
		m.ModelType = StringPtr(modelType)
		m.Categories = categories.Data
		m.Extra = extra.Data
		out = append(out, m)
	}
	return out, rows.Err()
}

// PurgeSceneCategories removes stored results for a scene in the given
// categories, optionally sparing one run.
//
// Used when re-analysing with a different model: the old categories' results
// must go, or stale detections would merge with fresh ones.
func (db *DB) PurgeSceneCategories(ctx context.Context, service string, sceneID int, categories []string, excludeRunID *int64) error {
	named := make([]string, 0, len(categories))
	for _, c := range categories {
		if c != "" {
			named = append(named, c)
		}
	}
	if len(named) == 0 {
		return nil
	}
	sort.Strings(named)

	return db.InTx(ctx, func(tx *sql.Tx) error {
		runs := `SELECT id FROM ai_model_runs
		         WHERE service = ? AND entity_type = 'scene' AND entity_id = ?`
		args := []any{service, sceneID}
		if excludeRunID != nil {
			runs += ` AND id != ?`
			args = append(args, *excludeRunID)
		}
		for _, c := range named {
			args = append(args, c)
		}

		in := Placeholders(len(named))
		for _, table := range []string{"ai_result_aggregates", "ai_result_timespans"} {
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM `+table+` WHERE run_id IN (`+runs+`) AND category IN (`+in+`)`,
				args...); err != nil {
				return fmt.Errorf("purge %s: %w", table, err)
			}
		}
		return nil
	})
}

// DeleteRun removes a run and everything it produced.
//
// The child rows are deleted explicitly rather than relying on ON DELETE
// CASCADE: the foreign keys are declared for documentation, but this engine
// does not enforce them (verified in the Phase 0 spike).
func (db *DB) DeleteRun(ctx context.Context, runID int64) error {
	return db.InTx(ctx, func(tx *sql.Tx) error {
		for _, table := range []string{
			"ai_result_timespans", "ai_result_aggregates", "ai_model_run_models",
		} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE run_id = ?`, runID); err != nil {
				return fmt.Errorf("delete from %s: %w", table, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM ai_model_runs WHERE id = ?`, runID); err != nil {
			return fmt.Errorf("delete run: %w", err)
		}
		return nil
	})
}

// ------------------------------------------------------------------ utils ---

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func nonEmptyMap(m map[string]any) any {
	if len(m) == 0 {
		return nil
	}
	return m
}

func nullIfEmptyString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func intArg(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func int64Arg(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func floatArgValue(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func numberFrom(m map[string]any, key string) (float64, bool) {
	if m == nil {
		return 0, false
	}
	return floatFromAny(m[key])
}

func floatFromAny(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case string:
		f, err := strconv.ParseFloat(t, 64)
		return f, err == nil
	default:
		return 0, false
	}
}
