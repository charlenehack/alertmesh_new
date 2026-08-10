package incident

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"gorm.io/gorm"

	"github.com/kuzane/alertmesh/internal/ai"
	"github.com/kuzane/alertmesh/internal/engine"
	"github.com/kuzane/alertmesh/internal/ingestion"
	"github.com/kuzane/alertmesh/internal/model"
	"github.com/kuzane/alertmesh/internal/notification"
	"github.com/kuzane/alertmesh/internal/realtime"
	"github.com/kuzane/alertmesh/pkg/metrics"
)

type Service struct {
	repo          *Repository
	timeline      *TimelineService
	db            *gorm.DB
	dispatcher    *notification.Dispatcher
	encryptionKey string

	// rootCtx is the application-wide context (cancelled on SIGTERM via
	// rootCancel in cmd/alertmesh/main.go).  Used as the parent for the
	// engine-callback paths (HandleAlertGroup / HandleResolvedAlert) and
	// for fire-and-forget dispatcher goroutines so that a graceful
	// shutdown actually propagates cancellation into in-flight work
	// instead of orphaning it on context.Background().
	rootCtx context.Context

	// groupKeyLocks serialises HandleAlertGroup / HandleResolvedAlert
	// invocations sharing the same group_key so concurrent ingest
	// goroutines (the Kafka manager spawns N workers per data source)
	// can never both observe "no open incident" and create two rows for
	// the same group.  Same lock also protects the find-active-then-
	// append/reopen path where the gap between SELECT and INSERT used
	// to be large enough to race the aggregator's batched flush.
	//
	// Implementation: lazily-allocated *sync.Mutex per key.  We never
	// delete entries from the map — group keys are bounded by the
	// alert label cardinality the engine accepts (typically O(10k)),
	// the per-key Mutex zero-value is 16 bytes, and a manual ref-count
	// reaper would dwarf the tiny memory win with bug surface.
	groupKeyLocks sync.Map
}

// NewService constructs the Service.  rootCtx must be the long-lived
// application context (typically the one cancelled by main on SIGTERM)
// so background dispatcher goroutines respect graceful shutdown.  Pass
// context.Background() in tests when shutdown semantics aren't being
// exercised.
func NewService(rootCtx context.Context, db *gorm.DB, dispatcher *notification.Dispatcher, encryptionKey string) *Service {
	if rootCtx == nil {
		rootCtx = context.Background()
	}
	return &Service{
		repo:          NewRepository(db),
		timeline:      NewTimelineService(db),
		db:            db,
		dispatcher:    dispatcher,
		encryptionKey: encryptionKey,
		rootCtx:       rootCtx,
	}
}

