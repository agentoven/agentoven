package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/notify"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func TestTemplatesFillWhatTheRunKnows(t *testing.T) {
	env := map[string]interface{}{
		"run_id": "r1", "recipe": "refunds", "text": "Please refund me",
		"input":  map[string]interface{}{"ticket": "T-9", "n": float64(3), "tags": []interface{}{"a", "b"}},
		"triage": map[string]interface{}{"category": "billing"},
	}
	got := renderTemplate("Run {{run_id}} of {{ recipe }}: {{input.ticket}} ({{input.n}}) {{triage.category}} {{input.tags}} [{{nope}}] [{{input.missing}}] {{text}}", env)
	want := `Run r1 of refunds: T-9 (3) billing ["a","b"] [] [] Please refund me`
	if got != want {
		t.Errorf("\n got %q\nwant %q", got, want)
	}
	if renderTemplate("no braces here", env) != "no braces here" || renderTemplate("{{ }}", env) != "{{ }}" {
		t.Error("text without a name is left alone")
	}
}

func notifyEngine(t *testing.T, channels ...models.NotificationChannel) (*Engine, *notify.Service) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	mem := store.NewMemoryStore()
	_ = mem.Migrate(context.Background())
	for i := range channels {
		if err := mem.CreateChannel(context.Background(), &channels[i]); err != nil {
			t.Fatal(err)
		}
	}
	svc := notify.NewService(mem)
	return NewEngine(mem, svc, "http://localhost:8080"), svc
}

func wsChannel(name string) models.NotificationChannel {
	return models.NotificationChannel{ID: name, Name: name, Kind: models.ChannelWebSocket, Kitchen: "default", Active: true}
}

func notifyStep(name string, cfg map[string]interface{}, deps ...string) models.Step {
	return models.Step{Name: name, Kind: models.StepNotify, DependsOn: deps, Config: cfg}
}

func runRecipeOn(t *testing.T, eng *Engine, input map[string]interface{}, steps ...models.Step) *models.RecipeRun {
	t.Helper()
	recipe := &models.Recipe{ID: "r-id", Name: "refunds", Kitchen: "default", Steps: steps}
	eng.store.CreateRecipe(context.Background(), recipe)
	id, err := eng.ExecuteRecipe(context.Background(), recipe, "default", input, "")
	if err != nil {
		t.Fatal(err)
	}
	return waitForRun(t, eng, id, 15)
}

