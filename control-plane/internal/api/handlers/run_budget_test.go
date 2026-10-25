package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/internal/guardrails"
	"github.com/agentoven/agentoven/control-plane/internal/resolver"
	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/sessions"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// toolLoopDriver asks for the lookup tool for its first n calls, then answers. Every call
// reports 100 tokens.
type toolLoopDriver struct {
	mu    sync.Mutex
	calls int
	loops int
	final string // the closing answer; "all finished" when empty
	gate  chan struct{}
	same  bool // ask for the identical tool call every time
}

func (d *toolLoopDriver) Kind() string { return "looping" }
func (d *toolLoopDriver) Call(_ context.Context, p *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, error) {
	d.mu.Lock()
	d.calls++
	n := d.calls
	d.mu.Unlock()
	if d.gate != nil {
		<-d.gate
	}
	resp := &models.RouteResponse{Provider: p.Name, Model: req.Model, FinishReason: "stop",
		Usage: models.TokenUsage{InputTokens: 90, OutputTokens: 10, TotalTokens: 100}}
	if n <= d.loops {
		q := fmt.Sprint("x", n)
		if d.same {
			q = "same"
		}
		raw, _ := json.Marshal(map[string]string{"q": q})
		resp.FinishReason = "tool_calls"
		resp.ToolCalls = []models.ToolCallResult{{ID: "t", Type: "function", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "lookup", Arguments: string(raw)}}}
		return resp, nil
	}
	resp.Content = "all finished"
	if d.final != "" {
		resp.Content = d.final
	}
	return resp, nil
}
func (d *toolLoopDriver) HealthCheck(context.Context, *models.ModelProvider) error { return nil }

type lookupGateway struct{}

func (lookupGateway) HandleJSONRPC(_ context.Context, _ string, req *models.MCPRequest) *models.MCPResponse {
	return &models.MCPResponse{Jsonrpc: "2.0", ID: req.ID, Result: models.MCPToolResult{
		Content: []models.MCPContent{{Type: "text", Text: "found"}},
	}}
}

func budgetHandlers(t *testing.T, loops int, budget models.RunBudget, approvalTools []string) *handlers.Handlers {
	t.Helper()
	return budgetHandlersWith(t, loops, budget, approvalTools, "", nil)
}

func budgetHandlersWith(t *testing.T, loops int, budget models.RunBudget, approvalTools []string, final string, rules []models.Guardrail) *handlers.Handlers {
	t.Helper()
	return taskHandlers(t, &toolLoopDriver{loops: loops, final: final}, budget, approvalTools, rules, executor.TaskManagerOptions{})
}

func taskHandlers(t *testing.T, drv *toolLoopDriver, budget models.RunBudget, approvalTools []string, rules []models.Guardrail, taskOpts executor.TaskManagerOptions) *handlers.Handlers {
	t.Helper()
	os.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	t.Cleanup(func() { s.Close() })

	mr := router.NewModelRouter(s)
	mr.RegisterDriver(drv)
	if err := s.CreateProvider(context.Background(), &models.ModelProvider{
		Name: "p", Kitchen: "default", Kind: "looping", Models: []string{"m"}, IsDefault: true,
	}); err != nil {
		t.Fatal(err)
	}
	agent := &models.Agent{
		Name: "worker", Kitchen: "default", Mode: models.AgentModeManaged, Status: models.AgentStatusReady,
		ResolvedConfig: &models.ResolvedIngredients{
			Model: &models.ResolvedModel{Model: "m", Provider: "p"},
			Tools: []models.ResolvedTool{{Name: "lookup", Schema: map[string]interface{}{"type": "object"}}},
		},
		MaxTurns: 50, Budget: &budget, ApprovalTools: approvalTools, Guardrails: rules,
	}
	if err := s.CreateAgent(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	sess := sessions.NewMemorySessionStore()
	ex := executor.NewExecutor(s, mr, lookupGateway{}, sess)
	j, err := executor.NewFileJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ex.SetJournal(j)
	return &handlers.Handlers{
		Store: s, Router: mr, Executor: ex, Resolver: resolver.NewResolver(s),
		Tasks:           executor.NewTaskManager(ex, taskOpts),
		PromptValidator: &contracts.CommunityPromptValidator{},
		Guardrails:      &guardrails.CommunityGuardrailService{},
		Sessions:        sess,
	}
}

func decode(t *testing.T, body string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("not JSON: %q", body)
	}
	return m
}

