package executor_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

type seenRequests struct {
	mu   sync.Mutex
	list []contracts.ApprovalRequest
}

func (s *seenRequests) ApprovalRequested(_ context.Context, r contracts.ApprovalRequest) {
	s.mu.Lock()
	s.list = append(s.list, r)
	s.mu.Unlock()
}

func (s *seenRequests) all() []contracts.ApprovalRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]contracts.ApprovalRequest(nil), s.list...)
}

type panicObserver struct{}

func (panicObserver) ApprovalRequested(context.Context, contracts.ApprovalRequest) { panic("boom") }

func TestTheObserverIsToldWhenARunPausesForABudget(t *testing.T) {
	d := &costDriver{replies: loopOf(3, "finished"), in: 90, out: 10}
	e, _, _ := newBudgetExecutor(t, d, orderGateway(), false)
	obs := &seenRequests{}
	e.SetApprovalObserver(obs)
	_, trace, err := e.Execute(context.Background(), budgetAgent(models.RunBudget{MaxTokens: 350}), "go", resolvedP(orderTool()), nil, false)
	asBudgetPause(t, err)
	got := obs.all()
	if len(got) != 1 || got[0].Gate != contracts.GateBudgetExtension || got[0].RecordID != trace.TraceID || got[0].Kitchen != "default" {
		t.Fatalf("requests = %+v, want one budget_extension for %s", got, trace.TraceID)
	}
}

func TestTheObserverIsToldWhenAToolCallNeedsApproval(t *testing.T) {
	d := &scriptedDriver{replies: []turnReply{{toolCalls: []models.ToolCallResult{toolCall("1", "issue_refund", map[string]interface{}{"order_id": "A-1"})}}}}
	gw := newFakeGateway()
	gw.register("issue_refund", func(map[string]interface{}) (string, bool) { return "refunded", false })
	e := newTestExecutor(t, d, gw)
	j, err := executor.NewFileJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e.SetJournal(j)
	obs := &seenRequests{}
	e.SetApprovalObserver(obs)
	agent := testAgent()
	agent.ApprovalTools = []string{"issue_refund"}
	_, trace, err := e.Execute(context.Background(), agent, "refund", resolvedWithTools(models.ResolvedTool{Name: "issue_refund", Schema: map[string]interface{}{"type": "object"}}), nil, false)
	if err != executor.ErrPausedForApproval {
		t.Fatalf("err = %v", err)
	}
	got := obs.all()
	if len(got) != 1 || got[0].Gate != contracts.GateToolApproval || got[0].RecordID != trace.TraceID {
		t.Fatalf("requests = %+v", got)
	}
}

// An observer that panics must not change what happens to the run: it is paused and can be resumed.
func TestAPanickingObserverLeavesTheRunPausedAndResumable(t *testing.T) {
	e, _, _, id := pausedAtTokensObserved(t, panicObserver{})
	if _, _, err := e.Resume(context.Background(), "default", id, &executor.ResumeDecision{Approved: true, Extend: &models.BudgetExtension{AddTokens: 5000}}); err != nil {
		t.Fatalf("the run should still be resumable: %v", err)
	}
}

func pausedAtTokensObserved(t *testing.T, o contracts.ApprovalObserver) (*executor.Executor, executor.Journal, *costDriver, string) {
	t.Helper()
	d := &costDriver{replies: loopOf(3, "finished"), in: 90, out: 10}
	e, j, _ := newBudgetExecutor(t, d, orderGateway(), false)
	e.SetApprovalObserver(o)
	_, trace, err := e.Execute(context.Background(), budgetAgent(models.RunBudget{MaxTokens: 350}), "go", resolvedP(orderTool()), nil, false)
	asBudgetPause(t, err)
	return e, j, d, trace.TraceID
}

// Expiry is a refusal: the run ends cancelled with the reason, no tool it was waiting on runs, and a
// late approval finds nothing to approve.
func TestExpiringAPauseCancelsTheRunAndALateApprovalFindsNothing(t *testing.T) {
	e, _, d, id := pausedAtTokens(t, loopOf(3, "finished"))
	calls := d.calls()
	ok, err := e.ExpirePause(context.Background(), "default", id, "nobody answered in 24h")
	if !ok || err != nil {
		t.Fatalf("ExpirePause = %v %v", ok, err)
	}
	js, err := e.RunState(context.Background(), "default", id)
	if err != nil || js.Status != executor.RunDone || js.Outcome != executor.OutcomeCancelled || js.FinalErr != "nobody answered in 24h" {
		t.Fatalf("state = %+v %v", js, err)
	}
	if left, _ := e.ListUnfinished(context.Background(), "default"); len(left) != 0 {
		t.Fatalf("an expired run is still listed: %+v", left)
	}
	if _, _, err := e.Resume(context.Background(), "default", id, &executor.ResumeDecision{Approved: true, Extend: &models.BudgetExtension{AddTokens: 99999}}); err == nil {
		t.Fatal("a late approval resumed an expired run")
	}
	if d.calls() != calls {
		t.Fatal("the model was called after the run expired")
	}
	if again, _ := e.ExpirePause(context.Background(), "default", id, "x"); again {
		t.Fatal("a run was expired twice")
	}
}

func TestExpiryDoesNotTouchAnotherKitchensRunOrAFinishedOne(t *testing.T) {
	e, _, _, id := pausedAtTokens(t, loopOf(3, "finished"))
	if ok, err := e.ExpirePause(context.Background(), "other", id, "x"); ok || err == nil {
		t.Fatalf("another kitchen's run: ok=%v err=%v", ok, err)
	}
	if _, _, err := e.Resume(context.Background(), "default", id, &executor.ResumeDecision{Approved: true, Extend: &models.BudgetExtension{AddTokens: 5000}}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := e.ExpirePause(context.Background(), "default", id, "x"); ok {
		t.Fatal("a finished run was expired")
	}
}

// A person's decision and the expiry arrive together: exactly one wins.
func TestADecisionAndAnExpiryRaceToOneWinner(t *testing.T) {
	e, _, d, id := pausedAtTokens(t, loopOf(3, "finished"))
	before := d.calls()
	var wg sync.WaitGroup
	var resumed, expired int32
	for i := 0; i < 6; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, _, err := e.Resume(context.Background(), "default", id, &executor.ResumeDecision{Approved: true, Extend: &models.BudgetExtension{AddTokens: 100000}}); err == nil {
				atomic.AddInt32(&resumed, 1)
			}
		}()
		go func() {
			defer wg.Done()
			if ok, _ := e.ExpirePause(context.Background(), "default", id, "late"); ok {
				atomic.AddInt32(&expired, 1)
			}
		}()
	}
	wg.Wait()
	if resumed+expired != 1 {
		t.Fatalf("%d resumes and %d expiries won; exactly one may", resumed, expired)
	}
	if expired == 1 && d.calls() != before {
		t.Fatal("the run was expired and also ran")
	}
}