// lockGroupKey returns a release function for the per-group_key mutex.
// Use as `defer s.lockGroupKey(key)()` — the call grabs the lock, the
// deferred returned closure releases it.  An empty key short-circuits to
// a no-op release so callers don't have to special-case "no group" rows.
func (s *Service) lockGroupKey(key string) func() {
	if key == "" {
		return func() {}
	}
	v, _ := s.groupKeyLocks.LoadOrStore(key, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// Timeline exposes the timeline service for callers that need to record
// incident events outside of the standard Ack/Resolve/Close flow.
func (s *Service) Timeline() *TimelineService {
	return s.timeline
}

// Dispatcher exposes the notification dispatcher.
func (s *Service) Dispatcher() *notification.Dispatcher {
	return s.dispatcher
}

// EncryptionKey exposes the AES-256 master key (callers must treat as secret).
func (s *Service) EncryptionKey() string {
	return s.encryptionKey
}

// HandleAlertGroup is the engine callback that creates or updates incidents from aggregated alert groups.
//
// Three-way branch:
//
//  1. Open / ack / in_progress incident exists for this group_key  → append.
//  2. Recently-resolved incident inside reopen_window               → reopen.
//  3. Otherwise                                                     → createIncident
//     (which itself records parent_incident_id when an older sibling
//     for the same group_key exists outside the reopen window).
func (s *Service) HandleAlertGroup(group engine.AlertGroup) {
	ctx := s.rootCtx

	// Serialise per group_key so two ingest goroutines for the same
	// group never both miss the existing-incident lookup.  See the
	// groupKeyLocks doc comment on Service for the rationale.
	defer s.lockGroupKey(group.GroupKey)()

	existing, reopen, err := s.repo.FindActiveByGroupKey(ctx, group.GroupKey, s.reopenWindow(ctx))
	if err == nil && existing != nil {
		if reopen {
			s.reopenIncident(ctx, existing, group)
			return
		}
		s.appendToIncident(ctx, existing, group)
		return
	}

	s.createIncident(ctx, group)
}

func (s *Service) createIncident(ctx context.Context, group engine.AlertGroup) {
	labelsJSON, _ := json.Marshal(group.Labels)

	var routeID *string
	if group.RouteID != "" {
		rid := group.RouteID
		routeID = &rid
	}

	var dsID *string
	if group.DataSourceID != "" {
		v := group.DataSourceID
		dsID = &v
	}

	// Hunt for a previous occurrence of this group_key whose reopen window
	// has already lapsed.  If found, link the new row via parent_incident_id
	// so the UI can render a "延续自 #xxx" breadcrumb.  Errors are non-fatal
	// — a missing parent is the common case and just leaves the field NULL.
	var parentID *string
	if prev, err := s.repo.FindLatestClosedByGroupKey(ctx, group.GroupKey); err == nil && prev != nil {
		pid := prev.ID
		parentID = &pid
	}

	now := time.Now()
	inc := &model.Incident{
		Title:             buildTitle(group),
		Severity:          group.Severity,
		Status:            model.IncidentStatusOpen,
		Source:            group.Labels["source"],
		Labels:            labelsJSON,
		GroupKey:          group.GroupKey,
		RouteID:           routeID,
		DataSourceID:      dsID,
		AIStatus:          model.AIStatusPending,
		LastAlertAt:       &now,
		ParentIncidentID:  parentID,
		RepeatSeqIndex:    0,
		SeverityStartedAt: &now,
	}

	if err := s.repo.Create(ctx, inc); err != nil {
		log.Error().Err(err).Msg("failed to create incident")
		return
	}

	alerts := convertAlerts(inc.ID, group)
	if err := s.repo.AddAlerts(ctx, alerts); err != nil {
		log.Error().Err(err).Msg("failed to persist alerts")
	}

	if err := s.timeline.Record(ctx, &model.IncidentTimeline{
		IncidentID: inc.ID,
		Action:     "created",
		ToStatus:   model.IncidentStatusOpen,
		Message:    "Incident created by rule engine",
	}); err != nil {
		log.Warn().Err(err).Str("incident_id", inc.ID).Msg("failed to record incident timeline")
	}

	metrics.IncidentsCreated.Inc()
	metrics.IncidentStatusChanges.WithLabelValues(model.IncidentStatusOpen).Inc()

	log.Info().
		Str("incident_id", inc.ID).
		Str("severity", inc.Severity).
		Str("group_key", group.GroupKey).
		Msg("incident created")

	// Async: dispatch notifications via policy/contact graph.  We optimistically
	// bump notification_count + last_notified_at synchronously *before* the
	// goroutine fires so the schedule gating in maybeRepeatNotify has the
	// timestamp to compare against on the very next alert in the same group.
	// If the dispatcher itself fails it logs + increments
	// alertmesh_notifications_dropped_total — accuracy of the UI counter
	// after a failure is acceptable trade-off vs. a race where two near-
	// simultaneous appends both think they're "first" and double-page.
	if s.dispatcher != nil {
		s.markNotified(ctx, inc.ID)
		sourceKind, dsName := s.resolveSource(ctx, group.DataSourceID, firstAlertSource(group))
		msg := notification.Message{
			IncidentID: inc.ID,
			Title:      inc.Title,
			Severity:   inc.Severity,
			Body:       buildNotificationBody(group, sourceKind, dsName),
			URL:        s.buildIncidentURL(ctx, inc.ID),
		}
		incCopy := *inc
		go s.dispatcher.DispatchForIncident(s.rootCtx, msg, &incCopy, s.encryptionKey)
	}

	// AI: log-shaped sources with ai_enabled may use manual "触发 AI 分析".
	// LLM spend on create only happens when data_sources.ai_auto_trigger is true.
	// Non-whitelist sources (and missing DataSourceID) get ai_status=disabled;
	// the UI hides the AI tab and /ai/trigger returns 400 for them.
	if ai.ShouldAutoEnqueue(ctx, s.db, group.DataSourceID) {
		go s.enqueueAITask(inc.ID)
	} else if !ai.ShouldRun(ctx, s.db, group.DataSourceID) {
		if err := s.db.WithContext(ctx).
			Model(&model.Incident{}).
			Where("id = ?", inc.ID).
			Update("ai_status", model.AIStatusDisabled).Error; err != nil {
			log.Warn().Err(err).Str("incident_id", inc.ID).Msg("failed to mark incident ai_status=disabled")
		}
	}

	// Push the new row to any open IncidentList / Dashboard / detail tab
	// — replaces the old 15-30s react-query refetchInterval timers.  Fired
	// last so the AI gate has had a chance to set ai_status correctly
	// before the browser does its invalidation refetch.
	s.notifyIncidentEvent(ctx, realtime.EventIncidentCreated, inc)
}

func (s *Service) appendToIncident(ctx context.Context, inc *model.Incident, group engine.AlertGroup) {
	alerts := convertAlerts(inc.ID, group)
	if err := s.repo.AddAlerts(ctx, alerts); err != nil {
		log.Error().Err(err).Str("incident_id", inc.ID).Msg("failed to append alerts")
	}
	log.Debug().Str("incident_id", inc.ID).Int("new_alerts", len(alerts)).Msg("alerts appended to existing incident")

	// Bump last_alert_at so the staleness reaper does not auto-resolve this
	// incident while alerts are still actively flowing in.  Best-effort:
	// a dropped UPDATE just means the reaper may close the incident later
	// than the operator expects, never earlier.
	now := time.Now()
	if err := s.db.WithContext(ctx).
		Model(&model.Incident{}).
		Where("id = ?", inc.ID).
		Update("last_alert_at", now).Error; err != nil {
		log.Warn().Err(err).Str("incident_id", inc.ID).Msg("failed to bump last_alert_at")
	} else {
		inc.LastAlertAt = &now
	}

	// Detail page is interested in the alert-count delta, list/dashboard
	// only need to know "this row touched" — both are served by a single
	// invalidation event broadcast on the firehose + per-incident topics.
	s.notifyIncidentEvent(ctx, realtime.EventIncidentAppended, inc)

	// Progressive repeat — see maybeRepeatNotify for the schedule logic.
	s.maybeRepeatNotify(ctx, inc, group)
}

// repeatScheduleV3 is the runtime form of SystemConfig key
// `notification.repeat_schedule` after JSON parsing.
//
// Two orthogonal concerns:
//
//  1. Cadence — IntervalSequence is the dense head of the per-tier repeat
//     pattern (e.g. 1m, 3m, 5m); once the tail is exhausted, every
//     subsequent step adds IntervalStep, capped at IntervalMax.  The
//     sequence index lives on the incident row (RepeatSeqIndex) so it
//     survives restarts and is reset to 0 on every severity escalation.
//
//  2. Escalation — SeverityChain is the ordered ladder
//     (typically P3 → P2 → P1 → P0).  Each tier carries a Dwell duration:
//     once the incident has been at that tier for ≥ Dwell, it bumps to
//     the next tier and resets the cadence.  P0 (or any tier with a
//     zero / nil Dwell) is terminal: the cadence keeps repeating but
//     severity stops climbing.
type repeatScheduleV3 struct {
	IntervalSequence []time.Duration
	IntervalStep     time.Duration
	IntervalMax      time.Duration
	SeverityChain    []scheduleTier
}

type scheduleTier struct {
	Severity string
	Dwell    time.Duration // 0 = terminal (no further escalation)
	Tag      string
}

// rawScheduleV3 mirrors the on-disk JSON shape (durations as minutes /
// Go-duration strings) so the parser can validate and convert in one pass.
type rawScheduleV3 struct {
	Version                 int       `json:"version"`
	IntervalSequenceMinutes []int     `json:"interval_sequence_minutes"`
	IntervalStepMinutes     int       `json:"interval_step_minutes"`
	IntervalMaxMinutes      int       `json:"interval_max_minutes"`
	SeverityChain           []rawTier `json:"severity_chain"`
}

type rawTier struct {
	Severity string  `json:"severity"`
	Dwell    *string `json:"dwell"` // nil / "" / "0" → terminal
	Tag      string  `json:"tag"`
}

// maybeRepeatNotify is the v3 re-notification entry point.  Each fresh
// batch of upstream alerts for an open incident triggers exactly one
// decision pass that may:
//
//   - escalate the incident's severity (when it has dwelled at the current
//     tier for ≥ Dwell), reset the sequence index, and dispatch with the
//     new tier's tag prefix; OR
//   - re-notify at the current tier if computeInterval(seqIndex) has
//     elapsed since LastNotifiedAt, then bump the sequence index; OR
//   - no-op (still inside the cadence window).
//
// Per-tier cadence is sourced from notification.repeat_schedule and is
// hot-reloadable (no service restart required).
func (s *Service) maybeRepeatNotify(ctx context.Context, inc *model.Incident, group engine.AlertGroup) {
	if s.dispatcher == nil {
		return
	}
	// Only repeat while the incident is actually unresolved — a resolved /
	// closed incident receiving stragglers should not page anyone again.
	switch inc.Status {
	case model.IncidentStatusResolved, model.IncidentStatusClosed:
		return
	}

	schedule := s.repeatSchedule(ctx)
	if schedule == nil {
		return // explicitly disabled or unparseable
	}

	tier := tierFor(schedule.SeverityChain, inc.Severity)
	if tier == nil {
		// Severity isn't on the ladder (custom tier?) — nothing to do.
		return
	}

	// (1) Dwell-based escalation takes precedence: if we've sat at this
	//     tier longer than Dwell, climb one rung, reset the sequence,
	//     and dispatch with the *new* tier's tag.
	if tier.Dwell > 0 {
		anchor := s.severityAnchor(inc)
		if time.Since(anchor) >= tier.Dwell {
			if next := nextTier(schedule.SeverityChain, inc.Severity); next != nil {
				s.escalateAndReset(ctx, inc, tier, next, group)
				return
			}
		}
	}

	// (2) Cadence repeat.  computeInterval picks the right wait based on
	//     how many times we've already re-notified at this tier
	//     (RepeatSeqIndex).  An incident with no LastNotifiedAt yet
	//     (server restart between create-time dispatch and first repeat
	//     window) gets a "first notification recovery" pass immediately.
	interval := computeInterval(schedule, inc.RepeatSeqIndex)
	lastNotified := inc.LastNotifiedAt
	if lastNotified != nil && time.Since(*lastNotified) < interval {
		log.Debug().
			Str("incident_id", inc.ID).
			Dur("since_last", time.Since(*lastNotified)).
			Dur("interval", interval).
			Int("seq_index", inc.RepeatSeqIndex).
			Str("tag", tier.Tag).
			Msg("dispatcher: within repeat window, skipping")
		return
	}

	titlePrefix := strings.TrimSpace(tier.Tag)
	if titlePrefix != "" {
		titlePrefix += " "
	}
	header := buildRepeatHeader(inc, group, lastNotified, interval)
	if lastNotified == nil {
		// First-ever notification for this incident — don't pretend it's a
		// repeat to the receiver.  Happens when the create-time dispatch
		// failed (server restart, empty policy graph) and the schedule is
		// catching up.
		titlePrefix = ""
		header = "Initial notification (recovered after server restart or empty policy graph)."
	}

	sourceKind, dsName := s.resolveSource(ctx, incidentDataSourceID(inc, group), firstAlertSource(group))
	body := header + "\n\n" + buildNotificationBody(group, sourceKind, dsName)
	msg := notification.Message{
		IncidentID: inc.ID,
		Title:      titlePrefix + inc.Title,
		Severity:   inc.Severity,
		Body:       body,
		URL:        s.buildIncidentURL(ctx, inc.ID),
	}

	log.Info().
		Str("incident_id", inc.ID).
		Bool("first", lastNotified == nil).
		Dur("interval", interval).
		Int("seq_index", inc.RepeatSeqIndex).
		Str("tag", tier.Tag).
		Msg("dispatcher: re-notifying ongoing incident")

	tagLabel := strings.TrimSpace(tier.Tag)
	if tagLabel == "" {
		tagLabel = "[REPEAT]"
	}
	metrics.IncidentRepeatNotifications.WithLabelValues(tagLabel).Inc()
	metrics.IncidentRepeatSequenceStep.Observe(float64(inc.RepeatSeqIndex))

	s.markNotified(ctx, inc.ID)
	s.bumpSeqIndex(ctx, inc)

	incCopy := *inc
	go s.dispatcher.DispatchForIncident(s.rootCtx, msg, &incCopy, s.encryptionKey)
}

// escalateAndReset bumps the incident to the next severity tier, zeroes
// the sequence index so the new tier starts at the dense head of the
// cadence, stamps severity_started_at = now(), and fires a single
// "[ATTENTION]"-style dispatch announcing the climb.  All in one
// non-batched UPDATE so a concurrent maybeRepeatNotify can't re-escalate
// the same row.
func (s *Service) escalateAndReset(
	ctx context.Context, inc *model.Incident, fromTier, toTier *scheduleTier, group engine.AlertGroup,
) {
	from := inc.Severity
	now := time.Now()
	res := s.db.WithContext(ctx).
		Model(&model.Incident{}).
		Where("id = ? AND severity = ?", inc.ID, from).
		Updates(map[string]any{
			"severity":            toTier.Severity,
			"repeat_seq_index":    0,
			"severity_started_at": now,
		})
	if res.Error != nil {
		log.Warn().Err(res.Error).Str("incident_id", inc.ID).Msg("dwell escalate: update failed")
		return
	}
	if res.RowsAffected == 0 {
		// Another goroutine raced us — the incident already moved off
		// `from`.  Refresh local state from the DB and let the next
		// maybeRepeatNotify cycle handle it.
		return
	}
	inc.Severity = toTier.Severity
	inc.RepeatSeqIndex = 0
	inc.SeverityStartedAt = &now

	metrics.IncidentsEscalated.WithLabelValues(from, toTier.Severity).Inc()
	_ = s.timeline.Record(ctx, &model.IncidentTimeline{
		IncidentID: inc.ID,
		Action:     "escalated",
		Message: fmt.Sprintf(
			"在 %s 驻留满 %s，自动升级 %s → %s（%s）",
			from, fromTier.Dwell, from, toTier.Severity, toTier.Tag,
		),
	})

	titlePrefix := strings.TrimSpace(toTier.Tag)
	if titlePrefix != "" {
		titlePrefix += " "
	}
	sourceKind, dsName := s.resolveSource(ctx, incidentDataSourceID(inc, group), firstAlertSource(group))
	body := fmt.Sprintf(
		"⚠️ 该告警在 %s 持续 %s 未恢复，已自动升级为 %s。\n\n%s",
		from, fromTier.Dwell, toTier.Severity, buildNotificationBody(group, sourceKind, dsName),
	)
	msg := notification.Message{
		IncidentID: inc.ID,
		Title:      titlePrefix + inc.Title,
		Severity:   inc.Severity,
		Body:       body,
		URL:        s.buildIncidentURL(ctx, inc.ID),
	}
	tagLabel := strings.TrimSpace(toTier.Tag)
	if tagLabel == "" {
		tagLabel = "[ATTENTION]"
	}
	metrics.IncidentRepeatNotifications.WithLabelValues(tagLabel).Inc()
	metrics.IncidentRepeatSequenceStep.Observe(0)

	s.markNotified(ctx, inc.ID)
	s.bumpSeqIndex(ctx, inc)

	incCopy := *inc
	go s.dispatcher.DispatchForIncident(s.rootCtx, msg, &incCopy, s.encryptionKey)
}

// computeInterval returns the wait the schedule wants for the given
// per-incident sequence index.  Indexes inside IntervalSequence map
// directly; beyond the tail, each step adds IntervalStep, capped at
// IntervalMax.  Defends against a degenerate schedule (empty sequence,
// zero step) by falling back to IntervalMax (or 30m if even that is 0).
func computeInterval(s *repeatScheduleV3, seqIndex int) time.Duration {
	if seqIndex < 0 {
		seqIndex = 0
	}
	if n := len(s.IntervalSequence); n > 0 && seqIndex < n {
		return s.IntervalSequence[seqIndex]
	}

	// Past the dense head — extend linearly from the last sequence value.
	var base time.Duration
	if n := len(s.IntervalSequence); n > 0 {
		base = s.IntervalSequence[n-1]
	}
	overflow := seqIndex - len(s.IntervalSequence) + 1 // steps past the tail
	if overflow < 1 {
		overflow = 1
	}
	d := base + time.Duration(overflow)*s.IntervalStep
	switch {
	case s.IntervalMax > 0 && d > s.IntervalMax:
		return s.IntervalMax
	case d > 0:
		return d
	case s.IntervalMax > 0:
		return s.IntervalMax
	default:
		return 30 * time.Minute
	}
}

// tierFor returns the schedule tier matching the incident's current
// severity, or nil when the severity is off the chain.
func tierFor(chain []scheduleTier, severity string) *scheduleTier {
	sev := strings.ToUpper(strings.TrimSpace(severity))
	for i := range chain {
		if strings.ToUpper(chain[i].Severity) == sev {
			return &chain[i]
		}
	}
	return nil
}

// nextTier returns the tier *after* the one matching severity, or nil
// when severity is the terminal tier (or off the chain).
func nextTier(chain []scheduleTier, severity string) *scheduleTier {
	sev := strings.ToUpper(strings.TrimSpace(severity))
	for i := range chain {
		if strings.ToUpper(chain[i].Severity) == sev && i+1 < len(chain) {
			return &chain[i+1]
		}
	}
	return nil
}

// severityAnchor returns SeverityStartedAt with a graceful fall-back to
// OpenedAt for legacy rows pre-migration 000045.
func (s *Service) severityAnchor(inc *model.Incident) time.Time {
	if inc.SeverityStartedAt != nil {
		return *inc.SeverityStartedAt
	}
	return inc.OpenedAt
}

// bumpSeqIndex increments repeat_seq_index in the DB and on the in-memory
// copy.  Errors are non-fatal: a dropped UPDATE just means the next
// repeat fires at the same cadence as this one (over-loud, never
// silently-quiet, which matches the "best practice be noisy on failure"
// mode of the wider lifecycle).
func (s *Service) bumpSeqIndex(ctx context.Context, inc *model.Incident) {
	if err := s.db.WithContext(ctx).
		Model(&model.Incident{}).
		Where("id = ?", inc.ID).
		Update("repeat_seq_index", gorm.Expr("repeat_seq_index + 1")).Error; err != nil {
		log.Warn().Err(err).Str("incident_id", inc.ID).Msg("bumpSeqIndex: update failed")
		return
	}
	inc.RepeatSeqIndex++
}

// repeatSchedule loads notification.repeat_schedule from system_configs
// and converts it to the runtime form.  Returns nil to mean "disabled":
//   - schedule key missing / value blank
//   - JSON malformed (logged at WARN; not fatal)
//   - schedule has zero entries after parsing
//   - legacy v2 array shape (operators must run migration 000045)
//
// There is no longer a fallback to the v2 array shape — the v3 object is
// strictly required.  Operators upgrading from v2 will see a single
// WARN log line and the lifecycle will stop re-notifying until they
// re-run the seed migration; this is intentional, silently degrading to
// the wrong cadence is worse than going quiet.
func (s *Service) repeatSchedule(ctx context.Context) *repeatScheduleV3 {
	const key = "notification.repeat_schedule"

	var row model.SystemConfig
	if err := s.db.WithContext(ctx).Where("key = ?", key).First(&row).Error; err != nil {
		return nil
	}
	v := strings.TrimSpace(row.Value)
	if v == "" {
		return nil
	}
	if strings.HasPrefix(v, "[") {
		log.Warn().Str("key", key).Msg(
			"repeat_schedule: legacy v2 array shape detected; please re-run migration 000045 — re-notifications are disabled until then",
		)
		return nil
	}

	var raw rawScheduleV3
	if err := json.Unmarshal([]byte(v), &raw); err != nil {
		log.Warn().Err(err).Str("key", key).Msg("repeat_schedule: invalid JSON, repeats disabled")
		return nil
	}

	out := &repeatScheduleV3{}
	for _, m := range raw.IntervalSequenceMinutes {
		if m <= 0 {
			continue
		}
		out.IntervalSequence = append(out.IntervalSequence, time.Duration(m)*time.Minute)
	}
	if raw.IntervalStepMinutes > 0 {
		out.IntervalStep = time.Duration(raw.IntervalStepMinutes) * time.Minute
	}
	if raw.IntervalMaxMinutes > 0 {
		out.IntervalMax = time.Duration(raw.IntervalMaxMinutes) * time.Minute
	}
	for _, t := range raw.SeverityChain {
		sev := strings.TrimSpace(t.Severity)
		if sev == "" {
			continue
		}
		tier := scheduleTier{
			Severity: strings.ToUpper(sev),
			Tag:      strings.TrimSpace(t.Tag),
		}
		if t.Dwell != nil {
			ds := strings.TrimSpace(*t.Dwell)
			if ds != "" && ds != "0" {
				if d, err := time.ParseDuration(ds); err == nil && d > 0 {
					tier.Dwell = d
				} else if err != nil {
					log.Warn().Err(err).Str("severity", sev).Str("dwell", ds).
						Msg("repeat_schedule: invalid dwell, treating tier as terminal")
				}
			}
		}
		out.SeverityChain = append(out.SeverityChain, tier)
	}

	if len(out.SeverityChain) == 0 {
		log.Warn().Str("key", key).Msg("repeat_schedule: severity_chain is empty, repeats disabled")
		return nil
	}
	return out
}

// markNotified bumps notification_count and last_notified_at without
// touching updated_at-only fields like status.  Called both on initial
// dispatch (createIncident) and every successful repeat (maybeRepeatNotify).
// Errors are non-fatal: the dispatch itself already happened, the column is
// only used to throttle subsequent repeats.
func (s *Service) markNotified(ctx context.Context, incidentID string) {
	now := time.Now()
	if err := s.db.WithContext(ctx).
		Model(&model.Incident{}).
		Where("id = ?", incidentID).
		Updates(map[string]any{
			"last_notified_at":   now,
			"notification_count": gorm.Expr("notification_count + 1"),
		}).Error; err != nil {
		log.Warn().Err(err).Str("incident_id", incidentID).Msg("markNotified: update failed")
	}
}

// reopenWindow returns the configured `incident.reopen_window` duration
// (default 5m).  Returning 0 disables reopen entirely.
func (s *Service) reopenWindow(ctx context.Context) time.Duration {
	const defaultWindow = 5 * time.Minute

	var row model.SystemConfig
	if err := s.db.WithContext(ctx).
		Where("key = ?", "incident.reopen_window").
		First(&row).Error; err != nil {
		return defaultWindow
	}
	v := strings.TrimSpace(row.Value)
	if v == "" {
		return defaultWindow
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Warn().Err(err).Str("value", v).Msg("incident.reopen_window: invalid Go duration, using default")
		return defaultWindow
	}
	return d
}

// stalenessTimeout returns the configured `incident.staleness_timeout`
// duration (default 0/disabled).  Returning 0 disables the staleness reaper.
func (s *Service) stalenessTimeout(ctx context.Context) time.Duration {
	const defaultTimeout = 0

	var row model.SystemConfig
	if err := s.db.WithContext(ctx).
		Where("key = ?", "incident.staleness_timeout").
		First(&row).Error; err != nil {
		return defaultTimeout
	}
	v := strings.TrimSpace(row.Value)
	if v == "" {
		return defaultTimeout
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Warn().Err(err).Str("value", v).Msg("incident.staleness_timeout: invalid Go duration, using default")
		return defaultTimeout
	}
	return d
}

// StalenessTimeout exposes the configured timeout for the background reaper.
func (s *Service) StalenessTimeout(ctx context.Context) time.Duration {
	return s.stalenessTimeout(ctx)
}

// reopenIncident promotes a previously-resolved incident back to open when a
// new alert with the same group_key fires inside the reopen window.  This
// avoids creating a brand-new incident row every time a flapping check
// recovers and re-fires within the cool-down — the on-call sees one
// continuous incident with a `[REOPENED]` notification + timeline entry
// instead of two unrelated rows.
func (s *Service) reopenIncident(ctx context.Context, inc *model.Incident, group engine.AlertGroup) {
	now := time.Now()
	if err := s.db.WithContext(ctx).
		Model(&model.Incident{}).
		Where("id = ?", inc.ID).
		Updates(map[string]any{
			"status":              model.IncidentStatusOpen,
			"resolved_at":         nil,
			"auto_resolved_at":    nil,
			"last_alert_at":       now,
			"repeat_seq_index":    0,
			"severity_started_at": now,
		}).Error; err != nil {
		log.Error().Err(err).Str("incident_id", inc.ID).Msg("reopenIncident: update failed")
		return
	}
	prevStatus := inc.Status
	inc.Status = model.IncidentStatusOpen
	inc.ResolvedAt = nil
	inc.AutoResolvedAt = nil
	inc.LastAlertAt = &now
	inc.RepeatSeqIndex = 0
	inc.SeverityStartedAt = &now

	alerts := convertAlerts(inc.ID, group)
	if err := s.repo.AddAlerts(ctx, alerts); err != nil {
		log.Error().Err(err).Str("incident_id", inc.ID).Msg("reopenIncident: append alerts failed")
	}

	_ = s.timeline.Record(ctx, &model.IncidentTimeline{
		IncidentID: inc.ID,
		Action:     "reopened",
		FromStatus: prevStatus,
		ToStatus:   model.IncidentStatusOpen,
		Message:    "Resolved 后窗口内同 group_key 再次触发，自动复活",
	})

	metrics.IncidentsReopened.Inc()
	metrics.IncidentStatusChanges.WithLabelValues(model.IncidentStatusOpen).Inc()

	log.Info().
		Str("incident_id", inc.ID).
		Str("group_key", group.GroupKey).
		Str("from_status", prevStatus).
		Msg("incident reopened within reopen_window")

	s.notifyIncidentEvent(ctx, realtime.EventIncidentCreated, inc)

	// Reopen always sends a fresh `[REOPENED]` notification — it doesn't
	// go through the schedule because the operator needs to know the
	// "resolved" they saw a few minutes ago wasn't the end of the story.
	if s.dispatcher != nil {
		sourceKind, dsName := s.resolveSource(ctx, incidentDataSourceID(inc, group), firstAlertSource(group))
		body := fmt.Sprintf(
			"⚠️ 该告警在 resolved 后 %s 内再次触发，已自动复活为 open 状态。\n\n%s",
			time.Since(timeOrZero(inc.ResolvedAt)).Truncate(time.Second),
			buildNotificationBody(group, sourceKind, dsName),
		)
		s.markNotified(ctx, inc.ID)
		msg := notification.Message{
			IncidentID: inc.ID,
			Title:      "[REOPENED] " + inc.Title,
			Severity:   inc.Severity,
			Body:       body,
			URL:        s.buildIncidentURL(ctx, inc.ID),
		}
		incCopy := *inc
		go s.dispatcher.DispatchForIncident(s.rootCtx, msg, &incCopy, s.encryptionKey)
	}
}

// HandleResolvedAlert is wired to engine.NewPipeline as the onResolved
// callback.  Prometheus signals recovery by pushing the same alert with
// endsAt ≤ now (translated to Status="resolved" by the v2 adapter); we
// look up the matching open incident and auto-close it without waiting
// for the staleness reaper.  No-op when no open incident matches — that
// covers Prometheus repeating the resolved signal after we've already
// closed.
func (s *Service) HandleResolvedAlert(alert ingestion.RawAlert, groupKey string) {
	ctx := s.rootCtx

	// Same per-group_key lock as HandleAlertGroup so a resolved signal
	// arriving simultaneously with a new firing alert can't race the
	// row lookup → autoResolve sequence.
	defer s.lockGroupKey(groupKey)()

	// Reopen window irrelevant here — we only resolve currently-active rows.
	existing, _, err := s.repo.FindActiveByGroupKey(ctx, groupKey, 0)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			log.Warn().Err(err).Str("group_key", groupKey).Msg("HandleResolvedAlert: lookup failed")
		}
		return
	}
	if existing == nil {
		return
	}

	if err := s.autoResolve(ctx, existing, "endsat_signal", "由 Prometheus 上游 endsAt 信号自动恢复"); err != nil {
		log.Warn().Err(err).Str("incident_id", existing.ID).Msg("HandleResolvedAlert: auto-resolve failed")
	}
}

