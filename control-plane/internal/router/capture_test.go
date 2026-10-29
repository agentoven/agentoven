package router_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

type recordingCapturer struct {
	mu    sync.Mutex
	calls []models.LLMCall
	err   error
}

func (c *recordingCapturer) CaptureLLMCall(_ context.Context, call models.LLMCall) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, call)
	return c.err
}

// A routed call is handed to the capture hook whole, under the ID of the span that records it, once
// for every provider tried: the ones that failed with their error, the one that answered with its
// response.
func TestCaptureHookReceivesEveryRoutedCallUnderItsSpan(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"no such model"}}`))
	}))
	defer bad.Close()
	good := openAIServer(t, `{"id":"chatcmpl-123","choices":[{"message":{"content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`, nil)
	mr, st := routerWith(t, nil,
		models.ModelProvider{Name: "first", Kind: "openai", Endpoint: bad.URL, Models: []string{"gpt-4o"}, Config: map[string]interface{}{"api_key": "k"}},
		models.ModelProvider{Name: "second", Kind: "openai", Endpoint: good.URL, Models: []string{"gpt-4o"}, Config: map[string]interface{}{"api_key": "k"}},
	)
	rc := &recordingCapturer{}
	mr.SetCallCapturer(rc)

	temp, maxTok := 0.2, 64
	req := &models.RouteRequest{
		Kitchen: "default", Model: "gpt-4o", Temperature: &temp, MaxTokens: &maxTok, StopSequences: []string{"END"},
		ToolChoice: "auto", Messages: []models.ChatMessage{{Role: "system", Content: "be brief"}, {Role: "user", Content: "hi there"}},
		Tools: []models.ToolDefinition{{Type: "function", Function: models.ToolFunction{Name: "lookup"}}},
	}
	if _, err := mr.Route(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(rc.calls) != 2 {
		t.Fatalf("captured %d calls, want 2 (one failed provider, one answer)", len(rc.calls))
	}
	traces, _ := st.ListTraces(context.Background(), "default", 10)
	tr, _ := st.GetTraceWithSpans(context.Background(), traces[0].ID)
	spans := map[string]models.Span{}
	for _, sp := range tr.Spans {
		spans[sp.ID] = sp
	}
	for _, c := range rc.calls {
		sp, ok := spans[c.SpanID]
		if !ok || sp.Kind != models.SpanKindLLM || c.TraceID != tr.ID || c.Kitchen != "default" {
			t.Errorf("captured call %+v does not match a span of the trace", c)
		}
		if c.Request == nil || c.Request.Temperature == nil || *c.Request.Temperature != 0.2 || *c.Request.MaxTokens != 64 ||
			len(c.Request.Tools) != 1 || len(c.Request.Messages) != 2 || c.Request.StopSequences[0] != "END" {
			t.Errorf("the request was not captured whole: %+v", c.Request)
		}
	}
	failed, answered := rc.calls[0], rc.calls[1]
	if failed.Response != nil || failed.Error == "" {
		t.Errorf("failed provider: %+v", failed)
	}
	if answered.Response == nil || answered.Response.ID != "chatcmpl-123" || answered.Response.Content != "hello" || answered.Error != "" {
		t.Errorf("answering provider: %+v", answered.Response)
	}
}

// A hook that fails never fails the call.
func TestCaptureHookFailureDoesNotFailTheCall(t *testing.T) {
	good := openAIServer(t, `{"choices":[{"message":{"content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, nil)
	mr, _ := routerWith(t, nil, models.ModelProvider{Name: "only", Kind: "openai", Endpoint: good.URL, Models: []string{"gpt-4o"}, Config: map[string]interface{}{"api_key": "k"}})
	rc := &recordingCapturer{err: context.DeadlineExceeded}
	mr.SetCallCapturer(rc)
	resp, err := mr.Route(context.Background(), &models.RouteRequest{Kitchen: "default", Model: "gpt-4o", Messages: []models.ChatMessage{{Role: "user", Content: "hi"}}})
	if err != nil || resp.Content != "hello" || len(rc.calls) != 1 {
		t.Fatalf("resp=%+v err=%v captured=%d", resp, err, len(rc.calls))
	}
}

// A caller can choose the ID of the trace a routed call leaves, so it can name that trace in its answer.
func TestACallerCanChooseTheRoutedTraceID(t *testing.T) {
	good := openAIServer(t, `{"choices":[{"message":{"content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, nil)
	mr, st := routerWith(t, nil, models.ModelProvider{Name: "only", Kind: "openai", Endpoint: good.URL, Models: []string{"gpt-4o"}, Config: map[string]interface{}{"api_key": "k"}})
	ctx := models.WithRouteTraceID(context.Background(), "11111111-2222-3333-4444-555555555555")
	if _, err := mr.Route(ctx, &models.RouteRequest{Kitchen: "default", Model: "gpt-4o", Messages: []models.ChatMessage{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if tr, err := st.GetTrace(context.Background(), "11111111-2222-3333-4444-555555555555"); err != nil || tr.AgentName != "router" {
		t.Fatalf("trace by chosen id: %+v %v", tr, err)
	}
}

// A call labelled with a parent trace is recorded under it, so work made of several calls reads as one tree.
func TestACallCanBeRecordedUnderAParentTrace(t *testing.T) {
	good := openAIServer(t, `{"choices":[{"message":{"content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, nil)
	mr, st := routerWith(t, nil, models.ModelProvider{Name: "only", Kind: "openai", Endpoint: good.URL, Models: []string{"gpt-4o"}, Config: map[string]interface{}{"api_key": "k"}})
	if _, err := mr.Route(context.Background(), &models.RouteRequest{Kitchen: "default", Model: "gpt-4o", Labels: map[string]string{"parent_trace_id": "parent-1"}, Messages: []models.ChatMessage{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	traces, _ := st.ListTraces(context.Background(), "default", 10)
	if len(traces) != 1 || traces[0].ParentTraceID != "parent-1" {
		t.Fatalf("traces = %+v", traces)
	}
}
