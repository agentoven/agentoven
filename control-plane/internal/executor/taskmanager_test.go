package executor_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/google/uuid"
)

func taskSpec(agent *models.Agent, resolved *models.ResolvedIngredients) executor.TaskSpec {
	return executor.TaskSpec{ID: uuid.New().String(), Agent: agent, Resolved: resolved, Message: models.ChatMessage{Role: "user", Content: "do the work"}}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func status(t *testing.T, m *executor.TaskManager, id string) executor.TaskStatus {
	t.Helper()
	s, err := m.Get(context.Background(), "default", id)
	if err != nil {
		t.Fatal(err)
	}
	return s.Status
}

func newTasks(t *testing.T, d *costDriver, gw *fakeGateway, opts executor.TaskManagerOptions) (*executor.TaskManager, *executor.Executor, string) {
	t.Helper()
	e, _, dir := journalDirExecutor(t, d, gw)
	return executor.NewTaskManager(e, opts), e, dir
}

func TestATaskOutlivesTheRequestThatStartedIt(t *testing.T) {
	d := &costDriver{replies: loopOf(3, "the report"), in: 10, out: 5}
	m, _, _ := newTasks(t, d, orderGateway(), executor.TaskManagerOptions{})

	reqCtx, cancelReq := context.WithCancel(context.Background())
	a := testAgent()
	a.MaxTurns = 20
	snap, err := m.Submit(reqCtx, taskSpec(a, resolvedWithTools(orderTool())))
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != executor.TaskQueued && snap.Status != executor.TaskRunning {
		t.Fatalf("Submit should return at once with a live task, got %s", snap.Status)
	}
	cancelReq() // the HTTP request that submitted it is gone

	waitFor(t, "completion", func() bool { return status(t, m, snap.ID) == executor.TaskCompleted })
	got, _ := m.Get(context.Background(), "default", snap.ID)
	if got.Result != "the report" || got.Turns != 4 || got.Usage.TotalTokens != 60 {
		t.Fatalf("unexpected result %+v", got)
	}
}

func TestATasksIDIsItsTraceIDAndItsJournal(t *testing.T) {
	d := &costDriver{replies: []turnReply{{content: "ok"}}, in: 10, out: 5}
	m, e, dir := newTasks(t, d, orderGateway(), executor.TaskManagerOptions{})
	snap, err := m.Submit(context.Background(), taskSpec(testAgent(), resolvedWithTools()))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "completion", func() bool { return status(t, m, snap.ID) == executor.TaskCompleted })
	if _, err := os.Stat(filepath.Join(dir, snap.ID+".jsonl")); err != nil {
		t.Fatalf("the task's journal should be named by its ID: %v", err)
	}
	if js, err := e.RunState(context.Background(), "default", snap.ID); err != nil || js.Outcome != executor.OutcomeCompleted {
		t.Fatalf("%v %+v", err, js)
	}
}

func TestADelegatedChildDoesNotReuseTheTasksID(t *testing.T) {
	d := &costDriver{in: 10, out: 5}
	var parentCalls int32
	d.fn = func(req *models.RouteRequest) turnReply {
		for _, m := range req.Messages {
			if m.Role == "user" && m.Content == "child job" {
				return turnReply{content: "child done"}
			}
		}
		if atomic.AddInt32(&parentCalls, 1) == 1 {
			return turnReply{toolCalls: []models.ToolCallResult{toolCall("1", "agentoven_delegate", map[string]interface{}{"agent": "helper", "message": "child job"})}}
		}
		return turnReply{content: "parent done"}
	}
	e, _, dir, s := journalDirExecutorStore(t, d, orderGateway())
	helper := testAgent()
	helper.Name = "helper"
	helper.Status = models.AgentStatusReady
	helper.ResolvedConfig = resolvedWithTools()
	if err := s.CreateAgent(context.Background(), helper); err != nil {
		t.Fatal(err)
	}
	m := executor.NewTaskManager(e, executor.TaskManagerOptions{})
	snap, err := m.Submit(context.Background(), taskSpec(testAgent(), resolvedWithTools()))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "completion", func() bool { return status(t, m, snap.ID) == executor.TaskCompleted })
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(files) != 2 {
		t.Fatalf("expected the task's journal and its child's, got %v", files)
	}
}