// AutoResolveStale is invoked by StartStalenessReaper when no firing alerts
// have arrived for the incident inside `incident.staleness_timeout`.  Same
// flow as HandleResolvedAlert but reason="staleness".
func (s *Service) AutoResolveStale(ctx context.Context, incidentID string, reason string) error {
	inc, err := s.repo.FindByID(ctx, incidentID)
	if err != nil {
		return err
	}
	// Race-guard: another goroutine (the resolved-signal path or an operator
	// click) may have already closed it between the reaper's scan and now.
	if inc.Status != model.IncidentStatusOpen && inc.Status != model.IncidentStatusAck && inc.Status != model.IncidentStatusInProgress {
		return nil
	}
	msg := reason
	if msg == "" {
		msg = fmt.Sprintf("超过 %s 未收到新告警，自动恢复", s.stalenessTimeout(ctx))
	}
	return s.autoResolve(ctx, inc, "staleness", msg)
}

// autoResolve is the shared path for both endsAt and staleness driven
// auto-resolution.  reason is the metric label, message is the timeline /
// notification body.  Idempotent: re-entry on an already-resolved incident
// is a no-op (the WHERE clause guards against double-counting).
func (s *Service) autoResolve(ctx context.Context, inc *model.Incident, reason, message string) error {
	now := time.Now()
	res := s.db.WithContext(ctx).
		Model(&model.Incident{}).
		Where("id = ? AND status IN ?", inc.ID, []string{
			model.IncidentStatusOpen,
			model.IncidentStatusAck,
			model.IncidentStatusInProgress,
		}).
		Updates(map[string]any{
			"status":           model.IncidentStatusResolved,
			"resolved_at":      now,
			"auto_resolved_at": now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		// Already closed by another path — silently skip to keep this idempotent.
		return nil
	}

	prevStatus := inc.Status
	inc.Status = model.IncidentStatusResolved
	inc.ResolvedAt = &now
	inc.AutoResolvedAt = &now

	// Sync the status of every firing alert that belongs to this incident so
	// the alert list on the detail page shows "resolved" instead of "firing".
	if err := s.db.WithContext(ctx).
		Model(&model.Alert{}).
		Where("incident_id = ? AND status = ?", inc.ID, model.AlertStatusFiring).
		Update("status", model.AlertStatusResolved).Error; err != nil {
		log.Warn().Err(err).Str("incident_id", inc.ID).Msg("autoResolve: failed to update alert statuses")
	}

	_ = s.timeline.Record(ctx, &model.IncidentTimeline{
		IncidentID: inc.ID,
		Action:     "auto_resolved",
		FromStatus: prevStatus,
		ToStatus:   model.IncidentStatusResolved,
		Message:    message,
	})

	metrics.IncidentsAutoResolved.WithLabelValues(reason).Inc()
	metrics.IncidentStatusChanges.WithLabelValues(model.IncidentStatusResolved).Inc()
	log.Info().
		Str("incident_id", inc.ID).
		Str("reason", reason).
		Str("from_status", prevStatus).
		Msg("incident auto-resolved")

	s.notifyIncidentEvent(ctx, realtime.EventIncidentResolved, inc)

	if s.dispatcher != nil {
		duration := time.Since(inc.OpenedAt).Truncate(time.Second)
		body := fmt.Sprintf("✅ 告警已自动恢复（%s）。\n\n持续时间：%s\n累计通知次数：%d",
			message, duration, inc.NotificationCount)
		msg := notification.Message{
			IncidentID: inc.ID,
			Title:      "[RESOLVED] " + inc.Title,
			Severity:   inc.Severity,
			Body:       body,
			URL:        s.buildIncidentURL(ctx, inc.ID),
		}
		incCopy := *inc
		go s.dispatcher.DispatchForIncident(s.rootCtx, msg, &incCopy, s.encryptionKey)
	}
	return nil
}

func timeOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// buildIncidentURL composes the deep link to the incident in the web UI.
// Returns "" when system.web_base_url is not configured — the dispatcher
// channels are responsible for hiding the URL field when it's empty.
func (s *Service) buildIncidentURL(ctx context.Context, incidentID string) string {
	var row model.SystemConfig
	if err := s.db.WithContext(ctx).
		Where("key = ?", "system.web_base_url").
		First(&row).Error; err != nil {
		return ""
	}
	base := strings.TrimRight(strings.TrimSpace(row.Value), "/")
	if base == "" {
		return ""
	}
	return fmt.Sprintf("%s/incidents/%s", base, incidentID)
}

func buildRepeatHeader(inc *model.Incident, group engine.AlertGroup, lastSent *time.Time, repeat time.Duration) string {
	since := time.Since(inc.OpenedAt).Truncate(time.Second)
	if lastSent == nil {
		return fmt.Sprintf("⚠️ 仍在告警 — 自 %s 起持续 %s。", inc.OpenedAt.Format(time.RFC3339), since)
	}
	gap := time.Since(*lastSent).Truncate(time.Second)
	return fmt.Sprintf(
		"⚠️ 持续告警 %s — 上次通知在 %s 前（重复间隔 %s）。新增 %d 条告警。",
		since, gap, repeat, len(group.Alerts),
	)
}

// DispatchAIFollowup is invoked by the AI orchestrator once an analysis
// completes successfully.  It builds a Markdown-rich message containing the
// agent's report excerpt and re-uses DispatchForIncident so the same
// policy/contact graph is honored.
//
// Safe to call from any goroutine; logs and swallows all errors so a
// notification failure never blocks the AI worker pool.
func (s *Service) DispatchAIFollowup(ctx context.Context, incidentID string) {
	if s.dispatcher == nil {
		return
	}

	inc, err := s.repo.FindByID(ctx, incidentID)
	if err != nil || inc == nil {
		log.Warn().Err(err).Str("incident_id", incidentID).Msg("ai followup: incident not found")
		return
	}

	// Don't page the AI conclusion for already-resolved incidents — the
	// on-call has likely moved on.
	if inc.Status == model.IncidentStatusResolved || inc.Status == model.IncidentStatusClosed {
		log.Debug().Str("incident_id", incidentID).Str("status", inc.Status).Msg("ai followup: incident closed, skipping")
		return
	}

	var analysis model.AIAnalysis
	if err := s.db.WithContext(ctx).
		Where("incident_id = ?", incidentID).
		Order("created_at DESC").
		First(&analysis).Error; err != nil {
		log.Warn().Err(err).Str("incident_id", incidentID).Msg("ai followup: no analysis row found")
		return
	}

	body := buildAIFollowupBody(inc, &analysis, s.buildIncidentURL(ctx, incidentID))
	msg := notification.Message{
		IncidentID: incidentID,
		Title:      "[AI 分析完成] " + inc.Title,
		Severity:   inc.Severity,
		Body:       body,
		URL:        s.buildIncidentURL(ctx, incidentID),
	}

	log.Info().Str("incident_id", incidentID).Msg("dispatcher: sending AI-followup notification")

	incCopy := *inc
	s.dispatcher.DispatchForIncident(ctx, msg, &incCopy, s.encryptionKey)

	// Best-effort timeline entry so it's visible in the UI.
	_ = s.timeline.Record(ctx, &model.IncidentTimeline{
		IncidentID: incidentID,
		Action:     "ai_notified",
		Message:    "AI 分析结论已通过通知策略推送",
	})
}

// buildAIFollowupBody assembles the notification body for the AI follow-up.
// We intentionally cap the report excerpt — IM channels (DingTalk / Feishu)
// have payload-size limits and full reports are easier to read in the web UI.
func buildAIFollowupBody(inc *model.Incident, a *model.AIAnalysis, url string) string {
	const maxReportBytes = 1500

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**事件:** %s\n", inc.Title))
	sb.WriteString(fmt.Sprintf("**级别:** %s\n", inc.Severity))
	sb.WriteString("**AI 分析:** 已完成 ✅\n\n")

	if rc := strings.TrimSpace(a.RootCause); rc != "" {
		sb.WriteString("**🎯 根因:**\n")
		sb.WriteString(rc)
		sb.WriteString("\n\n")
	}
	if sm := strings.TrimSpace(a.Summary); sm != "" {
		sb.WriteString("**📋 摘要:**\n")
		sb.WriteString(sm)
		sb.WriteString("\n\n")
	}

	report := strings.TrimSpace(a.Report)
	if report != "" {
		sb.WriteString("**📝 报告节选:**\n")
		sb.WriteString(truncateReport(report, maxReportBytes))
		sb.WriteString("\n")
	}

	if url != "" {
		sb.WriteString(fmt.Sprintf("\n查看完整分析: %s", url))
	}
	return sb.String()
}

// truncateReport keeps the first n bytes of a Markdown report at a UTF-8
// safe boundary and appends an ellipsis when it had to cut.
func truncateReport(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 { // back off into a multibyte char
		cut--
	}
	return s[:cut] + "\n\n…（报告过长已截断，完整内容请见 Web UI）"
}

func (s *Service) Ack(ctx context.Context, incidentID, userID, username string) error {
	inc, err := s.repo.FindByID(ctx, incidentID)
	if err != nil {
		return err
	}
	now := time.Now()
	inc.Status = model.IncidentStatusAck
	inc.AckedAt = &now
	if err := s.repo.Update(ctx, inc); err != nil {
		return err
	}

	metrics.IncidentStatusChanges.WithLabelValues(model.IncidentStatusAck).Inc()

	if err := s.timeline.Record(ctx, &model.IncidentTimeline{
		IncidentID: incidentID,
		Action:     "acked",
		FromStatus: model.IncidentStatusOpen,
		ToStatus:   model.IncidentStatusAck,
		UserID:     userID,
		Username:   username,
	}); err != nil {
		return err
	}

	s.notifyIncidentEvent(ctx, realtime.EventIncidentAck, inc)
	return nil
}

func (s *Service) Resolve(ctx context.Context, incidentID, userID, username string) error {
	inc, err := s.repo.FindByID(ctx, incidentID)
	if err != nil {
		return err
	}
	now := time.Now()
	fromStatus := inc.Status
	inc.Status = model.IncidentStatusResolved
	inc.ResolvedAt = &now
	if err := s.repo.Update(ctx, inc); err != nil {
		return err
	}

	metrics.IncidentStatusChanges.WithLabelValues(model.IncidentStatusResolved).Inc()

	if err := s.timeline.Record(ctx, &model.IncidentTimeline{
		IncidentID: incidentID,
		Action:     "resolved",
		FromStatus: fromStatus,
		ToStatus:   model.IncidentStatusResolved,
		UserID:     userID,
		Username:   username,
	}); err != nil {
		return err
	}

	s.notifyIncidentEvent(ctx, realtime.EventIncidentResolved, inc)
	return nil
}

func (s *Service) Close(ctx context.Context, incidentID, userID, username string) error {
	inc, err := s.repo.FindByID(ctx, incidentID)
	if err != nil {
		return err
	}
	fromStatus := inc.Status
	inc.Status = model.IncidentStatusClosed
	if err := s.repo.Update(ctx, inc); err != nil {
		return err
	}

	metrics.IncidentStatusChanges.WithLabelValues(model.IncidentStatusClosed).Inc()

	if err := s.timeline.Record(ctx, &model.IncidentTimeline{
		IncidentID: incidentID,
		Action:     "closed",
		FromStatus: fromStatus,
		ToStatus:   model.IncidentStatusClosed,
		UserID:     userID,
		Username:   username,
	}); err != nil {
		return err
	}

	s.notifyIncidentEvent(ctx, realtime.EventIncidentClosed, inc)
	return nil
}

func (s *Service) GetByID(ctx context.Context, id string) (*model.Incident, error) {
	return s.repo.FindByID(ctx, id)
}

func (s *Service) List(ctx context.Context, offset, limit int) ([]model.Incident, int64, error) {
	return s.repo.List(ctx, offset, limit)
}

func (s *Service) enqueueAITask(incidentID string) {
	task := &model.AITask{
		IncidentID: incidentID,
		Status:     model.AIStatusPending,
	}
	if err := s.db.Create(task).Error; err != nil {
		log.Error().Err(err).Str("incident_id", incidentID).Msg("failed to enqueue AI task")
		return
	}
	s.db.Exec("SELECT pg_notify('ai_task_ready', ?)", task.ID)
}

// notifyIncidentEvent fires a pg_notify('incident_event', json) so the
// realtime LISTEN goroutine on every alertmesh replica can fan the event
// out to its WebSocket subscribers, killing the polling timers the UI
// used to rely on.  Three properties matter:
//
//  1. We use pg_notify (not an in-process channel) so a multi-replica
//     deployment behind a load balancer fans out correctly even when the
//     replica that wrote the row isn't the one holding a given socket.
//  2. The payload is intentionally tiny — type + ids + minimal hints — so
//     it stays well under PostgreSQL's 8000-byte NOTIFY limit and the
//     browser is forced to refetch via REST (REST stays the source of
//     truth, the WS is just an invalidation signal).
//  3. We never return the pg_notify error.  A broken NOTIFY must not
//     fail the user-visible mutation; the UI degrades to its on-page
//     manual refresh button until the LISTEN reconnects.
func (s *Service) notifyIncidentEvent(ctx context.Context, eventType string, inc *model.Incident) {
	if inc == nil {
		return
	}
	payload := map[string]string{
		"type":        eventType,
		"incident_id": inc.ID,
	}
	if inc.Severity != "" {
		payload["severity"] = inc.Severity
	}
	if inc.Status != "" {
		payload["status"] = inc.Status
	}
	body, err := json.Marshal(payload)
	if err != nil {
		log.Warn().Err(err).Str("incident_id", inc.ID).Msg("notifyIncidentEvent: marshal failed")
		return
	}
	if err := s.db.WithContext(ctx).
		Exec("SELECT pg_notify('incident_event', ?)", string(body)).Error; err != nil {
		log.Warn().Err(err).Str("incident_id", inc.ID).Str("type", eventType).Msg("notifyIncidentEvent: pg_notify failed")
	}
}

// ipv4Re matches an IPv4 address embedded in longer strings (e.g. Tencent
// Cloud dimensions like "1251890496#101.42.12.33 #eip-03czz65d#未命名").
var ipv4Re = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)

