-- AI server baseline schema.
--
-- Collapses alembic revisions 0001_initial .. 0004 from the Python server into a
-- single baseline: there is no existing Go-side database to migrate from, so the
-- incremental history buys nothing.
--
-- Translation rules applied throughout (see the plan's Schema section):
--   * STRICT tables - the Postgres original relied on real types, and a
--     hand-written port is exactly where affinity mistakes creep in.
--   * DateTime  -> INTEGER holding unix MILLISECONDS. Every use is a range
--     comparison, MAX() or ordering, and the interactions pipeline compares at
--     sub-second precision. SQLite's CURRENT_TIMESTAMP is second-resolution text,
--     so there are deliberately no timestamp defaults: Go sets them explicitly.
--   * Float      -> REAL, String/Text/JSON -> TEXT, Boolean -> INTEGER.
--   * task_history.{submitted,started,finished}_at stay REAL epoch SECONDS,
--     because the TypeScript frontend reads them as numbers.
--   * Foreign-key enforcement is enabled on every production connection. The Go
--     deletion paths also remove children explicitly as defense in depth.

-- ---------------------------------------------------------------- plugins ---

CREATE TABLE plugin_meta (
    id               INTEGER PRIMARY KEY,
    name             TEXT NOT NULL UNIQUE,
    version          TEXT NOT NULL,
    required_backend TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'active',
    migration_head   TEXT,
    last_error       TEXT,
    human_name       TEXT,
    server_link      TEXT,
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL
) STRICT;

CREATE TABLE plugin_sources (
    id                INTEGER PRIMARY KEY,
    name              TEXT NOT NULL UNIQUE,
    url               TEXT NOT NULL,
    enabled           INTEGER NOT NULL DEFAULT 1,
    last_refreshed_at INTEGER,
    last_error        TEXT,
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL
) STRICT;

CREATE TABLE plugin_catalog (
    id                INTEGER PRIMARY KEY,
    source_id         INTEGER NOT NULL REFERENCES plugin_sources(id) ON DELETE CASCADE,
    plugin_name       TEXT NOT NULL,
    version           TEXT NOT NULL,
    description       TEXT,
    human_name        TEXT,
    server_link       TEXT,
    dependencies_json TEXT,
    manifest_json     TEXT,
    updated_at        INTEGER NOT NULL
) STRICT;
CREATE INDEX ix_plugin_catalog_source_id   ON plugin_catalog (source_id);
CREATE INDEX ix_plugin_catalog_plugin_name ON plugin_catalog (plugin_name);

CREATE TABLE plugin_settings (
    id            INTEGER PRIMARY KEY,
    plugin_name   TEXT NOT NULL,
    key           TEXT NOT NULL,
    type          TEXT NOT NULL DEFAULT 'string',
    label         TEXT,
    description   TEXT,
    default_value TEXT,
    options       TEXT,
    value         TEXT,
    updated_at    INTEGER NOT NULL,
    created_at    INTEGER NOT NULL
) STRICT;
CREATE INDEX ix_plugin_settings_plugin_name     ON plugin_settings (plugin_name);
CREATE INDEX ix_plugin_settings_plugin_name_key ON plugin_settings (plugin_name, key);

-- ------------------------------------------------------------ task history ---

-- NOTE: alembic 0001 declared task_id/action_id/item_id as INTEGER, but the ORM
-- (tasks/history.py) declares them String and the real values are uuid hex and
-- dotted action ids. The migration was simply wrong; TEXT is correct.
CREATE TABLE task_history (
    id           INTEGER PRIMARY KEY,
    task_id      TEXT NOT NULL UNIQUE,
    action_id    TEXT NOT NULL,
    service      TEXT NOT NULL,
    status       TEXT NOT NULL,
    started_at   REAL,
    finished_at  REAL,
    submitted_at REAL NOT NULL,
    duration_ms  INTEGER,
    items_sent   INTEGER,
    item_id      TEXT,
    error        TEXT,
    created_at   INTEGER NOT NULL
) STRICT;
CREATE INDEX ix_task_history_created_at             ON task_history (created_at);
CREATE INDEX ix_task_history_service_status_created ON task_history (service, status, created_at);

-- ----------------------------------------------------------- interactions ---

-- client_event_id is TEXT, folding in revision 0003 (ids like "evt_..." are not
-- integers; the Postgres migration cast the column with `USING`).
CREATE TABLE interaction_events (
    id              INTEGER PRIMARY KEY,
    client_event_id TEXT UNIQUE,
    session_id      TEXT NOT NULL,
    event_type      TEXT NOT NULL,
    entity_type     TEXT NOT NULL,
    entity_id       INTEGER NOT NULL,
    client_ts       INTEGER NOT NULL,
    metadata        TEXT
) STRICT;
CREATE INDEX ix_interaction_session_scene            ON interaction_events (session_id, entity_type, entity_id);
CREATE INDEX ix_interaction_client_ts                ON interaction_events (client_ts);
CREATE INDEX ix_interaction_events_event_type        ON interaction_events (event_type);
CREATE INDEX ix_interaction_events_entity_id         ON interaction_events (entity_id);
CREATE INDEX ix_interaction_events_session_entity_ts ON interaction_events (session_id, entity_type, entity_id, client_ts);

CREATE TABLE interaction_sessions (
    id                   INTEGER PRIMARY KEY,
    session_id           TEXT NOT NULL UNIQUE,
    last_event_ts        INTEGER NOT NULL,
    session_start_ts     INTEGER NOT NULL,
    last_entity_type     TEXT,
    last_entity_id       INTEGER,
    last_entity_event_ts INTEGER,
    updated_at           INTEGER NOT NULL,
    client_fingerprint   TEXT,
    ended_at             INTEGER
) STRICT;
CREATE INDEX ix_interaction_sessions_session_id         ON interaction_sessions (session_id);
CREATE INDEX ix_interaction_sessions_ended_at           ON interaction_sessions (ended_at);
CREATE INDEX ix_interaction_sessions_client_fingerprint ON interaction_sessions (client_fingerprint);
CREATE INDEX ix_interaction_sessions_fp_ended_last      ON interaction_sessions (client_fingerprint, ended_at, last_event_ts);

CREATE TABLE interaction_session_aliases (
    alias_session_id     TEXT PRIMARY KEY,
    canonical_session_id TEXT NOT NULL,
    created_at           INTEGER NOT NULL
) STRICT;
CREATE INDEX ix_interaction_session_aliases_canonical ON interaction_session_aliases (canonical_session_id);

CREATE TABLE scene_watch (
    id                      INTEGER PRIMARY KEY,
    session_id              TEXT NOT NULL,
    scene_id                INTEGER NOT NULL,
    page_entered_at         INTEGER NOT NULL,
    page_left_at            INTEGER,
    total_watched_s         REAL NOT NULL DEFAULT 0,
    watch_percent           REAL,
    -- Declared by the ORM and the original migration but never written by any
    -- code path, which is why the replay's `appendFast` branch is dead. Kept for
    -- schema fidelity.
    last_processed_event_ts INTEGER,
    created_at              INTEGER NOT NULL
) STRICT;
CREATE INDEX ix_scene_watch_session_id    ON scene_watch (session_id);
CREATE INDEX ix_scene_watch_scene_id      ON scene_watch (scene_id);
CREATE INDEX ix_scene_watch_session_scene ON scene_watch (session_id, scene_id);
CREATE INDEX ix_scene_watch_page_entered  ON scene_watch (page_entered_at);

CREATE TABLE scene_watch_segments (
    id             INTEGER PRIMARY KEY,
    scene_watch_id INTEGER NOT NULL,
    session_id     TEXT NOT NULL,
    scene_id       INTEGER NOT NULL,
    start_s        REAL NOT NULL,
    end_s          REAL NOT NULL,
    watched_s      REAL NOT NULL,
    created_at     INTEGER NOT NULL
) STRICT;
CREATE INDEX ix_scene_watch_segments_scene_watch_id   ON scene_watch_segments (scene_watch_id);
CREATE INDEX ix_scene_watch_segments_session_id       ON scene_watch_segments (session_id);
CREATE INDEX ix_scene_watch_segments_scene_id         ON scene_watch_segments (scene_id);
CREATE INDEX ix_scene_watch_segments_sess_scene_start ON scene_watch_segments (session_id, scene_id, start_s);
CREATE INDEX ix_scene_watch_segments_scene_start      ON scene_watch_segments (scene_id, start_s);

-- scene_id / image_id are the Stash entity ids, not autoincrementing surrogates.
CREATE TABLE scene_derived (
    scene_id       INTEGER PRIMARY KEY,
    last_viewed_at INTEGER,
    derived_o_count INTEGER NOT NULL DEFAULT 0,
    view_count     INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE TABLE image_derived (
    image_id       INTEGER PRIMARY KEY,
    last_viewed_at INTEGER,
    derived_o_count INTEGER NOT NULL DEFAULT 0,
    view_count     INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE TABLE interaction_library_search (
    id         INTEGER PRIMARY KEY,
    session_id TEXT NOT NULL,
    library    TEXT NOT NULL,
    query      TEXT,
    filters    TEXT,
    created_at INTEGER NOT NULL
) STRICT;
CREATE INDEX ix_interaction_library_search_session_id      ON interaction_library_search (session_id);
CREATE INDEX ix_interaction_library_search_library_created ON interaction_library_search (library, created_at);

-- ------------------------------------------------- AI models and results ---

CREATE TABLE ai_models (
    id          INTEGER PRIMARY KEY,
    service     TEXT NOT NULL,
    plugin_name TEXT,
    model_id    INTEGER,
    name        TEXT NOT NULL,
    version     REAL,
    model_type  TEXT,
    categories  TEXT,
    extra       TEXT,
    created_at  INTEGER NOT NULL,
    CONSTRAINT uq_ai_model_service_model_name UNIQUE (service, model_id, name)
) STRICT;
CREATE INDEX ix_ai_models_service ON ai_models (service);

CREATE TABLE ai_model_runs (
    id              INTEGER PRIMARY KEY,
    service         TEXT NOT NULL,
    plugin_name     TEXT,
    entity_type     TEXT NOT NULL,
    entity_id       INTEGER NOT NULL,
    status          TEXT NOT NULL DEFAULT 'completed',
    input_params    TEXT,
    started_at      INTEGER NOT NULL,
    completed_at    INTEGER,
    result_metadata TEXT
) STRICT;
CREATE INDEX ix_ai_model_runs_entity         ON ai_model_runs (entity_type, entity_id);
CREATE INDEX ix_ai_model_runs_service_entity ON ai_model_runs (service, entity_type, entity_id);

CREATE TABLE ai_model_run_models (
    id             INTEGER PRIMARY KEY,
    run_id         INTEGER NOT NULL REFERENCES ai_model_runs(id) ON DELETE CASCADE,
    model_id       INTEGER REFERENCES ai_models(id) ON DELETE SET NULL,
    input_params   TEXT,
    frame_interval REAL,
    created_at     INTEGER NOT NULL
) STRICT;
CREATE INDEX ix_ai_run_models_run   ON ai_model_run_models (run_id);
CREATE INDEX ix_ai_run_models_model ON ai_model_run_models (model_id);

CREATE TABLE ai_result_timespans (
    id           INTEGER PRIMARY KEY,
    run_id       INTEGER NOT NULL REFERENCES ai_model_runs(id) ON DELETE CASCADE,
    entity_type  TEXT NOT NULL,
    entity_id    INTEGER NOT NULL,
    payload_type TEXT NOT NULL,
    category     TEXT,
    str_value    TEXT,
    value_id     INTEGER,
    start_s      REAL NOT NULL,
    end_s        REAL,
    value_json   TEXT
) STRICT;
CREATE INDEX ix_ai_timespans_entity      ON ai_result_timespans (entity_type, entity_id);
CREATE INDEX ix_ai_timespans_run         ON ai_result_timespans (run_id);
CREATE INDEX ix_ai_timespans_payload     ON ai_result_timespans (payload_type, category, str_value);
CREATE INDEX ix_ai_timespans_start       ON ai_result_timespans (entity_type, entity_id, start_s);
CREATE INDEX ix_ai_timespans_run_payload ON ai_result_timespans (run_id, payload_type);

CREATE TABLE ai_result_aggregates (
    id           INTEGER PRIMARY KEY,
    run_id       INTEGER NOT NULL REFERENCES ai_model_runs(id) ON DELETE CASCADE,
    entity_type  TEXT NOT NULL,
    entity_id    INTEGER NOT NULL,
    payload_type TEXT NOT NULL,
    category     TEXT,
    str_value    TEXT,
    value_id     INTEGER,
    metric       TEXT NOT NULL,
    value_float  REAL,
    value_json   TEXT,
    created_at   INTEGER NOT NULL
) STRICT;
CREATE INDEX ix_ai_aggregates_entity            ON ai_result_aggregates (entity_type, entity_id);
CREATE INDEX ix_ai_aggregates_payload           ON ai_result_aggregates (payload_type, str_value, metric);
CREATE INDEX ix_ai_aggregates_run_payload_metric ON ai_result_aggregates (run_id, payload_type, metric);

-- ------------------------------------------------- recommendation prefs ---

CREATE TABLE recommendation_preferences (
    id             INTEGER PRIMARY KEY,
    context        TEXT NOT NULL,
    recommender_id TEXT NOT NULL,
    config         TEXT,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,
    CONSTRAINT uq_recommendation_preferences_context UNIQUE (context)
) STRICT;
