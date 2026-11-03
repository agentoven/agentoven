package workflow

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// These tests stand two engines over one store, the way a restart or a second replica does: the first is
// stopped in the middle of a gate or a wait, the second takes the run over from what the store holds.

const testKitchen = "default"

// countingStore counts the approval records an engine creates, to show a resumed gate does not make another.
type countingStore struct {
	*store.MemoryStore
	created atomic.Int32
}

func (c *countingStore) CreateApproval(ctx context.Context, r *models.ApprovalRecord) error {
	c.created.Add(1)
	return c.MemoryStore.CreateApproval(ctx, r)
}

type peer struct {
	eng     *Engine
	cancel  context.CancelFunc
	resumed atomic.Int32 // runs this engine took over and carried on
}

func newStore(t *testing.T) *countingStore {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv(EnvRunLeaseSeconds, "2")
	mem := store.NewMemoryStore()
	if err := mem.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &countingStore{MemoryStore: mem}
}

// start puts an engine with resume on over the store; like a starting server, it looks for runs to take over at once.
func start(t *testing.T, st store.Store, owner string) *peer {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	eng := NewEngine(st, nil, "http://localhost:1")
	p := &peer{eng: eng, cancel: cancel}
	eng.SetResumeHook(func(_, _, _, outcome, _ string) {
		if outcome == "resumed" {
			p.resumed.Add(1)
		}
	})
	if !eng.EnableResume(ctx, owner) { // this also looks for runs to take over, at once
		cancel()
		t.Fatal("resume did not turn on")
	}
	t.Cleanup(cancel)
	return p
}

// die stops an engine the way a process dies: it stops executing and renewing and records nothing about
// the runs it had. With release the leases are given up (a graceful shutdown); without it they run out.
func (p *peer) die(release bool) {
	p.cancel()
	if release {
		p.eng.ReleaseRuns(context.Background())
	} else {
		p.eng.runsMu.RLock()
		ids := make([]string, 0, len(p.eng.runs))
		for id := range p.eng.runs {
			ids = append(ids, id)
		}
		p.eng.runsMu.RUnlock()
		for _, id := range ids {
			p.eng.lost.Store(id, true)
			p.eng.CancelRun(id)
		}
	}
	time.Sleep(100 * time.Millisecond) // let the goroutines notice
}

func gateStep(name string, cfg map[string]interface{}, deps ...string) models.Step {
	return models.Step{Name: name, Kind: models.StepHumanGate, DependsOn: deps, Config: cfg}
}

