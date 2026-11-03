package handlers_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// Owner decision A8: a retired agent comes back only by being registered again. Every write that would change
// it, or bring it back as a side effect (a cool used to turn it into a cooled agent), answers 409 and says so.
func TestLifecycleWritesOnARetiredAgentAreRefused(t *testing.T) {
	h, s := bakeHandlers(t)
	a, _ := s.GetAgent(context.Background(), "default", "bot")
	a.Status = models.AgentStatusRetired
	if err := s.UpdateAgent(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func() (int, string){
		"update": func() (int, string) { return agentCall(t, h.UpdateAgent, "PUT", `{"description":"x"}`) },
		"bake":   func() (int, string) { return agentCall(t, h.BakeAgent, "POST", `{}`) },
		"recook": func() (int, string) { return agentCall(t, h.RecookAgent, "POST", `{}`) },
		"cool":   func() (int, string) { return agentCall(t, h.CoolAgent, "POST", ``) },
		"rewarm": func() (int, string) { return agentCall(t, h.RewarmAgent, "POST", ``) },
	} {
		code, body := call()
		if code != http.StatusConflict || !strings.Contains(body, "is retired") || !strings.Contains(body, "POST /api/v1/agents") {
			t.Errorf("%s: %d %s, want 409 and the way back", name, code, body)
		}
	}
	got, _ := s.GetAgent(context.Background(), "default", "bot")
	if got.Status != models.AgentStatusRetired || got.Description != "be brief" {
		t.Fatalf("a refused write changes nothing: %+v", got)
	}
}

// The check is also made where the write lands: an agent retired between the read and the cool stays retired.
func TestACoolCannotReviveAnAgentRetiredMeanwhile(t *testing.T) {
	_, s := bakeHandlers(t)
	a, _ := s.GetAgent(context.Background(), "default", "bot")
	a.Status = models.AgentStatusRetired
	if err := s.UpdateAgent(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	applied, err := s.UpdateAgentLifecycle(context.Background(), "default", "bot", models.AgentLifecycleGuard{Not: models.AgentStatusRetired},
		models.AgentLifecycle{Status: models.AgentStatusCooled})
	if err != nil || applied {
		t.Fatalf("a guard that excludes retired must not apply to a retired agent: %v %v", applied, err)
	}
	if got, _ := s.GetAgent(context.Background(), "default", "bot"); got.Status != models.AgentStatusRetired {
		t.Fatalf("%s", got.Status)
	}
}
