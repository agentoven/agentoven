package handlers_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// Owner decision A4: a PUT stays partial, and JSON null clears an optional field. The agent below was never
// baked, so the clear applies at once; the baked case waits in the draft like any edit.
func TestAPutWithNullClearsAnOptionalField(t *testing.T) {
	h, s := bakeHandlers(t)
	// The backup names a provider this kitchen does not have, which a write would refuse; the point here is the
	// clear, so it is seeded the way an older agent would hold it.
	seeded, _ := s.GetAgent(context.Background(), "default", "bot")
	seeded.BackupProvider, seeded.BackupModel, seeded.ContextBudget = "p2", "bm", 900
	if err := s.UpdateAgent(context.Background(), seeded); err != nil {
		t.Fatal(err)
	}
	// Leaving the fields out keeps them: the update is still partial.
	if code, body := agentCall(t, h.UpdateAgent, "PUT", `{"description":"changed"}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	a, _ := s.GetAgent(context.Background(), "default", "bot")
	if a.BackupProvider != "p2" || a.BackupModel != "bm" || a.ContextBudget != 900 || a.Description != "changed" {
		t.Fatalf("a field left out must stay: %+v", a)
	}
	if code, body := agentCall(t, h.UpdateAgent, "PUT", `{"backup_provider":null,"backup_model":null}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	a, _ = s.GetAgent(context.Background(), "default", "bot")
	if a.BackupProvider != "" || a.BackupModel != "" || a.ContextBudget != 900 || a.Description != "changed" || a.Draft != nil {
		t.Fatalf("null clears the named fields and nothing else: %+v", a)
	}
}

func TestAPutWithNullOnABakedAgentWaitsForTheBake(t *testing.T) {
	h, s := bakeHandlers(t)
	if code, body := agentCall(t, h.UpdateAgent, "PUT", `{"context_budget":900,"framework":"custom"}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if code, body := agentCall(t, h.BakeAgent, "POST", `{}`); code != http.StatusAccepted {
		t.Fatalf("bake: %d %s", code, body)
	}
	if a := waitStatus(t, s, models.AgentStatusReady); a.ContextBudget != 900 || a.Framework != "custom" {
		t.Fatalf("the bake must keep what was set: %+v", a)
	}

	if code, body := agentCall(t, h.UpdateAgent, "PUT", `{"context_budget":null}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	live, _ := s.GetAgent(context.Background(), "default", "bot")
	if live.ContextBudget != 900 || live.Draft == nil || len(live.Draft.Cleared) != 1 || live.Draft.Cleared[0] != "context_budget" {
		t.Fatalf("the clear waits in the draft and changes nothing live: %+v / %+v", live, live.Draft)
	}
	if code, body := agentCall(t, h.RecookAgent, "POST", `{"framework":null}`); code != http.StatusAccepted {
		t.Fatalf("recook: %d %s", code, body)
	}
	baked := waitStatus(t, s, models.AgentStatusReady)
	if baked.ContextBudget != 0 || baked.Framework != "" || baked.Draft != nil {
		t.Fatalf("a re-cook applies the draft and its own clears: %+v", baked)
	}
}

// An agent cannot be left without a model: null there is an error, not a silent no-op.
func TestNullForARequiredFieldIsRefused(t *testing.T) {
	h, s := bakeHandlers(t)
	for _, body := range []string{`{"model_name":null}`, `{"model_provider":null,"description":"x"}`} {
		code, resp := agentCall(t, h.UpdateAgent, "PUT", body)
		if code != http.StatusBadRequest || !strings.Contains(resp, "cannot be cleared") {
			t.Errorf("%s: %d %s", body, code, resp)
		}
	}
	if code, resp := agentCall(t, h.RecookAgent, "POST", `{"model_name":null}`); code == http.StatusAccepted || code == 200 {
		t.Errorf("a re-cook must refuse null for the model too: %d %s", code, resp)
	}
	a, _ := s.GetAgent(context.Background(), "default", "bot")
	if a.ModelName != "m1" || a.Description != "be brief" {
		t.Fatalf("a refused edit changes nothing: %+v", a)
	}
}

// null for a list is "no opinion", as before: only [] removes the guardrails.
func TestNullForAListStaysUnchanged(t *testing.T) {
	h, s := bakeHandlers(t)
	if code, body := agentCall(t, h.UpdateAgent, "PUT", `{"guardrails":null,"skills":null}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	a, _ := s.GetAgent(context.Background(), "default", "bot")
	if len(a.Guardrails) != 1 {
		t.Fatalf("null must not remove the guardrails: %+v", a.Guardrails)
	}
}
