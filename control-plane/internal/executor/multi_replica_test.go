package executor_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// flakyLeases is a lease table seen through a network that can be cut: while cut, every call from
// this replica to the table fails, as when its database connection is gone.
type flakyLeases struct {
	executor.TaskLeases
	cut atomic.Bool
}

var errCut = errors.New("connection to the lease table lost")

func (f *flakyLeases) Acquire(ctx context.Context, id, owner string, ttl time.Duration) (bool, error) {
	if f.cut.Load() {
		return false, errCut
	}
	return f.TaskLeases.Acquire(ctx, id, owner, ttl)
}
func (f *flakyLeases) Renew(ctx context.Context, id, owner string, ttl time.Duration) (executor.LeaseState, error) {
	if f.cut.Load() {
		return executor.LeaseState{}, errCut
	}
	return f.TaskLeases.Renew(ctx, id, owner, ttl)
}
func (f *flakyLeases) Release(ctx context.Context, id, owner string) error {
	if f.cut.Load() {
		return errCut
	}
	return f.TaskLeases.Release(ctx, id, owner)
}

// replica is one control-plane process: its own executor, model driver, tools and task manager,
// over the journal and the lease table every replica shares.
type replica struct {
	name   string
	m      *executor.TaskManager
	e      *executor.Executor
	d      *costDriver
	gw     *fakeGateway
	leases *flakyLeases
	tools  *int32 // how many times this replica ran the lookup_order tool
}

const testLeaseTTL = 300 * time.Millisecond

func newReplica(t *testing.T, name string, log executor.RecordLog, shared *executor.MemoryLeases, d *costDriver) *replica {
	t.Helper()
	var ran int32
	gw := newFakeGateway()
	gw.register("lookup_order", func(map[string]interface{}) (string, bool) { atomic.AddInt32(&ran, 1); return "delivered", false })
	e, _ := logExecutor(t, d, gw, log)
	m := executor.NewTaskManager(e, executor.TaskManagerOptions{})
	fl := &flakyLeases{TaskLeases: shared}
	m.SetLeases(fl, name, testLeaseTTL)
	return &replica{name: name, m: m, e: e, d: d, gw: gw, leases: fl, tools: &ran}
}

