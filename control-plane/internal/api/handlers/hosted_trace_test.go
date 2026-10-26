package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// A hosted AgentOven runner reports what it did turn by turn. The trace the control plane records
// for it must have spans, the model's thinking and a cost at that model's own price, like a run
// in-process does. (Before, it was one flat record with tokens and no cost.)
func TestAHostedRunnersTurnsBecomeSpansWithThinkingAndCost(t *testing.T) {
	var got map[string]interface{}
	proc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":"4","model":"gpt-4o","provider":"openai",
			"usage":{"input_tokens":2000,"output_tokens":300,"total_tokens":2300,"cached_tokens":1000,"thinking_tokens":100},
			"turns":[
			 {"number":1,"model":"gpt-4o","latency_ms":40,"usage":{"input_tokens":1000,"output_tokens":100,"total_tokens":1100,"thinking_tokens":40},
			  "response":"","thinking_blocks":[{"content":"I need the calculator","token_count":40}],
			  "tool_calls":[{"id":"t1","name":"calc","arguments":{"expr":"2+2"}}],"tool_results":[{"tool_call_id":"t1","name":"calc","content":"4","is_error":false}]},
			 {"number":2,"model":"gpt-4o","latency_ms":30,"usage":{"input_tokens":1000,"output_tokens":200,"total_tokens":1200,"cached_tokens":1000,"thinking_tokens":60},
			  "response":"4","thinking_blocks":[{"content":"the tool said 4","token_count":60}]}]}`))
	}))
	defer proc.Close()

	agent := nativeAgent(models.RuntimeAgentOven, proc.URL)
	agent.Guardrails = nil
	agent.ModelProvider, agent.ModelName = "oa", "gpt-4o"
	h, s := newNativeHandlers(t, agent)
	h.Router = router.NewModelRouter(s)
	if err := s.CreateProvider(context.Background(), &models.ModelProvider{Name: "oa", Kitchen: "default", Kind: "openai", Models: []string{"gpt-4o"},
		Config: map[string]interface{}{"cost_per_1k_input": 50.0, "cost_per_1k_output": 50.0}}); err != nil {
		t.Fatal(err)
	}

	rec := call(h.InvokeAgent, "/api/v1/agents/native-agent/invoke", `{"message":"2+2?","thinking_enabled":true}`, map[string]string{"agentName": "native-agent"})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if got["thinking_enabled"] != true {
		t.Errorf("the runner was not told to think: %v", got)
	}
	var out struct {
		TraceID string `json:"trace_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	tr, err := s.GetTraceWithSpans(context.Background(), out.TraceID)
	if err != nil {
		t.Fatal(err)
	}
	// gpt-4o has its own price (0.0025 in / 0.01 out), which the provider-wide default (50) must not override.
	want := 1.0*0.0025 + 0.1*0.01 + (0*0.0025 + 1.0*0.0025/1) + 0.2*0.01 // turn 1, then turn 2 (1000 cached, no cache rate = input rate)
	if tr.CostUSD < want-1e-9 || tr.CostUSD > want+1e-9 {
		t.Errorf("cost = %v, want %v (gpt-4o's own price)", tr.CostUSD, want)
	}
	if tr.Usage == nil || tr.Usage.ThinkingTokens != 100 || tr.Usage.CachedTokens != 1000 || tr.Usage.PriceSource != "builtin" {
		t.Errorf("usage = %+v", tr.Usage)
	}
	if len(tr.ThinkingBlocks) != 2 {
		t.Errorf("thinking blocks = %+v", tr.ThinkingBlocks)
	}
	var llm, tools int
	for _, sp := range tr.Spans {
		switch sp.Kind {
		case models.SpanKindLLM:
			llm++
			if sp.Model != "gpt-4o" || sp.Provider != "oa" || !strings.Contains(string(sp.Output), "thinking_blocks") {
				t.Errorf("llm span = %+v", sp)
			}
		case models.SpanKindTool:
			tools++
		}
	}
	if llm != 2 || tools != 1 {
		t.Errorf("llm spans %d, tool spans %d (spans: %d)", llm, tools, len(tr.Spans))
	}
}
