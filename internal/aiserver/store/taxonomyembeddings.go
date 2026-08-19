package store

import (
	"context"
	"database/sql"
	"fmt"
)

// StoredTaxonomyEmbedding is one cached taxonomy query vector.
type StoredTaxonomyEmbedding struct {
	StashID     string
	Model       string
	Dim         int
	ContentHash string
	Vector      []float32
}

// GetTaxonomyEmbeddings returns every cached taxonomy vector for one
// service/model pair, keyed by authoritative StashDB tag ID.
func (db *DB) GetTaxonomyEmbeddings(ctx context.Context, service, model string) (map[string]StoredTaxonomyEmbedding, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT stash_id, dim, content_hash, vector
		 FROM ai_taxonomy_embeddings
		 WHERE service = ? AND model = ?`, service, model)
	if err != nil {
		return nil, fmt.Errorf("get taxonomy embeddings: %w", err)
	}
	defer rows.Close()

	ret := make(map[string]StoredTaxonomyEmbedding)
	for rows.Next() {
		var item StoredTaxonomyEmbedding
		var blob []byte
		if err := rows.Scan(&item.StashID, &item.Dim, &item.ContentHash, &blob); err != nil {
			return nil, fmt.Errorf("scan taxonomy embedding: %w", err)
		}
		item.Model = model
		item.Vector = decodeFloat32s(blob)
		if item.Dim <= 0 || len(item.Vector) != item.Dim {
			return nil, fmt.Errorf("taxonomy embedding %q has %d values, want %d", item.StashID, len(item.Vector), item.Dim)
		}
		ret[item.StashID] = item
	}
	return ret, rows.Err()
}

// StoreTaxonomyEmbeddings atomically upserts a set of taxonomy vectors.
func (db *DB) StoreTaxonomyEmbeddings(ctx context.Context, service string, items []StoredTaxonomyEmbedding) error {
	if len(items) == 0 {
		return nil
	}
	return db.InTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO ai_taxonomy_embeddings
			     (service, stash_id, model, dim, content_hash, vector, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(service, stash_id, model) DO UPDATE SET
			     dim = excluded.dim,
			     content_hash = excluded.content_hash,
			     vector = excluded.vector,
			     created_at = excluded.created_at`)
		if err != nil {
			return fmt.Errorf("prepare taxonomy embedding upsert: %w", err)
		}
		defer stmt.Close()

		for _, item := range items {
			if item.StashID == "" || item.Model == "" || item.Dim <= 0 || len(item.Vector) != item.Dim {
				return fmt.Errorf("invalid taxonomy embedding for %q", item.StashID)
			}
			if _, err := stmt.ExecContext(ctx, service, item.StashID, item.Model, item.Dim,
				item.ContentHash, encodeFloat32s(item.Vector), NowMillis()); err != nil {
				return fmt.Errorf("store taxonomy embedding %q: %w", item.StashID, err)
			}
		}
		return nil
	})
}