func TestInvokeAnswers402WithTheNumbersWhenARunHitsItsBudget(t *testing.T) {
	h := budgetHandlers(t, 100, models.RunBudget{MaxTokens: 350}, nil)
	rec := call(h.InvokeAgent, "/invoke", msg("go"), map[string]string{"agentName": "worker"})

	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("expected 402, got %d: %s", rec.Code, rec.Body.String())
	}
	b := decode(t, rec.Body.String())
	if b["error"] != "run_budget_exhausted" || b["dimension"] != "tokens" || b["cap"] != float64(350) || b["trace_id"] == "" || b["status"] != "paused" {
		t.Fatalf("the 402 should say what was hit and which run to resume: %v", b)
	}
	if r, _ := b["resume"].(string); !strings.Contains(r, "/agents/worker/runs/") {
		t.Fatalf("expected a resume pointer, got %v", b["resume"])
	}
}

func TestResumeWithAnExtensionFinishesTheRun(t *testing.T) {
	h := budgetHandlers(t, 3, models.RunBudget{MaxTokens: 350}, nil)
	rec := call(h.InvokeAgent, "/invoke", msg("go"), map[string]string{"agentName": "worker"})
	id, _ := decode(t, rec.Body.String())["trace_id"].(string)
	if rec.Code != http.StatusPaymentRequired || id == "" {
		t.Fatalf("setup: expected a 402 with a trace id, got %d %s", rec.Code, rec.Body.String())
	}
	params := map[string]string{"agentName": "worker", "traceID": id}

	// Saying yes without saying how much more is refused, and does not spend the pause.
	rec = call(h.ResumeRun, "/resume", `{"approved":true}`, params)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("approving a budget pause with no extension should be 400, got %d: %s", rec.Code, rec.Body.String())
	}

	// A resume through another agent's URL finds nothing.
	rec = call(h.ResumeRun, "/resume", `{"approved":true,"extend":{"add_tokens":1000}}`, map[string]string{"agentName": "someone-else", "traceID": id})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("a run must only resume through its own agent, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = call(h.ResumeRun, "/resume", `{"approved":true,"extend":{"add_tokens":1000,"reason":"keep going"}}`, params)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if b := decode(t, rec.Body.String()); b["response"] != "all finished" || b["status"] != "completed" {
		t.Fatalf("unexpected resume body: %v", b)
	}
}

