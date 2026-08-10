export interface ApiResponse<T> {
  code: number
  message: string
  data: T
}

export interface PagedData<T> {
  items: T[]
  total: number
}

// ─── Incident ────────────────────────────────────────────────────────────────

export type Severity = 'P0' | 'P1' | 'P2' | 'P3'
export type IncidentStatus = 'open' | 'ack' | 'in_progress' | 'resolved' | 'closed'
// `disabled` means the originating data source isn't on the AI whitelist
// (only kafka / opensearch / elastic with ai_enabled=true qualify) — frontend uses
// it to hide the AI tab and trigger button on the incident detail page.
export type AIStatus = 'pending' | 'running' | 'done' | 'failed' | 'disabled'

export interface Incident {
  id: string
  title: string
  severity: Severity
  status: IncidentStatus
  source: string
  labels: Record<string, string>
  group_key: string
  assignee_id: string | null
  opened_at: string
  acked_at: string | null
  resolved_at: string | null
  // ── lifecycle v2 fields (migration 000041) ────────────────────────────
  // last_alert_at        — bumped on every firing alert that lands in this
  //                        incident; staleness reaper uses it to decide
  //                        which open incidents have gone silent.
  // parent_incident_id   — when this incident is a continuation of an
  //                        older one whose reopen window had already
  //                        closed, points at the previous row so the UI
  //                        can render a "延续自 #xxx" link.
  // auto_resolved_at     — set by HandleResolvedAlert (Prometheus endsAt
  //                        signal) or StartStalenessReaper (no firing
  //                        alert within incident.staleness_timeout).
  // notification_count   — total successful dispatches (initial + repeats
  //                        + escalations + reopened).
  // last_notified_at     — gates the progressive repeat schedule on the
  //                        backend; surfaced in the UI for context.
  last_alert_at?: string | null
  parent_incident_id?: string | null
  auto_resolved_at?: string | null
  notification_count?: number
  last_notified_at?: string | null
  ai_status: AIStatus
  ai_report_id: string | null
  alerts?: Alert[]
  timeline?: IncidentTimeline[]
  created_at: string
  updated_at: string
}

// ─── Alert ───────────────────────────────────────────────────────────────────

export type AlertStatus = 'firing' | 'resolved'

export interface Alert {
  id: string
  incident_id: string
  source: string
  fingerprint: string
  labels: Record<string, string>
  annotations: Record<string, string>
  starts_at: string
  ends_at: string | null
  status: AlertStatus
  created_at: string
  // Original payload (model.Alert.RawPayload, []byte → base64 on the wire
  // because the Go field is plain []byte rather than json.RawMessage).
  // Optional + nullable so legacy rows without one don't break the FE.
  raw_payload?: string | null
}

// ─── Timeline ────────────────────────────────────────────────────────────────

export interface IncidentTimeline {
  id: string
  incident_id: string
  // Mirrors model.IncidentTimeline.Action — discriminator for UI rendering.
  // Known values: created, acked, assigned, commented, resolved, closed,
  //   escalated, ai_triggered, ai_notified, auto_resolved, reopened.
  action: string
  from_status?: string
  to_status?: string
  user_id?: string
  username?: string
  message?: string
  created_at: string
}

// ─── User ────────────────────────────────────────────────────────────────────

export interface Role {
  id: number
  name: string
  description?: string
  status?: boolean
  parents?: string[]
  endpoints?: Endpoint[]
}

export interface Endpoint {
  path: string
  method: string
  module: string
  kind: string
  identity: string
  remark: string
}

export interface User {
  id: string
  username: string
  email: string
  display_name: string
  source: string
  is_active: boolean
  roles?: Role[]
}

export interface UserInfo {
  username: string
  roles: string[]
  permissions: string[]
}

// ─── Oncall ──────────────────────────────────────────────────────────────────

export interface OncallSchedule {
  id: string
  user_id: string
  start_time: string
  end_time: string
  created_at: string
}

// ─── System Config ───────────────────────────────────────────────────────────

export interface SystemConfig {
  key: string
  value: string
  description?: string
}

export interface AuthConfig {
  mode: 'local' | 'ldap' | 'oidc'
  config?: string
}

// ─── Alert Center ─────────────────────────────────────────────────────────────

export type ChannelType = 'dingtalk' | 'feishu' | 'slack' | 'email' | 'webhook'

// ─── Notification Policy (通知策略) ───────────────────────────────────────────

export interface NotificationPolicy {
  id: string
  name: string
  severities: Severity[]
  description: string
  contact_ids: string[]
  group_ids: string[]
  is_enabled: boolean
  linked_rules: number   // computed: how many alert_routes reference this policy
  created_at: string
  updated_at: string
}

export interface NotificationContact {
  id: string
  name: string
  email: string
  phone: string
  webhook_url: string
  webhook_token: string       // encrypted
  slack_bot_token: string     // encrypted
  slack_channel_id: string
  feishu_webhook: string
  feishu_secret: string       // encrypted
  dingtalk_webhook: string
  dingtalk_secret: string     // encrypted
  created_at: string
}

export interface NotificationContactGroup {
  id: string
  name: string
  description: string
  contact_ids: string[]
  created_at: string
}

export interface LabelMatcher {
  key: string
  op: '=' | '!=' | '=~' | '!~'
  value: string
}

export interface NotificationTemplate {
  id: string
  name: string
  channel_type: ChannelType
  subject: string
  body: string
  is_default: boolean
  description: string
  created_at: string
}

