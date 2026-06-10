package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/kuzane/alertmesh/internal/config"
	"github.com/kuzane/alertmesh/internal/incident"
	"github.com/kuzane/alertmesh/internal/ingestion"
	"github.com/kuzane/alertmesh/internal/realtime"
	"github.com/kuzane/alertmesh/internal/router"
	"github.com/kuzane/alertmesh/pkg/logger"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	if err := logger.Init(cfg.LogLevel); err != nil {
		fmt.Fprintf(os.Stderr, "failed to init logger: %v\n", err)
		os.Exit(1)
	}

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	app, err := InitApp(rootCtx, cfg)
	if err != nil {
		// log.Fatal calls os.Exit(1), which skips deferred functions —
		// release rootCtx explicitly first so we don't leak a goroutine
		// in the (rare) wire-failed-to-init code path.
		rootCancel()
		log.Fatal().Err(err).Msg("failed to initialize app") //nolint:gocritic // explicit pre-cancel above
	}

	app.Orchestrator.StartWorkerPool(rootCtx)

	app.Pipeline.StartReloadListener(rootCtx)

	// Realtime fan-out: LISTEN incident_event on a dedicated PG conn
	// and broadcast each notification to every WebSocket subscriber on
	// the matching topic.  This is what feeds the IncidentList /
	// Dashboard / IncidentDetail pages now that they no longer poll.
	realtime.Start(rootCtx, app.DB, app.RealtimeHub)

	// Background reaper that auto-resolves open incidents whose last firing
	// alert is older than incident.staleness_timeout.  Complements the
	// Prometheus endsAt-driven path inside engine.Pipeline.Process for
	// data sources (Kafka / OpenSearch / generic webhook) that do not
	// emit an explicit recovery signal.
	incident.StartStalenessReaper(rootCtx, app.DB, app.IncidentService)

	// Kafka consumer fleet — one Reader per enabled data_sources row of
	// kind=kafka.  Pure DB-driven: 表里有行就启 Reader，没行就空跑（PG
	// LISTEN + 5 分钟 reload floor 的固定开销可忽略）。无 env 开关；
	// alertmesh 仅作 consumer 角色，没有 producer / sink 入口。
	ingestion.StartKafka(rootCtx, cfg, app.DB, app.Pipeline.Process)
	if cfg.K8sEnabled {
		go ingestion.StartK8sInformer(cfg, app.Pipeline.Process)
	}

	go func() {
		log.Info().
			Str("addr", app.Server.Addr).
			Str("health", app.Server.Addr+"/healthz").
			Str("metrics", app.Server.Addr+"/metrics").
			Msg("alertmesh started")
		if err := app.Server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal().Err(err).Msg("http server error")
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	log.Info().Str("signal", sig.String()).Msg("shutting down")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	app.Orchestrator.Stop()
	app.Pipeline.Stop()
	rootCancel()

	// Gracefully close K8s Informer Watch connections to avoid
	// lingering connections on the K8s API server.
	router.ShutdownK8sCache(ctx)

	if err := app.Server.Shutdown(ctx); err != nil {
		log.Error().Err(err).Msg("server shutdown error")
	}

	log.Info().Msg("alertmesh stopped")
}
