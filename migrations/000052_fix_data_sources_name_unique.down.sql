DROP INDEX IF EXISTS uniq_data_sources_name_active;

ALTER TABLE data_sources ADD CONSTRAINT data_sources_name_key UNIQUE (name);
