package executor_test

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/sessions"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

type callRecorder struct {
	mu    sync.Mutex
	calls []models.LLMCall
}

func (c *callRecorder) CaptureLLMCall(_ context.Context, call models.LLMCall) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, call)
	return nil
}

// The executor records its spans when the run ends, but older turns' requests are dropped as the run
// goes on, so the capture hook is called at each call, under the span ID the turn will be recorded
// under. The turn says which span that is.
func TestEachTurnIsCapturedAtTheCallUnderItsSpanID(t *testing.T) {
	d := &scriptedDriver{replies: []turnReply{
		{toolCalls: []models.ToolCallResult{toolCall("1", "lookup_order", map[string]interface{}{"order_id": "A-1"})}},
		{content: "delivered"},
	}}
	os.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	t.Cleanup(func() { s.Close() })
	mr := router.NewModelRouter(s)
	mr.RegisterDriver(d)
	_ = s.CreateProvider(context.Background(), &models.ModelProvider{Name: "p", Kitchen: "default", Kind: "scripted", Models: []string{"test-model"}, IsDefault: true})
	rec := &callRecorder{}
	mr.SetCallCapturer(rec)
	e := executor.NewExecutor(s, mr, newFakeGateway(), sessions.NewMemorySessionStore())

	_, trace, err := e.Execute(context.Background(), testAgent(), "where is A-1?", resolvedWithTools(orderTool()), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Turns) != 2 || len(rec.calls) != 2 {
		t.Fatalf("turns=%d captured=%d", len(trace.Turns), len(rec.calls))
	}
	for i, c := range rec.calls {
		if c.SpanID == "" || c.SpanID != trace.Turns[i].SpanID || c.TraceID != trace.TraceID || c.Turn != i+1 || c.AgentName != "test-agent" {
			t.Errorf("turn %d: captured %+v, turn span %q", i+1, c, trace.Turns[i].SpanID)
		}
		if c.Request == nil || len(c.Request.Tools) != 1 || c.Request.Tools[0].Function.Name != "lookup_order" || c.Response == nil {
			t.Errorf("turn %d request/response not whole: %+v", i+1, c)
		}
	}
	// The second request includes the first turn's tool result: each call is captured with what it saw.
	if len(rec.calls[1].Request.Messages) <= len(rec.calls[0].Request.Messages) {
		t.Errorf("the second call's request should carry the conversation so far")
	}
	if rec.calls[0].SpanID == rec.calls[1].SpanID {
		t.Errorf("two turns share a span id")
	}
}

// With no hook installed a turn still names its span, so span ids do not depend on whether capture is on.
func TestTurnsNameTheirSpanWithNoCaptureHook(t *testing.T) {
	d := &scriptedDriver{replies: []turnReply{{content: "hi"}}}
	e := newTestExecutor(t, d, newFakeGateway())
	_, trace, err := e.Execute(context.Background(), testAgent(), "hi", resolvedWithTools(), nil, false)
	if err != nil || trace.Turns[0].SpanID == "" {
		t.Fatalf("err=%v turns=%+v", err, trace.Turns)
	}
}