// cleanInstanceIdentifier picks the most useful part of a cloud dimension
// string.  Plain IPs/IDs are kept as-is; hash-delimited blobs are reduced to
// the embedded IP when one exists, otherwise the original value is preserved.
func cleanInstanceIdentifier(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if net.ParseIP(s) != nil {
		return s
	}
	if ip := ipv4Re.FindString(s); ip != "" {
		return ip
	}
	return s
}

// containsIPv4 reports whether s has an IPv4 address embedded in it.
func containsIPv4(s string) bool {
	return ipv4Re.MatchString(strings.TrimSpace(s))
}

// buildTitle produces a concise, human-readable incident title.
func buildTitle(group engine.AlertGroup) string {
	name := group.Labels["alertname"]
	if name == "" {
		name = "Unknown Alert"
	}

	// Enrich the title with meaningful context for cloud-provider alarms
	// (e.g. Tencent Cloud): instance IP/ID and the triggering metric or event.
	// We try several common dimension fields because different cloud products
	// expose the identifying value under different keys (unInstanceId, objId,
	// objName, deviceName, or IP directly).
	parts := []string{name}
	instance := ""
	// Candidate keys ordered from most specific (IP) to least specific (cloud
	// IDs).  We do two passes: first prefer any candidate that contains an IP,
	// so labels like obj_name=10.100.84.10#8469859 win over device_name=主机名.
	// First pass: find a label value that contains an IPv4.
	ipKeys := []string{
		"ip", "instance_ip", "private_ip", "public_ip", "ip_address", "local_ip", "remote_ip",
		"instance", "hostname", "host", "node",
		"device_name", "obj_name", "obj_id", "instance_id",
	}
	for _, key := range ipKeys {
		if v := strings.TrimSpace(group.Labels[key]); v != "" && containsIPv4(v) {
			instance = cleanInstanceIdentifier(v)
			break
		}
	}
	// Second pass: no IP found – take the first non-empty candidate as name.
	var hostName string
	for _, key := range []string{"device_name", "hostname", "host", "node"} {
		if v := strings.TrimSpace(group.Labels[key]); v != "" && !containsIPv4(v) {
			hostName = v
			break
		}
	}
	if instance == "" {
		for _, key := range ipKeys {
			if v := strings.TrimSpace(group.Labels[key]); v != "" {
				instance = cleanInstanceIdentifier(v)
				break
			}
		}
	}
	// Final fallback: scan all labels for any value that looks like an IPv4.
	if instance == "" {
		for _, v := range group.Labels {
			if ip := ipv4Re.FindString(strings.TrimSpace(v)); ip != "" {
				instance = ip
				break
			}
		}
	}
	// Build instance label: "IP (主机名)" when both are available and differ.
	var instanceLabel string
	switch {
	case instance != "" && hostName != "" && hostName != instance:
		instanceLabel = instance + " (" + hostName + ")"
	case instance != "":
		instanceLabel = instance
	case hostName != "":
		instanceLabel = hostName
	}
	if instanceLabel != "" {
		parts = append(parts, instanceLabel)
	}
	var reason string
	if metric := strings.TrimSpace(group.Labels["metric"]); metric != "" {
		reason = metric
	} else if event := strings.TrimSpace(group.Labels["event_name"]); event != "" {
		reason = event
	}
	if reason != "" {
		parts = append(parts, reason)
	}

	return "[" + group.Severity + "] " + strings.Join(parts, " / ")
}

