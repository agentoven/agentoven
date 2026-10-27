package workflow

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// An agent whose process refuses the call (here a 401 whose body is not a JSON-RPC answer) is a failed
// step. It used to be recorded as a completed one whose "output" was the error, so a recipe reported
// success while no agent had run.
func TestAnAgentThatRefusesTheCallFailsTheStepInsteadOfCompletingIt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"authentication_required","message":"This agent only accepts calls from the control plane."}`))
	}))
	defer srv.Close()
	eng := setupTestEngine(t, srv)
	_ = eng.store.CreateAgent(context.Background(), &models.Agent{ID: "a", Name: "planner", Kitchen: "default", Status: models.AgentStatusReady, Mode: models.AgentModeManaged})

	run := runRecipeOn(t, eng, map[string]interface{}{},
		models.Step{Name: "plan", Kind: models.StepAgent, AgentRef: "planner"})
	if run.Status != models.RecipeRunFailed {
		t.Fatalf("a recipe whose only agent refused the call is %q, want failed", run.Status)
	}
	step := stepResult(run, "plan")
	if step.Status == "completed" || !strings.Contains(step.Error, "refused the call (HTTP 401)") || !strings.Contains(step.Error, "only accepts calls from the control plane") {
		t.Errorf("step = %q, error = %q", step.Status, step.Error)
	}
}

// An agent that reports its turns (the built-in runner does) gives its recipe step tokens, a cost at
// its provider's price for the model, and spans with the model, provider and thinking.
func TestAStepCarriesTheTokensCostAndSpansOfTheTurnsTheAgentReports(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"status":{"state":"completed"},
			"artifacts":[{"parts":[{"type":"text","text":"done"}]}],
			"metadata":{"exec_trace":{"agent_name":"planner","total_ms":50,"turns":[
				{"number":1,"model":"claude-haiku-4-5","latency_ms":30,"usage":{"input_tokens":1000,"output_tokens":100,"total_tokens":1100},
				 "thinking_blocks":[{"content":"plan the work","token_count":40}],
				 "tool_calls":[{"id":"t1","name":"lookup","arguments":{"q":"x"}}],"tool_results":[{"tool_call_id":"t1","name":"lookup","content":"found"}]},
				{"number":2,"model":"claude-haiku-4-5","latency_ms":20,"usage":{"input_tokens":2000,"output_tokens":50,"total_tokens":2050},"response":"done"}]}}}}`))
	}))
	defer srv.Close()
	eng := setupTestEngine(t, srv)
	var priced []string
	eng.SetPricer(func(_ context.Context, kitchen, provider, model string, u *models.TokenUsage) {
		priced = append(priced, provider+"/"+model)
		u.EstimatedCost = float64(u.TotalTokens) / 1000 // $1 per 1K, for an easy sum
		u.PriceSource = "catalog"
	})
	_ = eng.store.CreateAgent(context.Background(), &models.Agent{ID: "a", Name: "planner", Kitchen: "default", Status: models.AgentStatusReady,
		Mode: models.AgentModeManaged, ModelProvider: "anthropic", ModelName: "claude-haiku-4-5"})

	run := runRecipeOn(t, eng, map[string]interface{}{}, models.Step{Name: "plan", Kind: models.StepAgent, AgentRef: "planner"})
	if run.Status != models.RecipeRunCompleted {
		t.Fatalf("%s: %s", run.Status, run.Error)
	}
	step := stepResult(run, "plan")
	if step.Tokens != 3150 || step.CostUSD < 3.149 || step.CostUSD > 3.151 {
		t.Errorf("tokens %d cost %v, want 3150 and 3.15", step.Tokens, step.CostUSD)
	}
	if len(priced) != 2 || priced[0] != "anthropic/claude-haiku-4-5" {
		t.Errorf("turns were priced as %v", priced)
	}
	traces, _ := eng.store.ListTraces(context.Background(), "default", 20)
	var stepTrace *models.Trace
	for i := range traces {
		if traces[i].AgentName == "planner" {
			stepTrace, _ = eng.store.GetTraceWithSpans(context.Background(), traces[i].ID)
		}
	}
	if stepTrace == nil {
		t.Fatal("no trace for the step")
	}
	var llm, tool int
	for _, sp := range stepTrace.Spans {
		switch sp.Kind {
		case models.SpanKindLLM:
			llm++
			if sp.Model != "claude-haiku-4-5" || sp.Provider != "anthropic" || sp.Usage == nil || sp.Usage.EstimatedCost <= 0 {
				t.Errorf("llm span = %+v", sp)
			}
		case models.SpanKindTool:
			tool++
		}
	}
	if llm != 2 || tool != 1 || !strings.Contains(string(stepTrace.Spans[1].Output), "plan the work") {
		t.Errorf("llm spans %d, tool spans %d, spans %d", llm, tool, len(stepTrace.Spans))
	}
}
