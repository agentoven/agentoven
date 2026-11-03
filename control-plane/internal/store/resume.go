package store

import (
	"context"
	"sync"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// What a store must hold for a recipe run to outlive the process that started it (ADR-0043).
//
// These are separate interfaces, not part of Store: a store that does not implement them keeps the old
// behaviour (a run lives in the memory of the process that started it), and the engine says so in its log.

// RunResumeStore keeps who is executing each run, and when a gate or wait of a run ends.
//
// A run is executed by the process that holds its lease. The lease is renewed while the process is
// alive and runs out when it is not, which is what lets another process (or the same one, restarted)
// take the run over. Time is the store's own clock wherever two processes could disagree about it: a
// lease, a timer.
type RunResumeStore interface {
	// ClaimRun leases a run to owner for ttl and records the digest of its recipe (when not empty). It
	// reports false when another owner holds an unexpired lease. Claiming a run the owner already holds
	// renews it.
	ClaimRun(ctx context.Context, runID, owner string, ttl time.Duration, digest string) (bool, error)

	// ClaimResumableRuns leases to owner up to limit top-level runs that are running or paused and that
	// nobody holds: no lease, or one that ran out. A run that began less than a minute ago and has no lease
	// yet is left alone, because its starter is about to claim it. The leased runs are returned.
	ClaimResumableRuns(ctx context.Context, owner string, ttl time.Duration, limit int) ([]models.ResumableRun, error)

	// RenewRunLeases extends every unexpired lease owner holds on a running or paused run and returns the
	// ids it still holds. A run missing from the answer was taken over; its executor must stop.
	RenewRunLeases(ctx context.Context, owner string, ttl time.Duration) ([]string, error)

	// ReleaseRunLeases ends every lease owner holds, so that a process started in its place can take the
	// runs over without waiting for the leases to run out. For a graceful shutdown.
	ReleaseRunLeases(ctx context.Context, owner string) error

	// ArmRunTimer starts the timer of a gate or wait step, d from now on the store's clock, unless the step
	// already has one, and returns how long is left on it. Called again after a restart it returns what is
	// left of the original timer, not a fresh one; a negative or zero result means it has run out.
	ArmRunTimer(ctx context.Context, runID, step string, d time.Duration) (left time.Duration, err error)

	// ClearRunTimer removes the timer of a step that has ended.
	ClearRunTimer(ctx context.Context, runID, step string) error
}

// ApprovalResolver decides an approval record only if it is still pending. Two decisions that arrive
// together (a click and a chat reply, an answer and an expiry, two replicas) cannot both win: the loser
// is told so and changes nothing.
type ApprovalResolver interface {
	// ResolveApproval writes the record's decision when the stored record is still pending and reports
	// whether it did.
	ResolveApproval(ctx context.Context, record *models.ApprovalRecord) (bool, error)
}

// freshRunGrace is how long a run with no lease is left to the process that created it.
const freshRunGrace = time.Minute

type memLease struct {
	owner  string
	until  time.Time
	digest string
}

// memoryResume is the memory store's lease table. It is not part of the snapshot: after a restart every
// run is unleased, which is exactly what lets it be resumed. The timers are in the snapshot.
type memoryResume struct {
	mu     sync.Mutex
	leases map[string]*memLease
}

func timerKey(runID, step string) string { return runID + "\x00" + step }

func (m *MemoryStore) resume() *memoryResume {
	m.resumeOnce.Do(func() {
		m.resumeState = &memoryResume{leases: map[string]*memLease{}}
	})
	return m.resumeState
}

func (m *MemoryStore) ClaimRun(_ context.Context, runID, owner string, ttl time.Duration, digest string) (bool, error) {
	r := m.resume()
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if l, ok := r.leases[runID]; ok && l.owner != owner && now.Before(l.until) {
		return false, nil
	}
	l := r.leases[runID]
	if l == nil {
		l = &memLease{}
		r.leases[runID] = l
	}
	l.owner, l.until = owner, now.Add(ttl)
	if digest != "" {
		l.digest = digest
	}
	return true, nil
}

func (m *MemoryStore) ClaimResumableRuns(_ context.Context, owner string, ttl time.Duration, limit int) ([]models.ResumableRun, error) {
	r := m.resume()
	now := time.Now()
	m.mu.RLock()
	var candidates []models.RecipeRun
	for _, run := range m.recipeRuns {
		if run.ParentRunID != "" || (run.Status != models.RecipeRunRunning && run.Status != models.RecipeRunPaused) {
			continue
		}
		candidates = append(candidates, *run)
	}
	m.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	var out []models.ResumableRun
	for _, run := range candidates {
		if limit > 0 && len(out) >= limit {
			break
		}
		l := r.leases[run.ID]
		if l == nil {
			if now.Sub(run.StartedAt) < freshRunGrace {
				continue
			}
			l = &memLease{}
			r.leases[run.ID] = l
		} else if now.Before(l.until) {
			continue
		}
		l.owner, l.until = owner, now.Add(ttl)
		out = append(out, models.ResumableRun{Run: run, Digest: l.digest})
	}
	return out, nil
}

func (m *MemoryStore) RenewRunLeases(_ context.Context, owner string, ttl time.Duration) ([]string, error) {
	r := m.resume()
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	var held []string
	for id, l := range r.leases {
		if l.owner == owner && now.Before(l.until) {
			l.until = now.Add(ttl)
			held = append(held, id)
		}
	}
	return held, nil
}

func (m *MemoryStore) ReleaseRunLeases(_ context.Context, owner string) error {
	r := m.resume()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.leases {
		if l.owner == owner {
			l.until = time.Time{}
		}
	}
	return nil
}

func (m *MemoryStore) ArmRunTimer(_ context.Context, runID, step string, d time.Duration) (time.Duration, error) {
	k := timerKey(runID, step)
	m.mu.Lock()
	at, ok := m.runTimers[k]
	if !ok {
		at = time.Now().Add(d)
		m.runTimers[k] = at
	}
	m.mu.Unlock()
	if !ok {
		m.requestSave() // the timer is part of the snapshot: a restart must find it
	}
	return time.Until(at), nil
}

func (m *MemoryStore) ClearRunTimer(_ context.Context, runID, step string) error {
	m.mu.Lock()
	_, ok := m.runTimers[timerKey(runID, step)]
	delete(m.runTimers, timerKey(runID, step))
	m.mu.Unlock()
	if ok {
		m.requestSave()
	}
	return nil
}

// ResolveApproval implements ApprovalResolver.
func (m *MemoryStore) ResolveApproval(_ context.Context, record *models.ApprovalRecord) (bool, error) {
	m.mu.Lock()
	cur, ok := m.approvals[record.GateKey]
	if !ok || cur.Status != "pending" {
		m.mu.Unlock()
		return false, nil
	}
	copy := *record
	m.approvals[record.GateKey] = &copy
	m.mu.Unlock()
	m.requestSave()
	return true, nil
}

var (
	_ RunResumeStore   = (*MemoryStore)(nil)
	_ ApprovalResolver = (*MemoryStore)(nil)
)
