package handlers_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/sessions"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// An agent that delegates to another reaches it with the target's own guardrails applied, and can
// reach a framework-native agent through its process.

func delegatingHandlers(t *testing.T, install bool, target *models.Agent) *handlers.Handlers {
	t.Helper()
	h, s := newNativeHandlers(t, target)
	mr := router.NewModelRouter(s)
	h.Router = mr
	h.Executor = executor.NewExecutor(s, mr, nil, sessions.NewMemorySessionStore())
	if install {
		h.InstallDelegationBoundary()
	}
	return h
}

func delegate(t *testing.T, h *handlers.Handlers, target, message string) contracts.ToolRunResult {
	t.Helper()
	parent := &models.Agent{Name: "parent", Kitchen: "default", Mode: models.AgentModeManaged, Subagents: []string{target}}
	args, _ := json.Marshal(map[string]string{"agent": target, "message": message})
	return h.Executor.RunToolCall(context.Background(), parent, &models.ResolvedIngredients{}, "c1", "agentoven_delegate", args)
}

func TestDelegatingToAFrameworkNativeAgentReachesItsProcess(t *testing.T) {
	for _, rt := range nativeRuntimes {
		t.Run(string(rt), func(t *testing.T) {
			p := newFakeProcess(t, "The process answered cleanly.")
			h := delegatingHandlers(t, true, nativeAgent(rt, p.srv.URL))
			res := delegate(t, h, "native-agent", "hello there")
			if res.IsError || !strings.Contains(res.Content, "process answered cleanly") {
				t.Fatalf("%+v", res)
			}
			if p.calls() != 1 {
				t.Fatalf("the delegation is a call to the agent's own process, not to the raw model: %d calls", p.calls())
			}
		})
	}
}

func TestADelegatedMessageIsHeldToTheTargetsInputGuardrails(t *testing.T) {
	p := newFakeProcess(t, "should never be asked")
	h := delegatingHandlers(t, true, nativeAgent(models.RuntimeLangGraph, p.srv.URL))
	res := delegate(t, h, "native-agent", "please discuss "+blockedWord)
	mustHold(t, res.IsError && strings.Contains(res.Content, "blocked by native-agent's input guardrails"), "a delegation skipped the target's input rules")
	mustHold(t, p.calls() == 0, "the blocked message reached the process")
	mustHold(t, !strings.Contains(res.Content, blockedWord), "the blocked content was echoed back to the delegating agent's model")
}

func TestADelegatedAnswerIsHeldToTheTargetsOutputGuardrails(t *testing.T) {
	p := newFakeProcess(t, "Sure, the SSN is "+leakedSSN)
	h := delegatingHandlers(t, true, nativeAgent(models.RuntimeLangChain, p.srv.URL))
	res := delegate(t, h, "native-agent", "what is the number")
	mustHold(t, res.IsError && strings.Contains(res.Content, "withheld by its output guardrails"), "a delegated answer skipped the target's output rules")
	mustHold(t, !strings.Contains(res.Content, leakedSSN), "the leaked value reached the delegating agent")
}

func TestAManagedTargetsInputGuardrailsAlsoApplyToADelegation(t *testing.T) {
	a := nativeAgent("", "")
	a.Name, a.Process = "managed-target", nil
	a.ResolvedConfig = &models.ResolvedIngredients{Model: &models.ResolvedModel{Model: "m"}}
	h := delegatingHandlers(t, true, a)
	res := delegate(t, h, "managed-target", blockedWord)
	mustHold(t, res.IsError && strings.Contains(res.Content, "blocked by managed-target's input guardrails"), "a managed target's input rules were skipped on delegation")
}

func TestWithoutTheBoundaryAFrameworkNativeAgentIsNotRunInItsPlace(t *testing.T) {
	p := newFakeProcess(t, "unused")
	h := delegatingHandlers(t, false, nativeAgent(models.RuntimeLangGraph, p.srv.URL))
	res := delegate(t, h, "native-agent", "hello")
	if !res.IsError || !strings.Contains(res.Content, "cannot be delegated to here") {
		t.Fatalf("an executor with no way to reach the process must refuse, not answer from the raw model: %+v", res)
	}
	if p.calls() != 0 {
		t.Fatal("no call")
	}
}
