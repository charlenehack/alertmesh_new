CREATE TABLE IF NOT EXISTS ai_reports (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    period      VARCHAR(16)  NOT NULL,
    start_time  TIMESTAMPTZ  NOT NULL,
    end_time    TIMESTAMPTZ  NOT NULL,
    status      VARCHAR(16)  NOT NULL DEFAULT 'pending',
    report      TEXT         NOT NULL DEFAULT '',
    error       TEXT         NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_ai_reports_status     ON ai_reports (status);
CREATE INDEX IF NOT EXISTS idx_ai_reports_start_time ON ai_reports (start_time DESC);
CREATE INDEX IF NOT EXISTS idx_ai_reports_deleted_at ON ai_reports (deleted_at);
