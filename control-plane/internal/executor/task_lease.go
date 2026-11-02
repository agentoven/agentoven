package executor

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// ErrTaskOwned means another replica holds the lease on the task, so this one may not run it.
var ErrTaskOwned = fmt.Errorf("executor: the task is being run by another replica")

// DefaultLeaseTTL is how long a replica's claim on a task lasts without being renewed.
const DefaultLeaseTTL = 30 * time.Second

// SetLeases makes the manager take a lease from l before it runs a task and renew it while the task
// runs, so that replicas sharing a journal never run one task twice and can take over the tasks of a
// replica that died. owner names this replica (it must be unique among them; empty picks one from
// the host name and process ID). ttl is how long a lease lasts unrenewed (DefaultLeaseTTL). Call it
// before the manager is used. Without it a task simply belongs to its process.
func (m *TaskManager) SetLeases(l TaskLeases, owner string, ttl time.Duration) {
	if ttl <= 0 {
		ttl = DefaultLeaseTTL
	}
	if owner == "" {
		host, _ := os.Hostname()
		owner = fmt.Sprintf("%s-%d-%d", host, os.Getpid(), time.Now().UnixNano()%100000)
	}
	m.mu.Lock()
	m.leases, m.owner, m.leaseTTL = l, owner, ttl
	m.mu.Unlock()
}

// SetOnPause registers f to be told when a task stops for a person: a budget extension, a tool
// approval, or a stall. It is called once per pause, off the task's goroutine, with the task as it
// stands (never with its final answer). It is how a deployment notifies people that a task is waiting.
func (m *TaskManager) SetOnPause(f func(TaskSnapshot)) {
	m.mu.Lock()
	m.onPause = f
	m.mu.Unlock()
}

// Owner returns this replica's lease name ("" when leases are off).
func (m *TaskManager) Owner() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.owner
}

func (m *TaskManager) leaseConfig() (TaskLeases, string, time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.leases, m.owner, m.leaseTTL
}

// leaseHold is a task's lease while this replica holds it.
type leaseHold struct {
	stop chan struct{}
	once sync.Once
}

func (h *leaseHold) halt() { h.once.Do(func() { close(h.stop) }) }

// takeLease acquires the task's lease and starts renewing it. With leases off it does nothing.
// It returns ErrTaskOwned when another replica holds the task.
func (m *TaskManager) takeLease(ctx context.Context, t *task) error {
	leases, owner, ttl := m.leaseConfig()
	if leases == nil {
		return nil
	}
	started := time.Now()
	ok, err := leases.Acquire(context.WithoutCancel(ctx), t.snap.ID, owner, ttl)
	if err != nil {
		return fmt.Errorf("executor: could not take the task's lease: %w", err)
	}
	if !ok {
		return ErrTaskOwned
	}
	hold := &leaseHold{stop: make(chan struct{})}
	t.mu.Lock()
	if t.hold != nil {
		t.hold.halt()
	}
	t.hold, t.leaseLost = hold, false
	t.snap.Owner = owner
	t.mu.Unlock()
	go m.renewLoop(t, hold, leases, owner, ttl, started)
	return nil
}

// dropLease stops renewing and gives the lease up, unless it was already lost.
func (m *TaskManager) dropLease(t *task) {
	leases, owner, _ := m.leaseConfig()
	t.mu.Lock()
	hold, lost := t.hold, t.leaseLost
	t.hold = nil
	t.mu.Unlock()
	if hold == nil {
		return
	}
	hold.halt()
	if leases != nil && !lost {
		if err := leases.Release(context.Background(), t.snap.ID, owner); err != nil {
			log.Warn().Err(err).Str("task_id", t.snap.ID).Msg("Could not release a task lease; it will expire")
		}
	}
}

// renewLoop keeps the lease alive. The replica gives the task up on its own, before the lease can
// be taken by another replica, when it has not managed to renew for two thirds of the ttl: a replica
// cut off from the database must stop working on a task that someone else is about to run.
func (m *TaskManager) renewLoop(t *task, hold *leaseHold, leases TaskLeases, owner string, ttl time.Duration, lastOK time.Time) {
	tick := ttl / 3
	if tick <= 0 {
		tick = time.Millisecond
	}
	timer := time.NewTimer(tick)
	defer timer.Stop()
	for {
		select {
		case <-hold.stop:
			return
		case <-timer.C:
		}
		began := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), ttl/3+time.Millisecond)
		st, err := leases.Renew(ctx, t.snap.ID, owner, ttl)
		cancel()
		switch {
		case err == nil && st.Held:
			lastOK = began
			if st.CancelRequested {
				m.cancelLocal(t)
			}
		case err == nil && !st.Held:
			m.leaseLost(t, "the lease expired and was taken over")
			return
		default:
			if time.Since(lastOK) >= ttl*2/3 {
				m.leaseLost(t, fmt.Sprintf("the lease could not be renewed: %v", err))
				return
			}
		}
		timer.Reset(tick)
	}
}

// leaseLost stops the local run of a task that this replica no longer owns. Nothing is recorded in
// the journal: the task is the new owner's now.
func (m *TaskManager) leaseLost(t *task, why string) {
	log.Warn().Str("task_id", t.snap.ID).Str("why", why).Msg("Lost the lease on a task; stopping it here")
	t.mu.Lock()
	t.leaseLost = true
	cancel := t.cancel
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.cond.Broadcast()
}

// cancelLocal cancels a task running in this process, as Cancel does for a live task.
func (m *TaskManager) cancelLocal(t *task) {
	t.mu.Lock()
	if t.snap.Status.Terminal() {
		t.mu.Unlock()
		return
	}
	t.canceled = true
	cancel := t.cancel
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.cond.Broadcast()
}

// firePause tells the pause hook about a task that has just stopped for a person.
func (m *TaskManager) firePause(t *task) {
	m.mu.Lock()
	f := m.onPause
	m.mu.Unlock()
	if f == nil {
		return
	}
	snap := t.snapshot()
	snap.Result = "" // a pause never carries an answer
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error().Interface("panic", r).Str("task_id", snap.ID).Msg("A task pause hook panicked")
			}
		}()
		f(snap)
	}()
}

// holderOf reports the replica that holds a live lease on the task ("" when none or leases are off).
func (m *TaskManager) holderOf(ctx context.Context, id string) string {
	leases, _, _ := m.leaseConfig()
	if leases == nil {
		return ""
	}
	h, err := leases.Holder(ctx, id)
	if err != nil {
		return ""
	}
	return h
}

// StartAdoption looks, every interval until ctx ends, for tasks that were running when their replica
// died (the journal says running, no lease is held, and no replica has the task) and resumes them the
// way Recover does, with the same backoff and limit. Run it on every replica: a lease makes sure that
// exactly one of them takes each task.
func (m *TaskManager) StartAdoption(ctx context.Context, every time.Duration, maxRecoveries int, baseDelay time.Duration) {
	if every <= 0 {
		every = DefaultLeaseTTL
	}
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.Recover(ctx, maxRecoveries, baseDelay)
			}
		}
	}()
}

// beginRecovery marks a task as being recovered by this process. It is false if it already is.
func (m *TaskManager) beginRecovery(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.recovering == nil {
		m.recovering = map[string]bool{}
	}
	if m.recovering[id] {
		return false
	}
	m.recovering[id] = true
	return true
}

func (m *TaskManager) endRecovery(id string) {
	m.mu.Lock()
	delete(m.recovering, id)
	m.mu.Unlock()
}
