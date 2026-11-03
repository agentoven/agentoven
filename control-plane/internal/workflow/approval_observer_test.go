package workflow

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

type recordingObserver struct {
	mu   sync.Mutex
	seen []contracts.ApprovalRequest
}

func (o *recordingObserver) ApprovalRequested(_ context.Context, r contracts.ApprovalRequest) {
	o.mu.Lock()
	o.seen = append(o.seen, r)
	o.mu.Unlock()
}

type panickingObserver struct{}

func (panickingObserver) ApprovalRequested(context.Context, contracts.ApprovalRequest) { panic("boom") }

// gateEngine starts a run that reaches a human gate and waits there.
func gateEngine(t *testing.T, config map[string]interface{}, obs contracts.ApprovalObserver) (*Engine, string) {
	t.Helper()
	outputs := map[string]map[string]interface{}{"triage": a2aText("hello"), "approve": a2aText("approved"), "auto": a2aText("rejected path"), "reply": a2aText("sent")}
	srv := fakeAgentServer(outputs)
	t.Cleanup(srv.Close)
	eng := setupTestEngine(t, srv)
	if obs != nil {
		eng.SetApprovalObserver(obs)
	}
	ctx := context.Background()
	for name := range outputs {
		eng.store.CreateAgent(ctx, &models.Agent{Name: name, Kitchen: "default", Status: models.AgentStatusReady,
			A2AEndpoint: fmt.Sprintf("%s/api/v1/agents/%s/invoke", srv.URL, name)})
	}
	recipe := &models.Recipe{ID: "gate-observed", Name: "gate-observed", Kitchen: "default", Steps: gatePathSteps(config)}
	eng.store.CreateRecipe(ctx, recipe)
	runID, err := eng.ExecuteRecipe(ctx, recipe, "default", map[string]interface{}{"ticket": "t"}, "")
	if err != nil {
		t.Fatal(err)
	}
	waitForPaused(t, eng, runID)
	return eng, runID
}

func TestObserverIsToldWhenAGateStartsWaiting(t *testing.T) {
	obs := &recordingObserver{}
	_, runID := gateEngine(t, map[string]interface{}{"max_wait_minutes": 90}, obs)
	obs.mu.Lock()
	defer obs.mu.Unlock()
	if len(obs.seen) != 1 {
		t.Fatalf("observer was told %d times, want once: %+v", len(obs.seen), obs.seen)
	}
	got := obs.seen[0]
	if got.Gate != contracts.GateRecipeGate || got.Kitchen != "default" || got.RecordID != runID+":gate" || got.Deadline != 90*time.Minute {
		t.Fatalf("request = %+v", got)
	}
}

// A broken observer must never stop a gate from waiting or from being decided.
func TestAPanickingObserverDoesNotStopTheGate(t *testing.T) {
	eng, runID := gateEngine(t, map[string]interface{}{"on_reject": "branch"}, panickingObserver{})
	if ok, err := eng.ApproveGateWithMetadata(runID, "gate", true, "u", "a@x.test", "", "api", ""); !ok || err != nil {
		t.Fatalf("approve = %v %v", ok, err)
	}
	if run := waitForRun(t, eng, runID, 20); run.Status != models.RecipeRunCompleted {
		t.Fatalf("run = %s %s", run.Status, run.Error)
	}
}

// Expiry is a refusal: a recipe that routes a timeout carries on along that path, one that does not
// fails, and neither ever runs the approved path.
func TestExpiringAGateTakesTheTimeoutPathAndNeverTheApprovedOne(t *testing.T) {
	for name, tc := range map[string]struct {
		config map[string]interface{}
		status models.RecipeRunStatus
		auto   string
	}{
		"routed":   {map[string]interface{}{"on_reject": "branch", "on_timeout": "branch"}, models.RecipeRunCompleted, "completed"},
		"unrouted": {map[string]interface{}{"on_reject": "branch"}, models.RecipeRunFailed, ""},
	} {
		t.Run(name, func(t *testing.T) {
			eng, runID := gateEngine(t, tc.config, nil)
			ok, err := eng.ExpireGate(context.Background(), "default", runID+":gate", "no answer in 24h")
			if !ok || err != nil {
				t.Fatalf("ExpireGate = %v %v", ok, err)
			}
			run := waitForRun(t, eng, runID, 20)
			if run.Status != tc.status {
				t.Fatalf("run = %s %s, want %s", run.Status, run.Error, tc.status)
			}
			steps := map[string]string{}
			for _, sr := range run.StepResults {
				steps[sr.StepName] = sr.Status
				if sr.StepName == "gate" && sr.GateStatus != "timed_out" {
					t.Errorf("gate status = %s, want timed_out", sr.GateStatus)
				}
			}
			if steps["approve"] == "completed" || steps["auto"] != tc.auto {
				t.Errorf("steps = %v", steps)
			}
			// A decision that arrives after the expiry changes nothing.
			if ok, _ := eng.ApproveGateWithMetadata(runID, "gate", true, "u", "a@x.test", "", "api", ""); ok {
				t.Error("a gate that had timed out accepted an approval")
			}
		})
	}
}

func TestExpireGateRefusesWhatIsNotPendingInThatKitchen(t *testing.T) {
	eng, runID := gateEngine(t, map[string]interface{}{"on_reject": "branch"}, nil)
	ctx := context.Background()
	if ok, _ := eng.ExpireGate(ctx, "another-kitchen", runID+":gate", "x"); ok {
		t.Fatal("another kitchen's gate was expired")
	}
	if ok, _ := eng.ExpireGate(ctx, "default", runID+":nope", "x"); ok {
		t.Fatal("a gate that does not exist was expired")
	}
	if ok, _ := eng.ApproveGateWithMetadata(runID, "gate", true, "u", "a@x.test", "", "api", ""); !ok {
		t.Fatal("approve failed")
	}
	if ok, _ := eng.ExpireGate(ctx, "default", runID+":gate", "x"); ok {
		t.Fatal("a gate that was approved was expired")
	}
	if run := waitForRun(t, eng, runID, 20); run.Status != models.RecipeRunCompleted {
		t.Fatalf("run = %s %s", run.Status, run.Error)
	}
}

// An approval and an expiry that arrive together: exactly one wins, and the run ends the way that one says.
func TestAnApprovalAndAnExpiryRaceToOneWinner(t *testing.T) {
	eng, runID := gateEngine(t, map[string]interface{}{"on_reject": "branch", "on_timeout": "branch"}, nil)
	var approved, expired int32
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if ok, _ := eng.ApproveGateWithMetadata(runID, "gate", true, "u", "a@x.test", "", "api", ""); ok {
				atomic.AddInt32(&approved, 1)
			}
		}()
		go func() {
			defer wg.Done()
			if ok, _ := eng.ExpireGate(context.Background(), "default", runID+":gate", "late"); ok {
				atomic.AddInt32(&expired, 1)
			}
		}()
	}
	wg.Wait()
	if approved+expired != 1 {
		t.Fatalf("%d approvals and %d expiries won; exactly one decision may", approved, expired)
	}
	run := waitForRun(t, eng, runID, 20)
	for _, sr := range run.StepResults {
		if sr.StepName != "gate" {
			continue
		}
		want := "approved"
		if expired == 1 {
			want = "timed_out"
		}
		if sr.GateStatus != want {
			t.Errorf("gate = %s, want %s", sr.GateStatus, want)
		}
	}
}
