package sessions

import (
	"context"
	"sync"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/rs/zerolog/log"
)

// DefaultExpiryInterval is the default interval between expiry sweeps.
const DefaultExpiryInterval = 5 * time.Minute

// DefaultIdleTimeout is the default time after which idle sessions expire.
// Sessions with no messages for this duration are marked expired.
const DefaultIdleTimeout = 2 * time.Hour

// ExpiryJanitor periodically scans sessions and marks expired ones.
// A session is considered expired when:
//  1. ExpiresAt is set and in the past, OR
//  2. UpdatedAt + IdleTimeout is in the past (idle expiry)
type ExpiryJanitor struct {
	store       contracts.SessionStore
	interval    time.Duration
	idleTimeout time.Duration
	stopCh      chan struct{}
	mu          sync.Mutex
	running     bool
}

// NewExpiryJanitor creates a new session expiry janitor.
func NewExpiryJanitor(store contracts.SessionStore, interval, idleTimeout time.Duration) *ExpiryJanitor {
	if interval <= 0 {
		interval = DefaultExpiryInterval
	}
	if idleTimeout <= 0 {
		idleTimeout = DefaultIdleTimeout
	}
	return &ExpiryJanitor{
		store:       store,
		interval:    interval,
		idleTimeout: idleTimeout,
		stopCh:      make(chan struct{}),
	}
}

// Start begins the periodic expiry sweep. Safe to call multiple times.
func (j *ExpiryJanitor) Start() {
	j.mu.Lock()
	defer j.mu.Unlock()

	if j.running {
		return
	}
	j.running = true
	j.stopCh = make(chan struct{})

	go j.loop()
	log.Info().
		Dur("interval", j.interval).
		Dur("idle_timeout", j.idleTimeout).
		Msg("Session expiry janitor started")
}

// Stop halts the periodic sweep.
func (j *ExpiryJanitor) Stop() {
	j.mu.Lock()
	defer j.mu.Unlock()

	if !j.running {
		return
	}
	close(j.stopCh)
	j.running = false
	log.Info().Msg("Session expiry janitor stopped")
}

func (j *ExpiryJanitor) loop() {
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()

	for {
		select {
		case <-j.stopCh:
			return
		case <-ticker.C:
			j.sweep()
		}
	}
}

func (j *ExpiryJanitor) sweep() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	now := time.Now().UTC()
	expired := 0

	// For MemorySessionStore we can access the internal map directly.
	// For other implementations, we rely on the ListSessions interface.
	// Since ListSessions requires kitchen+agent, we use a type assertion
	// to access the full session map when possible.
	if memStore, ok := j.store.(*MemorySessionStore); ok {
		memStore.mu.Lock()
		defer memStore.mu.Unlock()

		for id, sess := range memStore.sessions {
			if shouldExpire(sess, now, j.idleTimeout) {
				sess.Status = models.SessionExpired
				sess.UpdatedAt = now
				expired++
				log.Debug().Str("session", id).Msg("Session expired by janitor")
			}
		}
	} else {
		// For non-memory stores, we'd need a SweepExpired method.
		// Log a warning and skip — the PG store will implement this natively.
		log.Debug().Msg("Session expiry sweep skipped (store does not support direct sweep)")
		_ = ctx // suppress unused warning
	}

	if expired > 0 {
		log.Info().Int("expired", expired).Msg("Session expiry sweep completed")
	}
}

// shouldExpire checks if a session should be marked as expired.
func shouldExpire(sess *models.Session, now time.Time, idleTimeout time.Duration) bool {
	// Only expire active or paused sessions
	if sess.Status != models.SessionActive && sess.Status != models.SessionPaused {
		return false
	}

	// Check explicit ExpiresAt
	if sess.ExpiresAt != nil && now.After(*sess.ExpiresAt) {
		return true
	}

	// Check idle timeout (no activity since UpdatedAt + idleTimeout)
	if !sess.UpdatedAt.IsZero() && now.After(sess.UpdatedAt.Add(idleTimeout)) {
		return true
	}

	return false
}
