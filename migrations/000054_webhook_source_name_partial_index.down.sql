-- Revert to the original non-partial unique constraint.
DROP INDEX IF EXISTS webhook_sources_name_key;
CREATE UNIQUE INDEX webhook_sources_name_key ON webhook_sources (name);
