-- 将 data_sources.name 的唯一约束改为软删除感知：
-- 只有 deleted_at IS NULL 的记录才参与唯一性检查，允许同名记录在删除后重新创建。
ALTER TABLE data_sources DROP CONSTRAINT IF EXISTS data_sources_name_key;

CREATE UNIQUE INDEX IF NOT EXISTS uniq_data_sources_name_active
    ON data_sources (name)
    WHERE deleted_at IS NULL;
