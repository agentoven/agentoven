package handlers_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	grails "github.com/agentoven/agentoven/control-plane/internal/guardrails"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// Owner decision S7: a bake and a re-cook are refused with 409 while a custom endpoint the agent names in its
// own guardrails is not approved, and go ahead once it is.
func TestABakeIsRefusedWhileAnOwnCustomEndpointIsNotApproved(t *testing.T) {
	h, s := bakeHandlers(t)
	a, _ := s.GetAgent(context.Background(), "default", "bot")
	a.Guardrails = append(a.Guardrails, models.Guardrail{ID: "hook", Kind: models.GuardrailCustom, Stage: models.GuardrailStageInput, Enabled: true,
		Config: map[string]interface{}{"endpoint": "https://hook.example.com/x", "secret_ref": "vault://hook"}})
	if err := s.UpdateAgent(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	approved := false
	grails.UseOwnEndpointPolicy(func(_ context.Context, kitchen, agent string, _ []models.Guardrail) error {
		if kitchen != "default" || agent != "bot" {
			t.Errorf("asked about %s/%s", kitchen, agent)
		}
		if !approved {
			return errors.New("the endpoint hook.example.com is not approved")
		}
		return nil
	})
	t.Cleanup(func() { grails.UseOwnEndpointPolicy(nil) })

	code, body := agentCall(t, h.BakeAgent, "POST", `{}`)
	if code != http.StatusConflict || !strings.Contains(body, "guardrail_endpoint_not_approved") || !strings.Contains(body, "not approved") {
		t.Fatalf("bake: %d %s", code, body)
	}
	if got, _ := s.GetAgent(context.Background(), "default", "bot"); got.Status != models.AgentStatusDraft {
		t.Fatalf("a refused bake changes nothing: %s", got.Status)
	}
	approved = true
	if code, body := agentCall(t, h.BakeAgent, "POST", `{}`); code != http.StatusAccepted {
		t.Fatalf("approved: %d %s", code, body)
	}
	waitStatus(t, s, models.AgentStatusReady)
	approved = false
	if code, body := agentCall(t, h.RecookAgent, "POST", `{}`); code != http.StatusConflict || !strings.Contains(body, "guardrail_endpoint_not_approved") {
		t.Fatalf("recook after the approval was withdrawn: %d %s", code, body)
	}
}
