package router

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	restful "github.com/emicklei/go-restful/v3"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"

	"github.com/kuzane/alertmesh/internal/engine"
	"github.com/kuzane/alertmesh/internal/httputil"
	"github.com/kuzane/alertmesh/internal/ingestion"
	"github.com/kuzane/alertmesh/internal/label"
	"github.com/kuzane/alertmesh/internal/model"
)

type alertHandler struct {
	pipeline *engine.Pipeline
	db       *gorm.DB
	adapters map[string]ingestion.Adapter
}

func newAlertHandler(pipeline *engine.Pipeline, db *gorm.DB) *alertHandler {
	h := &alertHandler{
		pipeline: pipeline,
		db:       db,
		adapters: make(map[string]ingestion.Adapter),
	}
	h.adapters["alertmanager"] = ingestion.NewAlertmanagerAdapter()
	h.adapters["cloud-rds"] = ingestion.NewCloudRDSAdapter()
	h.adapters["prometheus"] = ingestion.NewPrometheusAdapter()
	// "prometheus-v2" handles the bare JSON array shape Prometheus posts to
	// /api/v2/alerts when configured with `alerting.alertmanagers` (i.e.
	// alertmesh impersonating Alertmanager so no real Alertmanager is needed).
	h.adapters["prometheus-v2"] = ingestion.NewAlertmanagerV2Adapter()
	return h
}

func (h *alertHandler) registerRoutes(ws *restful.WebService) {
	// Alertmanager / Prometheus 直推：基础设施到基础设施调用，不走用户 JWT/RBAC，
	// 也不要求 RFC 9421 签名（Prometheus / Alertmanager 自身不会签名），
	// 仅依赖网络层（ingress allowlist / mTLS）做边界。
	ws.Route(ws.POST("/alerts/alertmanager").
		To(h.receiveAlertmanager).
		Doc("Receive Alertmanager webhook (no auth, infra-to-infra)").
		Metadata(label.MetaIdentity, label.AlertIngest).
		Metadata(label.MetaModule, label.AlertModuleName).
		Metadata(label.MetaKind, "Alert"))

	// Compatibility alias for the /api/v1/alerts/alertmanager endpoint —
	// accepts the same wrapped {status,alerts:[...]} JSON shape, NOT the
	// Prometheus Remote Write protobuf protocol.  Real Prometheus direct
	// push goes to /api/v2/alerts (registerV2Routes); see README §4.1.1.
	ws.Route(ws.POST("/alerts/prometheus/remote").
		To(h.receivePrometheus).
		Doc("Compatibility alias for /api/v1/alerts/alertmanager (NOT Prometheus Remote Write — for that use /api/v2/alerts)").
		Metadata(label.MetaIdentity, label.AlertIngest).
		Metadata(label.MetaModule, label.AlertModuleName).
		Metadata(label.MetaKind, "Alert"))

	// 通用 Webhook：第三方/外部系统调用，使用 HTTP Message Signatures (RFC 9421) +
	// 每个 source 独立的 Ed25519 keypair 校验，签名中间件在 router.Setup 里按
	// "kind == AlertWebhook" 单独挂载，因此这里不开启 user-JWT 的 auth 中间件。
	ws.Route(ws.POST("/alerts/webhook/{source}").
		To(h.receiveWebhook).
		Doc("Receive webhook alerts from any source (RFC 9421 Ed25519 per-source signature)").
		Metadata(label.MetaIdentity, label.AlertIngest).
		Metadata(label.MetaModule, label.AlertModuleName).
		Metadata(label.MetaKind, "AlertWebhook"))

	// 无签名 Webhook：复用 webhook_sources 表的 mapping 配置，但不要求 RFC 9421 签名。
	// 适用于腾讯云/阿里云等不能做签名的云平台，或自定义应用推送。
	// MetaKind 设为 "AlertWebhookPlain" 以绕过签名中间件。
	ws.Route(ws.POST("/alerts/webhook-plain/{source}").
		To(h.receiveWebhookPlain).
		Doc("Receive webhook alerts without signature (uses webhook_sources mapping)").
		Metadata(label.MetaIdentity, label.AlertIngest).
		Metadata(label.MetaModule, label.AlertModuleName).
		Metadata(label.MetaKind, "AlertWebhookPlain"))
}

// registerV2Routes wires the Alertmanager-v2-compatible endpoint.  It lives
// on a separate WebService (path /api/v2) so the URL Prometheus hard-codes
// to — `<host>/api/v2/alerts` — works without any rewrite layer.  Same trust
// model as /api/v1/alerts/alertmanager: infra-to-infra, no JWT, no RFC 9421.
//
// Wire shape: POST <host>/api/v2/alerts with a JSON ARRAY body — exactly
// what `prometheus.notifier` sends when it thinks it's talking to a real
// Alertmanager.  See internal/ingestion/alertmanager_v2.go for the schema.
func (h *alertHandler) registerV2Routes(ws *restful.WebService) {
	ws.Route(ws.POST("/alerts").
		To(h.receivePrometheusV2).
		Doc("Alertmanager v2 PostableAlerts API — Prometheus 直推（no real Alertmanager required）").
		Metadata(label.MetaIdentity, label.AlertIngest).
		Metadata(label.MetaModule, label.AlertModuleName).
		Metadata(label.MetaKind, "Alert"))
}

