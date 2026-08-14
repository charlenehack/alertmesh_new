package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	restful "github.com/emicklei/go-restful/v3"
	"github.com/mikespook/gorbac"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"

	"github.com/kuzane/alertmesh/internal/ai"
	"github.com/kuzane/alertmesh/internal/auth"
	"github.com/kuzane/alertmesh/internal/config"
	"github.com/kuzane/alertmesh/internal/engine"
	"github.com/kuzane/alertmesh/internal/incident"
	"github.com/kuzane/alertmesh/internal/notification"
	"github.com/kuzane/alertmesh/internal/store"
	"github.com/kuzane/alertmesh/internal/sysconfig"
	"github.com/kuzane/alertmesh/pkg/metrics"
)

// ProvideDB opens the PostgreSQL connection, runs pending SQL migrations, and
// (as a side effect) initialises the optional Redis client.
func ProvideDB(cfg *config.Config) (*gorm.DB, error) {
	db, err := store.NewPostgres(cfg)
	if err != nil {
		return nil, err
	}
	if err := store.Migrate(db); err != nil {
		return nil, err
	}
	if err := store.NewRedis(cfg); err != nil {
		log.Warn().Err(err).Msg("redis init failed")
	}
	return db, nil
}

// ProvideSysConfig creates the sysconfig service, runs first-boot Bootstrap
// (generates JWT secret + RSA key pair when the DB is fresh), then loads the
// RSA private key into auth.InitRSAFromPEM so the login endpoint can decrypt
// browser-encrypted passwords.
func ProvideSysConfig(db *gorm.DB, cfg *config.Config) (*sysconfig.Service, error) {
	svc := sysconfig.NewService(db, cfg.EncryptionKey)
	if err := svc.Bootstrap(context.Background()); err != nil {
		return nil, fmt.Errorf("system bootstrap: %w", err)
	}

	privPEM, err := svc.RSAPrivateKeyPEM(context.Background())
	if err != nil {
		return nil, fmt.Errorf("load rsa private key from db: %w", err)
	}
	if err := auth.InitRSAFromPEM(privPEM); err != nil {
		return nil, fmt.Errorf("init rsa from pem: %w", err)
	}

	return svc, nil
}

// ProvideJWTService resolves the JWT signing secret and returns an
// auth.JWTService.
func ProvideJWTService(syscfg *sysconfig.Service, cfg *config.Config) (*auth.JWTService, error) {
	secret := cfg.JWTSecret
	if secret == "" {
		var err error
		secret, err = syscfg.JWTSecret(context.Background())
		if err != nil {
			return nil, fmt.Errorf("load jwt secret from db: %w", err)
		}
	}
	return auth.NewJWTService(secret, cfg.JWTExpiryHours), nil
}

// ProvideRBAC creates the in-memory RBAC graph and loads roles from the database.
func ProvideRBAC(db *gorm.DB) *gorbac.RBAC {
	rbac := gorbac.New()
	if err := auth.InitRBAC(rbac, db); err != nil {
		log.Warn().Err(err).Msg("rbac init failed, will retry on first request")
	}
	return rbac
}

// ProvideDispatcher creates the notification dispatcher.
func ProvideDispatcher(db *gorm.DB, cfg *config.Config) *notification.Dispatcher {
	return notification.NewDispatcher(db, cfg.EncryptionKey)
}

// ProvideIncidentService wires the incident service with its dispatcher and
// the master encryption key needed to decrypt contact secrets at dispatch time.
// rootCtx is threaded through so background dispatcher goroutines fired from
// HandleAlertGroup / reopenIncident / etc. respect SIGTERM-driven shutdown.
func ProvideIncidentService(rootCtx context.Context, db *gorm.DB, dispatcher *notification.Dispatcher, cfg *config.Config) *incident.Service {
	return incident.NewService(rootCtx, db, dispatcher, cfg.EncryptionKey)
}

// ProvideIncidentCallback extracts the HandleAlertGroup method as a plain
// function value so that Wire can inject it into engine.NewPipeline.
func ProvideIncidentCallback(svc *incident.Service) engine.IncidentCallback {
	return svc.HandleAlertGroup
}

// ProvideResolvedCallback wires svc.HandleResolvedAlert into engine.NewPipeline
// so that Prometheus endsAt-driven recovery signals reach the incident layer
// for immediate auto-resolution (see plan: incident lifecycle v2).
func ProvideResolvedCallback(svc *incident.Service) engine.ResolvedCallback {
	return svc.HandleResolvedAlert
}

// ProvideAIDispatchHook exposes incident.Service.DispatchAIFollowup as the
// hook the AI orchestrator calls after a successful analysis run.  Pulling
// it through the wire graph this way keeps the `internal/ai` package free
// of any direct dependency on `internal/incident`.
func ProvideAIDispatchHook(svc *incident.Service) ai.AnalysisDoneHook {
	return svc.DispatchAIFollowup
}

// ProvideHTTPServer builds the top-level http.Server.
func ProvideHTTPServer(cfg *config.Config, container *restful.Container) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/healthz", http.HandlerFunc(metrics.HealthHandler))
	mux.Handle("/metrics", metrics.Handler())
	mux.Handle("/", container)

	return &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.ServerPort),
		Handler:           mux,
		ReadHeaderTimeout: 30 * time.Second,  // 只限制 header 读取时间，body（文件上传）不受此限制
		WriteTimeout:      300 * time.Second, // 匹配 K8s AI 分析前端 300s 超时
		IdleTimeout:       300 * time.Second,
	}
}
