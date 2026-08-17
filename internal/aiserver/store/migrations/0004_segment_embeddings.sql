-- Rebuild the embedding cache for multiple independently-addressable segments.
ALTER TABLE ai_scene_embeddings RENAME TO ai_scene_embeddings_legacy;
DROP INDEX ux_ai_scene_embeddings;

CREATE TABLE ai_scene_embeddings (
    id             INTEGER PRIMARY KEY,
    service        TEXT NOT NULL,
    scene_id       INTEGER NOT NULL,
    model          TEXT NOT NULL,
    dim            INTEGER NOT NULL,
    frame_count    INTEGER NOT NULL,
    frame_interval REAL NOT NULL,
    times          TEXT NOT NULL,
    vectors        BLOB NOT NULL,
    segment_start  REAL NOT NULL DEFAULT 0,
    segment_end    REAL NOT NULL DEFAULT 0,
    created_at     INTEGER NOT NULL
) STRICT;

INSERT INTO ai_scene_embeddings
    (id, service, scene_id, model, dim, frame_count, frame_interval, times,
     vectors, segment_start, segment_end, created_at)
SELECT id, service, scene_id, model, dim, frame_count, frame_interval, times,
       vectors,
       COALESCE(json_extract(times, '$[0]'), 0),
       COALESCE(json_extract(times, '$[#-1]'), 0),
       created_at
FROM ai_scene_embeddings_legacy;

DROP TABLE ai_scene_embeddings_legacy;
CREATE UNIQUE INDEX ux_ai_scene_embeddings
    ON ai_scene_embeddings (service, scene_id, model, segment_start);