func TestATaskPausesAtItsBudgetAndContinuesAfterAnExtension(t *testing.T) {
	d := &costDriver{replies: loopOf(10, "finished"), in: 90, out: 10}
	m, _, _ := newTasks(t, d, orderGateway(), executor.TaskManagerOptions{})
	a := budgetAgent(models.RunBudget{MaxTokens: 350})
	snap, _ := m.Submit(context.Background(), taskSpec(a, resolvedP(orderTool())))
	waitFor(t, "pause", func() bool { return status(t, m, snap.ID) == executor.TaskPaused })

	got, _ := m.Get(context.Background(), "default", snap.ID)
	if got.PauseKind != "budget" || got.BudgetPause == nil || got.BudgetPause.Dimension != models.BudgetDimTokens {
		t.Fatalf("a budget-paused task should say so: %+v", got)
	}

	if _, err := m.Resume(context.Background(), "default", "test-agent", snap.ID, &executor.ResumeDecision{Approved: true, Extend: &models.BudgetExtension{AddTokens: 5000}}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "completion", func() bool { return status(t, m, snap.ID) == executor.TaskCompleted })
	if r, _ := m.Get(context.Background(), "default", snap.ID); r.Result != "finished" || r.Attempts != 2 {
		t.Fatalf("%+v", r)
	}
}

func TestOnlyOneOfSeveralResumesOfATaskStarts(t *testing.T) {
	d := &costDriver{replies: loopOf(10, "finished"), in: 90, out: 10}
	m, _, _ := newTasks(t, d, orderGateway(), executor.TaskManagerOptions{})
	snap, _ := m.Submit(context.Background(), taskSpec(budgetAgent(models.RunBudget{MaxTokens: 350}), resolvedP(orderTool())))
	waitFor(t, "pause", func() bool { return status(t, m, snap.ID) == executor.TaskPaused })

	var wg sync.WaitGroup
	var ok int32
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.Resume(context.Background(), "default", "test-agent", snap.ID, &executor.ResumeDecision{Approved: true, Extend: &models.BudgetExtension{AddTokens: 5000}}, nil)
			if err == nil {
				atomic.AddInt32(&ok, 1)
			}
		}()
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("%d resumes started, want exactly 1", ok)
	}
	waitFor(t, "completion", func() bool { return status(t, m, snap.ID) == executor.TaskCompleted })
}

func TestCancellingARunningTaskStopsItAndEndsItsJournal(t *testing.T) {
	d := &costDriver{replies: loopOf(50, "never"), in: 10, out: 5, delay: 50 * time.Millisecond}
	m, e, _ := newTasks(t, d, orderGateway(), executor.TaskManagerOptions{})
	a := testAgent()
	a.MaxTurns = 100
	snap, _ := m.Submit(context.Background(), taskSpec(a, resolvedWithTools(orderTool())))
	waitFor(t, "to be running", func() bool { return d.calls() >= 2 })

	if _, err := m.Cancel(context.Background(), "default", snap.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "cancelled", func() bool { return status(t, m, snap.ID) == executor.TaskCancelled })
	calls := d.calls()
	time.Sleep(150 * time.Millisecond)
	if d.calls() > calls+1 {
		t.Fatalf("a cancelled task kept calling the model: %d -> %d", calls, d.calls())
	}
	js, err := e.RunState(context.Background(), "default", snap.ID)
	if err != nil || js.Status != executor.RunDone || js.Outcome != executor.OutcomeCancelled {
		t.Fatalf("the journal should record the cancel: %v %+v", err, js)
	}
	if un, _ := e.ListUnfinished(context.Background(), "default"); len(un) != 0 {
		t.Fatalf("a cancelled task must not be offered for recovery: %+v", un)
	}
}

