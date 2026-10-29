package tracetimeline

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

var update = flag.Bool("update", false, "rewrite the golden files")

var t0 = time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)

// sp builds a span that starts startMs after t0 and lasts durMs.
func sp(id, parent, name string, kind models.SpanKind, startMs, durMs int64) models.Span {
	return models.Span{
		ID: id, ParentSpanID: parent, Name: name, Kind: kind, Status: "completed",
		StartTime:  t0.Add(time.Duration(startMs) * time.Millisecond),
		EndTime:    t0.Add(time.Duration(startMs+durMs) * time.Millisecond),
		DurationMs: durMs,
	}
}

func llm(id, parent, name, model string, startMs, durMs int64, in, out int64, cost float64) models.Span {
	s := sp(id, parent, name, models.SpanKindLLM, startMs, durMs)
	s.Model, s.Provider = model, "openai"
	s.Usage = &models.TokenUsage{InputTokens: in, OutputTokens: out, TotalTokens: in + out, EstimatedCost: cost, PriceSource: "catalog"}
	return s
}

func trace() *models.Trace {
	return &models.Trace{ID: "t1", CreatedAt: t0, Environment: "prod", AgentVersion: "1.2.0"}
}

func golden(t *testing.T, name string, v any) {
	t.Helper()
	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", name+".golden.json")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("%s differs from its golden file; run with -update if the change is intended\n--- got ---\n%s", name, got)
	}
}

func TestPlainRun(t *testing.T) {
	spans := []models.Span{
		sp("root", "", "agent", models.SpanKindAgent, 0, 1000),
		llm("l1", "root", "llm.turn_1", "gpt-4o", 0, 400, 100, 20, 0.002),
		sp("tool1", "l1", "search", models.SpanKindTool, 200, 150),
		llm("l2", "root", "llm.turn_2", "gpt-4o", 400, 600, 300, 80, 0.006),
	}
	// Storage order must not matter.
	rev := []models.Span{spans[3], spans[1], spans[0], spans[2]}
	a, b := Build(trace(), spans, nil, Options{}), Build(trace(), rev, nil, Options{})
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if !bytes.Equal(ja, jb) {
		t.Fatalf("result depends on the order spans are stored in:\n%s\n%s", ja, jb)
	}
	// Two calls in a row: both decided when the run ended, and so did the tool inside the first.
	crit := map[string]bool{}
	for _, s := range a.Spans {
		crit[s.ID] = s.Critical
	}
	if !crit["l1"] || !crit["l2"] || !crit["tool1"] || a.CriticalPathMs != 1000 {
		t.Errorf("sequential run: critical=%v path=%d", crit, a.CriticalPathMs)
	}
	golden(t, "plain_run", a)
}

func TestRetriedCallAndFallback(t *testing.T) {
	route := sp("route", "", "router.route", models.SpanKindChain, 0, 900)
	f1 := sp("f1", "route", "llm.azure", models.SpanKindLLM, 0, 300)
	f1.Status, f1.Error = "failed", "429"
	f2 := sp("f2", "route", "llm.openai", models.SpanKindLLM, 300, 200)
	f2.Status, f2.Error = "failed", "500"
	ok := llm("ok", "route", "llm.anthropic", "claude", 500, 400, 50, 10, 0.001)
	tool1 := sp("tool1", "route", "lookup", models.SpanKindTool, 0, 10)
	tool1.Status = "failed"
	tool2 := sp("tool2", "route", "lookup", models.SpanKindTool, 20, 10)
	tool3 := sp("tool3", "route", "lookup", models.SpanKindTool, 40, 10) // a second call, not a retry: tool2 succeeded
	tl := Build(trace(), []models.Span{route, f1, f2, ok, tool1, tool2, tool3}, nil, Options{})
	byID := map[string]Span{}
	for _, s := range tl.Spans {
		byID[s.ID] = s
	}
	if byID["f2"].RetryOf != "f1" || byID["f2"].Attempt != 2 || byID["ok"].RetryOf != "f2" || byID["ok"].Attempt != 3 || byID["f1"].Attempt != 1 {
		t.Errorf("fallback chain wrong: f1=%+v f2=%+v ok=%+v", byID["f1"], byID["f2"], byID["ok"])
	}
	if byID["tool2"].RetryOf != "tool1" || byID["tool3"].RetryOf != "" || byID["tool3"].Attempt != 0 {
		t.Errorf("tool retry wrong: tool2=%+v tool3=%+v", byID["tool2"], byID["tool3"])
	}
	golden(t, "retry_and_fallback", tl)
}

func TestParallelChildrenCriticalPath(t *testing.T) {
	spans := []models.Span{
		sp("root", "", "recipe", models.SpanKindChain, 0, 1000),
		sp("a", "root", "branch-a", models.SpanKindAgent, 100, 300),
		sp("b", "root", "branch-b", models.SpanKindAgent, 100, 700),
		sp("b1", "b", "step-b1", models.SpanKindTool, 100, 200),
		sp("b2", "b", "step-b2", models.SpanKindTool, 300, 500),
	}
	tl := Build(trace(), spans, nil, Options{})
	crit := map[string]bool{}
	for _, s := range tl.Spans {
		crit[s.ID] = s.Critical
	}
	// b ends last; a ran beside it and finished sooner. Inside b, b1 ran before b2 and is on the chain.
	if !crit["root"] || !crit["b"] || !crit["b1"] || !crit["b2"] || crit["a"] {
		t.Errorf("critical flags: %v", crit)
	}
	// root 1000 less the 700 its children cover = 300; b 700 less the 700 its children cover = 0; b1 200; b2 500.
	if tl.CriticalPathMs != 1000 {
		t.Errorf("critical path = %d, want 1000", tl.CriticalPathMs)
	}
	golden(t, "parallel_children", tl)
}