func TestResumeThatHitsTheNewCapAnswers402Again(t *testing.T) {
	h := budgetHandlers(t, 100, models.RunBudget{MaxTokens: 350}, nil)
	rec := call(h.InvokeAgent, "/invoke", msg("go"), map[string]string{"agentName": "worker"})
	id, _ := decode(t, rec.Body.String())["trace_id"].(string)

	rec = call(h.ResumeRun, "/resume", `{"approved":true,"extend":{"add_tokens":100}}`, map[string]string{"agentName": "worker", "traceID": id})
	if rec.Code != http.StatusPaymentRequired || decode(t, rec.Body.String())["trace_id"] != id {
		t.Fatalf("a run that runs out again should be paused again on the same run, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDenyingABudgetPauseEndsTheRun(t *testing.T) {
	h := budgetHandlers(t, 100, models.RunBudget{MaxTokens: 350}, nil)
	rec := call(h.InvokeAgent, "/invoke", msg("go"), map[string]string{"agentName": "worker"})
	id, _ := decode(t, rec.Body.String())["trace_id"].(string)

	rec = call(h.ResumeRun, "/resume", `{"approved":false,"note":"enough"}`, map[string]string{"agentName": "worker", "traceID": id})
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("ending a run at its cap is reported as the cap, got %d: %s", rec.Code, rec.Body.String())
	}
	rec = call(h.ResumeRun, "/resume", `{"approved":true,"extend":{"add_tokens":100}}`, map[string]string{"agentName": "worker", "traceID": id})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an ended run cannot be resumed, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestADollarCapOnAnUnpricedModelIs422(t *testing.T) {
	h := budgetHandlers(t, 0, models.RunBudget{MaxCostUSD: 1}, nil)
	rec := call(h.InvokeAgent, "/invoke", msg("go"), map[string]string{"agentName": "worker"})
	if rec.Code != http.StatusUnprocessableEntity || decode(t, rec.Body.String())["error"] != "unpriced_model" {
		t.Fatalf("expected 422 unpriced_model, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAnApprovalPauseIs202NotABadGateway(t *testing.T) {
	h := budgetHandlers(t, 100, models.RunBudget{}, []string{"lookup"})
	rec := call(h.InvokeAgent, "/invoke", msg("go"), map[string]string{"agentName": "worker"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("a run waiting for approval is not an error, expected 202 got %d: %s", rec.Code, rec.Body.String())
	}
	b := decode(t, rec.Body.String())
	if b["status"] != "paused" || b["trace_id"] == "" || b["pending"] == nil {
		t.Fatalf("the 202 should say what is pending: %v", b)
	}
}

func TestAResumedRunsAnswerStillPassesTheOutputGuardrails(t *testing.T) {
	// Resuming is another way for an agent's words to reach a caller; it must not be a way
	// around the rules the first request was held to.
	h := budgetHandlersWith(t, 3, models.RunBudget{MaxTokens: 350}, nil, "their SSN is "+leakedSSN, nativeGuardrails())
	rec := call(h.InvokeAgent, "/invoke", msg("go"), map[string]string{"agentName": "worker"})
	id, _ := decode(t, rec.Body.String())["trace_id"].(string)
	if rec.Code != http.StatusPaymentRequired || id == "" {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body.String())
	}

	rec = call(h.ResumeRun, "/resume", `{"approved":true,"extend":{"add_tokens":5000}}`, map[string]string{"agentName": "worker", "traceID": id})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a leaky resumed answer should be blocked, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), leakedSSN) {
		t.Fatalf("the blocked text reached the caller: %s", rec.Body.String())
	}
}

func TestOnlyCallersAllowedToRaiseABudgetCanDoSo(t *testing.T) {
	h := budgetHandlersWith(t, 6, models.RunBudget{MaxTokens: 350}, nil, "done", nil)
	rec := call(h.InvokeAgent, "/invoke", msg("go"), map[string]string{"agentName": "worker"})
	id, _ := decode(t, rec.Body.String())["trace_id"].(string)
	params := map[string]string{"agentName": "worker", "traceID": id}

	var asked int
	h.AuthorizeBudgetExtension = func(*http.Request) error {
		asked++
		return errors.New("your role cannot extend budgets")
	}
	rec = call(h.ResumeRun, "/resume", `{"approved":true,"extend":{"add_tokens":9000}}`, params)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "budget:extend") {
		t.Fatalf("expected a 403 naming the permission, got %d: %s", rec.Code, rec.Body.String())
	}
	// A refusal does not consume the pause...
	h.AuthorizeBudgetExtension = func(*http.Request) error { return nil }
	rec = call(h.ResumeRun, "/resume", `{"approved":true,"extend":{"add_tokens":9000}}`, params)
	if rec.Code != http.StatusOK {
		t.Fatalf("the pause should still be there for someone who may extend it, got %d: %s", rec.Code, rec.Body.String())
	}
	if asked != 1 {
		t.Fatalf("asked %d", asked)
	}
}

func TestEndingAPausedRunNeedsNoBudgetPermission(t *testing.T) {
	h := budgetHandlersWith(t, 6, models.RunBudget{MaxTokens: 350}, nil, "done", nil)
	rec := call(h.InvokeAgent, "/invoke", msg("go"), map[string]string{"agentName": "worker"})
	id, _ := decode(t, rec.Body.String())["trace_id"].(string)
	h.AuthorizeBudgetExtension = func(*http.Request) error { return errors.New("no") }
	rec = call(h.ResumeRun, "/resume", `{"approved":false,"note":"stop"}`, map[string]string{"agentName": "worker", "traceID": id})
	if rec.Code == http.StatusForbidden {
		t.Fatalf("declining to spend more is always allowed: %d %s", rec.Code, rec.Body.String())
	}
}
