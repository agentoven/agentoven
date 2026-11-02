package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// A reader who may see a trace but not its content gets timings, usage and cost, and never a body, on every
// endpoint that returns one (ADR-0043).
func TestAReaderWithoutContentAccessSeesNoBodies(t *testing.T) {
	h, s := newTestHandlers(t)
	seedTraces(t, s)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.CreateSpan(ctx, &models.Span{ID: "sp1", TraceID: "t1", Name: "llm", Kind: models.SpanKindLLM, StartTime: now, EndTime: now,
		Input: json.RawMessage(`{"messages":[{"role":"user","content":"my card is 4111"}]}`), Output: json.RawMessage(`"secret reply"`),
		Events: []models.SpanEvent{{Name: "first token", Timestamp: now, Attributes: map[string]interface{}{"text": "secret"}}}}); err != nil {
		t.Fatal(err)
	}
	allowed := true
	h.SetTraceContentGuard(func(*http.Request) bool { return allowed })

	do := func(handler http.HandlerFunc, url string, param ...string) string {
		req := withKitchen(httptest.NewRequest(http.MethodGet, url, nil), "default")
		for i := 0; i+1 < len(param); i += 2 {
			req = withChiParam(req, param[i], param[i+1])
		}
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", url, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	calls := map[string]func() string{
		"list":     func() string { return do(h.ListTraces, "/traces") },
		"get":      func() string { return do(h.GetTrace, "/traces/t1?spans=true", "traceId", "t1") },
		"spans":    func() string { return do(h.ListSpans, "/traces/t1/spans", "traceId", "t1") },
		"one span": func() string { return do(h.GetSpan, "/spans/sp1", "spanId", "sp1") },
	}

	allowed = true
	for name, call := range calls {
		body := call()
		if name != "list" && !strings.Contains(body, "4111") && !strings.Contains(body, "Where is my refund") {
			t.Errorf("%s: a reader with content access must see the body: %s", name, body)
		}
	}

	allowed = false
	for name, call := range calls {
		body := call()
		for _, leak := range []string{"4111", "secret", "Where is my refund", "out t1"} {
			if strings.Contains(body, leak) {
				t.Errorf("%s leaks %q to a reader without content access: %s", name, leak, body)
			}
		}
		if !strings.Contains(body, "content_withheld") {
			t.Errorf("%s must say the content is withheld", name)
		}
	}
	// What they may see is still there.
	if body := calls["get"](); !strings.Contains(body, `"cost_usd":0.01`) || !strings.Contains(body, `"duration_ms":800`) {
		t.Errorf("timings and cost must remain: %s", body)
	}
	// A search over the bodies would reveal what they hold, so it is ignored for them.
	if body := do(h.ListTraces, "/traces?q=refund"); strings.Count(body, `"id":`) < 4 {
		t.Errorf("a search must not narrow what a reader without content access sees: %s", body)
	}
}
