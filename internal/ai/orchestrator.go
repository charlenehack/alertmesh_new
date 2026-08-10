package ai

import (
	"context"
	"encoding/json"
	"time"

	"github.com/rs/zerolog/log"
	"gorm.io/gorm"

	"github.com/kuzane/alertmesh/internal/config"
	"github.com/kuzane/alertmesh/internal/model"
	"github.com/kuzane/alertmesh/pkg/metrics"
)

// AnalysisDoneHook is invoked once an AI analysis row has been persisted and
// the incident.ai_status flipped to `done`.  We use a function type instead
// of pulling in `incident.Service` directly to avoid a circular import
// (incident already imports notification, which is fine; ai must stay
// dependency-free of incident).  The wired-in implementation is
// incident.Service.DispatchAIFollowup.
type AnalysisDoneHook func(ctx context.Context, incidentID string)

// Orchestrator manages the AI worker goroutine pool.
type Orchestrator struct {
	db             *gorm.DB
	cfg            *config.Config
	wsHub          *WSHub
	workers        int
	onAnalysisDone AnalysisDoneHook
	cancel         context.CancelFunc
	// rootCtx is the application-wide context (typically the one
	// cancelled by main on SIGTERM).  Used as the parent for the
	// fire-and-forget AI follow-up dispatch goroutine so a graceful
	// shutdown actually cancels in-flight work instead of leaking it.
	rootCtx context.Context
}

// NewOrchestrator builds the worker pool.  rootCtx must be the long-lived
// application context (typically the one cancelled by main on SIGTERM) so
// the fire-and-forget AI follow-up dispatch goroutine respects graceful
// shutdown.  `hook` may be nil during tests or when AI follow-up
// notifications are intentionally disabled — the workers will then only
// persist the analysis and broadcast it via WebSocket.
func NewOrchestrator(rootCtx context.Context, db *gorm.DB, cfg *config.Config, wsHub *WSHub, hook AnalysisDoneHook) *Orchestrator {
	if rootCtx == nil {
		rootCtx = context.Background()
	}
	return &Orchestrator{
		db:             db,
		cfg:            cfg,
		wsHub:          wsHub,
		workers:        cfg.AIWorkers,
		onAnalysisDone: hook,
		rootCtx:        rootCtx,
	}
}

// StartWorkerPool launches the configured number of AI worker goroutines.
func (o *Orchestrator) StartWorkerPool(ctx context.Context) {
	ctx, o.cancel = context.WithCancel(ctx)

	for i := 0; i < o.workers; i++ {
		go o.worker(ctx, i)
	}

	log.Info().Int("workers", o.workers).Msg("ai worker pool started")
}

// Stop shuts down all workers.
func (o *Orchestrator) Stop() {
	if o.cancel != nil {
		o.cancel()
	}
}

func (o *Orchestrator) worker(ctx context.Context, id int) {
	workerLog := log.With().Int("worker_id", id).Str("component", "ai_worker").Logger()
	workerLog.Info().Msg("worker started")

	// Try PG LISTEN/NOTIFY for real-time push, fall back to polling
	notifyCh := o.setupPGNotify(ctx, id)

	pollTicker := time.NewTicker(10 * time.Second)
	defer pollTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			workerLog.Info().Msg("worker stopped")
			return
		case <-notifyCh:
			o.processNextTask(ctx, workerLog)
		case <-pollTicker.C:
			o.processNextTask(ctx, workerLog)
		}
	}
}

// setupPGNotify sets up a PG LISTEN connection for real-time task notifications.
func (o *Orchestrator) setupPGNotify(ctx context.Context, workerID int) <-chan struct{} {
	ch := make(chan struct{}, 1)

	go func() {
		for {
			if ctx.Err() != nil {
				return
			}
			if err := o.listenPG(ctx, ch); err != nil {
				log.Warn().Err(err).Int("worker_id", workerID).Msg("PG LISTEN error, will retry")
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
				}
			}
		}
	}()

	return ch
}

