package executor_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func journalDirExecutor(t *testing.T, d *costDriver, gw *fakeGateway) (*executor.Executor, *executor.FileJournal, string) {
	t.Helper()
	e, j, dir, _ := journalDirExecutorStore(t, d, gw)
	return e, j, dir
}

func journalDirExecutorStore(t *testing.T, d *costDriver, gw *fakeGateway) (*executor.Executor, *executor.FileJournal, string, store.Store) {
	t.Helper()
	e, _, s := newBudgetExecutor(t, d, gw, false)
	dir := t.TempDir()
	j, err := executor.NewFileJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	e.SetJournal(j)
	return e, j, dir, s
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

// ── growth ────────────────────────────────────────────────────

func TestAJournalGrowsWithTheRunNotWithItsSquare(t *testing.T) {
	big := strings.Repeat("x", 1000)
	run := func(turns int) int64 {
		gw := newFakeGateway()
		gw.register("lookup_order", func(map[string]interface{}) (string, bool) { return big, false })
		d := &costDriver{replies: loopOf(turns, "done"), in: 10, out: 5}
		e, _, dir := journalDirExecutor(t, d, gw)
		a := testAgent()
		a.MaxTurns = turns + 5
		_, trace, err := e.Execute(context.Background(), a, "go", resolvedWithTools(orderTool()), nil, false)
		if err != nil {
			t.Fatal(err)
		}
		return fileSize(t, filepath.Join(dir, trace.TraceID+".jsonl"))
	}
	small, large := run(100), run(200)
	// Twice the turns is about twice the file. Under the old format (the whole history on
	// every turn) it was about four times.
	if ratio := float64(large) / float64(small); ratio > 2.4 {
		t.Fatalf("doubling the run grew the journal %.1fx (%d -> %d bytes), it should be near 2x", ratio, small, large)
	}
}

func TestTheTraceKeepsFullRequestsOnlyForRecentTurns(t *testing.T) {
	d := &costDriver{replies: loopOf(30, "done"), in: 10, out: 5}
	e, _, _ := journalDirExecutor(t, d, orderGateway())
	a := testAgent()
	a.MaxTurns = 40
	_, trace, err := e.Execute(context.Background(), a, "go", resolvedWithTools(orderTool()), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if trace.Turns[0].Request != nil || trace.Turns[5].Request != nil {
		t.Fatal("old turns should not keep the whole history they were sent")
	}
	if last := trace.Turns[len(trace.Turns)-1]; last.Request == nil {
		t.Fatal("the latest turn keeps its request")
	}
	if len(trace.Turns[0].ToolCalls) == 0 {
		t.Fatal("older turns still keep their tool calls")
	}
}

// ── replay ────────────────────────────────────────────────────

func TestLoadRebuildsTheSameConversationFromDeltas(t *testing.T) {
	d := &costDriver{replies: loopOf(5, "done"), in: 10, out: 5}
	e, j, _ := journalDirExecutor(t, d, orderGateway())
	a := testAgent()
	a.MaxTurns = 10
	_, trace, err := e.Execute(context.Background(), a, "go", resolvedWithTools(orderTool()), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	js, err := j.Load(context.Background(), trace.TraceID)
	if err != nil {
		t.Fatal(err)
	}
	// system + user + 5 x (assistant tool call, tool result)
	if js.CompletedTurns != 6 || js.Outcome != executor.OutcomeCompleted {
		t.Fatalf("turns=%d outcome=%q", js.CompletedTurns, js.Outcome)
	}
	var roles []string
	for _, m := range js.Messages {
		roles = append(roles, m.Role)
	}
	got := strings.Join(roles, ",")
	if !strings.HasPrefix(got, "system,user,assistant,tool,assistant,tool") && !strings.HasPrefix(got, "user,assistant,tool,assistant,tool") {
		t.Fatalf("unexpected history shape: %s", got)
	}
	if js.Usage.TotalTokens != 6*15 {
		t.Fatalf("usage = %d, want %d", js.Usage.TotalTokens, 6*15)
	}
}

func TestARewrittenHistoryIsStoredWholeNotAsABrokenDelta(t *testing.T) {
	_, j, _ := journalDirExecutor(t, &costDriver{}, newFakeGateway())
	ctx := context.Background()
	msg := func(r, c string) models.ChatMessage { return models.ChatMessage{Role: r, Content: c} }
	agent := testAgent()
	if err := j.Start(ctx, executor.JournalHeader{TraceID: "r1", Kitchen: "default", Agent: agent, StartedAt: time.Now(),
		InitialMessages: []models.ChatMessage{msg("user", "a"), msg("assistant", "b")}}); err != nil {
		t.Fatal(err)
	}
	_ = j.CommitTurn(ctx, "r1", executor.Turn{Number: 1}, []models.ChatMessage{msg("user", "a"), msg("assistant", "b"), msg("user", "c")})
	// The history is compacted: the front is replaced, so it no longer extends what was stored.
	rewritten := []models.ChatMessage{msg("system", "summary of a and b"), msg("user", "c"), msg("assistant", "d")}
	_ = j.CommitTurn(ctx, "r1", executor.Turn{Number: 2}, rewritten)

	js, err := j.Load(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if len(js.Messages) != 3 || js.Messages[0].Content != "summary of a and b" || js.Messages[2].Content != "d" {
		t.Fatalf("a rewritten history must replay as written, got %+v", js.Messages)
	}
	// And the next commit continues from it.
	_ = j.CommitTurn(ctx, "r1", executor.Turn{Number: 3}, append(rewritten, msg("user", "e")))
	if js, _ = j.Load(ctx, "r1"); len(js.Messages) != 4 || js.Messages[3].Content != "e" {
		t.Fatalf("got %+v", js.Messages)
	}
}

func TestASchema1JournalStillReplaysAndResumes(t *testing.T) {
	dir := t.TempDir()
	j, err := executor.NewFileJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	agent, _ := json.Marshal(testAgent())
	resolved, _ := json.Marshal(resolvedWithTools(orderTool()))
	lines := []string{
		`{"type":"header","time":"` + now + `","header":{"trace_id":"old1","kitchen":"default","agent":` + string(agent) + `,"resolved":` + string(resolved) + `,"user_message":"where is my order","started_at":"` + now + `"}}`,
		`{"type":"turn","time":"` + now + `","turn":{"number":1,"latency_ms":40},"messages":[{"role":"user","content":"where is my order"},{"role":"assistant","content":"","tool_calls":[{"id":"1","type":"function","function":{"name":"lookup_order","arguments":"{\"order_id\":\"A\"}"}}]},{"role":"tool","content":"delivered","name":"lookup_order","tool_call_id":"1"}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`,
	}
	if err := os.WriteFile(filepath.Join(dir, "old1.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	js, err := j.Load(context.Background(), "old1")
	if err != nil {
		t.Fatal(err)
	}
	if js.CompletedTurns != 1 || len(js.Messages) != 3 || js.Usage.TotalTokens != 15 || js.Status != executor.RunRunning {
		t.Fatalf("legacy state wrong: %+v", js)
	}

	d := &costDriver{replies: []turnReply{{content: "it was delivered"}}, in: 10, out: 5}
	e, _, _ := newBudgetExecutor(t, d, orderGateway(), false)
	e.SetJournal(j)
	resp, _, err := e.Resume(context.Background(), "default", "old1", nil)
	if err != nil || resp != "it was delivered" {
		t.Fatalf("resuming a schema-1 run: %q %v", resp, err)
	}
	if js, _ = j.Load(context.Background(), "old1"); js.Status != executor.RunDone || js.Usage.TotalTokens != 30 {
		t.Fatalf("after resume: status=%s usage=%d", js.Status, js.Usage.TotalTokens)
	}
}

// ── crash safety ──────────────────────────────────────────────

func TestARunThatDiesBeforeItsFirstTurnResumesWithItsInput(t *testing.T) {
	gw := orderGateway()
	d1 := &costDriver{in: 10, out: 5} // no replies: the first model call fails, like a crash
	e1, j, _ := journalDirExecutor(t, d1, gw)
	_, trace, err := e1.Execute(context.Background(), testAgent(), "where is order A-1", resolvedWithTools(orderTool()), nil, false)
	if err == nil {
		t.Fatal("expected the first call to fail")
	}

	d2 := &costDriver{replies: []turnReply{{content: "found it"}}, in: 10, out: 5}
	e2, _, _ := newBudgetExecutor(t, d2, gw, false)
	e2.SetJournal(j)
	resp, _, err := e2.Resume(context.Background(), "default", trace.TraceID, nil)
	if err != nil || resp != "found it" {
		t.Fatalf("resume: %q %v", resp, err)
	}
	seen := false
	for _, m := range d2.requests[0].Messages {
		if m.Role == "user" && m.Content == "where is order A-1" {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("the resumed call was not sent the user's message: %+v", d2.requests[0].Messages)
	}
}

func TestATornLastRecordIsIgnoredAndTheRunStillResumes(t *testing.T) {
	d := &costDriver{replies: loopOf(2, "x")[:2], in: 10, out: 5} // two tool turns, then the driver runs dry
	e, j, dir := journalDirExecutor(t, d, orderGateway())
	_, trace, err := e.Execute(context.Background(), testAgent(), "go", resolvedWithTools(orderTool()), nil, false)
	if err == nil {
		t.Fatal("expected the simulated crash")
	}
	path := filepath.Join(dir, trace.TraceID+".jsonl")
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(`{"type":"commit","time":"2026-01-01T00:00:00Z","turn":{"num`) // power cut mid-write
	f.Close()

	if js, err := j.Load(context.Background(), trace.TraceID); err != nil || js.CompletedTurns != 2 {
		t.Fatalf("a torn tail must not stop the run loading: %v %+v", err, js)
	}

	d2 := &costDriver{replies: []turnReply{{content: "recovered"}}, in: 10, out: 5}
	e2, _, _ := newBudgetExecutor(t, d2, orderGateway(), false)
	e2.SetJournal(mustJournal(t, dir))
	if resp, _, err := e2.Resume(context.Background(), "default", trace.TraceID, nil); err != nil || resp != "recovered" {
		t.Fatalf("resume after a torn tail: %q %v", resp, err)
	}
	// The partial record is gone, so the file is clean for whoever reads it next.
	raw, _ := os.ReadFile(path)
	for i, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var m map[string]interface{}
		if json.Unmarshal([]byte(line), &m) != nil {
			t.Fatalf("line %d is not valid JSON after the resume: %q", i, line)
		}
	}
}

func mustJournal(t *testing.T, dir string) *executor.FileJournal {
	t.Helper()
	j, err := executor.NewFileJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

// A run that died while its tools ran: the model's reply is on record, so the resume does
// not ask (and pay) again, and it does not repeat a side effect it cannot prove did not happen.
func inFlightRun(t *testing.T, recorded map[string]executor.ToolResult, calls ...executor.ToolCall) (*executor.Executor, *executor.FileJournal, *costDriver, *fakeGateway, string) {
	t.Helper()
	dir := t.TempDir()
	j := mustJournal(t, dir)
	ctx := context.Background()
	agent := testAgent()
	agent.MaxTurns = 10
	refund := models.ResolvedTool{Name: "issue_refund", Schema: map[string]interface{}{"type": "object"}}
	lookup := orderTool()
	lookup.Idempotent = true
	resolved := resolvedWithTools(refund, lookup)

	user := models.ChatMessage{Role: "user", Content: "refund A-1 and look it up"}
	if err := j.Start(ctx, executor.JournalHeader{TraceID: "crash1", Kitchen: "default", Agent: agent, Resolved: resolved,
		UserMessage: user.Content, StartedAt: time.Now(), InitialMessages: []models.ChatMessage{user}}); err != nil {
		t.Fatal(err)
	}
	var tcs []models.ToolCallResult
	for _, c := range calls {
		tcs = append(tcs, toolCall(c.ID, c.Name, c.Arguments))
	}
	assistant := models.ChatMessage{Role: "assistant", ToolCalls: tcs}
	if err := j.AppendAssistant(ctx, "crash1", 1, assistant, calls, models.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}, 30); err != nil {
		t.Fatal(err)
	}
	for _, r := range recorded {
		if err := j.AppendToolResult(ctx, "crash1", 1, r); err != nil {
			t.Fatal(err)
		}
	}

	gw := newFakeGateway()
	d := &costDriver{replies: []turnReply{{content: "all handled"}}, in: 10, out: 5}
	e, _, _ := newBudgetExecutor(t, d, gw, false)
	e.SetJournal(j)
	return e, j, d, gw, "crash1"
}

func TestARunThatDiedMidToolsDoesNotRepeatASideEffectItCannotVerify(t *testing.T) {
	calls := []executor.ToolCall{
		{ID: "r", Name: "issue_refund", Arguments: map[string]interface{}{"order_id": "A-1"}},
		{ID: "l", Name: "lookup_order", Arguments: map[string]interface{}{"order_id": "A-1"}},
	}
	e, j, d, gw, id := inFlightRun(t, nil, calls...)
	var refunds, lookups int32
	gw.register("issue_refund", func(map[string]interface{}) (string, bool) { atomic.AddInt32(&refunds, 1); return "refunded", false })
	gw.register("lookup_order", func(map[string]interface{}) (string, bool) { atomic.AddInt32(&lookups, 1); return "delivered", false })

	resp, _, err := e.Resume(context.Background(), "default", id, nil)
	if err != nil || resp != "all handled" {
		t.Fatalf("resume: %q %v", resp, err)
	}
	if refunds != 0 {
		t.Fatal("a refund that may already have happened was run again")
	}
	if lookups != 1 {
		t.Fatalf("an idempotent lookup should be re-run once, ran %d", lookups)
	}
	if d.calls() != 1 {
		t.Fatalf("the model's earlier reply was on record; only the follow-up should be asked, got %d calls", d.calls())
	}
	var told string
	for _, m := range d.requests[0].Messages {
		if m.Role == "tool" && m.ToolCallID == "r" {
			told = m.Content
		}
	}
	if !strings.Contains(told, "Outcome unknown") {
		t.Fatalf("the model should be told the refund's outcome is unknown, got %q", told)
	}
	js, _ := j.Load(context.Background(), id)
	if js.Usage.TotalTokens != 30 { // the recorded reply (15) + the follow-up (15)
		t.Fatalf("the interrupted reply's cost must be counted: %d", js.Usage.TotalTokens)
	}
}

func TestAToolResultThatWasRecordedIsNotRunAgain(t *testing.T) {
	calls := []executor.ToolCall{{ID: "r", Name: "issue_refund", Arguments: map[string]interface{}{"order_id": "A-1"}}}
	e, _, d, gw, id := inFlightRun(t, map[string]executor.ToolResult{
		"r": {ToolCallID: "r", Name: "issue_refund", Content: "refunded #77"},
	}, calls...)
	var refunds int32
	gw.register("issue_refund", func(map[string]interface{}) (string, bool) {
		atomic.AddInt32(&refunds, 1)
		return "refunded again", false
	})

	if _, _, err := e.Resume(context.Background(), "default", id, nil); err != nil {
		t.Fatal(err)
	}
	if refunds != 0 {
		t.Fatal("a call whose result is on record must not run again")
	}
	var told string
	for _, m := range d.requests[0].Messages {
		if m.Role == "tool" && m.ToolCallID == "r" {
			told = m.Content
		}
	}
	if told != "refunded #77" {
		t.Fatalf("the recorded result should reach the model, got %q", told)
	}
}

func TestAFinalAnswerThatWasRecordedButNeverDeliveredIsReturnedWithoutAskingAgain(t *testing.T) {
	dir := t.TempDir()
	j := mustJournal(t, dir)
	ctx := context.Background()
	user := models.ChatMessage{Role: "user", Content: "hi"}
	_ = j.Start(ctx, executor.JournalHeader{TraceID: "fin1", Kitchen: "default", Agent: testAgent(), Resolved: resolvedWithTools(),
		StartedAt: time.Now(), InitialMessages: []models.ChatMessage{user}})
	_ = j.AppendAssistant(ctx, "fin1", 1, models.ChatMessage{Role: "assistant", Content: "the answer"}, nil, models.TokenUsage{TotalTokens: 15}, 5)

	d := &costDriver{in: 10, out: 5}
	e, _, _ := newBudgetExecutor(t, d, newFakeGateway(), false)
	e.SetJournal(j)
	resp, _, err := e.Resume(ctx, "default", "fin1", nil)
	if err != nil || resp != "the answer" || d.calls() != 0 {
		t.Fatalf("resp=%q err=%v calls=%d", resp, err, d.calls())
	}
	if js, _ := j.Load(ctx, "fin1"); js.Status != executor.RunDone {
		t.Fatal("the recovered run should be marked done")
	}
}

func TestSubagentSpendIsNotLostWhenTheParentDiesBeforeCommitting(t *testing.T) {
	dir := t.TempDir()
	j := mustJournal(t, dir)
	ctx := context.Background()
	_ = j.Start(ctx, executor.JournalHeader{TraceID: "p1", Kitchen: "default", Agent: testAgent(), Resolved: resolvedWithTools(),
		StartedAt: time.Now(), InitialMessages: []models.ChatMessage{{Role: "user", Content: "hi"}}})
	_ = j.AppendAssistant(ctx, "p1", 1, models.ChatMessage{Role: "assistant"}, []executor.ToolCall{{ID: "d", Name: "agentoven_delegate"}}, models.TokenUsage{TotalTokens: 100}, 5)
	_ = j.AppendToolResult(ctx, "p1", 1, executor.ToolResult{ToolCallID: "d", Name: "agentoven_delegate", Content: "done",
		ChildUsage: models.TokenUsage{TotalTokens: 5000, EstimatedCost: 0.5}})
	// ...and the process dies before the turn commits.
	js, err := j.Load(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if js.Usage.TotalTokens != 5100 || js.Usage.EstimatedCost != 0.5 {
		t.Fatalf("the child's spend must survive: %+v", js.Usage)
	}
}

// ── outcomes ──────────────────────────────────────────────────

func TestRunningOutOfTurnsIsRecordedAsSuch(t *testing.T) {
	d := &costDriver{replies: loopOf(10, "x"), in: 10, out: 5}
	e, j, _ := journalDirExecutor(t, d, orderGateway())
	a := testAgent()
	a.MaxTurns = 2
	resp, trace, err := e.Execute(context.Background(), a, "go", resolvedWithTools(orderTool()), nil, false)
	if err != nil || !strings.HasPrefix(resp, "[Max turns") {
		t.Fatalf("%q %v", resp, err)
	}
	if trace.Outcome != executor.OutcomeMaxTurns {
		t.Fatalf("trace outcome = %q", trace.Outcome)
	}
	if js, _ := j.Load(context.Background(), trace.TraceID); js.Outcome != executor.OutcomeMaxTurns {
		t.Fatalf("journal outcome = %q", js.Outcome)
	}
}

func TestEndingAtTheBudgetIsRecordedAsSuch(t *testing.T) {
	d := &costDriver{replies: loopOf(10, "x"), in: 90, out: 10}
	e, j, _ := journalDirExecutor(t, d, orderGateway())
	_, _, err := e.Execute(context.Background(), budgetAgent(models.RunBudget{MaxTokens: 450}), "go", resolvedP(orderTool()), nil, false)
	asBudgetPause(t, err)
	un, _ := e.ListUnfinished(context.Background(), "default")
	if len(un) != 1 {
		t.Fatalf("setup: %+v", un)
	}
	if _, _, err := e.Resume(context.Background(), "default", un[0].TraceID, &executor.ResumeDecision{Approved: false}); err == nil {
		t.Fatal("expected the ended-at-budget error")
	}
	if js, _ := j.Load(context.Background(), un[0].TraceID); js.Outcome != executor.OutcomeBudgetExhausted {
		t.Fatalf("outcome = %q", js.Outcome)
	}
}

// ── housekeeping ──────────────────────────────────────────────

func TestSweepRemovesOldFinishedRunsEndsStaleOnesAndLeavesPausedAlone(t *testing.T) {
	dir := t.TempDir()
	j := mustJournal(t, dir)
	ctx := context.Background()
	start := func(id string) {
		if err := j.Start(ctx, executor.JournalHeader{TraceID: id, Kitchen: "default", Agent: testAgent(), StartedAt: time.Now(),
			InitialMessages: []models.ChatMessage{{Role: "user", Content: "hi"}}}); err != nil {
			t.Fatal(err)
		}
	}
	start("done-old")
	_ = j.MarkDone(ctx, "done-old", "ok", nil)
	start("done-new")
	_ = j.MarkDone(ctx, "done-new", "ok", nil)
	start("stale")
	start("paused")
	_ = j.MarkBudgetPaused(ctx, "paused", models.BudgetPause{Dimension: models.BudgetDimTokens})

	// A sweep run later than "done-old" finished by more than the retention, but not much
	// later than "done-new".
	old := time.Now().Add(-10 * 24 * time.Hour)
	for _, id := range []string{"done-old", "stale", "paused"} {
		_ = os.Chtimes(filepath.Join(dir, id+".jsonl"), old, old)
	}
	// done-old's sidecar records when it finished.
	rewriteDoneAt(t, filepath.Join(dir, "done-old.meta.json"), old)

	removed, abandoned := j.Sweep(time.Now(), 7*24*time.Hour, 3*24*time.Hour)
	if removed != 1 || abandoned != 1 {
		t.Fatalf("removed=%d abandoned=%d, want 1 and 1", removed, abandoned)
	}
	if _, err := os.Stat(filepath.Join(dir, "done-old.jsonl")); !os.IsNotExist(err) {
		t.Fatal("an old finished run should be gone")
	}
	if _, err := os.Stat(filepath.Join(dir, "done-new.jsonl")); err != nil {
		t.Fatal("a recently finished run must stay")
	}
	if js, _ := j.Load(ctx, "stale"); js.Outcome != executor.OutcomeAbandoned {
		t.Fatalf("a run with no activity should be ended as abandoned, got %+v", js.Outcome)
	}
	if js, _ := j.Load(ctx, "paused"); js.Status != executor.RunPaused {
		t.Fatalf("a paused run is waiting for a person, not stale: %s", js.Status)
	}
}

func rewriteDoneAt(t *testing.T, path string, at time.Time) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	_ = json.Unmarshal(raw, &m)
	m["done_at"] = at.Format(time.RFC3339Nano)
	out, _ := json.Marshal(m)
	_ = os.WriteFile(path, out, 0o644)
}

func TestListUnfinishedUsesTheSidecarToSkipFinishedRuns(t *testing.T) {
	dir := t.TempDir()
	j := mustJournal(t, dir)
	ctx := context.Background()
	for _, id := range []string{"a", "b"} {
		_ = j.Start(ctx, executor.JournalHeader{TraceID: id, Kitchen: "default", Agent: testAgent(), StartedAt: time.Now()})
	}
	_ = j.MarkDone(ctx, "a", "ok", nil)

	// Break the finished run's journal. If the scan had to read it, a failure would be
	// swallowed either way, so make the *unfinished* one the witness: it must still be listed.
	_ = os.WriteFile(filepath.Join(dir, "a.jsonl"), []byte("not json at all\n"), 0o644)
	un, err := j.ListUnfinished(ctx, "default")
	if err != nil || len(un) != 1 || un[0].TraceID != "b" {
		t.Fatalf("got %+v %v", un, err)
	}
}
