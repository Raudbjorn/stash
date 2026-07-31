-- Marker writeback provenance, and cached frame embeddings.
--
-- Both belong to the AI database rather than Stash's: they are derived data
-- that can be regenerated, and keeping them out means Stash's own backup and
-- schema remain untouched by this feature.

-- Which markers this server created, so a re-run can replace exactly its own
-- and never a human's.
--
-- Without this the only options are "never clean up" (duplicate markers
-- accumulate on every re-run) or "delete every marker with a matching tag"
-- (which silently destroys markers the user placed by hand). Recording what we
-- created is the only version that is safe to run twice.
CREATE TABLE ai_marker_writeback (
    id         INTEGER PRIMARY KEY,
    run_id     INTEGER NOT NULL REFERENCES ai_model_runs(id) ON DELETE CASCADE,
    scene_id   INTEGER NOT NULL,
    -- marker_id is Stash's scene_markers.id. It lives in a different database,
    -- so no foreign key is possible and a marker deleted in Stash leaves a row
    -- here; the writeback tolerates that rather than assuming it cannot happen.
    marker_id  INTEGER NOT NULL,
    service    TEXT NOT NULL,
    tag_name   TEXT NOT NULL,
    start_s    REAL NOT NULL,
    end_s      REAL,
    created_at INTEGER NOT NULL
) STRICT;

CREATE INDEX ix_ai_marker_writeback_run     ON ai_marker_writeback (run_id);
CREATE INDEX ix_ai_marker_writeback_scene   ON ai_marker_writeback (service, scene_id);
CREATE UNIQUE INDEX ux_ai_marker_writeback_marker ON ai_marker_writeback (marker_id);

-- Per-frame embeddings, kept so the trained head and similarity search do not
-- have to re-run inference over the whole library every time they are tuned.
--
-- One row per scene rather than per frame: a 30-minute scene at one frame every
-- two seconds is 900 vectors, and 900 rows per scene would dominate the
-- database for no benefit - nothing ever queries a single frame's vector
-- without wanting its neighbours.
CREATE TABLE ai_scene_embeddings (
    id             INTEGER PRIMARY KEY,
    service        TEXT NOT NULL,
    scene_id       INTEGER NOT NULL,
    model          TEXT NOT NULL,
    dim            INTEGER NOT NULL,
    frame_count    INTEGER NOT NULL,
    frame_interval REAL NOT NULL,
    -- times is a JSON array of frame timestamps; vectors is the raw
    -- little-endian float32 block, frame-major. A BLOB rather than JSON because
    -- 900x768 floats as text would be roughly ten times the size and would have
    -- to be parsed on every read.
    times          TEXT NOT NULL,
    vectors        BLOB NOT NULL,
    created_at     INTEGER NOT NULL
) STRICT;

CREATE UNIQUE INDEX ux_ai_scene_embeddings ON ai_scene_embeddings (service, scene_id, model);

-- The trained tagging head: weights fitted from the user's own markers.
--
-- Stored rather than shipped. That is the whole point of the approach - no
-- weights are distributed, the model is fitted locally against the user's own
-- taxonomy, and it can be discarded and refitted at any time.
CREATE TABLE ai_trained_heads (
    id            INTEGER PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE,
    embed_model   TEXT NOT NULL,
    dim           INTEGER NOT NULL,
    -- labels is a JSON array naming each output, in weight order.
    labels        TEXT NOT NULL,
    -- weights is (len(labels) x (dim+1)) float32, row-major, bias last.
    weights       BLOB NOT NULL,
    -- metrics is a JSON object of training statistics, shown in the UI so the
    -- user can judge whether the head is worth using.
    metrics       TEXT,
    trained_at    INTEGER NOT NULL,
    sample_count  INTEGER NOT NULL
) STRICT;
