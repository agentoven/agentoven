package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/internal/tracetimeline"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func seedTimelineTrace(t *testing.T, s store.Store) time.Time {
	t.Helper()
	ctx := models.WithTraceStamp(context.Background(), models.TraceStamp{Environment: "staging", AgentVersion: "2.0.0"})
	t0 := time.Now().UTC().Add(-time.Minute)
	if err := s.CreateTrace(ctx, &models.Trace{ID: "root", AgentName: "a", Kitchen: "default", Status: "completed", DurationMs: 500, CreatedAt: t0}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTrace(ctx, &models.Trace{ID: "child", AgentName: "b", Kitchen: "default", Status: "completed", ParentTraceID: "root", CreatedAt: t0}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTrace(ctx, &models.Trace{ID: "other", AgentName: "c", Kitchen: "default", Status: "completed", CreatedAt: t0}); err != nil {
		t.Fatal(err)
	}
	err := s.CreateSpans(ctx, []models.Span{
		{ID: "r", TraceID: "root", Name: "agent", Kind: models.SpanKindAgent, Status: "completed", StartTime: t0, EndTime: t0.Add(500 * time.Millisecond), DurationMs: 500},
		{ID: "l", TraceID: "root", ParentSpanID: "r", Name: "llm.turn_1", Kind: models.SpanKindLLM, Status: "completed", StartTime: t0, EndTime: t0.Add(300 * time.Millisecond), DurationMs: 300, Model: "gpt-4o", Usage: &models.TokenUsage{TotalTokens: 10, EstimatedCost: 0.01}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return t0
}

func TestTimelineIsComputedByTheAPIAndCarriesEnvironmentAndVersion(t *testing.T) {
	h, s := newTestHandlers(t)
	t0 := seedTimelineTrace(t, s)
	// A governance event on the root trace, and one on its child, and one on another trace.
	for _, ev := range []models.AuditEvent{
		{ID: "e1", Timestamp: t0.Add(100 * time.Millisecond), Action: "guardrail.output_blocked", Resource: "guardrails", Kitchen: "default", UserEmail: "chef@x.io", TraceID: "root"},
		{ID: "e2", Timestamp: t0.Add(200 * time.Millisecond), Action: "run.budget_paused", Resource: "run", Kitchen: "default", Details: map[string]interface{}{"trace_id": "child"}},
		{ID: "e3", Timestamp: t0.Add(50 * time.Millisecond), Action: "guardrail.input_blocked", Resource: "guardrails", Kitchen: "default", TraceID: "other"},
	} {
		ev := ev
		if err := s.CreateAuditEvent(context.Background(), &ev); err != nil {
			t.Fatal(err)
		}
	}
	tl, rec := getJSON[tracetimeline.Timeline](t, func(w http.ResponseWriter, r *http.Request) {
		h.TraceTimeline(w, withChiParam(r, "traceId", "root"))
	}, "/api/v1/traces/root/timeline")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if tl.SchemaVersion != tracetimeline.SchemaVersion || tl.Environment != "staging" || tl.AgentVersion != "2.0.0" {
		t.Errorf("timeline header: %+v", tl)
	}
	if len(tl.Spans) != 2 || tl.Spans[0].OffsetMs != 0 || tl.Spans[1].Depth != 1 || tl.TotalMs != 500 {
		t.Errorf("spans: %+v total=%d", tl.Spans, tl.TotalMs)
	}
	if len(tl.Models) != 1 || tl.Models[0].Model != "gpt-4o" || tl.Models[0].CostUSD != 0.01 {
		t.Errorf("model roll-up: %+v", tl.Models)
	}
	// The lane holds the events of this trace and of the traces under it, and not of another trace.
	if len(tl.Lanes) != 1 || tl.Lanes[0].Name != "governance" || len(tl.Lanes[0].Events) != 2 {
		t.Fatalf("governance lane: %+v", tl.Lanes)
	}
	if got := tl.Lanes[0].Events[0]; got.Action != "guardrail.output_blocked" || got.OffsetMs != 100 || got.Actor != "chef@x.io" {
		t.Errorf("first lane event: %+v", got)
	}
}

func TestTimelineOfATraceWithNoEventsHasAnEmptyLaneNotAnError(t *testing.T) {
	h, s := newTestHandlers(t)
	seedTimelineTrace(t, s)
	tl, rec := getJSON[tracetimeline.Timeline](t, func(w http.ResponseWriter, r *http.Request) {
		h.TraceTimeline(w, withChiParam(r, "traceId", "other"))
	}, "/api/v1/traces/other/timeline")
	if rec.Code != http.StatusOK || len(tl.Lanes) != 1 || len(tl.Lanes[0].Events) != 0 {
		t.Errorf("status %d lanes %+v", rec.Code, tl.Lanes)
	}
}

func TestTimelineOfAnotherKitchensTraceIsNotFound(t *testing.T) {
	h, s := newTestHandlers(t)
	seedTimelineTrace(t, s)
	req := withChiParam(withKitchen(httptest.NewRequest(http.MethodGet, "/x", nil), "other-kitchen"), "traceId", "root")
	rec := httptest.NewRecorder()
	h.TraceTimeline(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rec.Code)
	}
}

func TestTraceAuditListsTheTraceAndItsChildrenInOrderWithCoverage(t *testing.T) {
	h, s := newTestHandlers(t)
	t0 := seedTimelineTrace(t, s)
	old := models.AuditEvent{ID: "old", Timestamp: t0.Add(-time.Hour), Action: "agent.invoke", Resource: "agents", Kitchen: "default"} // before trace ids existed
	for _, ev := range []models.AuditEvent{
		old,
		{ID: "late", Timestamp: t0.Add(300 * time.Millisecond), Action: "approval.decided", Kitchen: "default", TraceID: "child"},
		{ID: "early", Timestamp: t0.Add(10 * time.Millisecond), Action: "guardrail.input_blocked", Kitchen: "default", TraceID: "root"},
	} {
		ev := ev
		if err := s.CreateAuditEvent(context.Background(), &ev); err != nil {
			t.Fatal(err)
		}
	}
	type resp struct {
		Count  int                 `json:"count"`
		Events []models.AuditEvent `json:"events"`
		Cov    struct {
			From     *time.Time `json:"from"`
			Complete bool       `json:"complete"`
		} `json:"coverage"`
	}
	out, rec := getJSON[resp](t, func(w http.ResponseWriter, r *http.Request) {
		h.TraceAudit(w, withChiParam(r, "traceId", "root"))
	}, "/api/v1/traces/root/audit")
	if rec.Code != http.StatusOK || out.Count != 2 || out.Events[0].ID != "early" || out.Events[1].ID != "late" {
		t.Fatalf("events out of order or missing: %d %+v", rec.Code, out.Events)
	}
	// Details carry the trace id, so a hash chain over them attests it.
	if out.Events[0].Details["trace_id"] != "root" {
		t.Errorf("trace id was not written into the details: %+v", out.Events[0].Details)
	}
	// The earliest event that names a trace came 10 ms after this trace began, so events from before
	// that may be missing and the answer must not claim to be complete.
	if out.Cov.From == nil || !out.Cov.From.After(t0) || out.Cov.Complete {
		t.Errorf("coverage: %+v", out.Cov)
	}
}

func TestTraceAuditSaysSoWhenNoEventHasEverNamedATrace(t *testing.T) {
	h, s := newTestHandlers(t)
	seedTimelineTrace(t, s)
	out, _ := getJSON[struct {
		Count int `json:"count"`
		Cov   struct {
			From     *time.Time `json:"from"`
			Complete bool       `json:"complete"`
			Note     string     `json:"note"`
		} `json:"coverage"`
	}](t, func(w http.ResponseWriter, r *http.Request) {
		h.TraceAudit(w, withChiParam(r, "traceId", "root"))
	}, "/api/v1/traces/root/audit")
	if out.Count != 0 || out.Cov.From != nil || out.Cov.Complete || out.Cov.Note == "" {
		t.Errorf("an empty answer must not imply nothing happened: %+v", out)
	}
}

func TestTracesFilterByEnvironmentAndAgentVersion(t *testing.T) {
	h, s := newTestHandlers(t)
	seedTimelineTrace(t, s)
	_ = s.CreateTrace(models.WithTraceStamp(context.Background(), models.TraceStamp{Environment: "prod", AgentVersion: "1.0.0"}),
		&models.Trace{ID: "p1", AgentName: "a", Kitchen: "default", Status: "completed", CreatedAt: time.Now().UTC()})
	list := func(q string) int {
		out, _ := getJSON[[]models.Trace](t, h.ListTraces, "/api/v1/traces"+q)
		return len(out)
	}
	if list("?environment=prod") != 1 || list("?environment=staging") != 3 || list("?agent_version=1.0.0") != 1 || list("?environment=prod&agent_version=2.0.0") != 0 {
		t.Errorf("environment/agent_version filters wrong")
	}
}

var _ = handlers.Handlers{}