func saveRecipe(t *testing.T, st store.Store, steps ...models.Step) *models.Recipe {
	t.Helper()
	r := &models.Recipe{ID: "r1", Name: "r1", Kitchen: testKitchen, Steps: steps}
	if err := st.CreateRecipe(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	return r
}

func until(t *testing.T, what string, within time.Duration, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func runOf(t *testing.T, st store.Store, id string) *models.RecipeRun {
	t.Helper()
	r, err := st.GetRecipeRun(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func ended(t *testing.T, st store.Store, id string, within time.Duration) *models.RecipeRun {
	t.Helper()
	var run *models.RecipeRun
	until(t, "the run to end", within, func() bool {
		run = runOf(t, st, id)
		return run.Status == models.RecipeRunCompleted || run.Status == models.RecipeRunFailed
	})
	return run
}

// tookOver waits for a peer's first look at the store to have taken over n runs.
func tookOver(t *testing.T, p *peer, n int32) {
	t.Helper()
	until(t, "the engine to take the run over", 5*time.Second, func() bool { return p.resumed.Load() >= n })
	if got := p.resumed.Load(); got != n {
		t.Fatalf("took over %d runs, want %d", got, n)
	}
}

func pendingGate(t *testing.T, st store.Store, runID, step string) {
	t.Helper()
	until(t, "the gate to be waiting", 5*time.Second, func() bool {
		r, err := st.GetApproval(context.Background(), runID+":"+step)
		return err == nil && r.Status == "pending" && runOf(t, st, runID).Status == models.RecipeRunPaused
	})
}

func TestAGateWaitingThroughARestartIsStillWaitingAndIsApprovedOnce(t *testing.T) {
	st := newStore(t)
	recipe := saveRecipe(t, st, gateStep("sign", nil), models.Step{Name: "after", Kind: models.StepFanOut, DependsOn: []string{"sign"}})
	a := start(t, st, "A")
	id, err := a.eng.ExecuteRecipe(context.Background(), recipe, testKitchen, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	pendingGate(t, st, id, "sign")
	a.die(true)

	b := start(t, st, "B")
	pendingGate(t, st, id, "sign")
	tookOver(t, b, 1)
	if got := st.created.Load(); got != 1 {
		t.Errorf("%d approval records were created, want 1: the resumed gate must reuse its record", got)
	}

	ok, err := b.eng.ApproveGateAs(id, "sign", true, Approver{ID: "u1", Email: "u1@example.com"}, "api", "go ahead")
	if err != nil || !ok {
		t.Fatalf("approve: %v %v", ok, err)
	}
	run := ended(t, st, id, 10*time.Second)
	if run.Status != models.RecipeRunCompleted {
		t.Fatalf("%s: %s", run.Status, run.Error)
	}
	gate := stepResult(run, "sign")
	if gate == nil || gate.GateStatus != "approved" || !gate.Resumed || gate.Output["approver_email"] != "u1@example.com" {
		t.Errorf("gate result: %+v", gate)
	}
	count := 0
	for _, sr := range run.StepResults {
		if sr.StepName == "after" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("the step after the gate has %d results, want 1", count)
	}
	// A second answer to the same gate changes nothing.
	if ok, _ := b.eng.ApproveGateAs(id, "sign", false, Approver{ID: "u2"}, "api", "too late"); ok {
		t.Error("a gate that was decided took a second decision")
	}
}

func TestAGateDecidedWhileNothingWasRunningProceedsExactlyOnceWhoeverTakesTheRunOver(t *testing.T) {
	st := newStore(t)
	recipe := saveRecipe(t, st, gateStep("sign", nil), models.Step{Name: "after", Kind: models.StepFanOut, DependsOn: []string{"sign"}})
	a := start(t, st, "A")
	id, _ := a.eng.ExecuteRecipe(context.Background(), recipe, testKitchen, nil, "")
	pendingGate(t, st, id, "sign")
	a.die(true)

	// Downtime: a reply arrives through another path (a chat callback, another replica) and decides the gate.
	rec, _ := st.GetApproval(context.Background(), id+":sign")
	now := time.Now().UTC()
	rec.Status, rec.ResolvedAt, rec.ApproverID = "approved", &now, "chat-user"
	if won, err := st.ResolveApproval(context.Background(), rec); err != nil || !won {
		t.Fatalf("decide during downtime: %v %v", won, err)
	}

	// Two processes start together and both look for work: exactly one gets the run.
	b, c := start(t, st, "B"), start(t, st, "C")
	var wg sync.WaitGroup
	for _, p := range []*peer{b, c} {
		wg.Add(1)
		go func(p *peer) { defer wg.Done(); p.eng.ResumeRuns(context.Background()) }(p)
	}
	wg.Wait()
	run := ended(t, st, id, 10*time.Second)
	if took := b.resumed.Load() + c.resumed.Load(); took != 1 {
		t.Fatalf("%d processes took the run, want exactly 1", took)
	}
	if run.Status != models.RecipeRunCompleted {
		t.Fatalf("%s: %s", run.Status, run.Error)
	}
	gate := stepResult(run, "sign")
	if gate == nil || gate.GateStatus != "approved" || gate.Output["approver_id"] != "chat-user" {
		t.Errorf("gate result: %+v", gate)
	}
	if got := st.created.Load(); got != 1 {
		t.Errorf("%d approval records, want 1", got)
	}
}

func TestADecisionAndAnExpiryCannotBothWin(t *testing.T) {
	st := newStore(t)
	eng := NewEngine(st, nil, "http://localhost:1")
	rec := &models.ApprovalRecord{ID: "x", GateKey: "run:step", RunID: "run", StepName: "step", Kitchen: testKitchen, Status: "pending", RequestedAt: time.Now()}
	if err := st.CreateApproval(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var ok bool
			switch i % 3 {
			case 0:
				ok, _ = eng.ApproveGateAs("run", "step", true, Approver{ID: "a"}, "api", "")
			case 1:
				ok, _ = eng.ApproveGateAs("run", "step", false, Approver{ID: "b"}, "api", "")
			default:
				ok, _ = eng.ExpireGate(context.Background(), testKitchen, "run:step", "late")
			}
			if ok {
				wins.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d decisions took effect, want exactly 1", wins.Load())
	}
	// A kitchen that does not own the gate cannot expire it either.
	rec2 := &models.ApprovalRecord{ID: "y", GateKey: "run2:step", RunID: "run2", StepName: "step", Kitchen: testKitchen, Status: "pending", RequestedAt: time.Now()}
	_ = st.CreateApproval(context.Background(), rec2)
	if ok, _ := eng.ExpireGate(context.Background(), "other", "run2:step", "x"); ok {
		t.Error("another kitchen expired this kitchen's gate")
	}
}

func TestAWaitKeepsItsOwnEndAcrossARestart(t *testing.T) {
	st := newStore(t)
	recipe := saveRecipe(t, st, waitStep("pause", map[string]interface{}{"duration": "3s"}),
		models.Step{Name: "after", Kind: models.StepFanOut, DependsOn: []string{"pause"}})
	a := start(t, st, "A")
	begun := time.Now()
	id, _ := a.eng.ExecuteRecipe(context.Background(), recipe, testKitchen, nil, "")
	until(t, "the wait to show", 3*time.Second, func() bool {
		sr := stepResult(runOf(t, st, id), "pause")
		return sr != nil && sr.Status == "waiting" && sr.WakeAt != nil
	})
	a.die(true)
	time.Sleep(900 * time.Millisecond) // downtime counts against the wait

	b := start(t, st, "B")
	b.eng.ResumeRuns(context.Background())
	run := ended(t, st, id, 10*time.Second)
	if run.Status != models.RecipeRunCompleted {
		t.Fatalf("%s: %s", run.Status, run.Error)
	}
	took := time.Since(begun)
	if took < 2900*time.Millisecond || took > 3900*time.Millisecond {
		t.Errorf("the wait ended after %v, want about 3s from its start (not 3s from the restart)", took)
	}
	if w := stepResult(run, "pause"); w == nil || !w.Resumed || w.Status != "completed" {
		t.Errorf("wait result: %+v", w)
	}
}

func TestAWaitThatRanOutWhileTheControlPlaneWasDownEndsAtOnce(t *testing.T) {
	st := newStore(t)
	recipe := saveRecipe(t, st, waitStep("pause", map[string]interface{}{"duration": "1s"}),
		models.Step{Name: "after", Kind: models.StepFanOut, DependsOn: []string{"pause"}})
	a := start(t, st, "A")
	id, _ := a.eng.ExecuteRecipe(context.Background(), recipe, testKitchen, nil, "")
	until(t, "the wait to show", 3*time.Second, func() bool {
		sr := stepResult(runOf(t, st, id), "pause")
		return sr != nil && sr.Status == "waiting"
	})
	a.die(true)
	time.Sleep(1300 * time.Millisecond)

	b := start(t, st, "B")
	resumed := time.Now()
	b.eng.ResumeRuns(context.Background())
	run := ended(t, st, id, 5*time.Second)
	if run.Status != models.RecipeRunCompleted {
		t.Fatalf("%s: %s", run.Status, run.Error)
	}
	if d := time.Since(resumed); d > 1500*time.Millisecond {
		t.Errorf("an expired wait took %v after the restart", d)
	}
	if stepResult(run, "after") == nil {
		t.Error("the step after the wait never ran")
	}
}

func TestAGateWhoseSLARanOutDuringDowntimeTimesOutAtOnce(t *testing.T) {
	st := newStore(t)
	recipe := saveRecipe(t, st, gateStep("sign", map[string]interface{}{"max_wait_minutes": 0.025}), // 1.5s
		models.Step{Name: "after", Kind: models.StepFanOut, DependsOn: []string{"sign"}})
	a := start(t, st, "A")
	id, _ := a.eng.ExecuteRecipe(context.Background(), recipe, testKitchen, nil, "")
	pendingGate(t, st, id, "sign")
	a.die(true)
	time.Sleep(1700 * time.Millisecond)

	b := start(t, st, "B")
	b.eng.ResumeRuns(context.Background())
	run := ended(t, st, id, 5*time.Second)
	if gate := stepResult(run, "sign"); run.Status != models.RecipeRunFailed || gate == nil || !strings.Contains(gate.Error, "exceeded SLA") {
		t.Fatalf("%s: %s %+v", run.Status, run.Error, gate)
	}
	rec, _ := st.GetApproval(context.Background(), id+":sign")
	if rec.Status != "timed_out" {
		t.Errorf("record status %q, want timed_out", rec.Status)
	}
	if stepResult(run, "after") != nil {
		t.Error("the step after an expired gate ran")
	}
	if ok, _ := b.eng.ApproveGateAs(id, "sign", true, Approver{ID: "late"}, "api", ""); ok {
		t.Error("an expired gate was approved")
	}
}

func TestAGateKeepsTheRestOfItsSLAAcrossARestart(t *testing.T) {
	st := newStore(t)
	recipe := saveRecipe(t, st, gateStep("sign", map[string]interface{}{"max_wait_minutes": 0.05}), // 3s
		models.Step{Name: "after", Kind: models.StepFanOut, DependsOn: []string{"sign"}})
	a := start(t, st, "A")
	begun := time.Now()
	id, _ := a.eng.ExecuteRecipe(context.Background(), recipe, testKitchen, nil, "")
	pendingGate(t, st, id, "sign")
	a.die(true)
	time.Sleep(800 * time.Millisecond)
	b := start(t, st, "B")
	b.eng.ResumeRuns(context.Background())
	run := ended(t, st, id, 8*time.Second)
	if run.Status != models.RecipeRunFailed {
		t.Fatalf("%s: %s", run.Status, run.Error)
	}
	if took := time.Since(begun); took < 2900*time.Millisecond || took > 3900*time.Millisecond {
		t.Errorf("the gate timed out after %v, want about 3s from its start", took)
	}
}

func TestARunIsNotTakenOverWhileItsProcessKeepsItsLease(t *testing.T) {
	st := newStore(t)
	recipe := saveRecipe(t, st, gateStep("sign", nil))
	a := start(t, st, "A")
	id, _ := a.eng.ExecuteRecipe(context.Background(), recipe, testKitchen, nil, "")
	pendingGate(t, st, id, "sign")
	b := start(t, st, "B")
	time.Sleep(2500 * time.Millisecond) // longer than the lease: A has renewed it
	if n := b.eng.ResumeRuns(context.Background()); n != 0 {
		t.Fatalf("B took over %d runs that A is executing", n)
	}
	// A dies without giving the lease up: B waits for it to run out, then takes the run.
	a.die(false)
	if n := b.eng.ResumeRuns(context.Background()); n != 0 {
		t.Fatalf("B took the run before the lease ran out")
	}
	until(t, "the lease to run out", 6*time.Second, func() bool { return b.eng.ResumeRuns(context.Background()) == 1 })
	b.eng.ApproveGateAs(id, "sign", true, Approver{ID: "u"}, "api", "")
	if run := ended(t, st, id, 10*time.Second); run.Status != models.RecipeRunCompleted {
		t.Fatalf("%s: %s", run.Status, run.Error)
	}
}

func TestAProcessThatLosesItsLeaseStopsWithoutFailingTheRun(t *testing.T) {
	st := newStore(t)
	recipe := saveRecipe(t, st, gateStep("sign", nil))
	a := start(t, st, "A")
	id, _ := a.eng.ExecuteRecipe(context.Background(), recipe, testKitchen, nil, "")
	pendingGate(t, st, id, "sign")

	// A is too slow to renew (a long pause): its lease runs out and B takes the run.
	a.cancel()
	st.ReleaseRunLeases(context.Background(), "A")
	b := start(t, st, "B")
	tookOver(t, b, 1)
	a.eng.renewLeases(context.Background()) // A wakes up and finds it no longer holds the run
	until(t, "A to stop executing the run", 3*time.Second, func() bool {
		a.eng.runsMu.RLock()
		defer a.eng.runsMu.RUnlock()
		return len(a.eng.runs) == 0
	})
	if got := runOf(t, st, id).Status; got != models.RecipeRunPaused {
		t.Fatalf("the run is %s after A stopped; A must not fail it", got)
	}
	b.eng.ApproveGateAs(id, "sign", true, Approver{ID: "u"}, "api", "")
	if run := ended(t, st, id, 10*time.Second); run.Status != models.RecipeRunCompleted {
		t.Fatalf("%s: %s", run.Status, run.Error)
	}
}

func TestAChangedRecipeIsNotResumed(t *testing.T) {
	st := newStore(t)
	recipe := saveRecipe(t, st, gateStep("sign", nil))
	a := start(t, st, "A")
	id, _ := a.eng.ExecuteRecipe(context.Background(), recipe, testKitchen, nil, "")
	pendingGate(t, st, id, "sign")
	a.die(true)

	changed := *recipe
	changed.Steps = []models.Step{gateStep("sign", nil), {Name: "extra", Kind: models.StepFanOut, DependsOn: []string{"sign"}}}
	if err := st.UpdateRecipe(context.Background(), &changed); err != nil {
		t.Fatal(err)
	}
	b := start(t, st, "B")
	b.eng.ResumeRuns(context.Background())
	run := ended(t, st, id, 5*time.Second)
	if run.Status != models.RecipeRunFailed || !strings.Contains(run.Error, "changed") {
		t.Fatalf("%s: %s", run.Status, run.Error)
	}
}

func TestAStepThatMayHaveActedIsNotRunAgainAfterARestart(t *testing.T) {
	st := newStore(t)
	steps := []models.Step{
		{Name: "act", Kind: models.StepAgent, AgentRef: "someone"},
		{Name: "safe", Kind: models.StepFanOut},
		{Name: "repeatable", Kind: models.StepAgent, AgentRef: "someone", Config: map[string]interface{}{"on_restart": "rerun"}},
	}
	for _, c := range []struct {
		step string
		want resumeAction
	}{{"act", resumeFail}, {"safe", resumeRerun}, {"repeatable", resumeRerun}} {
		var step *models.Step
		for i := range steps {
			if steps[i].Name == c.step {
				step = &steps[i]
			}
		}
		if got := resumePolicy(step, models.StepResult{Status: "running"}); got != c.want {
			t.Errorf("%s: policy %v, want %v", c.step, got, c.want)
		}
	}

	recipe := saveRecipe(t, st, steps[0])
	run := &models.RecipeRun{ID: "11111111-1111-1111-1111-111111111111", RecipeID: recipe.ID, Kitchen: testKitchen, Status: models.RecipeRunRunning,
		StartedAt: time.Now().Add(-time.Hour), StepResults: []models.StepResult{{StepName: "act", StepKind: "agent", Status: "running", StartedAt: time.Now().Add(-time.Hour)}}}
	if err := st.CreateRecipeRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	b := start(t, st, "B")
	b.eng.ResumeRuns(context.Background())
	done := ended(t, st, run.ID, 10*time.Second)
	act := stepResult(done, "act")
	if done.Status != models.RecipeRunFailed || act == nil || act.Status != "failed" || !strings.Contains(act.Error, "not run again") {
		t.Fatalf("%s: %s %+v", done.Status, done.Error, act)
	}
}

func TestAGateInASubRecipeSurvivesARestartOfTheParent(t *testing.T) {
	st := newStore(t)
	child := &models.Recipe{ID: "child", Name: "child", Kitchen: testKitchen, Steps: []models.Step{gateStep("sign", nil)}}
	if err := st.CreateRecipe(context.Background(), child); err != nil {
		t.Fatal(err)
	}
	parent := &models.Recipe{ID: "parent", Name: "parent", Kitchen: testKitchen, Steps: []models.Step{
		{Name: "call", Kind: models.StepSubRecipe, RecipeRef: "child"},
		{Name: "after", Kind: models.StepFanOut, DependsOn: []string{"call"}},
	}}
	if err := st.CreateRecipe(context.Background(), parent); err != nil {
		t.Fatal(err)
	}
	a := start(t, st, "A")
	id, _ := a.eng.ExecuteRecipe(context.Background(), parent, testKitchen, nil, "")
	var subID string
	until(t, "the child run to wait at its gate", 5*time.Second, func() bool {
		sr := stepResult(runOf(t, st, id), "call")
		if sr == nil || sr.SubRunID == "" {
			return false
		}
		subID = sr.SubRunID
		rec, err := st.GetApproval(context.Background(), subID+":sign")
		return err == nil && rec.Status == "pending"
	})
	a.die(true)

	b := start(t, st, "B")
	tookOver(t, b, 1)
	until(t, "the child's gate to be waiting again", 5*time.Second, func() bool { return b.eng.GetPendingGates(subID) != nil })
	if ok, err := b.eng.ApproveGateAs(subID, "sign", true, Approver{ID: "u"}, "api", ""); err != nil || !ok {
		t.Fatalf("approve: %v %v", ok, err)
	}
	run := ended(t, st, id, 10*time.Second)
	if run.Status != models.RecipeRunCompleted {
		t.Fatalf("%s: %s", run.Status, run.Error)
	}
	if call := stepResult(run, "call"); call == nil || call.SubRunID != subID || !call.Resumed {
		t.Errorf("sub-recipe step: %+v", call)
	}
	if got := st.created.Load(); got != 1 {
		t.Errorf("%d approval records, want 1", got)
	}
}

func TestResumeIsOffWhenTheStoreCannotLeaseOrItIsSwitchedOff(t *testing.T) {
	t.Setenv(EnvRunResume, "false")
	st := newStore(t)
	if NewEngine(st, nil, "").EnableResume(context.Background(), "A") {
		t.Error("resume turned on although AGENTOVEN_RUN_RESUME=false")
	}
}

func TestACheckpointShowsTheStepsAsTheyStartAndEnd(t *testing.T) {
	st := newStore(t)
	recipe := saveRecipe(t, st, waitStep("first", map[string]interface{}{"duration": "1s"}), gateStep("sign", nil, "first"))
	a := start(t, st, "A")
	id, _ := a.eng.ExecuteRecipe(context.Background(), recipe, testKitchen, nil, "")
	until(t, "the first step to be recorded as it ends and the gate as waiting", 5*time.Second, func() bool {
		run := runOf(t, st, id)
		f, g := stepResult(run, "first"), stepResult(run, "sign")
		return f != nil && f.Status == "completed" && g != nil && g.Status == "waiting" && g.GateStatus == "waiting" && run.Status == models.RecipeRunPaused
	})
	a.eng.ApproveGate(id, "sign", true)
	if run := ended(t, st, id, 10*time.Second); run.Status != models.RecipeRunCompleted || len(run.StepResults) != 2 {
		t.Fatalf("%s: %s %+v", run.Status, run.Error, run.StepResults)
	}
}