// Notification body rendering caps.  These are channel-agnostic guards
// that protect downstream IM providers (Slack mrkdwn ≈ 3000 chars,
// DingTalk markdown ≈ 5000 chars, Feishu lark_md is generous, Email is
// effectively unbounded).  Per-value cap also keeps a single very long
// annotation (e.g. a whole 5xx response_body) from drowning out everything
// else in the same message.
const (
	notifyValueMaxLen = 256
	notifyBodyMaxLen  = 3000
)

// skipAnnoKeys are annotations rendered as their own dedicated sections
// (summary above the body, description in the trailing details block),
// so they are excluded from the generic context section.
var skipAnnoKeys = map[string]struct{}{
	"summary":     {},
	"description": {},
}

// resolvedSourceTTL bounds how long a (kind, name) tuple stays in the
// in-process cache before we re-query data_sources.  Five minutes lines
// up with KafkaManager's reload floor — operators who renamed a row
// cannot perceive the lag because the new name and the new mapping land
// at the same observer-visible cadence.  CRUD on data_sources fires
// pg_notify('data_source_event') anyway; we deliberately do not subscribe
// because the worst case is a stale (kind, name) string in 一两条 IM
// notifications, well below the cost of holding a long-lived connection
// just to evict.
const resolvedSourceTTL = 5 * time.Minute

