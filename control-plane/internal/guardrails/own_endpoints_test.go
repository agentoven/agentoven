package guardrails

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func customRule(enabled bool) models.Guardrail {
	return models.Guardrail{ID: "mine", Kind: models.GuardrailCustom, Stage: models.GuardrailStageInput, Enabled: enabled,
		Config: map[string]interface{}{"endpoint": "https://hook.example.com/x", "secret_ref": "vault://hook"}}
}

// Owner decision S7: the edition's policy decides whether an agent's own custom endpoint may be used, and an
// unapproved one stops the agent the way a workspace rule that cannot be applied does.
func TestAnUnapprovedOwnCustomEndpointStopsTheAgent(t *testing.T) {
	t.Cleanup(func() { UseOwnEndpointPolicy(nil) })
	var asked []string
	UseOwnEndpointPolicy(func(_ context.Context, kitchen, agent string, own []models.Guardrail) error {
		asked = append(asked, kitchen+"/"+agent)
		return errors.New("the endpoint is not approved")
	})
	own := []models.Guardrail{customRule(true)}

	for name, src := range map[string]*fakeSource{"with workspace rules": {rules: []models.Guardrail{{ID: "ws", Kind: models.GuardrailPromptInjection, Stage: models.GuardrailStageInput, Enabled: true}}}, "without": {}} {
		_, err := Effective(context.Background(), src, "k1", "bot", own)
		var pe *PolicyError
		if !errors.As(err, &pe) || !strings.Contains(err.Error(), "not approved") || pe.Agent != "bot" {
			t.Errorf("%s: an unapproved endpoint must stop the agent: %v", name, err)
		}
	}
	if _, err := Effective(context.Background(), nil, "k1", "bot", own); err == nil {
		t.Error("with no workspace source too")
	}
	if len(asked) != 3 || asked[0] != "k1/bot" {
		t.Fatalf("asked %v", asked)
	}
	// A rule that is off, and rules that are not custom, are not asked about.
	asked = nil
	if _, err := Effective(context.Background(), &fakeSource{}, "k1", "bot", []models.Guardrail{customRule(false), {ID: "p", Kind: models.GuardrailPromptInjection, Stage: models.GuardrailStageInput, Enabled: true}}); err != nil || len(asked) != 0 {
		t.Fatalf("nothing to approve: %v %v", err, asked)
	}
}

func TestAnApprovedOwnCustomEndpointRunsAndIsStampedWithItsAgent(t *testing.T) {
	t.Cleanup(func() { UseOwnEndpointPolicy(nil) })
	UseOwnEndpointPolicy(func(context.Context, string, string, []models.Guardrail) error { return nil })
	got, err := Effective(context.Background(), &fakeSource{}, "k1", "bot", []models.Guardrail{customRule(true)})
	if err != nil || len(got) != 1 || got[0].KitchenID != "k1" || got[0].AgentName != "bot" {
		t.Fatalf("%+v %v", got, err)
	}
	// What a client sent for the kitchen and the agent is overwritten.
	forged := customRule(true)
	forged.KitchenID, forged.AgentName = "other-kitchen", "other-agent"
	got, _ = Effective(context.Background(), &fakeSource{}, "k1", "bot", []models.Guardrail{forged})
	if got[0].KitchenID != "k1" || got[0].AgentName != "bot" {
		t.Fatalf("the server says whose rule it is: %+v", got[0])
	}
}

func TestNoPolicyMeansNothingIsAsked(t *testing.T) {
	UseOwnEndpointPolicy(nil)
	if err := CheckOwnEndpoints(context.Background(), "k", "a", []models.Guardrail{customRule(true)}); err != nil {
		t.Fatal(err)
	}
}
