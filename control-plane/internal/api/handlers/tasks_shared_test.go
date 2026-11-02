package handlers_test

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// refusingGate is a spend gate whose budget is used up until it is lifted.
type refusingGate struct{ open atomic.Bool }

func (g *refusingGate) Allow(context.Context, models.SpendQuery) error {
	if g.open.Load() {
		return nil
	}
	return &models.SpendRefusedError{Scope: "kitchen", Reason: "the monthly budget is used up", Spent: 10, Cap: 10}
}
func (g *refusingGate) Record(context.Context, models.SpendRecord) {}

func TestATaskPausedAtTheKitchensBudgetResumesWithoutAnExtensionAndTellsTheHook(t *testing.T) {
	h := budgetHandlersWith(t, 1, models.RunBudget{}, nil, "done after the budget was raised", nil)
	gate := &refusingGate{}
	h.Router.SetSpendGate(gate)

	var mu sync.Mutex
	var pauses []executor.TaskSnapshot
	h.SetTaskPauseHook(func(s executor.TaskSnapshot) { mu.Lock(); pauses = append(pauses, s); mu.Unlock() })

	id := createdID(t, postTask(h, taskBody("worker", "go")))
	paused := waitTask(t, h, id, "paused")
	bp, _ := paused["budget_pause"].(map[string]interface{})
	if paused["pause_kind"] != "budget" || bp["scope"] != "kitchen" {
		t.Fatalf("%v", paused)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(pauses)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	if len(pauses) != 1 || pauses[0].ID != id || pauses[0].PauseKind != "budget" || pauses[0].BudgetPause.Scope != "kitchen" {
		t.Fatalf("one pause, one hook call: %+v", pauses)
	}
	mu.Unlock()

	resume := func(body string) int {
		return callAs(h.ResumeTask, http.MethodPost, "/resume", body, map[string]string{"taskID": id}, "default", nil, nil).Code
	}
	// Still used up: resuming tries again and pauses again, which is the right answer to "try again".
	if code := resume(`{"approved":true}`); code != http.StatusAccepted {
		t.Fatalf("a pause at a budget outside the task needs no extension, got %d", code)
	}
	waitTask(t, h, id, "paused")
	gate.open.Store(true) // finance raised the kitchen's budget
	if code := resume(`{"approved":true}`); code != http.StatusAccepted {
		t.Fatalf("%d", code)
	}
	done := waitTask(t, h, id, "completed")
	if done["result"] != "done after the budget was raised" {
		t.Fatalf("%v", done)
	}
}

func TestAHandlersJournalCanBeReplacedAndTasksShareLeases(t *testing.T) {
	h := budgetHandlersWith(t, 0, models.RunBudget{}, nil, "ok", nil)
	j, err := executor.NewFileJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h.SetJournal(j)
	leases := executor.NewMemoryLeases(nil)
	h.SetTaskLeases(leases, "replica-a", 2*time.Second)
	id := createdID(t, postTask(h, taskBody("worker", "go")))
	done := waitTask(t, h, id, "completed")
	if done["owner"] != "replica-a" {
		t.Fatalf("the task names the replica that ran it: %v", done)
	}
	if holder, _ := leases.Holder(context.Background(), id); holder != "" {
		t.Fatalf("a finished task holds no lease, %q does", holder)
	}
	if _, err := j.Load(context.Background(), id); err != nil {
		t.Fatalf("the task's journal is in the journal that was installed: %v", err)
	}
}