// resolvedSourceEntry caches one data_sources row's surfaceable identity
// (kind + name).  We do NOT cache the full row to keep the entry cheap
// and to guarantee we never reach into config / secret_enc downstream.
type resolvedSourceEntry struct {
	kind      string
	name      string
	expiresAt time.Time
}

// resolvedSourceCache is process-wide because the data_sources lookup
// is purely informational and the row count is bounded by the registry
// (typically O(10) rows).  sync.Map suits the read-heavy access pattern.
var resolvedSourceCache sync.Map // map[string]resolvedSourceEntry

// resolveSource maps an outgoing alert's data_source_id back to the
// (kind, friendly-name) pair we surface on the IM message body's
// "**消息源:**" line.  The fallbackSource is whatever the upstream adapter
// stamped onto RawAlert.Source — alertmanager / prometheus / webhook etc.
// — so legacy push-style sources without a data_sources row still get a
// meaningful label without us inventing names.
//
// Contract:
//   - dsID == ""              → (fallbackSource, "")
//   - row found in registry   → (row.Kind, row.Name)
//   - row missing or DB error → (fallbackSource, "")  + warn log
//
// The returned dsName is empty when there is no corresponding registry
// row; the renderer joins it with " : " only when non-empty so the
// rendered string stays clean (`kafka : higress-prod` vs. plain
// `alertmanager`).
func (s *Service) resolveSource(ctx context.Context, dsID, fallbackSource string) (string, string) {
	if dsID == "" {
		return fallbackSource, ""
	}
	if v, ok := resolvedSourceCache.Load(dsID); ok {
		entry := v.(resolvedSourceEntry)
		if time.Now().Before(entry.expiresAt) {
			return entry.kind, entry.name
		}
		// Expired entry; fall through and refresh.  The store at the
		// bottom overwrites this stale reading.
	}
	if s.db == nil {
		return fallbackSource, ""
	}
	var row model.DataSource
	err := s.db.WithContext(ctx).
		Select("kind", "name").
		Where("id = ?", dsID).
		First(&row).Error
	if err != nil {
		// Missing row → operator deleted the source after the alert was
		// ingested.  Don't poison the cache; just return the fallback so
		// future queries get a chance to find the row if it comes back.
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			log.Warn().Err(err).Str("data_source_id", dsID).Msg("resolveSource: data_sources lookup failed")
		}
		return fallbackSource, ""
	}
	resolvedSourceCache.Store(dsID, resolvedSourceEntry{
		kind:      row.Kind,
		name:      row.Name,
		expiresAt: time.Now().Add(resolvedSourceTTL),
	})
	return row.Kind, row.Name
}

