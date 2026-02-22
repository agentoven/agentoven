// Package retention implements data retention policies for the AgentOven
// control plane. It periodically archives and/or purges expired traces
// and audit events based on the kitchen's PlanLimits and ArchivePolicy.
//
// Retention windows:
//   - Community: 7 days for traces, 30 days for audit events
//   - Pro:       90 days for traces, 400 days for audit events
//   - Enterprise: configurable per kitchen
//
// Archive modes:
//   - none:              purge expired data (community default)
//   - archive-and-purge: archive to durable store, then delete from hot store (pro default)
//   - archive-only:      archive but keep in hot store (migration/validation)
//   - purge-only:        delete without archiving (explicit opt-in)
//
// The janitor runs as a background goroutine and respects context cancellation
// for graceful shutdown. Archive failures are fail-safe: data is NOT deleted
// if archiving fails.
package retention

import (
	"context"
	"sync"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// DefaultTraceRetentionDays is the community-tier trace retention.
const DefaultTraceRetentionDays = 7

// DefaultAuditRetentionDays is the community-tier audit event retention.
const DefaultAuditRetentionDays = 30

// DefaultArchiveBatchSize is the max records per archive write.
const DefaultArchiveBatchSize = 5000

// CycleStats tracks what happened in a single retention cycle.
type CycleStats struct {
	Kitchen        string
	TracesArchived int
	TracesPurged   int
	AuditArchived  int
	AuditPurged    int
	ArchiveRecords []models.ArchiveRecord
	Errors         []error
}

// Janitor periodically archives and purges expired data based on PlanLimits.
type Janitor struct {
	store    store.Store
	interval time.Duration

	// archiveDrivers is a registry of pluggable archive backends.
	archiveDrivers map[string]contracts.ArchiveDriver
	driverMu       sync.RWMutex

	// defaultBackend is used when a kitchen's policy doesn't specify one.
	defaultBackend string
}

// NewJanitor creates a new retention janitor that runs on the given interval.
func NewJanitor(s store.Store, interval time.Duration) *Janitor {
	if interval < time.Minute {
		interval = time.Hour // minimum 1 hour
	}
	return &Janitor{
		store:          s,
		interval:       interval,
		archiveDrivers: make(map[string]contracts.ArchiveDriver),
	}
}

// RegisterArchiver adds an archive driver. The first registered driver
// becomes the default backend for kitchens without explicit policy.
func (j *Janitor) RegisterArchiver(driver contracts.ArchiveDriver) {
	j.driverMu.Lock()
	defer j.driverMu.Unlock()
	kind := driver.Kind()
	if len(j.archiveDrivers) == 0 {
		j.defaultBackend = kind
	}
	j.archiveDrivers[kind] = driver
	log.Info().Str("kind", kind).Msg("Archive driver registered")
}

// SetDefaultBackend overrides which archive driver is used when a
// kitchen's policy doesn't specify a backend.
func (j *Janitor) SetDefaultBackend(kind string) {
	j.driverMu.Lock()
	defer j.driverMu.Unlock()
	j.defaultBackend = kind
}

// GetArchiver returns the registered driver for the given kind.
func (j *Janitor) GetArchiver(kind string) (contracts.ArchiveDriver, bool) {
	j.driverMu.RLock()
	defer j.driverMu.RUnlock()
	d, ok := j.archiveDrivers[kind]
	return d, ok
}

// ListArchivers returns the kinds of all registered archive drivers.
func (j *Janitor) ListArchivers() []string {
	j.driverMu.RLock()
	defer j.driverMu.RUnlock()
	kinds := make([]string, 0, len(j.archiveDrivers))
	for k := range j.archiveDrivers {
		kinds = append(kinds, k)
	}
	return kinds
}

// Start runs the janitor in a background goroutine. It blocks until ctx is canceled.
func (j *Janitor) Start(ctx context.Context) {
	log.Info().
		Dur("interval", j.interval).
		Strs("archivers", j.ListArchivers()).
		Str("default_backend", j.defaultBackend).
		Msg("Retention janitor started")

	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()

	// Run once immediately on startup
	j.runCycle(ctx)

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("Retention janitor stopped")
			return
		case <-ticker.C:
			j.runCycle(ctx)
		}
	}
}

