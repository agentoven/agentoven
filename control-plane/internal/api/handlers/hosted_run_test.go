package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// A hosted process records what the control plane sends it; the scope provider records what the
// control plane asks of accounting.
type scopeProvider struct {
	mu      sync.Mutex
	opened  []openedScope
	outcome models.RunOutcome
}

type openedScope struct {
	kitchen, agent, runID string
	budget                models.RunBudget
}

func (p *scopeProvider) Open(_ context.Context, kitchen, agent, runID string, b models.RunBudget) contracts.RunScope {
	p.mu.Lock()
	p.opened = append(p.opened, openedScope{kitchen, agent, runID, b})
	p.mu.Unlock()
	return scopeFunc(func() models.RunOutcome { return p.outcome })
}

type scopeFunc func() models.RunOutcome

func (f scopeFunc) Close() models.RunOutcome { return f() }

type seenCall struct {
	path, header string
	body         map[string]interface{}
}

func recordingProcess(t *testing.T) (*httptest.Server, func() []seenCall) {
	t.Helper()
	var mu sync.Mutex
	var seen []seenCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		seen = append(seen, seenCall{path: r.URL.Path, header: r.Header.Get(handlers.RunIDHeader), body: body})
		mu.Unlock()
		switch r.URL.Path {
		case "/invoke/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"type\":\"token\",\"content\":\"ok\"}\n\ndata: {\"type\":\"done\"}\n\n"))
		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"response": "fine", "usage": map[string]int{"total_tokens": 7},
				"jsonrpc": "2.0", "id": 1, "result": map[string]interface{}{"status": map[string]string{"state": "completed"}}})
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []seenCall { mu.Lock(); defer mu.Unlock(); return append([]seenCall(nil), seen...) }
}

func hostedHandlers(t *testing.T, budget *models.RunBudget, p *scopeProvider) (*handlers.Handlers, func() []seenCall) {
	t.Helper()
	srv, seen := recordingProcess(t)
	agent := nativeAgent(models.RuntimeDeepAgents, srv.URL)
	agent.Guardrails = nil
	agent.Budget = budget
	h, _ := newNativeHandlers(t, agent)
	if p != nil {
		h.RunScopes = p
	}
	return h, seen
}

func TestAHostedProcessIsToldWhichRunItsModelCallsBelongTo(t *testing.T) {
	p := &scopeProvider{outcome: models.RunOutcome{Usage: models.TokenUsage{TotalTokens: 321, EstimatedCost: 0.12}, Calls: 3}}
	h, seen := hostedHandlers(t, &models.RunBudget{MaxTokens: 5000}, p)

	rec := call(h.InvokeAgent, "/invoke", msg("hi"), map[string]string{"agentName": "native-agent"})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if len(p.opened) != 1 || p.opened[0].agent != "native-agent" || p.opened[0].kitchen != "default" || p.opened[0].budget.MaxTokens != 5000 {
		t.Fatalf("a scope under the agent's budget should have been opened: %+v", p.opened)
	}
	call1 := seen()[0]
	if call1.header != p.opened[0].runID || call1.body["run_id"] != p.opened[0].runID || call1.body["trace_id"] != p.opened[0].runID {
		t.Fatalf("the process must be handed the run ID in the header and the body: header=%q body=%v want %q", call1.header, call1.body, p.opened[0].runID)
	}

	// What the gateway counted is what the run cost, not what the process says it did.
	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if u, _ := out["usage"].(map[string]interface{}); u["total_tokens"] != float64(321) {
		t.Fatalf("the response should carry the gateway's count: %v", out["usage"])
	}
}

func TestAHostedCallThatHitItsBudgetIs402WithWhatItSpent(t *testing.T) {
	p := &scopeProvider{outcome: models.RunOutcome{
		Usage:   models.TokenUsage{TotalTokens: 5000},
		Stopped: &models.BudgetPause{Dimension: models.BudgetDimTokens, Spent: 5000, Cap: 5000, Scope: "gateway"},
	}}
	h, _ := hostedHandlers(t, &models.RunBudget{MaxTokens: 5000}, p)
	rec := call(h.InvokeAgent, "/invoke", msg("hi"), map[string]string{"agentName": "native-agent"})
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("expected 402, got %d: %s", rec.Code, rec.Body.String())
	}
	b := decode(t, rec.Body.String())
	if b["error"] != "run_budget_exhausted" || b["scope"] != "gateway" || b["dimension"] != "tokens" || b["status"] != "stopped" {
		t.Fatalf("%v", b)
	}
	if _, resumable := b["resume"]; resumable {
		t.Fatal("a hosted process cannot be checkpointed from outside, so it is not offered a resume")
	}
}

func TestNoScopeIsOpenedWithoutABudgetOrWithoutAProvider(t *testing.T) {
	p := &scopeProvider{}
	h, seen := hostedHandlers(t, nil, p)
	call(h.InvokeAgent, "/invoke", msg("hi"), map[string]string{"agentName": "native-agent"})
	if len(p.opened) != 0 || seen()[0].header != "" || seen()[0].body["run_id"] != nil {
		t.Fatalf("an agent with no budget is not scoped: %+v header=%q", p.opened, seen()[0].header)
	}

	h2, seen2 := hostedHandlers(t, &models.RunBudget{MaxTokens: 10}, nil)
	rec := call(h2.InvokeAgent, "/invoke", msg("hi"), map[string]string{"agentName": "native-agent"})
	if rec.Code != http.StatusOK || seen2()[0].header != "" {
		t.Fatalf("the community edition has no scopes and runs as before: %d header=%q", rec.Code, seen2()[0].header)
	}
}

func TestStreamingAndA2ACallsAreScopedToo(t *testing.T) {
	p := &scopeProvider{}
	h, seen := hostedHandlers(t, &models.RunBudget{MaxTokens: 5000}, p)
	params := map[string]string{"agentName": "native-agent"}

	call(h.StreamInvokeAgent, "/invoke/stream", msg("hi"), params)
	call(h.A2AAgentEndpoint, "/a2a", a2aBody("hi"), params)
	if len(p.opened) != 2 {
		t.Fatalf("each hosted call gets its own scope, opened %d", len(p.opened))
	}
	for i, c := range seen() {
		if c.header != p.opened[i].runID {
			t.Fatalf("call %d (%s) was not told its run: header %q, want %q", i, c.path, c.header, p.opened[i].runID)
		}
	}
	if p.opened[0].runID == p.opened[1].runID {
		t.Fatal("each call is its own run")
	}
}