func (o *Orchestrator) listenPG(ctx context.Context, ch chan<- struct{}) error {
	sqlDB, err := o.db.DB()
	if err != nil {
		return err
	}

	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, "LISTEN ai_task_ready"); err != nil {
		return err
	}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Use a raw connection to wait for notifications
		err := conn.Raw(func(driverConn any) error {
			// pgx doesn't expose WaitForNotification on the stdlib adapter,
			// so we poll with a short timeout
			if pgConn, ok := driverConn.(interface {
				WaitForNotification(ctx context.Context) error
			}); ok {
				waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				return pgConn.WaitForNotification(waitCtx)
			}
			// Fallback: sleep briefly
			time.Sleep(2 * time.Second)
			return nil
		})

		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			// Timeout is expected, just continue
			continue
		}

		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (o *Orchestrator) processNextTask(ctx context.Context, _ interface{}) {
	var task model.AITask

	// SELECT FOR UPDATE SKIP LOCKED ensures only one worker picks up each task
	err := o.db.WithContext(ctx).Raw(`
		SELECT * FROM ai_tasks
		WHERE status = ? AND deleted_at IS NULL
		ORDER BY priority DESC, created_at ASC
		LIMIT 1
		FOR UPDATE SKIP LOCKED`,
		model.AIStatusPending,
	).Scan(&task).Error

	if err != nil || task.ID == "" {
		return
	}

	now := time.Now()
	o.db.Model(&task).Updates(map[string]interface{}{
		"status":     model.AIStatusRunning,
		"started_at": &now,
	})

	o.db.Model(&model.Incident{}).
		Where("id = ?", task.IncidentID).
		Update("ai_status", model.AIStatusRunning)

	// Create streaming callback for WebSocket
	cb := NewStreamCallback(o.wsHub, task.IncidentID)

	// Broadcast analysis start
	startEvt, _ := json.Marshal(WSEvent{Type: "analysis_start", Content: task.IncidentID})
	o.wsHub.Broadcast(task.IncidentID, startEvt)

	agent := NewAgent(o.db, o.cfg)
	report, err := agent.Analyze(ctx, task.IncidentID, cb)

	if err != nil {
		log.Error().
			Err(err).
			Str("incident_id", task.IncidentID).
			Str("task_id", task.ID).
			Msg("AI analysis failed")

		o.db.Model(&task).Updates(map[string]interface{}{
			"status": model.AIStatusFailed,
			"error":  err.Error(),
		})
		o.db.Model(&model.Incident{}).
			Where("id = ?", task.IncidentID).
			Update("ai_status", model.AIStatusFailed)

		metrics.AITasksTotal.WithLabelValues("failed").Inc()

		errEvt, _ := json.Marshal(WSEvent{Type: "analysis_error", Content: err.Error()})
		o.wsHub.Broadcast(task.IncidentID, errEvt)
		return
	}

	analysis := &model.AIAnalysis{
		IncidentID: task.IncidentID,
		TaskID:     task.ID,
		Report:     report,
	}
	o.db.Create(analysis)

	finished := time.Now()
	o.db.Model(&task).Updates(map[string]interface{}{
		"status":      model.AIStatusDone,
		"finished_at": &finished,
	})

	o.db.Model(&model.Incident{}).
		Where("id = ?", task.IncidentID).
		Updates(map[string]interface{}{
			"ai_status":    model.AIStatusDone,
			"ai_report_id": analysis.ID,
		})

	metrics.AITasksTotal.WithLabelValues("done").Inc()

	// Broadcast the complete report to anyone watching the incident in the
	// web UI; this drives the streaming "AI 分析完成" state on the AI tab.
	doneEvt, _ := json.Marshal(WSEvent{Type: "analysis_done", Content: report})
	o.wsHub.Broadcast(task.IncidentID, doneEvt)

	// Fire the AI follow-up notification through the configured policy /
	// contact graph.  Run in its own goroutine with a fresh context so a
	// slow IM provider can't stall the worker pool.
	if o.onAnalysisDone != nil {
		go o.onAnalysisDone(o.rootCtx, task.IncidentID)
	}
}

// EnqueueTask creates a new AI analysis task for the given incident.
func EnqueueTask(db *gorm.DB, incidentID string) error {
	task := &model.AITask{
		IncidentID: incidentID,
		Status:     model.AIStatusPending,
	}
	if err := db.Create(task).Error; err != nil {
		return err
	}
	db.Exec("SELECT pg_notify('ai_task_ready', ?)", task.ID)
	return nil
}

// ResetStaleTasks resets any tasks stuck in 'running' state back to 'pending'.
// Called at startup to recover from unclean shutdowns.
func ResetStaleTasks(db *gorm.DB) {
	result := db.Model(&model.AITask{}).
		Where("status = ?", model.AIStatusRunning).
		Updates(map[string]interface{}{"status": model.AIStatusPending, "started_at": nil})
	if result.Error != nil {
		log.Warn().Err(result.Error).Msg("reset stale ai tasks failed")
		return
	}
	if result.RowsAffected > 0 {
		log.Info().Int64("count", result.RowsAffected).Msg("reset stale running ai tasks to pending")
		// 同步把对应 incident 的 ai_status 也重置
		db.Exec(`
			UPDATE incidents i
			SET ai_status = 'pending'
			FROM ai_tasks t
			WHERE t.incident_id = i.id AND t.status = 'pending'
			  AND i.ai_status = 'running'
		`)
	}
}