func (r *replica) status(t *testing.T, id string) executor.TaskSnapshot {
	t.Helper()
	s, err := r.m.Get(context.Background(), "default", id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// blockOnSecondCall is a driver that answers the first call with a tool call and then hangs until
// its context ends, like a model call that never returns because the replica is gone.
func blockOnSecondCall() *costDriver {
	d := &costDriver{in: 10, out: 5}
	d.fn = func(req *models.RouteRequest) turnReply {
		return turnReply{toolCalls: []models.ToolCallResult{toolCall("c1", "lookup_order", map[string]interface{}{"order_id": "A1"})}}
	}
	return d
}

func TestASecondReplicaTakesOverATaskWhoseReplicaDied(t *testing.T) {
	log, leases := newMemLog(), executor.NewMemoryLeases(nil)

	// Replica A runs a task: one turn calls a tool, then its next model call hangs.
	da := &costDriver{in: 10, out: 5}
	var calls int32
	da.fn = func(req *models.RouteRequest) turnReply {
		if atomic.AddInt32(&calls, 1) == 1 {
			return turnReply{toolCalls: []models.ToolCallResult{toolCall("c1", "lookup_order", map[string]interface{}{"order_id": "A1"})}}
		}
		return turnReply{content: "never delivered"}
	}
	da.onCall = func(n int) {
		if n >= 2 {
			da.mu.Lock()
			da.delay = time.Hour // from the second call on, the model never answers
			da.mu.Unlock()
		}
	}
	a := newReplica(t, "replica-a", log, leases, da)

	db := &costDriver{replies: []turnReply{{content: "finished on replica B"}}, in: 10, out: 5}
	b := newReplica(t, "replica-b", log, leases, db)

	ag := testAgent()
	ag.MaxTurns = 10
	snap, err := a.m.Submit(context.Background(), taskSpec(ag, resolvedWithTools(orderTool())))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "A to be stuck on its second model call", func() bool { return da.calls() >= 2 })

	// While A is alive and renewing, B cannot touch the task.
	if s := b.status(t, snap.ID); s.Status != executor.TaskRunning || s.Owner != "replica-a" {
		t.Fatalf("B should see the task running on A: %+v", s)
	}
	if _, err := b.m.Resume(context.Background(), "default", "test-agent", snap.ID, nil, nil); !errors.Is(err, executor.ErrTaskNotResumable) {
		t.Fatalf("B must not resume a task A is running: %v", err)
	}
	b.m.Recover(context.Background(), 3, time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	if db.calls() != 0 {
		t.Fatal("B must not run a task A owns")
	}

	// A is cut off from the lease table (as a dead replica is). It stops the task itself, before
	// the lease can pass to anyone else.
	a.leases.cut.Store(true)
	waitFor(t, "A to give the task up", func() bool { return a.status(t, snap.ID).Status != executor.TaskRunning })
	if h, _ := leases.Holder(context.Background(), snap.ID); h == "replica-a" {
		// not yet expired in the table; A has stopped working but B must still wait
		b.m.Recover(context.Background(), 3, time.Millisecond)
		if db.calls() != 0 {
			t.Fatal("B must wait for the lease to expire")
		}
	}
	waitFor(t, "the lease to expire", func() bool { h, _ := leases.Holder(context.Background(), snap.ID); return h == "" })

	// B recovers it from the shared journal.
	b.m.Recover(context.Background(), 3, time.Millisecond)
	waitFor(t, "B to finish the task", func() bool {
		return b.status(t, snap.ID).Status == executor.TaskCompleted
	})
	got := b.status(t, snap.ID)
	if got.Result != "finished on replica B" || got.Turns != 2 {
		t.Fatalf("%+v", got)
	}
	if atomic.LoadInt32(a.tools) != 1 || atomic.LoadInt32(b.tools) != 0 {
		t.Fatalf("the tool call that already ran is not run again: A %d, B %d", *a.tools, *b.tools)
	}
	if db.calls() != 1 {
		t.Fatalf("B asked the model once, for the turn A never finished: %d", db.calls())
	}
	// Both replicas now tell the same story, and the journal has one ending.
	if s := a.status(t, snap.ID); s.Status != executor.TaskCompleted || s.Result != got.Result {
		t.Fatalf("A reads the task back from the journal: %+v", s)
	}
	js, _ := a.e.RunState(context.Background(), "default", snap.ID)
	if js.Outcome != executor.OutcomeCompleted || js.Recoveries != 1 {
		t.Fatalf("%+v", js)
	}
}

func TestOfTwoReplicasLookingAtADeadTaskExactlyOneRecoversIt(t *testing.T) {
	log, leases := newMemLog(), executor.NewMemoryLeases(nil)

	// A run that died after one turn, written by a replica that is gone.
	dead := &costDriver{replies: loopOf(2, "x")[:1], in: 10, out: 5}
	ed, _ := logExecutor(t, dead, orderGateway(), log)
	ag := testAgent()
	ag.MaxTurns = 10
	_, trace, err := ed.Execute(context.Background(), ag, "go", resolvedWithTools(orderTool()), nil, false)
	if err == nil {
		t.Fatal("expected the simulated crash")
	}

	a := newReplica(t, "replica-a", log, leases, &costDriver{replies: []turnReply{{content: "recovered by A"}}, in: 10, out: 5})
	b := newReplica(t, "replica-b", log, leases, &costDriver{replies: []turnReply{{content: "recovered by B"}}, in: 10, out: 5})

	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, r := range []*replica{a, b, a, b} {
		wg.Add(1)
		go func(r *replica) {
			defer wg.Done()
			<-start
			r.m.Recover(context.Background(), 3, time.Millisecond)
		}(r)
	}
	close(start)
	wg.Wait()
	waitFor(t, "completion", func() bool {
		s, err := a.m.Get(context.Background(), "default", trace.TraceID)
		return err == nil && s.Status == executor.TaskCompleted
	})
	if total := a.d.calls() + b.d.calls(); total != 1 {
		t.Fatalf("the task's next turn must be run once across replicas, ran %d times", total)
	}
	js, _ := a.e.RunState(context.Background(), "default", trace.TraceID)
	if js.Recoveries != 1 {
		t.Fatalf("one recovery was counted, not one per replica that looked: %d", js.Recoveries)
	}
}

func TestACancelFromAnotherReplicaReachesTheOneRunningTheTask(t *testing.T) {
	log, leases := newMemLog(), executor.NewMemoryLeases(nil)
	da := &costDriver{in: 10, out: 5, delay: time.Hour, replies: []turnReply{{content: "never"}}}
	a := newReplica(t, "replica-a", log, leases, da)
	b := newReplica(t, "replica-b", log, leases, &costDriver{in: 10, out: 5})

	snap, err := a.m.Submit(context.Background(), taskSpec(testAgent(), resolvedWithTools(orderTool())))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "A to be waiting on the model", func() bool { return da.calls() >= 1 })

	got, err := b.m.Cancel(context.Background(), "default", snap.ID)
	if err != nil || got.Status != executor.TaskRunning {
		t.Fatalf("B asks A to cancel; the task is still running until A acts on it: %+v %v", got, err)
	}
	waitFor(t, "A to cancel the task", func() bool { return a.status(t, snap.ID).Status == executor.TaskCancelled })
	js, _ := b.e.RunState(context.Background(), "default", snap.ID)
	if js.Outcome != executor.OutcomeCancelled {
		t.Fatalf("%+v", js)
	}
}

func TestAPausedTaskCanBeResumedOnAnotherReplicaExactlyOnce(t *testing.T) {
	log, leases := newMemLog(), executor.NewMemoryLeases(nil)
	a := newReplica(t, "replica-a", log, leases, &costDriver{replies: loopOf(10, "finished"), in: 90, out: 10})
	b := newReplica(t, "replica-b", log, leases, &costDriver{replies: []turnReply{{content: "finished on B"}}, in: 10, out: 5})
	c := newReplica(t, "replica-c", log, leases, &costDriver{replies: []turnReply{{content: "finished on C"}}, in: 10, out: 5})

	var pauses int32
	var pausedKind atomic.Value
	a.m.SetOnPause(func(s executor.TaskSnapshot) {
		atomic.AddInt32(&pauses, 1)
		pausedKind.Store(s.PauseKind)
		if s.Result != "" {
			t.Errorf("a pause never carries an answer: %+v", s)
		}
	})
	snap, _ := a.m.Submit(context.Background(), taskSpec(budgetAgent(models.RunBudget{MaxTokens: 350}), resolvedP(orderTool())))
	waitFor(t, "the pause", func() bool { return a.status(t, snap.ID).Status == executor.TaskPaused })
	waitFor(t, "the pause hook", func() bool { return atomic.LoadInt32(&pauses) == 1 })
	if pausedKind.Load() != "budget" {
		t.Fatalf("%v", pausedKind.Load())
	}
	if s := b.status(t, snap.ID); s.Status != executor.TaskPaused {
		t.Fatalf("B sees the pause too: %+v", s)
	}

	var wg sync.WaitGroup
	var started int32
	for _, r := range []*replica{b, c, a} {
		wg.Add(1)
		go func(r *replica) {
			defer wg.Done()
			_, err := r.m.Resume(context.Background(), "default", "test-agent", snap.ID,
				&executor.ResumeDecision{Approved: true, Extend: &models.BudgetExtension{ID: "ext-1", AddTokens: 9000}}, nil)
			if err == nil {
				atomic.AddInt32(&started, 1)
			}
		}(r)
	}
	wg.Wait()
	if started != 1 {
		t.Fatalf("one replica resumes a paused task, %d did", started)
	}
	waitFor(t, "completion", func() bool {
		s, err := a.m.Get(context.Background(), "default", snap.ID)
		return err == nil && s.Status == executor.TaskCompleted
	})
	// A late resume on a replica that remembered the task as paused is refused, not run.
	if _, err := a.m.Resume(context.Background(), "default", "test-agent", snap.ID, &executor.ResumeDecision{Approved: true, Extend: &models.BudgetExtension{AddTokens: 1}}, nil); !errors.Is(err, executor.ErrTaskNotResumable) {
		t.Fatalf("%v", err)
	}
	if n := atomic.LoadInt32(&pauses); n != 1 {
		t.Fatalf("one pause, one notification: %d", n)
	}
}

func TestWithoutLeasesATaskStillBelongsToItsProcess(t *testing.T) {
	d := &costDriver{replies: []turnReply{{content: "ok"}}, in: 10, out: 5}
	m, _, _ := newTasks(t, d, orderGateway(), executor.TaskManagerOptions{})
	snap, err := m.Submit(context.Background(), taskSpec(testAgent(), resolvedWithTools()))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "completion", func() bool { return status(t, m, snap.ID) == executor.TaskCompleted })
	if got, _ := m.Get(context.Background(), "default", snap.ID); got.Owner != "" {
		t.Fatalf("no replicas, no owner: %+v", got)
	}
}

func TestAnAdoptionLoopFindsATaskItsReplicaLeftBehind(t *testing.T) {
	log, leases := newMemLog(), executor.NewMemoryLeases(nil)
	dead := &costDriver{replies: loopOf(2, "x")[:1], in: 10, out: 5}
	ed, _ := logExecutor(t, dead, orderGateway(), log)
	ag := testAgent()
	ag.MaxTurns = 10
	_, trace, err := ed.Execute(context.Background(), ag, "go", resolvedWithTools(orderTool()), nil, false)
	if err == nil {
		t.Fatal("expected the simulated crash")
	}
	b := newReplica(t, "replica-b", log, leases, &costDriver{replies: []turnReply{{content: "adopted"}}, in: 10, out: 5})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.m.StartAdoption(ctx, 20*time.Millisecond, 3, time.Millisecond)
	waitFor(t, "adoption", func() bool {
		s, err := b.m.Get(context.Background(), "default", trace.TraceID)
		return err == nil && s.Status == executor.TaskCompleted
	})
}
