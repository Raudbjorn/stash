package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
)

// Marker writeback provenance and cached embeddings.
//
// The provenance table is what makes re-analysis safe to run: it records
// exactly which Stash markers this server created, so a second run can replace
// its own and leave a user's hand-placed markers alone. Without it the choice is
// between accumulating duplicates forever and deleting markers we did not make.

// WrittenMarker records one marker this server created in Stash.
type WrittenMarker struct {
	MarkerID int
	SceneID  int
	Service  string
	TagName  string
	Start    float64
	End      *float64
}

// RecordMarkers stores the provenance of markers just written to Stash.
func (db *DB) RecordMarkers(ctx context.Context, runID int64, markers []WrittenMarker) error {
	if len(markers) == 0 {
		return nil
	}

	now := NowMillis()
	return db.InTx(ctx, func(tx *sql.Tx) error {
		for _, m := range markers {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO ai_marker_writeback
				     (run_id, scene_id, marker_id, service, tag_name, start_s, end_s, created_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
				 ON CONFLICT(marker_id) DO UPDATE SET
				     run_id     = excluded.run_id,
				     scene_id   = excluded.scene_id,
				     service    = excluded.service,
				     tag_name   = excluded.tag_name,
				     start_s    = excluded.start_s,
				     end_s      = excluded.end_s,
				     created_at = excluded.created_at`,
				runID, m.SceneID, m.MarkerID, m.Service, m.TagName, m.Start, m.End, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// PreviousMarkers returns the marker ids this service previously created for a
// scene, so they can be removed before new ones are written.
func (db *DB) PreviousMarkers(ctx context.Context, service string, sceneID int) ([]int, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT marker_id FROM ai_marker_writeback
		 WHERE service = ? AND scene_id = ? ORDER BY marker_id`, service, sceneID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// GeneratedMarkerIDs returns every marker this server recorded for a scene,
// regardless of which provider created it. Human-truth consumers must use this
// broader query so one provider cannot learn from another provider's output.
func (db *DB) GeneratedMarkerIDs(ctx context.Context, sceneID int) ([]int, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT marker_id FROM ai_marker_writeback
		 WHERE scene_id = ? ORDER BY marker_id`, sceneID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ForgetMarkers drops provenance rows for markers no longer in Stash.
//
// Called after deleting them, and also when Stash reports one already gone: a
// user deleting a generated marker by hand must not make the next run fail or
// re-delete something that has since taken its id.
func (db *DB) ForgetMarkers(ctx context.Context, markerIDs []int) error {
	if len(markerIDs) == 0 {
		return nil
	}

	return db.InTx(ctx, func(tx *sql.Tx) error {
		for _, chunk := range ChunkInts(markerIDs, maxSQLParams) {
			args := make([]any, len(chunk))
			for i, id := range chunk {
				args[i] = id
			}
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM ai_marker_writeback WHERE marker_id IN (`+Placeholders(len(chunk))+`)`,
				args...); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---------------------------------------------------------- embeddings ------

// StoredEmbeddings is a scene's cached frame vectors.
type StoredEmbeddings struct {
	SceneID       int
	Model         string
	Dim           int
	FrameInterval float64
	Times         []float64
	SegmentStart  float64
	SegmentEnd    float64
	// Vectors is FrameCount*Dim values, frame-major.
	Vectors []float32
}

// FrameCount is how many frames were embedded.
func (e StoredEmbeddings) FrameCount() int {
	if e.Dim == 0 {
		return 0
	}
	return len(e.Vectors) / e.Dim
}

// StoreEmbeddings caches a scene's frame vectors, replacing any previous set.
func (db *DB) StoreEmbeddings(ctx context.Context, service string, in StoredEmbeddings) error {
	if in.Dim <= 0 || len(in.Vectors) == 0 {
		return nil
	}
	if len(in.Vectors)%in.Dim != 0 {
		return fmt.Errorf("embedding block is %d values, not a multiple of dim %d", len(in.Vectors), in.Dim)
	}

	times, err := json.Marshal(in.Times)
	if err != nil {
		return err
	}

	// A BLOB rather than JSON: 900 frames of 768 float32 is 2.7 MB raw and
	// roughly ten times that as text, which would have to be parsed on every
	// read.
	blob := encodeFloat32s(in.Vectors)
	segmentEnd := in.SegmentEnd
	if segmentEnd <= in.SegmentStart && len(in.Times) > 0 {
		segmentEnd = in.Times[len(in.Times)-1]
	}

	_, err = db.sql.ExecContext(ctx,
		`INSERT INTO ai_scene_embeddings
		     (service, scene_id, model, dim, frame_count, frame_interval, times, vectors,
		      segment_start, segment_end, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(service, scene_id, model, segment_start) DO UPDATE SET
		     dim            = excluded.dim,
		     frame_count    = excluded.frame_count,
		     frame_interval = excluded.frame_interval,
		     times          = excluded.times,
		     vectors        = excluded.vectors,
		     segment_end    = excluded.segment_end,
		     created_at     = excluded.created_at`,
		service, in.SceneID, in.Model, in.Dim, in.FrameCount(), in.FrameInterval,
		string(times), blob, in.SegmentStart, segmentEnd, NowMillis())
	return err
}

// GetEmbeddings returns a scene's cached vectors, or nil when there are none.
func (db *DB) GetEmbeddings(ctx context.Context, service string, sceneID int, model string) (*StoredEmbeddings, error) {
	var (
		out       StoredEmbeddings
		timesJSON string
		blob      []byte
	)

	err := db.sql.QueryRowContext(ctx,
		`SELECT dim, frame_interval, times, vectors, segment_start, segment_end
		 FROM ai_scene_embeddings
		 WHERE service = ? AND scene_id = ? AND model = ? AND segment_start = 0`,
		service, sceneID, model).Scan(
		&out.Dim, &out.FrameInterval, &timesJSON, &blob, &out.SegmentStart, &out.SegmentEnd,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal([]byte(timesJSON), &out.Times); err != nil {
		return nil, fmt.Errorf("decode embedding timestamps: %w", err)
	}
	out.SceneID = sceneID
	out.Model = model
	out.Vectors = decodeFloat32s(blob)
	return &out, nil
}

// EmbeddedScenes lists the scenes with cached vectors for a model.
//
// Used by the trainer, which needs the whole set and must not load it all into
// memory at once.
func (db *DB) EmbeddedScenes(ctx context.Context, service, model string) ([]int, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT DISTINCT scene_id FROM ai_scene_embeddings
		 WHERE service = ? AND model = ? ORDER BY scene_id`, service, model)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// GetEmbeddingSegments returns every cached segment for a scene/model.
func (db *DB) GetEmbeddingSegments(ctx context.Context, service string, sceneID int, model string) ([]StoredEmbeddings, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT dim, frame_interval, times, vectors, segment_start, segment_end
		 FROM ai_scene_embeddings
		 WHERE service = ? AND scene_id = ? AND model = ?
		 ORDER BY segment_start`, service, sceneID, model)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ret := make([]StoredEmbeddings, 0)
	for rows.Next() {
		var (
			item      StoredEmbeddings
			timesJSON string
			blob      []byte
		)
		if err := rows.Scan(
			&item.Dim,
			&item.FrameInterval,
			&timesJSON,
			&blob,
			&item.SegmentStart,
			&item.SegmentEnd,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(timesJSON), &item.Times); err != nil {
			return nil, fmt.Errorf("decode segment embedding timestamps: %w", err)
		}
		item.SceneID = sceneID
		item.Model = model
		item.Vectors = decodeFloat32s(blob)
		ret = append(ret, item)
	}
	return ret, rows.Err()
}

// DeleteEmbeddings removes a scene's cached vectors.
func (db *DB) DeleteEmbeddings(ctx context.Context, service string, sceneID int) error {
	_, err := db.sql.ExecContext(ctx,
		`DELETE FROM ai_scene_embeddings WHERE service = ? AND scene_id = ?`, service, sceneID)
	return err
}

// encodeFloat32s packs vectors as little-endian float32.
//
// Explicit rather than unsafe: the database file is read by other tools and
// possibly on another architecture, so the byte order is part of the format
// rather than whatever the compiling machine happened to use.
func encodeFloat32s(values []float32) []byte {
	out := make([]byte, len(values)*4)
	for i, v := range values {
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(v))
	}
	return out
}

func decodeFloat32s(blob []byte) []float32 {
	out := make([]float32, len(blob)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
	}
	return out
}

// ------------------------------------------------------- trained heads ------

// TrainedHead is a fitted multi-label classifier over embeddings.
type TrainedHead struct {
	Name       string
	EmbedModel string
	Dim        int
	Labels     []string
	// Weights is len(Labels) rows of Dim+1 values, bias last.
	Weights     []float32
	Metrics     map[string]any
	SampleCount int
}

// StoreTrainedHead saves a fitted head, replacing any of the same name.
func (db *DB) StoreTrainedHead(ctx context.Context, head TrainedHead) error {
	if head.Dim <= 0 || len(head.Labels) == 0 {
		return fmt.Errorf("a trained head needs a dimension and at least one label")
	}
	if len(head.Weights) != len(head.Labels)*(head.Dim+1) {
		return fmt.Errorf("weight block is %d values, expected %d",
			len(head.Weights), len(head.Labels)*(head.Dim+1))
	}

	labels, err := json.Marshal(head.Labels)
	if err != nil {
		return err
	}

	var metrics any
	if head.Metrics != nil {
		encoded, err := json.Marshal(head.Metrics)
		if err != nil {
			return err
		}
		metrics = string(encoded)
	}

	_, err = db.sql.ExecContext(ctx,
		`INSERT INTO ai_trained_heads
		     (name, embed_model, dim, labels, weights, metrics, trained_at, sample_count)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET
		     embed_model  = excluded.embed_model,
		     dim          = excluded.dim,
		     labels       = excluded.labels,
		     weights      = excluded.weights,
		     metrics      = excluded.metrics,
		     trained_at   = excluded.trained_at,
		     sample_count = excluded.sample_count`,
		head.Name, head.EmbedModel, head.Dim, string(labels),
		encodeFloat32s(head.Weights), metrics, NowMillis(), head.SampleCount)
	return err
}

// GetTrainedHead loads a head by name, or nil when it does not exist.
func (db *DB) GetTrainedHead(ctx context.Context, name string) (*TrainedHead, error) {
	var (
		head        TrainedHead
		labelsJSON  string
		metricsJSON sql.NullString
		blob        []byte
	)

	err := db.sql.QueryRowContext(ctx,
		`SELECT name, embed_model, dim, labels, weights, metrics, sample_count
		 FROM ai_trained_heads WHERE name = ?`, name).
		Scan(&head.Name, &head.EmbedModel, &head.Dim, &labelsJSON, &blob, &metricsJSON, &head.SampleCount)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal([]byte(labelsJSON), &head.Labels); err != nil {
		return nil, fmt.Errorf("decode head labels: %w", err)
	}
	if metricsJSON.Valid && metricsJSON.String != "" {
		if err := json.Unmarshal([]byte(metricsJSON.String), &head.Metrics); err != nil {
			return nil, fmt.Errorf("decode head metrics: %w", err)
		}
	}
	head.Weights = decodeFloat32s(blob)
	return &head, nil
}

// ListTrainedHeads names the stored heads, newest first.
func (db *DB) ListTrainedHeads(ctx context.Context) ([]TrainedHead, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT name, embed_model, dim, labels, metrics, sample_count
		 FROM ai_trained_heads ORDER BY trained_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []TrainedHead{}
	for rows.Next() {
		var (
			head        TrainedHead
			labelsJSON  string
			metricsJSON sql.NullString
		)
		if err := rows.Scan(&head.Name, &head.EmbedModel, &head.Dim,
			&labelsJSON, &metricsJSON, &head.SampleCount); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(labelsJSON), &head.Labels)
		if metricsJSON.Valid && metricsJSON.String != "" {
			_ = json.Unmarshal([]byte(metricsJSON.String), &head.Metrics)
		}
		// Weights are deliberately not loaded here: a list for the UI does not
		// need megabytes of them.
		out = append(out, head)
	}
	return out, rows.Err()
}
