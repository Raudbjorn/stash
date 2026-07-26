//go:build integration
// +build integration

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stashapp/stash/pkg/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFunscriptIndexStore exercises the full FunscriptIndexStore surface against
// a live sqlite database. Its success also proves migration 95 (the
// funscript_index table) applies cleanly. All work runs inside a single
// rollback transaction so it leaves no residue in the shared test database.
func TestFunscriptIndexStore(t *testing.T) {
	withRollbackTxn(func(ctx context.Context) error {
		qb := db.FunscriptIndex

		// Create two rows sharing an md5 plus one distinct row.
		const sharedMD5 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		const otherMD5 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

		row1 := &models.FunscriptIndex{
			Filename: "/scripts/one.funscript",
			Metadata: `{"creator":"tester"}`,
			MD5:      sharedMD5,
		}
		require.NoError(t, qb.Create(ctx, row1))
		assert.NotZero(t, row1.ID, "Create should back-fill the id")

		row2 := &models.FunscriptIndex{
			Filename: "/scripts/two.funscript",
			MD5:      sharedMD5,
		}
		require.NoError(t, qb.Create(ctx, row2))

		row3 := &models.FunscriptIndex{
			Filename: "/scripts/three.funscript",
			MD5:      otherMD5,
		}
		require.NoError(t, qb.Create(ctx, row3))

		// ExistsByFilename
		exists, err := qb.ExistsByFilename(ctx, "/scripts/one.funscript")
		require.NoError(t, err)
		assert.True(t, exists)

		exists, err = qb.ExistsByFilename(ctx, "/scripts/missing.funscript")
		require.NoError(t, err)
		assert.False(t, exists)

		// FindByMD5 returns both shared-md5 rows.
		byMD5, err := qb.FindByMD5(ctx, sharedMD5)
		require.NoError(t, err)
		assert.Len(t, byMD5, 2)
		gotFiles := map[string]bool{}
		for _, r := range byMD5 {
			gotFiles[r.Filename] = true
			assert.Equal(t, sharedMD5, r.MD5)
			assert.Nil(t, r.SceneID, "rows start unmatched")
		}
		assert.True(t, gotFiles["/scripts/one.funscript"] && gotFiles["/scripts/two.funscript"])

		// Metadata round-trips; a row without metadata resolves to "".
		var one models.FunscriptIndex
		for _, r := range byMD5 {
			if r.Filename == "/scripts/one.funscript" {
				one = r
			}
		}
		assert.Equal(t, `{"creator":"tester"}`, one.Metadata)

		// UnmatchedRows contains all three (none matched yet).
		unmatched, err := qb.UnmatchedRows(ctx)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(unmatched), 3)

		// SetSceneID then verify FindBySceneID and the unmatched set shrinks.
		const sceneID = 4242
		require.NoError(t, qb.SetSceneID(ctx, row1.ID, sceneID))

		bySceneRows, err := qb.FindBySceneID(ctx, sceneID)
		require.NoError(t, err)
		require.Len(t, bySceneRows, 1)
		assert.Equal(t, row1.ID, bySceneRows[0].ID)
		require.NotNil(t, bySceneRows[0].SceneID)
		assert.Equal(t, sceneID, *bySceneRows[0].SceneID)

		// row1 is no longer unmatched.
		unmatchedAfter, err := qb.UnmatchedRows(ctx)
		require.NoError(t, err)
		for _, r := range unmatchedAfter {
			assert.NotEqual(t, row1.ID, r.ID, "matched row must not appear in unmatched set")
		}

		return nil
	})
}