func TestCancelRacingCompletionLeavesOneConsistentOutcome(t *testing.T) {
	for i := 0; i < 20; i++ {
		d := &costDriver{replies: loopOf(2, "done"), in: 10, out: 5}
		m, _, _ := newTasks(t, d, orderGateway(), executor.TaskManagerOptions{})
		a := testAgent()
		a.MaxTurns = 10
		snap, _ := m.Submit(context.Background(), taskSpec(a, resolvedWithTools(orderTool())))
		var wg sync.WaitGroup
		for k := 0; k < 4; k++ {
			wg.Add(1)
			go func() { defer wg.Done(); _, _ = m.Cancel(context.Background(), "default", snap.ID) }()
		}
		wg.Wait()
		waitFor(t, "a terminal state", func() bool { return status(t, m, snap.ID).Terminal() })
		final := status(t, m, snap.ID)
		time.Sleep(20 * time.Millisecond)
		if again := status(t, m, snap.ID); again != final {
			t.Fatalf("the outcome changed after it was final: %s -> %s", final, again)
		}
		if final != executor.TaskCompleted && final != executor.TaskCancelled {
			t.Fatalf("unexpected outcome %s", final)
		}
	}
}

func TestSlotsAreLimitedPerKitchenAndExcessIsQueuedThenRefused(t *testing.T) {
	gate := make(chan struct{})
	d := &costDriver{replies: []turnReply{{content: "a"}, {content: "b"}, {content: "c"}, {content: "d"}}, in: 10, out: 5,
		onCall: func(int) { <-gate }}
	m, _, _ := newTasks(t, d, orderGateway(), executor.TaskManagerOptions{PerKitchen: 1, QueueMax: 1})

	submit := func() (executor.TaskSnapshot, error) {
		return m.Submit(context.Background(), taskSpec(testAgent(), resolvedWithTools()))
	}
	first, err := submit()
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first running", func() bool { return status(t, m, first.ID) == executor.TaskRunning })
	second, err := submit()
	if err != nil {
		t.Fatal(err)
	}
	if status(t, m, second.ID) != executor.TaskQueued {
		t.Fatalf("the second task should wait for a slot, got %s", status(t, m, second.ID))
	}
	if _, err := submit(); !errors.Is(err, executor.ErrTaskQueueFull) {
		t.Fatalf("with the slot taken and the queue full the third is refused, got %v", err)
	}

	close(gate)
	waitFor(t, "both to finish", func() bool {
		return status(t, m, first.ID) == executor.TaskCompleted && status(t, m, second.ID) == executor.TaskCompleted
	})
}

