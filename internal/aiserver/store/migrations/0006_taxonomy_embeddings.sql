-- Cache Voyage text-query vectors for authoritative taxonomy entries.
CREATE TABLE ai_taxonomy_embeddings (
    id           INTEGER PRIMARY KEY,
    service      TEXT NOT NULL,
    stash_id     TEXT NOT NULL,
    model        TEXT NOT NULL,
    dim          INTEGER NOT NULL,
    content_hash TEXT NOT NULL,
    vector       BLOB NOT NULL,
    created_at   INTEGER NOT NULL
) STRICT;

CREATE UNIQUE INDEX ux_ai_taxonomy_embeddings
    ON ai_taxonomy_embeddings (service, stash_id, model);