func TestANotifyStepPushesItsMessageToTheListenersOfAWebsocketChannel(t *testing.T) {
	eng, svc := notifyEngine(t, wsChannel("ops"))
	listener := svc.Hub().Subscribe("default", "ops")
	defer listener.Close()

	run := runRecipeOn(t, eng, map[string]interface{}{"ticket": "T-9"},
		notifyStep("tell-the-team", map[string]interface{}{
			"channels": []interface{}{"ops"},
			"subject":  "Approval needed: {{input.ticket}}",
			"message":  "Run {{run_id}} of {{recipe}} is waiting for a person.",
			"payload":  map[string]interface{}{"priority": "high", "ticket": "{{input.ticket}}"},
		}),
		models.Step{Name: "after", Kind: models.StepFanOut, DependsOn: []string{"tell-the-team"}},
	)
	if run.Status != models.RecipeRunCompleted {
		t.Fatalf("%s: %s", run.Status, run.Error)
	}
	select {
	case raw := <-listener.C:
		var ev map[string]interface{}
		_ = json.Unmarshal(raw, &ev)
		p, _ := ev["payload"].(map[string]interface{})
		if ev["type"] != "notification" || ev["run_id"] != run.ID || ev["recipe_name"] != "refunds" || ev["step_name"] != "tell-the-team" {
			t.Errorf("event: %v", ev)
		}
		if p["subject"] != "Approval needed: T-9" || p["message"] != "Run "+run.ID+" of refunds is waiting for a person." || p["priority"] != "high" || p["ticket"] != "T-9" {
			t.Errorf("payload: %v", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the listener received nothing")
	}
	step := stepResult(run, "tell-the-team")
	if step.Output["sent"] != float64(1) && step.Output["sent"] != 1 {
		t.Errorf("output: %v", step.Output)
	}
	if len(step.NotifyResults) != 1 || step.NotifyResults[0].Recipients != 1 {
		t.Errorf("the step should record who it reached: %+v", step.NotifyResults)
	}
	if stepResult(run, "after") == nil {
		t.Error("the run did not carry on")
	}
}

func TestANotifyStepRepeatsWhatTheStepBeforeSaid(t *testing.T) {
	eng, svc := notifyEngine(t, wsChannel("ops"))
	l := svc.Hub().Subscribe("default", "ops")
	defer l.Close()
	run := runRecipeOn(t, eng, nil,
		models.Step{Name: "prep", Kind: models.StepFanOut},
		notifyStep("tell", map[string]interface{}{"channels": []interface{}{"ops"}, "message": "Done: {{step}} after {{prep.fan_out}}"}, "prep"),
	)
	if run.Status != models.RecipeRunCompleted {
		t.Fatalf("%s: %s", run.Status, run.Error)
	}
	var ev struct{ Payload map[string]interface{} }
	_ = json.Unmarshal(<-l.C, &ev)
	if ev.Payload["message"] != "Done: tell after true" {
		t.Errorf("%v", ev.Payload)
	}
}

func TestANotifyStepThatCannotReachATargetStopsTheRunUnlessToldToCarryOn(t *testing.T) {
	for _, tc := range []struct {
		onFailure string
		status    models.RecipeRunStatus
	}{{"", models.RecipeRunFailed}, {"stop", models.RecipeRunFailed}, {"carry_on", models.RecipeRunCompleted}} {
		t.Run("on_failure="+tc.onFailure, func(t *testing.T) {
			eng, svc := notifyEngine(t, wsChannel("ops"))
			l := svc.Hub().Subscribe("default", "ops")
			defer l.Close()
			cfg := map[string]interface{}{"channels": []interface{}{"ops", "nowhere"}, "message": "hi"}
			if tc.onFailure != "" {
				cfg["on_failure"] = tc.onFailure
			}
			run := runRecipeOn(t, eng, nil, notifyStep("tell", cfg))
			if run.Status != tc.status {
				t.Fatalf("status %s (%s), want %s", run.Status, run.Error, tc.status)
			}
			if tc.status == models.RecipeRunFailed && !strings.Contains(run.Error, `channel "nowhere" not found`) {
				t.Errorf("the failure should name the target: %q", run.Error)
			}
			select {
			case <-l.C: // the reachable target still got it
			case <-time.After(time.Second):
				t.Error("the channel that could be reached was not sent to")
			}
			step := stepResult(run, "tell")
			if step.Output["sent"] != float64(1) && step.Output["sent"] != 1 {
				t.Errorf("output %v", step.Output)
			}
		})
	}
}

func TestANotifyStepWithNothingToSayOrNowhereToSendFails(t *testing.T) {
	for name, cfg := range map[string]map[string]interface{}{
		"no message":  {"channels": []interface{}{"ops"}},
		"no target":   {"message": "hi"},
		"bad failure": {"channels": []interface{}{"ops"}, "message": "hi", "on_failure": "shrug"},
	} {
		t.Run(name, func(t *testing.T) {
			eng, _ := notifyEngine(t, wsChannel("ops"))
			run := runRecipeOn(t, eng, nil, notifyStep("tell", cfg))
			if run.Status != models.RecipeRunFailed || !strings.Contains(run.Error, "notify step 'tell'") {
				t.Errorf("status %s, error %q", run.Status, run.Error)
			}
		})
	}
}

func TestANotifyStepNeedsTheNotificationServiceToExist(t *testing.T) {
	eng := setupTestEngine(t, nil) // no notifier
	run := runRecipeOn(t, eng, nil, notifyStep("tell", map[string]interface{}{"channels": []interface{}{"ops"}, "message": "hi"}))
	if run.Status != models.RecipeRunFailed || !strings.Contains(run.Error, "not available") {
		t.Errorf("status %s, error %q", run.Status, run.Error)
	}
}

func TestValidateNotifyStep(t *testing.T) {
	ok := models.Step{Name: "n", Config: map[string]interface{}{"message": "hi", "channels": []interface{}{"ops"}}}
	if err := ValidateNotifyStep(&ok); err != nil {
		t.Error(err)
	}
	viaTool := models.Step{Name: "n", NotifyTools: []string{"pager"}, Config: map[string]interface{}{"message": "hi"}}
	if err := ValidateNotifyStep(&viaTool); err != nil {
		t.Errorf("a tool is a target too: %v", err)
	}
	for _, s := range []models.Step{
		{Name: "n", Config: map[string]interface{}{"channels": []interface{}{"ops"}}},
		{Name: "n", Config: map[string]interface{}{"message": "  ", "channels": []interface{}{"ops"}}},
		{Name: "n", Config: map[string]interface{}{"message": "hi"}},
		{Name: "n", Config: map[string]interface{}{"message": "hi", "channels": []interface{}{"ops"}, "on_failure": "x"}},
		{Name: "n"},
	} {
		if ValidateNotifyStep(&s) == nil {
			t.Errorf("%v should be refused", s.Config)
		}
	}
}