func TestACancelledQueuedTaskNeverRuns(t *testing.T) {
	gate := make(chan struct{})
	d := &costDriver{replies: []turnReply{{content: "a"}, {content: "b"}}, in: 10, out: 5, onCall: func(int) { <-gate }}
	m, _, _ := newTasks(t, d, orderGateway(), executor.TaskManagerOptions{PerKitchen: 1, QueueMax: 4})
	first, _ := m.Submit(context.Background(), taskSpec(testAgent(), resolvedWithTools()))
	waitFor(t, "first running", func() bool { return status(t, m, first.ID) == executor.TaskRunning })
	second, _ := m.Submit(context.Background(), taskSpec(testAgent(), resolvedWithTools()))

	if _, err := m.Cancel(context.Background(), "default", second.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second cancelled", func() bool { return status(t, m, second.ID) == executor.TaskCancelled })
	close(gate)
	waitFor(t, "first done", func() bool { return status(t, m, first.ID) == executor.TaskCompleted })
	if d.calls() != 1 {
		t.Fatalf("the cancelled task reached the model: %d calls", d.calls())
	}
}

func TestATaskIsInvisibleFromAnotherKitchen(t *testing.T) {
	d := &costDriver{replies: []turnReply{{content: "ok"}}, in: 10, out: 5}
	m, _, _ := newTasks(t, d, orderGateway(), executor.TaskManagerOptions{})
	snap, _ := m.Submit(context.Background(), taskSpec(testAgent(), resolvedWithTools()))
	waitFor(t, "completion", func() bool { return status(t, m, snap.ID) == executor.TaskCompleted })

	ctx := context.Background()
	if _, err := m.Get(ctx, "other", snap.ID); !errors.Is(err, executor.ErrTaskNotFound) {
		t.Fatalf("Get across kitchens: %v", err)
	}
	if _, err := m.Cancel(ctx, "other", snap.ID); !errors.Is(err, executor.ErrTaskNotFound) {
		t.Fatalf("Cancel across kitchens: %v", err)
	}
	if _, err := m.Resume(ctx, "other", "test-agent", snap.ID, nil, nil); !errors.Is(err, executor.ErrTaskNotFound) {
		t.Fatalf("Resume across kitchens: %v", err)
	}
	if _, _, _, err := m.Events("other", snap.ID, 0); !errors.Is(err, executor.ErrTaskNotFound) {
		t.Fatalf("Events across kitchens: %v", err)
	}
	if list := m.List(ctx, "other"); len(list) != 0 {
		t.Fatalf("List leaked %+v", list)
	}
}

func TestEventsHaveIncreasingSequenceNumbersAndCanBeResumedFromOne(t *testing.T) {
	d := &costDriver{replies: loopOf(2, "done"), in: 10, out: 5}
	m, _, _ := newTasks(t, d, orderGateway(), executor.TaskManagerOptions{})
	a := testAgent()
	a.MaxTurns = 10
	snap, _ := m.Submit(context.Background(), taskSpec(a, resolvedWithTools(orderTool())))
	waitFor(t, "completion", func() bool { return status(t, m, snap.ID) == executor.TaskCompleted })

	all, done, _, err := m.Events("default", snap.ID, 0)
	if err != nil || !done || len(all) < 6 {
		t.Fatalf("%v done=%v events=%d", err, done, len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i].Seq <= all[i-1].Seq {
			t.Fatalf("sequence numbers must increase: %d then %d", all[i-1].Seq, all[i].Seq)
		}
	}
	mid := all[len(all)/2].Seq
	rest, _, _, _ := m.Events("default", snap.ID, mid)
	if len(rest) != len(all)-len(all)/2-1 {
		t.Fatalf("resuming after seq %d should give the remaining %d events, got %d", mid, len(all)-len(all)/2-1, len(rest))
	}
	last := all[len(all)-1].Event
	if last.Type != executor.EventTaskStatus || last.Content != string(executor.TaskCompleted) {
		t.Fatalf("the log should end with the final status, got %+v", last)
	}
}

func TestEventsAreRedactedBeforeAnyViewerSeesThem(t *testing.T) {
	d := &costDriver{replies: []turnReply{{content: "the secret is 4111"}}, in: 10, out: 5}
	m, _, _ := newTasks(t, d, orderGateway(), executor.TaskManagerOptions{})
	spec := taskSpec(testAgent(), resolvedWithTools())
	spec.Redact = func(ev executor.Event) executor.Event {
		ev.Content = strings.ReplaceAll(ev.Content, "4111", "[redacted]")
		return ev
	}
	snap, _ := m.Submit(context.Background(), spec)
	waitFor(t, "completion", func() bool { return status(t, m, snap.ID) == executor.TaskCompleted })
	evs, _, _, _ := m.Events("default", snap.ID, 0)
	for _, ev := range evs {
		if strings.Contains(ev.Event.Content, "4111") {
			t.Fatalf("an unredacted event reached the log: %+v", ev.Event)
		}
	}
}

func TestAFailedRunIsInterruptedAndResumable(t *testing.T) {
	d := &costDriver{replies: loopOf(2, "x")[:2], in: 10, out: 5} // two turns, then the model "goes down"
	m, e, _ := newTasks(t, d, orderGateway(), executor.TaskManagerOptions{})
	a := testAgent()
	a.MaxTurns = 10
	snap, _ := m.Submit(context.Background(), taskSpec(a, resolvedWithTools(orderTool())))
	waitFor(t, "interrupted", func() bool { return status(t, m, snap.ID) == executor.TaskInterrupted })
	got, _ := m.Get(context.Background(), "default", snap.ID)
	if got.Error == "" || got.Turns != 2 {
		t.Fatalf("%+v", got)
	}

	d.mu.Lock()
	d.replies = append(d.replies, turnReply{content: "back up"})
	d.mu.Unlock()
	if _, err := m.Resume(context.Background(), "default", "test-agent", snap.ID, nil, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "completion", func() bool { return status(t, m, snap.ID) == executor.TaskCompleted })
	if js, _ := e.RunState(context.Background(), "default", snap.ID); js.Outcome != executor.OutcomeCompleted {
		t.Fatalf("%+v", js)
	}
}

func TestATaskThatCouldNotStartIsFailedNotInterrupted(t *testing.T) {
	d := &costDriver{in: 10, out: 5}
	m, _, _ := newTasks(t, d, orderGateway(), executor.TaskManagerOptions{})
	// A dollar cap on an unpriced model is refused before the run exists.
	a := budgetAgent(models.RunBudget{MaxCostUSD: 1})
	snap, _ := m.Submit(context.Background(), taskSpec(a, resolvedP()))
	waitFor(t, "failure", func() bool { return status(t, m, snap.ID) == executor.TaskFailed })
	if got, _ := m.Get(context.Background(), "default", snap.ID); !strings.Contains(got.Error, "no known price") {
		t.Fatalf("%+v", got)
	}
}

func TestAFreshProcessReadsTasksBackFromTheJournal(t *testing.T) {
	d := &costDriver{replies: loopOf(10, "finished"), in: 90, out: 10}
	m, e, dir := newTasks(t, d, orderGateway(), executor.TaskManagerOptions{})
	snap, _ := m.Submit(context.Background(), taskSpec(budgetAgent(models.RunBudget{MaxTokens: 350}), resolvedP(orderTool())))
	waitFor(t, "pause", func() bool { return status(t, m, snap.ID) == executor.TaskPaused })

	// A new process: a new executor and manager over the same journal directory.
	d2 := &costDriver{replies: []turnReply{{content: "finished after restart"}}, in: 10, out: 5}
	e2, _, _ := newBudgetExecutor(t, d2, orderGateway(), false)
	e2.SetJournal(mustJournal(t, dir))
	_ = e
	m2 := executor.NewTaskManager(e2, executor.TaskManagerOptions{})

	got, err := m2.Get(context.Background(), "default", snap.ID)
	if err != nil || got.Status != executor.TaskPaused || got.PauseKind != "budget" {
		t.Fatalf("after a restart the task should still be paused at its budget: %v %+v", err, got)
	}
	if list := m2.List(context.Background(), "default"); len(list) != 1 || list[0].ID != snap.ID {
		t.Fatalf("the paused task should be listed: %+v", list)
	}
	if _, err := m2.Resume(context.Background(), "default", "test-agent", snap.ID, &executor.ResumeDecision{Approved: true, Extend: &models.BudgetExtension{AddTokens: 9000}}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "completion", func() bool { return status(t, m2, snap.ID) == executor.TaskCompleted })
}

func TestRecoveryResumesInterruptedTasksAndGivesUpOnOnesThatKeepFailing(t *testing.T) {
	// A run that died: header and one turn on disk, nothing finished it.
	d := &costDriver{replies: loopOf(2, "x")[:1], in: 10, out: 5}
	e1, j, dir := journalDirExecutor(t, d, orderGateway())
	a := testAgent()
	a.MaxTurns = 10
	_, trace, err := e1.Execute(context.Background(), a, "go", resolvedWithTools(orderTool()), nil, false)
	if err == nil {
		t.Fatal("expected the simulated crash")
	}
	_ = j

	// Always failing: every recovery dies again.
	bad := &costDriver{in: 10, out: 5}
	e2, _, _ := newBudgetExecutor(t, bad, orderGateway(), false)
	e2.SetJournal(mustJournal(t, dir))
	m2 := executor.NewTaskManager(e2, executor.TaskManagerOptions{})
	for i := 0; i < 5; i++ {
		m2.Recover(context.Background(), 3, time.Millisecond)
		time.Sleep(40 * time.Millisecond)
	}
	js, _ := e2.RunState(context.Background(), "default", trace.TraceID)
	if js.Recoveries != 3 {
		t.Fatalf("recovery should stop after 3 tries, recorded %d", js.Recoveries)
	}
	bad.mu.Lock()
	attempts := len(bad.requests)
	bad.mu.Unlock()
	if attempts != 3 {
		t.Fatalf("expected exactly 3 recovery attempts to reach the model, got %d", attempts)
	}

	// A fresh run that dies once and recovers cleanly.
	d3 := &costDriver{replies: loopOf(2, "x")[:1], in: 10, out: 5}
	e3, _, dir3 := journalDirExecutor(t, d3, orderGateway())
	_, trace3, _ := e3.Execute(context.Background(), a, "go", resolvedWithTools(orderTool()), nil, false)
	good := &costDriver{replies: []turnReply{{content: "recovered"}}, in: 10, out: 5}
	e4, _, _ := newBudgetExecutor(t, good, orderGateway(), false)
	e4.SetJournal(mustJournal(t, dir3))
	m4 := executor.NewTaskManager(e4, executor.TaskManagerOptions{})
	m4.Recover(context.Background(), 3, time.Millisecond)
	waitFor(t, "recovered completion", func() bool {
		s, err := m4.Get(context.Background(), "default", trace3.TraceID)
		return err == nil && s.Status == executor.TaskCompleted
	})
}

func TestFinishedTasksLeaveMemoryButStayReadable(t *testing.T) {
	d := &costDriver{replies: []turnReply{{content: "ok"}}, in: 10, out: 5}
	m, _, _ := newTasks(t, d, orderGateway(), executor.TaskManagerOptions{Retain: 20 * time.Millisecond})
	snap, _ := m.Submit(context.Background(), taskSpec(testAgent(), resolvedWithTools()))
	waitFor(t, "completion", func() bool { return status(t, m, snap.ID) == executor.TaskCompleted })
	waitFor(t, "eviction", func() bool {
		_, _, _, err := m.Events("default", snap.ID, 0)
		return errors.Is(err, executor.ErrTaskNotFound)
	})
	if got, err := m.Get(context.Background(), "default", snap.ID); err != nil || got.Status != executor.TaskCompleted || got.Result != "ok" {
		t.Fatalf("an evicted task is still read from its journal: %v %+v", err, got)
	}
}

func TestTaskBudgetFillsGapsAndHonoursTheCeiling(t *testing.T) {
	t.Setenv("AGENTOVEN_RUN_BUDGET_MAX_TOKENS", "500000")
	a := &models.Agent{Name: "x"}
	got := executor.ApplyTaskBudget(a, true)
	if got.MaxCostUSD != 5 || got.MaxTokens != 500000 || got.MaxActiveSeconds != 7200 || got.MaxTurns != 200 {
		t.Fatalf("defaults, with tokens held to the ceiling: %+v", got)
	}
	if got = executor.ApplyTaskBudget(a, false); got.MaxCostUSD != 0 {
		t.Fatalf("no dollar default for a model with no known price: %+v", got)
	}
	a.Budget = &models.RunBudget{MaxCostUSD: 20, MaxTurns: 9, MaxTokens: 100}
	got = executor.ApplyTaskBudget(a, true)
	if got.MaxCostUSD != 20 || got.MaxTurns != 9 || got.MaxTokens != 100 {
		t.Fatalf("an agent's own limits win over the defaults: %+v", got)
	}
	a.Budget = &models.RunBudget{MaxCostUSD: 9999}
	if got = executor.ApplyTaskBudget(a, true); got.MaxCostUSD != 50 {
		t.Fatalf("and the ceiling wins over the agent: %+v", got)
	}
}