func TestSkewedHostedAgentSpanIsClampedAndMarked(t *testing.T) {
	spans := []models.Span{
		sp("root", "", "agent", models.SpanKindAgent, 0, 1000),
		sp("hosted", "root", "hosted-agent", models.SpanKindAgent, -5000, 6200), // starts 5s early, ends 200ms late
		sp("late", "root", "late", models.SpanKindTool, 1000, 1),                // inside the 1 ms rounding tolerance after clamping
		sp("rounded", "root", "rounded", models.SpanKindTool, 900, 101),
	}
	tl := Build(trace(), spans, nil, Options{})
	byID := map[string]Span{}
	for _, s := range tl.Spans {
		byID[s.ID] = s
	}
	h := byID["hosted"]
	if !h.Skew || h.OffsetMs != 0 || h.DurationMs != 1000 {
		t.Errorf("hosted span not clamped into its parent: %+v", h)
	}
	if byID["rounded"].Skew {
		t.Errorf("1 ms overshoot is rounding, not skew: %+v", byID["rounded"])
	}
	if len(tl.Spans) != 4 {
		t.Errorf("a skewed span must never be dropped; got %d spans", len(tl.Spans))
	}
	golden(t, "skewed_span", tl)
}

func TestOrphansAttachToRoot(t *testing.T) {
	spans := []models.Span{
		sp("root", "", "agent", models.SpanKindAgent, 0, 1000),
		sp("kid", "root", "kid", models.SpanKindTool, 10, 10),
		sp("lost", "gone", "lost", models.SpanKindTool, 20, 10),            // parent not in the trace
		sp("lost-kid", "lost", "lost-kid", models.SpanKindTool, 21, 5),     // follows its parent
		sp("second-root", "", "second-root", models.SpanKindAgent, 30, 10), // a second root
		sp("loop-a", "loop-b", "loop-a", models.SpanKindTool, 40, 10),
		sp("loop-b", "loop-a", "loop-b", models.SpanKindTool, 41, 5),
	}
	tl := Build(trace(), spans, nil, Options{})
	byID := map[string]Span{}
	for _, s := range tl.Spans {
		byID[s.ID] = s
	}
	for _, id := range []string{"lost", "second-root", "loop-a"} {
		if !byID[id].Orphan || byID[id].Depth != 1 {
			t.Errorf("%s should be an orphan under the root: %+v", id, byID[id])
		}
	}
	if byID["lost-kid"].Orphan || byID["lost-kid"].Depth != 2 || byID["loop-b"].Orphan {
		t.Errorf("children of an orphan keep their parent: %+v %+v", byID["lost-kid"], byID["loop-b"])
	}
	if byID["kid"].Orphan || len(tl.Spans) != 7 {
		t.Errorf("a span with a parent in the trace is not an orphan; got %d spans", len(tl.Spans))
	}
	golden(t, "orphans", tl)
}

func TestLargeTracePagesAndTotalsCoverEverything(t *testing.T) {
	spans := []models.Span{sp("root", "", "agent", models.SpanKindAgent, 0, 100000)}
	for i := 0; i < 4999; i++ {
		spans = append(spans, llm(fmt.Sprintf("s%05d", i), "root", fmt.Sprintf("llm.turn_%d", i), "gpt-4o", int64(i*10), 10, 10, 5, 0.001))
	}
	first := Build(trace(), spans, nil, Options{Limit: 1000})
	if len(first.Spans) != 1000 || !first.Truncated || first.Cursor != "1000" || first.SpanCount != 5000 {
		t.Fatalf("first page: %d spans truncated=%v cursor=%q count=%d", len(first.Spans), first.Truncated, first.Cursor, first.SpanCount)
	}
	if first.Totals.InputTokens != 49990 || len(first.Models) != 1 || first.Models[0].Calls != 4999 {
		t.Errorf("totals must cover every span, not the page: %+v %+v", first.Totals, first.Models)
	}
	seen := map[string]bool{}
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		tl := Build(trace(), spans, nil, Options{Limit: 1000, Cursor: cursor})
		for _, s := range tl.Spans {
			if seen[s.ID] {
				t.Fatalf("span %s appears on two pages", s.ID)
			}
			seen[s.ID] = true
		}
		if !tl.Truncated {
			break
		}
		cursor = tl.Cursor
	}
	if len(seen) != 5000 {
		t.Errorf("paging returned %d of 5000 spans", len(seen))
	}
	// The golden file is the first page's shape and totals, not 1000 spans.
	first.Spans = first.Spans[:3]
	golden(t, "large_trace_first_page", first)
}

func TestNoSpans(t *testing.T) {
	tl := Build(&models.Trace{ID: "t", DurationMs: 42, CreatedAt: t0}, nil, nil, Options{})
	if tl.SchemaVersion != SchemaVersion || tl.Spans == nil || tl.Lanes == nil || tl.TotalMs != 42 {
		t.Errorf("empty trace: %+v", tl)
	}
}

func TestLanesAreOnTheSpanClock(t *testing.T) {
	lane := Lane{Name: "governance", Events: []LaneEvent{{At: t0.Add(250 * time.Millisecond), Action: "guardrail.output_blocked"}}}
	tl := Build(trace(), []models.Span{sp("root", "", "agent", models.SpanKindAgent, 0, 500)}, []Lane{lane}, Options{})
	if got := tl.Lanes[0].Events[0].OffsetMs; got != 250 {
		t.Errorf("lane event offset = %d, want 250", got)
	}
}