// buildNotificationBody renders the IM / email message body for one
// AlertGroup.  Layout (top-to-bottom):
//
//  1. Header: 告警数量 (+ "同组 N 条" suffix when N>1).  alertname and
//     severity are intentionally omitted — they already live in the
//     adapter-rendered title bar (e.g. Slack header `[P3] route-name`),
//     repeating them here just adds visual noise.
//  2. Summary: annotations.summary on its own emphasised line, when set.
//  3. 消息源: a single line "kind : ds_name" (dsName joined with " : "
//     when non-empty) so operators immediately see which data source
//     row produced the alert without needing to click through to the
//     web UI.  Replaces the previous "**维度:**" KV-fanout that was a
//     near-duplicate of the new label-renaming "context" section.
//  4. 上下文 (annotations): all annotations except summary/description.
//  5. 详情: annotations.description as the trailing long-form block.
//
// All keys within a section are sorted alphabetically so the same group
// of alerts always renders to identical text — important for IM
// deduplication and for diffing notifications across re-sends.
//
// sourceKind / dsName are pre-resolved by the caller via
// Service.resolveSource so the renderer stays pure (no DB access in the
// hot path; no goroutine-unsafe state).  When the upstream alert had no
// data_source_id sourceKind falls back to RawAlert.Source (alertmanager,
// prometheus, webhook, ...) and dsName is empty.
func buildNotificationBody(group engine.AlertGroup, sourceKind, dsName string) string {
	var sb strings.Builder
	suffix := ""
	if len(group.Alerts) > 1 {
		suffix = fmt.Sprintf("（同组 %d 条，仅展示首条字段）", len(group.Alerts))
	}
	writeLine(&sb, fmt.Sprintf("**告警数量:** %d%s", len(group.Alerts), suffix))

	var first ingestion.RawAlert
	if len(group.Alerts) > 0 {
		first = group.Alerts[0]
	}

	if s := first.Annotations["summary"]; s != "" {
		sb.WriteString("\n")
		writeLine(&sb, truncateValue(s))
	}

	source := strings.TrimSpace(sourceKind)
	if source == "" {
		source = "unknown"
	}
	if name := strings.TrimSpace(dsName); name != "" {
		source = source + " : " + name
	}
	sb.WriteString("\n")
	writeLine(&sb, "**消息源:** "+source)

	writeKVSection(&sb, "**上下文:**", first.Annotations, skipAnnoKeys)

	if d := first.Annotations["description"]; d != "" {
		sb.WriteString("\n**详情:**\n")
		sb.WriteString(truncateValue(d))
	}

	return capBody(sb.String())
}

