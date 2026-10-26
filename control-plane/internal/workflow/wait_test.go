package workflow

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func TestWaitLengthsAreReadTheWayTheyAreWritten(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"30s": 30 * time.Second, "5m": 5 * time.Minute, "2h": 2 * time.Hour, "3d": 72 * time.Hour,
		"90": 90 * time.Second, "1.5h": 90 * time.Minute, " 10 M ": 10 * time.Minute, "0.2s": 200 * time.Millisecond, "0": 0,
	} {
		got, err := ParseWaitDuration(in)
		if err != nil || got != want {
			t.Errorf("%q: got %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "soon", "5 minutes", "-3m", "m", "1w"} {
		if _, err := ParseWaitDuration(in); err == nil {
			t.Errorf("%q should not be a length of time", in)
		}
	}
}

func waitStep(name string, cfg map[string]interface{}, deps ...string) models.Step {
	return models.Step{Name: name, Kind: models.StepWait, DependsOn: deps, Config: cfg}
}

func runWait(t *testing.T, steps ...models.Step) (*Engine, string) {
	t.Helper()
	eng := setupTestEngine(t, nil)
	recipe := &models.Recipe{ID: "w", Name: "w", Kitchen: "default", Steps: steps}
	eng.store.CreateRecipe(context.Background(), recipe)
	runID, err := eng.ExecuteRecipe(context.Background(), recipe, "default", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	return eng, runID
}

func stepResult(run *models.RecipeRun, name string) *models.StepResult {
	for i := range run.StepResults {
		if run.StepResults[i].StepName == name {
			return &run.StepResults[i]
		}
	}
	return nil
}

func TestAWaitStepWaitsThenTheRunCarriesOn(t *testing.T) {
	start := time.Now()
	eng, id := runWait(t, waitStep("pause", map[string]interface{}{"duration": "0.4s"}),
		models.Step{Name: "after", Kind: models.StepFanOut, DependsOn: []string{"pause"}})
	run := waitForRun(t, eng, id, 15)
	if run.Status != models.RecipeRunCompleted {
		t.Fatalf("%s: %s", run.Status, run.Error)
	}
	if time.Since(start) < 400*time.Millisecond {
		t.Errorf("the run finished after %v: it did not wait", time.Since(start))
	}
	w := stepResult(run, "pause")
	if w == nil || w.Status != "completed" || w.Output["released_early"] != false || w.Output["waited_seconds"].(float64) < 0.35 {
		t.Errorf("wait result: %+v", w)
	}
	if stepResult(run, "after") == nil {
		t.Error("the step after the wait never ran")
	}
}

func TestAWaitThatIsAlreadyOverDoesNotWait(t *testing.T) {
	eng, id := runWait(t, waitStep("pause", map[string]interface{}{"until": time.Now().Add(-time.Hour).Format(time.RFC3339)}))
	run := waitForRun(t, eng, id, 10)
	if run.Status != models.RecipeRunCompleted {
		t.Fatalf("%s: %s", run.Status, run.Error)
	}
}

func TestAWaitUntilATimeEndsAtThatTime(t *testing.T) {
	until := time.Now().Add(500 * time.Millisecond)
	eng, id := runWait(t, waitStep("pause", map[string]interface{}{"until": until.Format(time.RFC3339Nano)}))
	run := waitForRun(t, eng, id, 15)
	if run.Status != models.RecipeRunCompleted || time.Now().Before(until.Add(-50*time.Millisecond)) {
		t.Errorf("status %s, finished at %v, wanted after %v", run.Status, time.Now(), until)
	}
}

func waitUntilWaiting(t *testing.T, eng *Engine, id string) *models.StepResult {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		run, _ := eng.store.GetRecipeRun(context.Background(), id)
		if run != nil {
			for i := range run.StepResults {
				if run.StepResults[i].Status == "waiting" {
					return &run.StepResults[i]
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the run never showed a waiting step")
	return nil
}

func TestAWaitingStepIsVisibleWithItsWakeTimeAndCanBeReleased(t *testing.T) {
	eng, id := runWait(t, waitStep("pause", map[string]interface{}{"duration": "1h"}))
	w := waitUntilWaiting(t, eng, id)
	if w.StepName != "pause" || w.WakeAt == nil || time.Until(*w.WakeAt) < 59*time.Minute {
		t.Fatalf("a waiting step should say when it ends: %+v", w)
	}
	if eng.ReleaseWait(id, "no-such-step") {
		t.Error("releasing a step that is not waiting should say so")
	}
	if !eng.ReleaseWait(id, "pause") {
		t.Fatal("the waiting step was not found")
	}
	run := waitForRun(t, eng, id, 10)
	if run.Status != models.RecipeRunCompleted {
		t.Fatalf("%s: %s", run.Status, run.Error)
	}
	if r := stepResult(run, "pause"); r == nil || r.Output["released_early"] != true {
		t.Errorf("the result should say it was released early: %+v", r)
	}
	if eng.ReleaseWait(id, "pause") {
		t.Error("a step that has finished is not waiting")
	}
}

func TestCancelingARunEndsItsWait(t *testing.T) {
	eng, id := runWait(t, waitStep("pause", map[string]interface{}{"duration": "1h"}))
	waitUntilWaiting(t, eng, id)
	start := time.Now()
	if !eng.CancelRun(id) {
		t.Fatal("run not found")
	}
	run := waitForRun(t, eng, id, 10)
	if run.Status != models.RecipeRunFailed || time.Since(start) > 3*time.Second {
		t.Errorf("status %s after %v", run.Status, time.Since(start))
	}
}

func TestAWaitWithNothingToWaitForFailsAndSaysWhy(t *testing.T) {
	for name, cfg := range map[string]map[string]interface{}{
		"nothing set":     nil,
		"unreadable time": {"duration": "5 minutes"},
		"bad until":       {"until": "tomorrow"},
		"too long":        {"duration": "31d"},
	} {
		t.Run(name, func(t *testing.T) {
			eng, id := runWait(t, waitStep("pause", cfg))
			run := waitForRun(t, eng, id, 10)
			if run.Status != models.RecipeRunFailed || !strings.Contains(run.Error, "wait step 'pause'") {
				t.Errorf("status %s, error %q", run.Status, run.Error)
			}
		})
	}
}

func TestValidateWaitStepRefusesWhatCannotRunButNotATimeThatIsFarAway(t *testing.T) {
	if err := ValidateWaitStep(&models.Step{Name: "p", Config: map[string]interface{}{"duration": "5m"}}); err != nil {
		t.Error(err)
	}
	for _, cfg := range []map[string]interface{}{nil, {"duration": "soon"}, {"duration": "40d"}, {"until": "tomorrow"}} {
		if ValidateWaitStep(&models.Step{Name: "p", Config: cfg}) == nil {
			t.Errorf("%v should be refused when saved", cfg)
		}
	}
	far := map[string]interface{}{"until": time.Now().Add(90 * 24 * time.Hour).Format(time.RFC3339)}
	if err := ValidateWaitStep(&models.Step{Name: "p", Config: far}); err != nil {
		t.Errorf("a fixed time far away is for the day it runs: %v", err)
	}
}