// runCycle performs one retention sweep across all kitchens.
func (j *Janitor) runCycle(ctx context.Context) {
	start := time.Now()
	kitchens, err := j.store.ListKitchens(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("Retention janitor: failed to list kitchens")
		return
	}

	totalTraces := 0
	totalAudit := 0
	totalArchived := 0

	for _, kitchen := range kitchens {
		stats := j.processKitchen(ctx, kitchen)
		totalTraces += stats.TracesPurged
		totalAudit += stats.AuditPurged
		totalArchived += stats.TracesArchived + stats.AuditArchived

		for _, e := range stats.Errors {
			log.Warn().Err(e).Str("kitchen", kitchen.ID).Msg("Retention cycle error")
		}
	}

	elapsed := time.Since(start)
	if totalTraces > 0 || totalAudit > 0 || totalArchived > 0 {
		log.Info().
			Int("purged_traces", totalTraces).
			Int("purged_audit", totalAudit).
			Int("archived_records", totalArchived).
			Int("kitchens", len(kitchens)).
			Dur("elapsed", elapsed).
			Msg("Retention cycle complete")
	}
}

// processKitchen handles archive+purge for a single kitchen.
func (j *Janitor) processKitchen(ctx context.Context, kitchen models.Kitchen) CycleStats {
	stats := CycleStats{Kitchen: kitchen.ID}

	// Resolve retention windows
	traceRetentionDays, auditRetentionDays := j.resolveRetentionDays(ctx, kitchen)

	// Resolve archive policy
	policy := j.resolveArchivePolicy(ctx, kitchen)

	// Find expired traces
	traceCutoff := time.Now().AddDate(0, 0, -traceRetentionDays)
	expiredTraces, err := j.findExpiredTraces(ctx, kitchen.ID, traceCutoff)
	if err != nil {
		stats.Errors = append(stats.Errors, err)
		return stats
	}

	// Find expired audit events
	auditCutoff := time.Now().AddDate(0, 0, -auditRetentionDays)
	expiredAudit, err := j.findExpiredAuditEvents(ctx, kitchen.ID, auditCutoff)
	if err != nil {
		stats.Errors = append(stats.Errors, err)
		return stats
	}

	if len(expiredTraces) == 0 && len(expiredAudit) == 0 {
		return stats
	}

	// Execute based on archive mode
	switch policy.Mode {
	case models.ArchiveModeNone, models.ArchiveModePurgeOnly:
		// Purge without archiving
		j.purgeTraces(ctx, expiredTraces, &stats)
		j.purgeAuditEvents(ctx, expiredAudit, &stats)

	case models.ArchiveModeArchiveAndPurge:
		// Archive first, then purge only if archive succeeded
		archiveOK := j.archiveAndPurge(ctx, kitchen.ID, policy, expiredTraces, expiredAudit, &stats)
		if !archiveOK {
			log.Warn().Str("kitchen", kitchen.ID).Msg("Archive failed — skipping purge (fail-safe)")
		}

	case models.ArchiveModeArchiveOnly:
		// Archive only, do not purge
		j.archiveData(ctx, kitchen.ID, policy, expiredTraces, expiredAudit, &stats)

	default:
		// Unknown mode, default to purge-only for safety
		j.purgeTraces(ctx, expiredTraces, &stats)
		j.purgeAuditEvents(ctx, expiredAudit, &stats)
	}

	return stats
}

