package executor_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// stalledRenewals is a lease table seen from a process that is frozen: its renewals do not get
// through (they wait on the gate), exactly like the renewal goroutine of a paused VM.
type stalledRenewals struct {
	executor.TaskLeases
	gate chan struct{}
}

func (s *stalledRenewals) Renew(ctx context.Context, id, owner string, ttl time.Duration) (executor.LeaseState, error) {
	select {
	case <-s.gate:
	case <-ctx.Done():
		return executor.LeaseState{}, ctx.Err()
	}
	return s.TaskLeases.Renew(ctx, id, owner, ttl)
}

func TestMemoryLeaseEpochChangesWhenATaskChangesHands(t *testing.T) {
	now := time.Now()
	l := executor.NewMemoryLeases(func() time.Time { return now })
	ctx := context.Background()
	if _, held, _ := l.Epoch(ctx, "t", "a"); held {
		t.Fatal("nobody holds the task yet")
	}
	l.Acquire(ctx, "t", "a", time.Second)
	e1, held, _ := l.Epoch(ctx, "t", "a")
	if !held || e1 == 0 {
		t.Fatal("a holds it")
	}
	l.Acquire(ctx, "t", "a", time.Second) // extending is not a change of hands
	if e, _, _ := l.Epoch(ctx, "t", "a"); e != e1 {
		t.Fatal("extending a live lease keeps its epoch")
	}
	if _, held, _ := l.Epoch(ctx, "t", "b"); held {
		t.Fatal("b does not hold it")
	}
	now = now.Add(2 * time.Second) // expired, and then taken by the same owner: still a new epoch
	l.Acquire(ctx, "t", "a", time.Second)
	if e2, _, _ := l.Epoch(ctx, "t", "a"); e2 == e1 {
		t.Fatal("a lease that lapsed and was taken again must have a new epoch, or a frozen holder could not tell")
	}
	l.Release(ctx, "t", "a")
	l.Acquire(ctx, "t", "b", time.Second)
	if e3, held, _ := l.Epoch(ctx, "t", "b"); !held || e3 <= e1 {
		t.Fatal("epochs only grow")
	}
}

// Review finding 11: a process frozen for longer than the lease ttl wakes up with a tool call about to
// start while another replica has adopted the task. The tool must not run.
func TestAFrozenReplicaDoesNotRunAToolOfATaskAnotherReplicaAdopted(t *testing.T) {
	log, shared := newMemLog(), executor.NewMemoryLeases(nil)
	ctx := context.Background()

	// A's model call returns a tool call, but only once the freeze is over.
	thaw := make(chan struct{})
	da := &costDriver{in: 10, out: 5, replies: []turnReply{
		{toolCalls: []models.ToolCallResult{toolCall("c1", "lookup_order", map[string]interface{}{"order_id": "A1"})}},
	}}
	da.onCall = func(int) { <-thaw }
	var ranOnA int32
	gwA := newFakeGateway()
	gwA.register("lookup_order", func(map[string]interface{}) (string, bool) {
		atomic.AddInt32(&ranOnA, 1)
		return "charged the card", false
	})
	eA, _ := logExecutor(t, da, gwA, log)
	mA := executor.NewTaskManager(eA, executor.TaskManagerOptions{})
	frozen := &stalledRenewals{TaskLeases: shared, gate: make(chan struct{})}
	mA.SetLeases(frozen, "replica-a", testLeaseTTL)

	ag := testAgent()
	ag.MaxTurns = 10
	snap, err := mA.Submit(ctx, taskSpec(ag, resolvedWithTools(orderTool())))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "A to be inside its model call", func() bool { return da.calls() >= 1 })

	// The lease lapses (A's renewals are stuck) and replica B adopts the task.
	db := &costDriver{replies: []turnReply{{content: "finished on replica B"}}, in: 10, out: 5}
	b := newReplica(t, "replica-b", log, shared, db)
	waitFor(t, "B to adopt the task", func() bool {
		b.m.Recover(ctx, 3, time.Millisecond)
		return db.calls() > 0
	})

	// A thaws: its model call returns first, and the renewal that would tell it the lease is gone has
	// not run yet. That is the window.
	close(thaw)
	waitFor(t, "A to try its tool call", func() bool { return da.calls() >= 1 })
	time.Sleep(300 * time.Millisecond)
	if n := atomic.LoadInt32(&ranOnA); n != 0 {
		t.Fatalf("the frozen replica ran the tool %d time(s) after another replica adopted the task", n)
	}
	close(frozen.gate)
	waitFor(t, "A to stand down", func() bool {
		s, err := mA.Get(ctx, "default", snap.ID)
		return err != nil || s.Status != executor.TaskRunning || s.Owner != "replica-a"
	})
	if n := atomic.LoadInt32(&ranOnA); n != 0 {
		t.Fatalf("tool ran on A after it stood down: %d", n)
	}
}

// Without a lease (one replica) nothing is checked and tools run as before.
func TestATaskWithoutLeasesRunsItsToolsUnfenced(t *testing.T) {
	log := newMemLog()
	d := &costDriver{replies: []turnReply{
		{toolCalls: []models.ToolCallResult{toolCall("c1", "lookup_order", map[string]interface{}{"order_id": "A1"})}},
		{content: "done"},
	}, in: 10, out: 5}
	var ran int32
	gw := newFakeGateway()
	gw.register("lookup_order", func(map[string]interface{}) (string, bool) { atomic.AddInt32(&ran, 1); return "ok", false })
	e, _ := logExecutor(t, d, gw, log)
	m := executor.NewTaskManager(e, executor.TaskManagerOptions{})
	ag := testAgent()
	snap, err := m.Submit(context.Background(), taskSpec(ag, resolvedWithTools(orderTool())))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the task to finish", func() bool {
		s, _ := m.Get(context.Background(), "default", snap.ID)
		return s.Status.Terminal()
	})
	if atomic.LoadInt32(&ran) != 1 {
		t.Fatal("the tool must run once")
	}
}
