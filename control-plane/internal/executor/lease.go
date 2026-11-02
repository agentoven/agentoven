package executor

import (
	"context"
	"sync"
	"time"
)

// TaskLeases decides which control-plane replica runs a task. A task is a goroutine in one process,
// so when several replicas share a journal something must say which of them owns a task, or two of
// them would resume the same interrupted task and run its tools twice. The answer is a lease: a
// replica takes it before it runs a task, renews it while it runs, and releases it when the task
// stops running (finished, paused for a person, or failed). A replica that dies stops renewing, the
// lease expires, and another replica may take the task over from its journal.
//
// Implementations must make Acquire atomic across replicas (Pro keeps the leases in PostgreSQL).
// The task manager does not install leases by default: with none, a task belongs to its process,
// which is right for one replica.
type TaskLeases interface {
	// Acquire takes the lease on a task for owner for ttl. It succeeds when nobody holds a live
	// lease on the task, or owner already does (which extends it). It reports whether owner holds it.
	Acquire(ctx context.Context, taskID, owner string, ttl time.Duration) (bool, error)
	// Renew extends owner's lease for ttl. Held is false when owner no longer holds it (it expired
	// and someone else took it, or it was released). CancelRequested is true when someone asked, with
	// RequestCancel, for the task to be cancelled while it runs here.
	Renew(ctx context.Context, taskID, owner string, ttl time.Duration) (LeaseState, error)
	// Release gives the lease up. Releasing one that is not held is not an error.
	Release(ctx context.Context, taskID, owner string) error
	// Holder returns the owner of a live lease on the task, "" when there is none.
	Holder(ctx context.Context, taskID string) (string, error)
	// RequestCancel asks the owner of a running task to cancel it, and reports whether a live
	// lease exists to deliver the request to. The owner sees it on its next Renew.
	RequestCancel(ctx context.Context, taskID string) (bool, error)
}

// LeaseState is what a renewal learned.
type LeaseState struct {
	Held            bool
	CancelRequested bool
}

// MemoryLeases is a TaskLeases for one process or for tests: replicas simulated in one process share
// one value of it the way real replicas share a database table.
type MemoryLeases struct {
	mu     sync.Mutex
	now    func() time.Time
	leases map[string]*memLease
}

type memLease struct {
	owner     string
	expires   time.Time
	cancelReq bool
}

// NewMemoryLeases returns an empty lease table. now may be nil for the wall clock.
func NewMemoryLeases(now func() time.Time) *MemoryLeases {
	if now == nil {
		now = time.Now
	}
	return &MemoryLeases{now: now, leases: map[string]*memLease{}}
}

var _ TaskLeases = (*MemoryLeases)(nil)

func (m *MemoryLeases) live(id string) *memLease {
	l := m.leases[id]
	if l != nil && !m.now().Before(l.expires) {
		delete(m.leases, id)
		return nil
	}
	return l
}

func (m *MemoryLeases) Acquire(_ context.Context, id, owner string, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l := m.live(id); l != nil && l.owner != owner {
		return false, nil
	} else if l != nil {
		l.expires = m.now().Add(ttl)
		return true, nil
	}
	m.leases[id] = &memLease{owner: owner, expires: m.now().Add(ttl)}
	return true, nil
}

func (m *MemoryLeases) Renew(_ context.Context, id, owner string, ttl time.Duration) (LeaseState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.live(id)
	if l == nil || l.owner != owner {
		return LeaseState{}, nil
	}
	l.expires = m.now().Add(ttl)
	return LeaseState{Held: true, CancelRequested: l.cancelReq}, nil
}

func (m *MemoryLeases) Release(_ context.Context, id, owner string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l := m.leases[id]; l != nil && l.owner == owner {
		delete(m.leases, id)
	}
	return nil
}

func (m *MemoryLeases) Holder(_ context.Context, id string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l := m.live(id); l != nil {
		return l.owner, nil
	}
	return "", nil
}

func (m *MemoryLeases) RequestCancel(_ context.Context, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l := m.live(id); l != nil {
		l.cancelReq = true
		return true, nil
	}
	return false, nil
}
