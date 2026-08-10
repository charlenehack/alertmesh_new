package router

import (
	"net/http"
	"path/filepath"

	restful "github.com/emicklei/go-restful/v3"
	"github.com/mikespook/gorbac"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"

	ai_pkg "github.com/kuzane/alertmesh/internal/ai"
	"github.com/kuzane/alertmesh/internal/auth"
	"github.com/kuzane/alertmesh/internal/config"
	"github.com/kuzane/alertmesh/internal/engine"
	"github.com/kuzane/alertmesh/internal/incident"
	"github.com/kuzane/alertmesh/internal/realtime"
	"github.com/kuzane/alertmesh/internal/router/middleware"
	"github.com/kuzane/alertmesh/internal/sysconfig"
)

// Setup creates and configures the go-restful Container with all routes and
// middleware.
func Setup(
	db *gorm.DB,
	rbac *gorbac.RBAC,
	pipeline *engine.Pipeline,
	incSvc *incident.Service,
	wsHub *ai_pkg.WSHub,
	rtHub *realtime.Hub,
	jwtSvc *auth.JWTService,
	syscfg *sysconfig.Service,
	cfg *config.Config,
) *restful.Container {
	container := restful.NewContainer()

	// CORS: 允许开发环境跨域直连（生产环境 Nginx 同域无需此 filter 生效）
	cors := restful.CrossOriginResourceSharing{
		AllowedHeaders: []string{"Content-Type", "Authorization"},
		AllowedMethods: []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions},
		AllowedDomains: []string{},   // 空 = 允许所有 Origin
		CookiesAllowed: false,
		Container:      container,
	}
	container.Filter(cors.Filter)

	// Global filters. Order matters: audit first so we record every attempt,
	// then the webhook-signature gate (no-op for non-AlertWebhook routes),
	// then JWT auth, then RBAC. The webhook filter must run BEFORE AuthFilter
	// because the trusted-webhook routes deliberately have no auth=true
	// metadata; their authentication is the RFC 9421 signature itself.
	container.Filter(middleware.NewAuditFilter(db))
	container.Filter(middleware.WebhookSignatureFilter(db))
	container.Filter(middleware.AuthFilter(jwtSvc))
	container.Filter(middleware.ACLFilter(rbac))

	ws := new(restful.WebService)
	ws.Path("/api/v1").
		Consumes(restful.MIME_JSON).
		Produces(restful.MIME_JSON)

	// Register route groups
	alertH := newAlertHandler(pipeline, db)
	alertH.registerRoutes(ws)

	incidentH := newIncidentHandler(incSvc)
	incidentH.registerRoutes(ws)

	aiH := newAIHandler(db, wsHub, cfg)
	aiH.registerRoutes(ws)

	systemH := newSystemHandler(db, jwtSvc, syscfg)
	systemH.registerRoutes(ws)

	alertCenterH := newAlertCenterHandler(db, cfg.EncryptionKey)
	alertCenterH.registerRoutes(ws)

	dataSourceH := newDataSourceHandler(db, cfg)
	dataSourceH.registerRoutes(ws)

	realtimeH := newRealtimeHandler(rtHub)
	realtimeH.registerRoutes(ws)

	// Nginx config handler
	nginxWorkDir := cfg.NginxWorkDir
	if absDir, err := filepath.Abs(nginxWorkDir); err == nil {
		nginxWorkDir = absDir
	}
	if err := ensureWorkDir(nginxWorkDir); err != nil {
		log.Warn().Err(err).Str("dir", nginxWorkDir).Msg("failed to create nginx work dir")
	}
	nginxH := newNginxHandler(nginxWorkDir, db, cfg)
	nginxH.registerRoutes(ws)

	k8sMgmtH := newK8sMgmtHandler(db, cfg)
	k8sMgmtH.registerRoutes(ws)

	aiReportH := newAIReportHandler(db, cfg)
	aiReportH.registerRoutes(ws)

	container.Add(ws)

	// Second WebService for the Alertmanager v2 wire-compatible endpoint.
	// Prometheus's notifier hard-codes the URL suffix `/api/v2/alerts` when
	// configured with `alerting.alertmanagers`, so we expose the route at
	// the exact same path Alertmanager itself uses.  The route inherits the
	// container-level filter chain (audit + signature + auth + ACL); since
	// it carries no acl=true / auth=true metadata, AuthFilter / ACLFilter
	// are no-ops for it — same trust model as /api/v1/alerts/alertmanager.
	wsV2 := new(restful.WebService)
	wsV2.Path("/api/v2").
		Consumes(restful.MIME_JSON).
		Produces(restful.MIME_JSON)
	alertH.registerV2Routes(wsV2)
	container.Add(wsV2)

	return container
}