export interface AlertRoute {
  id: string
  name: string
  priority: number
  matchers: LabelMatcher[]
  group_by: string[]
  channel_ids: string[]
  is_enabled: boolean
  description: string
  created_at: string
}

export interface AggregationPolicy {
  id: string
  name: string
  matchers: LabelMatcher[]
  group_by: string[]
  group_wait: number
  group_interval: number
  repeat_interval: number
  is_enabled: boolean
  description: string
  created_at: string
}

export interface SilencePolicy {
  id: string
  name: string
  comment: string
  matchers: LabelMatcher[]
  starts_at: string
  ends_at: string
  created_by: string
  is_active: boolean
  created_at: string
}

// ─── Webhook Source (RFC 9421 trusted alert source) ───────────────────────────

/** gjson paths into the signed webhook JSON → RawAlert (see docs/log-alert-denoising.md). */
export interface WebhookPayloadMapping {
  alertname_path: string
  severity_path?: string
  default_severity?: string
  service_path?: string
  description_path?: string
  summary_path?: string
  starts_at_path?: string
  fingerprint_path?: string
  label_paths?: Record<string, string>
  status_path?: string
  status_firing_value?: string
  status_resolved_value?: string
}

export interface WebhookSource {
  id: string
  name: string
  client_id?: string
  public_key?: string
  allow_skew: number
  is_enabled: boolean
  is_unsigned?: boolean
  description?: string
  /** Body → RawAlert field paths (OpenSearch/Elastic/Kibana alerting, etc.). */
  mapping?: WebhookPayloadMapping
  last_used_at?: string | null
  created_at: string
  updated_at: string
}

/**
 * Returned ONLY by createWebhookSource and rotateWebhookSourceKey.
 * `private_key_pem` is shown to the user once and never persisted server-side;
 * the UI must surface it in a one-time reveal modal with a clear warning.
 */
export interface WebhookSourceCreated extends WebhookSource {
  private_key_pem: string
}

// ─── LLM Provider (AI 大模型供应商配置, admin-only) ───────────────────────────
//
// `api_key` is always returned masked as "******" by the backend so the raw
// secret never reaches the browser.  When editing, leaving the field blank
// or as the placeholder keeps the existing ciphertext on the server.
export type LLMProviderKind = 'openai' | 'azure' | 'ollama' | 'anthropic' | 'deepseek' | string

// Output language directive for the AI agent.
//   zh   – always 简体中文 (default)
//   en   – always English
//   auto – follow the language of the incident / question
export type LLMProviderLanguage = 'zh' | 'en' | 'auto'

export interface LLMProvider {
  id: string
  name: string
  provider: LLMProviderKind
  base_url: string
  api_key: string
  model: string
  temperature: number
  is_default: boolean
  is_enabled: boolean
  // AI behaviour knobs (migration 33). 0 ⇒ use server-side default.
  language: LLMProviderLanguage
  chat_report_max_chars: number
  chat_history_max_turns: number
  created_at: string
  updated_at: string
}

export interface LLMProviderTestResult {
  ok: boolean
  model: string
  sample: string
}

// ─── Data Source (Prometheus / Kafka / OpenSearch / K8s, admin-only) ─────────
//
// Mirrors `internal/model/data_source.go`.  Two-column secret split:
//   - `config` (jsonb)  – non-secret per-kind structured config; ALWAYS visible.
//   - `secret_keys` []  – names of the secrets currently populated server-side.
//                         The actual ciphertext NEVER reaches the browser.
// On create/update the UI sends a `secrets: { token: "ENC:…", … }` map; blank
// or "******" entries mean "keep existing".

export type DataSourceKind = 'prometheus' | 'k8s' | 'opensearch' | 'kafka' | 'elastic'

// Kubernetes detector toggles surfaced as checkboxes in the drawer.  These
// mirror the closed enum in router/data_sources.go::allowedK8sEvents — the
// future K8s connector reads `config.events[]` to decide which informers to
// wire up.
export type K8sEventKind =
  | 'pod_restart'
  | 'pod_pending'
  | 'hpa_scale'
  | 'node_not_ready'
  | 'failed_scheduling'

export interface DataSource {
  id: string
  name: string
  kind: DataSourceKind
  description: string
  is_enabled: boolean
  is_default: boolean
  // Log-shaped sources only: allows manual "触发 AI 分析" on incidents.
  ai_enabled: boolean
  // When true with ai_enabled: enqueue AI on new incident create (default false).
  ai_auto_trigger: boolean
  endpoint: string
  config: Record<string, unknown>
  secret_keys: string[]            // which secret names are populated server-side
  last_error?: string | null
  last_test_at?: string | null
  last_test_ok?: boolean | null
  created_at: string
  updated_at: string
}

export interface DataSourceTestResult {
  ok: boolean
  message: string
  detail?: unknown
}

// ─── Prometheus query proxy response shape (mirrors Prometheus's HTTP API) ───
// Only the bits we actually render in the Explore page are typed here; the
// rest is left as `unknown` so we don't have to fight Prometheus's somewhat
// loose schema for vector vs matrix vs scalar responses.
export interface PromQueryResponse {
  status: 'success' | 'error'
  data?: {
    resultType: 'matrix' | 'vector' | 'scalar' | 'string'
    result: unknown
  }
  errorType?: string
  error?: string
}