// firstAlertSource pulls RawAlert.Source off the head of an AlertGroup
// for use as the resolveSource fallback.  Returns "" when the group is
// empty (only happens in tests / pathological code paths) so the
// caller's downstream "" → "unknown" rendering keeps working.
func firstAlertSource(group engine.AlertGroup) string {
	if len(group.Alerts) == 0 {
		return ""
	}
	return group.Alerts[0].Source
}

// incidentDataSourceID prefers the DataSourceID stored on the incident
// row (it was captured at create-time from the very first alert that
// opened the incident, see HandleAlertGroup) over the per-call group's
// DataSourceID.  Reason: an incident may collect alerts from multiple
// data_source_ids if routing matchers overlap, and the operator-visible
// "where did this incident come from" should remain the source of the
// FIRST alert, not the latest re-notify trigger.  Falls through to the
// group when the incident row predates the column (legacy data).
func incidentDataSourceID(inc *model.Incident, group engine.AlertGroup) string {
	if inc != nil && inc.DataSourceID != nil && *inc.DataSourceID != "" {
		return *inc.DataSourceID
	}
	return group.DataSourceID
}

// writeLine appends s + "\n".  Pulled out so tests can pin the trailing
// newline contract without sprinkling magic strings.
func writeLine(sb *strings.Builder, s string) {
	sb.WriteString(s)
	sb.WriteString("\n")
}

// writeKVSection prints a markdown bullet list for every key in m that
// is not in skip and whose value is non-empty.  Keys are sorted
// alphabetically.  The whole section (including the heading) is omitted
// when no rows would be emitted.
func writeKVSection(sb *strings.Builder, heading string, m map[string]string, skip map[string]struct{}) {
	if len(m) == 0 {
		return
	}
	keys := make([]string, 0, len(m))
	for k, v := range m {
		if v == "" {
			continue
		}
		if _, dropped := skip[k]; dropped {
			continue
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return
	}
	sort.Strings(keys)
	sb.WriteString("\n")
	writeLine(sb, heading)
	for _, k := range keys {
		writeLine(sb, fmt.Sprintf("- %s: %s", k, truncateValue(m[k])))
	}
}

// truncateValue applies the per-row cap.  We measure in runes so a
// dense Chinese description doesn't get cut mid-codepoint.
func truncateValue(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if len(r) <= notifyValueMaxLen {
		return s
	}
	return string(r[:notifyValueMaxLen]) + fmt.Sprintf(" …(共 %d 字)", len(r))
}

// capBody applies the whole-message cap as a final safety net.  Same
// rune-aware slicing as truncateValue so we never split a multibyte
// codepoint in half.
func capBody(s string) string {
	r := []rune(s)
	if len(r) <= notifyBodyMaxLen {
		return s
	}
	return string(r[:notifyBodyMaxLen]) + " …(已截断)"
}

func convertAlerts(incidentID string, group engine.AlertGroup) []model.Alert {
	alerts := make([]model.Alert, 0, len(group.Alerts))
	for _, ra := range group.Alerts {
		labelsJSON, _ := json.Marshal(ra.Labels)
		annotationsJSON, _ := json.Marshal(ra.Annotations)
		alerts = append(alerts, model.Alert{
			IncidentID:  incidentID,
			Source:      ra.Source,
			Fingerprint: ra.Fingerprint,
			Labels:      labelsJSON,
			Annotations: annotationsJSON,
			StartsAt:    ra.StartsAt,
			EndsAt:      ra.EndsAt,
			Status:      ra.Status,
			RawPayload:  ra.RawPayload,
		})
	}
	return alerts
}
