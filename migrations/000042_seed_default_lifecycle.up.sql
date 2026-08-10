-- Default values for the new incident lifecycle controls.  Operators can
-- override any of these in the SystemSettings UI without re-running migrations.
--
--   notification.repeat_schedule
--     JSON array describing the progressive re-notify ladder.  Each entry has:
--       after    — duration since opened_at when this rung becomes active
--       interval — minimum gap between two notifications while in this rung
--       tag      — title prefix put on the outgoing notification
--       escalate — when true, also bump severity one level (P3→P2 etc.)
--     Entries are evaluated bottom-up; the highest "after" still ≤ elapsed wins.
--
--   incident.staleness_timeout
--     Go duration.  When an open/ack incident has not received any new firing
--     alert for longer than this window, the staleness reaper auto-resolves
--     it and dispatches a [RESOLVED] notification.  Mirrors what Alertmanager
--     does internally when EndsAt is bumped silently into the past.
--
--   incident.reopen_window
--     Go duration.  Same group_key arriving within this window after a
--     resolved_at flips the incident back to open instead of creating a new
--     row.  Set to 0 to disable reopen entirely (every resolved + new alert
--     creates a new incident, like the pre-v2 behaviour).

INSERT INTO system_configs (key, value, description) VALUES
    ('notification.repeat_schedule',
     '[{"after":"0s","interval":"30m","tag":"[REPEAT]"},{"after":"3h","interval":"2h","tag":"[ATTENTION]","escalate":true},{"after":"24h","interval":"6h","tag":"[REPEAT]"}]',
     'JSON ladder for progressive re-notification of ongoing incidents.'),
    ('incident.staleness_timeout', '0',
     'Auto-resolve open incidents with no new firing alert for this duration. Set to 0 to disable; auto-resolve then requires an explicit recovery signal (e.g. Tencent Cloud alarmStatus=0 or Prometheus endsAt).'),
    ('incident.reopen_window', '5m',
     'Same group_key within this window after resolve reopens the old incident.')
ON CONFLICT (key) DO NOTHING;

-- Default escalation policies so a fresh install does in fact escalate
-- unacknowledged P3/P2/P1 incidents instead of silently sitting open.
-- ack_timeout is in seconds (matches the existing column type).
INSERT INTO escalation_policies (name, from_severity, to_severity, ack_timeout, is_enabled, description) VALUES
    ('默认升级 P3→P2', 'P3', 'P2', 3600,  true, '未确认满 1 小时自动升级到 P2'),
    ('默认升级 P2→P1', 'P2', 'P1', 7200,  true, '未确认满 2 小时自动升级到 P1'),
    ('默认升级 P1→P0', 'P1', 'P0', 14400, true, '未确认满 4 小时自动升级到 P0')
ON CONFLICT DO NOTHING;