// resolveRetentionDays returns the trace and audit retention windows for a kitchen.
func (j *Janitor) resolveRetentionDays(ctx context.Context, kitchen models.Kitchen) (int, int) {
	limits := models.CommunityLimits()
	traceRetention := limits.MaxOutputRetentionDays
	auditRetention := limits.MaxAuditRetentionDays

	if traceRetention <= 0 {
		traceRetention = DefaultTraceRetentionDays
	}
	if auditRetention <= 0 {
		auditRetention = DefaultAuditRetentionDays
	}

	settings, err := j.store.GetKitchenSettings(ctx, kitchen.ID)
	if err == nil && settings != nil {
		if settings.MaxOutputRetentionDays > 0 {
			traceRetention = settings.MaxOutputRetentionDays
		}
		if settings.MaxAuditRetentionDays > 0 {
			auditRetention = settings.MaxAuditRetentionDays
		}
	}
	return traceRetention, auditRetention
}

// resolveArchivePolicy returns the effective archive policy for a kitchen.
func (j *Janitor) resolveArchivePolicy(ctx context.Context, kitchen models.Kitchen) models.ArchivePolicy {
	settings, err := j.store.GetKitchenSettings(ctx, kitchen.ID)
	if err == nil && settings != nil && settings.ArchivePolicy != nil {
		policy := *settings.ArchivePolicy
		// Fill in default backend if not specified
		if policy.Backend == "" {
			j.driverMu.RLock()
			policy.Backend = j.defaultBackend
			j.driverMu.RUnlock()
		}
		return policy
	}

	// Default: purge-only if no archivers registered, archive-and-purge if any are
	j.driverMu.RLock()
	hasArchivers := len(j.archiveDrivers) > 0
	defaultBackend := j.defaultBackend
	j.driverMu.RUnlock()

	if hasArchivers {
		return models.ArchivePolicy{
			Mode:    models.ArchiveModeArchiveAndPurge,
			Backend: defaultBackend,
		}
	}
	return models.ArchivePolicy{Mode: models.ArchiveModeNone}
}

// findExpiredTraces returns traces older than cutoff for the given kitchen.
func (j *Janitor) findExpiredTraces(ctx context.Context, kitchen string, cutoff time.Time) ([]models.Trace, error) {
	traces, err := j.store.ListTraces(ctx, kitchen, 10000)
	if err != nil {
		return nil, err
	}
	var expired []models.Trace
	for _, t := range traces {
		if t.CreatedAt.Before(cutoff) {
			expired = append(expired, t)
		}
	}
	return expired, nil
}

// findExpiredAuditEvents returns audit events older than cutoff for the given kitchen.
func (j *Janitor) findExpiredAuditEvents(ctx context.Context, kitchen string, cutoff time.Time) ([]models.AuditEvent, error) {
	filter := models.AuditFilter{Kitchen: kitchen, Limit: 10000}
	events, err := j.store.ListAuditEvents(ctx, filter)
	if err != nil {
		return nil, err
	}
	var expired []models.AuditEvent
	for _, e := range events {
		if e.Timestamp.Before(cutoff) {
			expired = append(expired, e)
		}
	}
	return expired, nil
}

// archiveAndPurge archives data, then purges only if archive succeeded.
func (j *Janitor) archiveAndPurge(ctx context.Context, kitchen string, policy models.ArchivePolicy, traces []models.Trace, audit []models.AuditEvent, stats *CycleStats) bool {
	ok := j.archiveData(ctx, kitchen, policy, traces, audit, stats)
	if !ok {
		return false
	}
	j.purgeTraces(ctx, traces, stats)
	j.purgeAuditEvents(ctx, audit, stats)
	return true
}

