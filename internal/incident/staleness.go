package incident

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
	"gorm.io/gorm"

	"github.com/kuzane/alertmesh/internal/model"
)

// stalenessScanInterval is the wall-clock cadence of the reaper's DB scan.
// Kept short relative to the typical staleness_timeout (10m default) so an
// incident is closed within ~30s of its timeout expiring; tweak only if
// the scan query starts showing up in pg_stat_statements.
const stalenessScanInterval = 30 * time.Second

// StartStalenessReaper kicks off a background goroutine that auto-resolves
// open incidents which have not received a new firing alert within the
// configured `incident.staleness_timeout` window.  This is the
// non-Prometheus complement to engine.Pipeline.onResolved: Kafka /
// OpenSearch / generic webhook ingestion paths don't have an equivalent
// of `endsAt ≤ now`, so the reaper supplies the close signal by
// observing silence on `incidents.last_alert_at`.
//
// Each scan cycle:
//
//  1. Loads the staleness_timeout from SystemConfig (allows runtime
//     re-tuning without restart).  0 disables the loop entirely.
//  2. Picks every open / ack / in_progress row whose last_alert_at is
//     older than (now - timeout).  Bounded LIMIT so a backlog can't
//     starve the worker — the next cycle picks up the rest.
//  3. Calls svc.AutoResolveStale per row, which is itself idempotent
//     (status guard inside the UPDATE), so racing with HandleResolvedAlert
//     or an operator-driven Resolve is safe.
//
// Returns immediately and runs until ctx is cancelled.
func StartStalenessReaper(ctx context.Context, db *gorm.DB, svc *Service) {
	go func() {
		timeout := svc.StalenessTimeout(ctx)
		log.Info().
			Dur("interval", stalenessScanInterval).
			Dur("staleness_timeout", timeout).
			Bool("enabled", timeout > 0).
			Msg("incident staleness reaper started")
		ticker := time.NewTicker(stalenessScanInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				log.Info().Msg("incident staleness reaper stopped")
				return
			case <-ticker.C:
				stalenessScanOnce(ctx, db, svc)
			}
		}
	}()
}

func stalenessScanOnce(ctx context.Context, db *gorm.DB, svc *Service) {
	timeout := svc.StalenessTimeout(ctx)
	if timeout <= 0 {
		log.Debug().Dur("staleness_timeout", timeout).Msg("staleness reaper: disabled")
		return
	}
	log.Debug().Dur("staleness_timeout", timeout).Msg("staleness reaper: scanning")
	cutoff := time.Now().Add(-timeout)

	var stale []model.Incident
	// Cap the batch to keep one cycle bounded; the next tick gets the rest.
	if err := db.WithContext(ctx).
		Where("status IN ? AND deleted_at IS NULL AND last_alert_at IS NOT NULL AND last_alert_at < ?",
			[]string{
				model.IncidentStatusOpen,
				model.IncidentStatusAck,
				model.IncidentStatusInProgress,
			}, cutoff,
		).
		Order("last_alert_at ASC").
		Limit(200).
		Find(&stale).Error; err != nil {
		log.Warn().Err(err).Msg("staleness reaper: scan failed")
		return
	}

	for i := range stale {
		inc := &stale[i]
		if err := svc.AutoResolveStale(ctx, inc.ID,
			"超过 "+timeout.String()+" 未收到新告警，自动恢复",
		); err != nil {
			log.Warn().Err(err).Str("incident_id", inc.ID).Msg("staleness reaper: auto-resolve failed")
		}
	}
}