func (h *alertHandler) receivePrometheusV2(req *restful.Request, resp *restful.Response) {
	h.ingest(req, resp, "prometheus-v2")
}

func (h *alertHandler) receiveAlertmanager(req *restful.Request, resp *restful.Response) {
	h.ingest(req, resp, "alertmanager")
}

func (h *alertHandler) receivePrometheus(req *restful.Request, resp *restful.Response) {
	h.ingest(req, resp, "prometheus")
}

func (h *alertHandler) receiveWebhook(req *restful.Request, resp *restful.Response) {
	source := req.PathParameter("source")
	h.ingest(req, resp, source)
}

func (h *alertHandler) receiveWebhookPlain(req *restful.Request, resp *restful.Response) {
	source := req.PathParameter("source")
	h.ingest(req, resp, source)
}

func (h *alertHandler) ingest(req *restful.Request, resp *restful.Response, source string) {
	body, err := io.ReadAll(req.Request.Body)
	if err != nil {
		httputil.BadRequest(resp, "failed to read request body")
		return
	}
	defer func() { _ = req.Request.Body.Close() }()

	adapter, ok := h.adapters[source]
	if !ok && h.db != nil {
		dyn, err := h.webhookAdapterForSource(req.Request.Context(), source)
		if err == nil {
			adapter, ok = dyn, true
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			httputil.BadRequest(resp, err.Error())
			return
		}
	}
	if !ok {
		httputil.Error(resp, http.StatusUnprocessableEntity, "unknown alert source: "+source)
		return
	}

	alerts, err := adapter.Adapt(body)
	if err != nil {
		// Log a small body preview so an operator wiring up Prometheus can
		// see WHY their payload was rejected (most common cause: posting the
		// /api/v1/alerts/alertmanager wrapper shape `{status,alerts:[...]}`
		// to /api/v2/alerts, which expects the bare PostableAlerts array).
		log.Error().
			Err(err).
			Str("source", source).
			Str("path", req.Request.URL.Path).
			Int("body_bytes", len(body)).
			Str("body_preview", previewBody(body)).
			Msg("alert ingestion: adapter rejected payload")
		httputil.BadRequest(resp, err.Error())
		return
	}

	// One INFO line per ingestion call so operators can confirm Prometheus is
	// actually reaching the endpoint without sifting through audit logs.
	// Also emits when len(alerts)==0 (Prometheus's notifier sends empty
	// arrays at startup) so a misconfigured /api/v2/alerts that 404s shows
	// up clearly as an absence of these lines.
	log.Info().
		Str("component", "ingest").
		Str("source", source).
		Str("path", req.Request.URL.Path).
		Int("count", len(alerts)).
		Msg("alerts accepted")

	for _, alert := range alerts {
		h.pipeline.Process(alert)
	}

	httputil.Success(resp, map[string]int{"accepted": len(alerts)})
}

// previewBody returns at most the first 256 bytes of body, with newlines and
// tabs collapsed, so error logs stay one line per request.  Body is already
// JSON-only (Consumes(MIME_JSON)) so leaking 256 bytes can't include binary
// noise; this is the same trade-off Prometheus's own debug logging makes.
func previewBody(body []byte) string {
	const maxBytes = 256
	if len(body) > maxBytes {
		body = body[:maxBytes]
	}
	out := make([]byte, 0, len(body))
	for _, b := range body {
		switch b {
		case '\n', '\r', '\t':
			out = append(out, ' ')
		default:
			out = append(out, b)
		}
	}
	return string(out)
}

// webhookAdapterForSource builds a WebhookAdapter from webhook_sources.mapping
// when {source} matches an enabled row (RFC 9421 path parameter).
func (h *alertHandler) webhookAdapterForSource(ctx context.Context, source string) (ingestion.Adapter, error) {
	if h.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	var row model.WebhookSource
	err := h.db.WithContext(ctx).Where("name = ? AND is_enabled = ?", source, true).First(&row).Error
	if err != nil {
		return nil, err
	}
	m, jerr := ingestion.WebhookMappingFromJSON(row.Mapping)
	if jerr != nil {
		return nil, jerr
	}
	if strings.TrimSpace(m.AlertnamePath) == "" ||
		(strings.TrimSpace(m.SeverityPath) == "" && strings.TrimSpace(m.DefaultSeverity) == "") {
		return nil, fmt.Errorf("webhook source %q: mapping must set alertname_path and (severity_path or default_severity) (configure in 告警中心 → Webhook 可信源)", source)
	}
	return ingestion.NewWebhookAdapter(source, m), nil
}
