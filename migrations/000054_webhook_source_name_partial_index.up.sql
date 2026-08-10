-- Fix: name unique constraint should exclude soft-deleted rows so that
-- a deleted webhook source's name can be reused.
DROP INDEX IF EXISTS webhook_sources_name_key;
CREATE UNIQUE INDEX webhook_sources_name_key ON webhook_sources (name) WHERE deleted_at IS NULL;