// archiveData writes expired data to the archive backend.
func (j *Janitor) archiveData(ctx context.Context, kitchen string, policy models.ArchivePolicy, traces []models.Trace, audit []models.AuditEvent, stats *CycleStats) bool {
	driver, ok := j.GetArchiver(policy.Backend)
	if !ok {
		log.Warn().
			Str("backend", policy.Backend).
			Str("kitchen", kitchen).
			Msg("Archive driver not found — cannot archive")
		stats.Errors = append(stats.Errors, &archiveError{backend: policy.Backend, msg: "driver not registered"})
		return false
	}

	allOK := true

	// Archive traces in batches
	if len(traces) > 0 {
		for i := 0; i < len(traces); i += DefaultArchiveBatchSize {
			end := i + DefaultArchiveBatchSize
			if end > len(traces) {
				end = len(traces)
			}
			batch := traces[i:end]

			uri, err := driver.ArchiveTraces(ctx, kitchen, batch)
			if err != nil {
				log.Warn().Err(err).
					Str("kitchen", kitchen).
					Str("backend", policy.Backend).
					Int("batch_size", len(batch)).
					Msg("Failed to archive traces")
				stats.Errors = append(stats.Errors, err)
				allOK = false
				continue
			}

			stats.TracesArchived += len(batch)
			stats.ArchiveRecords = append(stats.ArchiveRecords, models.ArchiveRecord{
				ID:          uuid.New().String(),
				Kitchen:     kitchen,
				DataKind:    "traces",
				RecordCount: len(batch),
				Backend:     policy.Backend,
				URI:         uri,
				Compressed:  policy.CompressArchives,
				Encrypted:   policy.EncryptionKeyID != "",
				OldestItem:  batch[len(batch)-1].CreatedAt,
				NewestItem:  batch[0].CreatedAt,
				CreatedAt:   time.Now().UTC(),
			})
		}
	}

	// Archive audit events in batches
	if len(audit) > 0 {
		for i := 0; i < len(audit); i += DefaultArchiveBatchSize {
			end := i + DefaultArchiveBatchSize
			if end > len(audit) {
				end = len(audit)
			}
			batch := audit[i:end]

			uri, err := driver.ArchiveAuditEvents(ctx, kitchen, batch)
			if err != nil {
				log.Warn().Err(err).
					Str("kitchen", kitchen).
					Str("backend", policy.Backend).
					Int("batch_size", len(batch)).
					Msg("Failed to archive audit events")
				stats.Errors = append(stats.Errors, err)
				allOK = false
				continue
			}

			stats.AuditArchived += len(batch)
			stats.ArchiveRecords = append(stats.ArchiveRecords, models.ArchiveRecord{
				ID:          uuid.New().String(),
				Kitchen:     kitchen,
				DataKind:    "audit_events",
				RecordCount: len(batch),
				Backend:     policy.Backend,
				URI:         uri,
				Compressed:  policy.CompressArchives,
				Encrypted:   policy.EncryptionKeyID != "",
				OldestItem:  batch[len(batch)-1].Timestamp,
				NewestItem:  batch[0].Timestamp,
				CreatedAt:   time.Now().UTC(),
			})
		}
	}

	return allOK
}

// purgeTraces deletes traces from the hot store.
func (j *Janitor) purgeTraces(ctx context.Context, traces []models.Trace, stats *CycleStats) {
	for _, t := range traces {
		if err := j.store.DeleteTrace(ctx, t.ID); err != nil {
			log.Warn().Err(err).Str("trace_id", t.ID).Msg("Failed to delete expired trace")
			stats.Errors = append(stats.Errors, err)
			continue
		}
		stats.TracesPurged++
	}
}

// purgeAuditEvents deletes audit events from the hot store.
func (j *Janitor) purgeAuditEvents(ctx context.Context, events []models.AuditEvent, stats *CycleStats) {
	for _, e := range events {
		if err := j.store.DeleteAuditEvent(ctx, e.ID); err != nil {
			log.Warn().Err(err).Str("event_id", e.ID).Msg("Failed to delete expired audit event")
			stats.Errors = append(stats.Errors, err)
			continue
		}
		stats.AuditPurged++
	}
}

// archiveError is a simple error type for archive failures.
type archiveError struct {
	backend string
	msg     string
}

func (e *archiveError) Error() string {
	return "archive driver " + e.backend + ": " + e.msg
}
