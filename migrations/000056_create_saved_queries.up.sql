CREATE TABLE IF NOT EXISTS saved_queries (
    id                UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    name              VARCHAR(255) NOT NULL,
    data_source_kind  VARCHAR(32)  NOT NULL,
    data_source_id    UUID,
    natural_language  TEXT         NOT NULL DEFAULT '',
    query_text        TEXT         NOT NULL DEFAULT '',
    is_shared         BOOLEAN      NOT NULL DEFAULT false,
    created_by        UUID,
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_saved_queries_kind      ON saved_queries (data_source_kind);
CREATE INDEX IF NOT EXISTS idx_saved_queries_ds_id     ON saved_queries (data_source_id);
CREATE INDEX IF NOT EXISTS idx_saved_queries_created_by ON saved_queries (created_by);
CREATE INDEX IF NOT EXISTS idx_saved_queries_deleted_at ON saved_queries (deleted_at);
