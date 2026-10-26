package router_test

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// fakeCatalog answers pricing lookups from a fixed table.
type fakeCatalog map[string]*models.ModelCapability

func (f fakeCatalog) Lookup(kind, model string) *models.ModelCapability {
	if c, ok := f[kind+"/"+model]; ok {
		return c
	}
	return f[model]
}

func routerWith(t *testing.T, cat router.CatalogLookup, providers ...models.ModelProvider) (*router.ModelRouter, store.Store) {
	t.Helper()
	os.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	t.Cleanup(func() { s.Close() })
	for i := range providers {
		providers[i].Kitchen = "default"
		if err := s.CreateProvider(context.Background(), &providers[i]); err != nil {
			t.Fatal(err)
		}
	}
	mr := router.NewModelRouter(s)
	if cat != nil {
		mr.SetCatalog(cat)
	}
	return mr, s
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestEachModelIsPricedAtItsOwnRateNotTheProvidersAverage(t *testing.T) {
	mr, _ := routerWith(t, nil,
		models.ModelProvider{Name: "oa", Kind: "openai", Models: []string{"gpt-4o", "gpt-4o-mini"}},
		// A provider-wide default must not flatten the models that have prices of their own.
		models.ModelProvider{Name: "flat", Kind: "openai", Models: []string{"gpt-4o", "house-model"},
			Config: map[string]interface{}{"cost_per_1k_input": 9.0, "cost_per_1k_output": 9.0}},
		models.ModelProvider{Name: "deal", Kind: "openai", Models: []string{"gpt-4o", "house-model"},
			Config: map[string]interface{}{"cost_per_1k_input": 9.0, "cost_per_1k_output": 9.0,
				"model_costs": map[string]interface{}{"gpt-4o": map[string]interface{}{"input": 0.001, "output": 0.004}}}},
	)
	ctx := context.Background()
	cases := []struct {
		provider, model, source string
		in, out                 float64
	}{
		{"oa", "gpt-4o", "builtin", 0.0025, 0.01},
		{"oa", "gpt-4o-mini", "builtin", 0.00015, 0.0006},
		{"oa", "gpt-4o-2024-08-06", "builtin", 0.0025, 0.01}, // a dated snapshot is priced like its model
		{"flat", "gpt-4o", "builtin", 0.0025, 0.01},          // the provider default does not override a known model
		{"flat", "house-model", "provider_default", 9, 9},    // it prices the models that have no price of their own
		{"deal", "gpt-4o", "model_override", 0.001, 0.004},   // an operator's price for one model wins
		{"deal", "house-model", "provider_default", 9, 9},
	}
	for _, c := range cases {
		p := mr.PriceFor(ctx, "default", c.provider, c.model)
		if p.Source != c.source || p.InputPer1K != c.in || p.OutputPer1K != c.out || !p.Known {
			t.Errorf("%s/%s: %+v, want %s %v/%v", c.provider, c.model, p, c.source, c.in, c.out)
		}
	}
}

func TestCostCountsCacheThinkingAndLongContextAtTheirOwnRates(t *testing.T) {
	p := router.Price{InputPer1K: 1, OutputPer1K: 4, CacheReadPer1K: 0.1, CacheWritePer1K: 1.25}
	// 1000 prompt tokens: 600 fresh, 300 read from cache, 100 written; 200 output.
	u := models.TokenUsage{InputTokens: 1000, OutputTokens: 200, CachedTokens: 300, CacheCreation: 100}
	if got, want := p.Cost(u), 0.6+0.03+0.125+0.8; !near(got, want) {
		t.Errorf("cache cost = %v, want %v", got, want)
	}
	// Thinking tokens are output tokens: billed at the output rate unless the model has its own rate.
	u = models.TokenUsage{InputTokens: 1000, OutputTokens: 300, ThinkingTokens: 100}
	if got, want := p.Cost(u), 1.0+1.2; !near(got, want) {
		t.Errorf("thinking at the output rate = %v, want %v", got, want)
	}
	p.ReasoningPer1K = 8
	if got, want := p.Cost(u), 1.0+0.8+0.8; !near(got, want) {
		t.Errorf("thinking at its own rate = %v, want %v", got, want)
	}
	// Past the long-context threshold the whole call bills at the higher tier.
	long := router.Price{InputPer1K: 1, OutputPer1K: 4, LongContextOver: 200000, LongInputPer1K: 2, LongOutputPer1K: 6}
	if got, want := long.Cost(models.TokenUsage{InputTokens: 300000, OutputTokens: 1000}), 600.0+6.0; !near(got, want) {
		t.Errorf("long-context cost = %v, want %v", got, want)
	}
	if got, want := long.Cost(models.TokenUsage{InputTokens: 100000, OutputTokens: 1000}), 100.0+4.0; !near(got, want) {
		t.Errorf("short-context cost = %v, want %v", got, want)
	}
}

func TestCostRoutingRanksProvidersByTheModelAskedFor(t *testing.T) {
	// "dear" is cheap for its first model and expensive for the one being asked for. Ranking providers
	// by their first model (or by an average) would send the call there.
	ok := `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	var served []string
	mk := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			served = append(served, name)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(ok))
		}))
	}
	dearSrv, cheapSrv := mk("dear"), mk("cheap")
	defer dearSrv.Close()
	defer cheapSrv.Close()
	costs := func(in, out float64) map[string]interface{} {
		return map[string]interface{}{"input": in, "output": out}
	}
	mr, _ := routerWith(t, nil,
		models.ModelProvider{Name: "dear", Kind: "openai", Endpoint: dearSrv.URL, Models: []string{"tiny", "m"},
			Config: map[string]interface{}{"api_key": "k", "model_costs": map[string]interface{}{"tiny": costs(0.0001, 0.0001), "m": costs(5, 5)}}},
		models.ModelProvider{Name: "cheap", Kind: "openai", Endpoint: cheapSrv.URL, Models: []string{"m"},
			Config: map[string]interface{}{"api_key": "k", "model_costs": map[string]interface{}{"m": costs(0.01, 0.01)}}},
	)
	if _, err := mr.Route(context.Background(), &models.RouteRequest{Kitchen: "default", Model: "m", Strategy: models.RoutingCostOptimized,
		Messages: []models.ChatMessage{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if len(served) != 1 || served[0] != "cheap" {
		t.Errorf("the call went to %v, want the provider that is cheaper for model m", served)
	}
}

// openAIServer answers chat completions with a fixed body and records the request.
func openAIServer(t *testing.T, body string, got *map[string]interface{}) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if got != nil {
			_ = json.Unmarshal(raw, got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestOpenAIReasoningAndCachingAreCapturedAndPriced(t *testing.T) {
	var sent map[string]interface{}
	srv := openAIServer(t, `{"id":"x","choices":[{"message":{"content":"42","reasoning_content":"first, add them"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":1000,"completion_tokens":200,"total_tokens":1200,
		"prompt_tokens_details":{"cached_tokens":400},"completion_tokens_details":{"reasoning_tokens":50}}}`, &sent)
	cat := fakeCatalog{"o3-mini": {InputCostPer1K: 0.001, OutputCostPer1K: 0.004, CacheReadCostPer1K: 0.0005, SupportsThinking: true, Source: "catalog"}}
	mr, _ := routerWith(t, cat, models.ModelProvider{Name: "oa", Kind: "openai", Endpoint: srv.URL, Models: []string{"o3-mini"}, Config: map[string]interface{}{"api_key": "k"}})

	resp, err := mr.Route(context.Background(), &models.RouteRequest{Kitchen: "default", Model: "o3-mini", ThinkingEnabled: true, ThinkingBudget: 1500,
		Messages: []models.ChatMessage{{Role: "user", Content: "2+2?"}}})
	if err != nil {
		t.Fatal(err)
	}
	if sent["reasoning_effort"] != "low" {
		t.Errorf("thinking was not requested of the model: %v", sent)
	}
	if len(resp.ThinkingBlocks) != 1 || resp.ThinkingBlocks[0].Content != "first, add them" || resp.ThinkingBlocks[0].TokenCount != 50 {
		t.Errorf("thinking = %+v", resp.ThinkingBlocks)
	}
	u := resp.Usage
	if u.ThinkingTokens != 50 || u.CachedTokens != 400 || u.InputTokens != 1000 || u.OutputTokens != 200 {
		t.Errorf("usage = %+v", u)
	}
	// 600 fresh prompt tokens, 400 cached, 200 output (thinking included).
	if want := 0.6*0.001 + 0.4*0.0005 + 0.2*0.004; !near(u.EstimatedCost, want) || u.PriceSource != "catalog" {
		t.Errorf("cost = %v (%s), want %v", u.EstimatedCost, u.PriceSource, want)
	}
	if !near(u.CacheSavingsUSD, 0.4*(0.001-0.0005)) {
		t.Errorf("cache savings = %v", u.CacheSavingsUSD)
	}
}

func TestThinkingIsNotSentToAModelThatCannotThink(t *testing.T) {
	var sent map[string]interface{}
	srv := openAIServer(t, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, &sent)
	mr, _ := routerWith(t, nil, models.ModelProvider{Name: "oa", Kind: "openai", Endpoint: srv.URL, Models: []string{"gpt-4o"}, Config: map[string]interface{}{"api_key": "k"}})
	if _, err := mr.Route(context.Background(), &models.RouteRequest{Kitchen: "default", Model: "gpt-4o", ThinkingEnabled: true, Messages: []models.ChatMessage{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := sent["reasoning_effort"]; ok {
		t.Errorf("a parameter gpt-4o does not have was sent: %v", sent)
	}
}

func TestOllamaAndOpenRouterReasoningAreCaptured(t *testing.T) {
	srv := openAIServer(t, `{"choices":[{"message":{"content":"<think>weigh both</think>go left","reasoning":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":9,"total_tokens":14}}`, nil)
	mr, _ := routerWith(t, nil, models.ModelProvider{Name: "local", Kind: "ollama", Endpoint: srv.URL, Models: []string{"deepseek-r1"}})
	resp, err := mr.Route(context.Background(), &models.RouteRequest{Kitchen: "default", Model: "deepseek-r1", ThinkingEnabled: true, Messages: []models.ChatMessage{{Role: "user", Content: "which way?"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "go left" || len(resp.ThinkingBlocks) != 1 || resp.ThinkingBlocks[0].Content != "weigh both" {
		t.Errorf("content %q, thinking %+v", resp.Content, resp.ThinkingBlocks)
	}
	if resp.Usage.EstimatedCost != 0 || resp.Usage.PriceSource != "local" {
		t.Errorf("a local model is free: %+v", resp.Usage)
	}

	srv2 := openAIServer(t, `{"choices":[{"message":{"content":"yes","reasoning_details":[{"type":"reasoning.text","text":"step one"},{"type":"reasoning.summary","summary":"in short"}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":9,"total_tokens":14}}`, nil)
	mr2, _ := routerWith(t, nil, models.ModelProvider{Name: "or", Kind: "openrouter", Endpoint: srv2.URL, Models: []string{"some/model"}, Config: map[string]interface{}{"api_key": "k"}})
	resp, err = mr2.Route(context.Background(), &models.RouteRequest{Kitchen: "default", Model: "some/model", Messages: []models.ChatMessage{{Role: "user", Content: "ok?"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ThinkingBlocks) != 1 || resp.ThinkingBlocks[0].Content != "step one\nin short" {
		t.Errorf("reasoning_details = %+v", resp.ThinkingBlocks)
	}
}

func TestAnthropicThinkingIsRequestedCapturedAndSentBackWithToolResults(t *testing.T) {
	var reqs []map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var m map[string]interface{}
		_ = json.Unmarshal(raw, &m)
		reqs = append(reqs, m)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"m1","stop_reason":"tool_use","content":[
			{"type":"thinking","thinking":"I should look it up","signature":"SIG-1"},
			{"type":"redacted_thinking","data":"OPAQUE"},
			{"type":"text","text":"checking"},
			{"type":"tool_use","id":"tu1","name":"lookup","input":{"q":"x"}}],
			"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":400,"cache_creation_input_tokens":200}}`))
	}))
	defer srv.Close()
	cat := fakeCatalog{"claude-sonnet-4-5": {InputCostPer1K: 0.003, OutputCostPer1K: 0.015, CacheReadCostPer1K: 0.0003, CacheWriteCostPer1K: 0.00375, SupportsThinking: true, Source: "catalog"}}
	mr, _ := routerWith(t, cat, models.ModelProvider{Name: "an", Kind: "anthropic", Endpoint: srv.URL, Models: []string{"claude-sonnet-4-5"}, Config: map[string]interface{}{"api_key": "k"}})

	tool := models.ToolDefinition{Type: "function"}
	tool.Function.Name = "lookup"
	req := &models.RouteRequest{Kitchen: "default", Model: "claude-sonnet-4-5", ThinkingEnabled: true, ThinkingBudget: 2000, Tools: []models.ToolDefinition{tool},
		Messages: []models.ChatMessage{{Role: "user", Content: "find x"}}}
	resp, err := mr.Route(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	th, _ := reqs[0]["thinking"].(map[string]interface{})
	if th["type"] != "enabled" || th["budget_tokens"] != float64(2000) || reqs[0]["max_tokens"].(float64) <= 2000 {
		t.Errorf("thinking request = %v, max_tokens %v", th, reqs[0]["max_tokens"])
	}
	if len(resp.ThinkingBlocks) != 2 || resp.ThinkingBlocks[0].Signature != "SIG-1" || !resp.ThinkingBlocks[1].Redacted || resp.ThinkingBlocks[1].Data != "OPAQUE" {
		t.Fatalf("thinking blocks = %+v", resp.ThinkingBlocks)
	}
	u := resp.Usage
	if u.InputTokens != 700 || u.CachedTokens != 400 || u.CacheCreation != 200 || u.TotalTokens != 750 {
		t.Errorf("usage = %+v", u)
	}
	if want := 0.1*0.003 + 0.4*0.0003 + 0.2*0.00375 + 0.05*0.015; !near(u.EstimatedCost, want) {
		t.Errorf("cost = %v, want %v", u.EstimatedCost, want)
	}

	// The next turn carries the tool result; the assistant turn must start with the same thinking.
	req.Messages = append(req.Messages,
		models.ChatMessage{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls, Thinking: resp.ThinkingBlocks},
		models.ChatMessage{Role: "tool", ToolCallID: "tu1", Content: "found"})
	if _, err := mr.Route(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	msgs := reqs[1]["messages"].([]interface{})
	blocks := msgs[1].(map[string]interface{})["content"].([]interface{})
	first, second := blocks[0].(map[string]interface{}), blocks[1].(map[string]interface{})
	if first["type"] != "thinking" || first["signature"] != "SIG-1" || first["thinking"] != "I should look it up" ||
		second["type"] != "redacted_thinking" || second["data"] != "OPAQUE" {
		t.Errorf("the assistant turn did not start with the model's thinking: %v", blocks)
	}
	if blocks[len(blocks)-1].(map[string]interface{})["type"] != "tool_use" {
		t.Errorf("blocks = %v", blocks)
	}
}

func TestGeminiThoughtsAreCapturedAndBilledAsOutput(t *testing.T) {
	var sent map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &sent)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"considering options","thought":true},{"text":"Blue."}]},"finishReason":"STOP"}],
			"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"thoughtsTokenCount":30,"totalTokenCount":150,"cachedContentTokenCount":40}}`))
	}))
	defer srv.Close()
	cat := fakeCatalog{"gemini-2.5-flash": {InputCostPer1K: 0.001, OutputCostPer1K: 0.01, CacheReadCostPer1K: 0.00025, Source: "catalog"}}
	mr, _ := routerWith(t, cat, models.ModelProvider{Name: "g", Kind: "gemini", Endpoint: srv.URL, Models: []string{"gemini-2.5-flash"}, Config: map[string]interface{}{"api_key": "k"}})
	resp, err := mr.Route(context.Background(), &models.RouteRequest{Kitchen: "default", Model: "gemini-2.5-flash", ThinkingEnabled: true, ThinkingBudget: 4000, Messages: []models.ChatMessage{{Role: "user", Content: "colour?"}}})
	if err != nil {
		t.Fatal(err)
	}
	tc := sent["generationConfig"].(map[string]interface{})["thinkingConfig"].(map[string]interface{})
	if tc["includeThoughts"] != true || tc["thinkingBudget"] != float64(4000) {
		t.Errorf("thinkingConfig = %v", tc)
	}
	if resp.Content != "Blue." || len(resp.ThinkingBlocks) != 1 || resp.ThinkingBlocks[0].Content != "considering options" || resp.ThinkingBlocks[0].TokenCount != 30 {
		t.Errorf("content %q thinking %+v", resp.Content, resp.ThinkingBlocks)
	}
	u := resp.Usage
	if u.OutputTokens != 50 || u.ThinkingTokens != 30 || u.CachedTokens != 40 {
		t.Errorf("usage = %+v", u)
	}
	// 60 fresh prompt tokens, 40 cached, 50 output tokens of which 30 were thinking: thinking is billed.
	if want := 0.06*0.001 + 0.04*0.00025 + 0.05*0.01; !near(u.EstimatedCost, want) {
		t.Errorf("cost = %v, want %v", u.EstimatedCost, want)
	}
}

func TestARoutedCallLeavesATraceWithSpansForEveryProviderTried(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"no such model","type":"invalid_request_error"}}`))
	}))
	defer bad.Close()
	good := openAIServer(t, `{"choices":[{"message":{"content":"hello","reasoning_content":"say hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"completion_tokens_details":{"reasoning_tokens":3}}}`, nil)
	mr, st := routerWith(t, nil,
		models.ModelProvider{Name: "first", Kind: "openai", Endpoint: bad.URL, Models: []string{"gpt-4o"}, Config: map[string]interface{}{"api_key": "k"}},
		models.ModelProvider{Name: "second", Kind: "openai", Endpoint: good.URL, Models: []string{"gpt-4o"}, Config: map[string]interface{}{"api_key": "k"}},
	)
	resp, err := mr.Route(context.Background(), &models.RouteRequest{Kitchen: "default", Model: "gpt-4o", Messages: []models.ChatMessage{{Role: "user", Content: "hi there"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Attempts) != 1 || resp.Attempts[0].Provider != "first" || resp.Attempts[0].Error == "" {
		t.Errorf("attempts = %+v", resp.Attempts)
	}
	traces, _ := st.ListTraces(context.Background(), "default", 10)
	if len(traces) != 1 {
		t.Fatalf("traces = %d", len(traces))
	}
	tr, err := st.GetTraceWithSpans(context.Background(), traces[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if tr.AgentName != "router" || tr.InputText != "hi there" || tr.OutputText != "hello" || len(tr.ThinkingBlocks) != 1 || tr.Usage == nil || tr.Usage.ThinkingTokens != 3 {
		t.Errorf("trace = %+v", tr)
	}
	var root, failed, answered *models.Span
	for i := range tr.Spans {
		sp := &tr.Spans[i]
		switch {
		case sp.ParentSpanID == "":
			root = sp
		case sp.Status == "failed":
			failed = sp
		default:
			answered = sp
		}
	}
	if root == nil || failed == nil || answered == nil {
		t.Fatalf("spans = %+v", tr.Spans)
	}
	if failed.Provider != "first" || failed.Error == "" || failed.ParentSpanID != root.ID {
		t.Errorf("failed span = %+v", failed)
	}
	if answered.Provider != "second" || answered.Model != "gpt-4o" || answered.Usage == nil || answered.Usage.EstimatedCost <= 0 ||
		!strings.Contains(string(answered.Output), "say hi") || answered.ParentSpanID != root.ID {
		t.Errorf("answered span = %+v", answered)
	}
}

func TestAFailedRoutedCallIsATraceToo(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadRequest) }))
	defer bad.Close()
	mr, st := routerWith(t, nil, models.ModelProvider{Name: "only", Kind: "openai", Endpoint: bad.URL, Models: []string{"gpt-4o"}, Config: map[string]interface{}{"api_key": "k"}})
	if _, err := mr.Route(context.Background(), &models.RouteRequest{Kitchen: "default", Model: "gpt-4o", Messages: []models.ChatMessage{{Role: "user", Content: "hi"}}}); err == nil {
		t.Fatal("expected an error")
	}
	traces, _ := st.ListTraces(context.Background(), "default", 10)
	if len(traces) != 1 || traces[0].Status != "failed" {
		t.Fatalf("traces = %+v", traces)
	}
	tr, _ := st.GetTraceWithSpans(context.Background(), traces[0].ID)
	if len(tr.Spans) != 2 {
		t.Errorf("spans = %d", len(tr.Spans))
	}
}
