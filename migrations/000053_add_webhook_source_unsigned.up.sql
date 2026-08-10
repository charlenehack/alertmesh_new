-- Add is_unsigned flag to webhook_sources for sources that don't support
-- RFC 9421 signatures (e.g. Tencent Cloud, Alibaba Cloud webhooks).
-- These sources use the /api/v1/alerts/webhook-plain/{source} endpoint.
ALTER TABLE webhook_sources ADD COLUMN IF NOT EXISTS is_unsigned BOOLEAN NOT NULL DEFAULT FALSE;
