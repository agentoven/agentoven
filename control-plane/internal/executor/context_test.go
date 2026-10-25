package executor_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func taskCtx() context.Context { return executor.WithTaskMode(context.Background()) }

func isSummarizerRequest(req *models.RouteRequest) bool {
	return len(req.Messages) == 1 && strings.HasPrefix(req.Messages[0].Content, "You are compacting")
}

func wellFormedRequest(t *testing.T, msgs []models.ChatMessage) {
	t.Helper()
	asked := map[string]bool{}
	for i, m := range msgs {
		switch m.Role {
		case "assistant":
			for _, tc := range m.ToolCalls {
				asked[tc.ID] = true
			}
		case "tool":
			if !asked[m.ToolCallID] {
				t.Fatalf("message %d is a tool result for %q with no request before it:\n%+v", i, m.ToolCallID, msgs)
			}
		}
	}
}

func toolResultBytes(n int) *fakeGateway {
	gw := newFakeGateway()
	gw.register("lookup_order", func(map[string]interface{}) (string, bool) { return strings.Repeat("r", n), false })
	return gw
}

// ── tool results ──────────────────────────────────────────────

func TestATasksHugeToolResultIsCappedBeforeItEntersTheConversation(t *testing.T) {
	d := &costDriver{replies: loopOf(1, "done"), in: 10, out: 5}
	e, _, _ := journalDirExecutorStore2(t, d, toolResultBytes(200_000))
	a := testAgent()
	a.MaxTurns = 5
	if _, _, err := e.Execute(taskCtx(), a, "go", resolvedWithTools(orderTool()), nil, false); err != nil {
		t.Fatal(err)
	}
	var seen string
	for _, m := range d.requests[len(d.requests)-1].Messages {
		if m.Role == "tool" {
			seen = m.Content
		}
	}
	if len(seen) == 0 || len(seen) > executor.DefaultToolResultBytes {
		t.Fatalf("the model was given %d bytes of tool result, the cap is %d", len(seen), executor.DefaultToolResultBytes)
	}
	if !strings.Contains(seen, "omitted from the middle") {
		t.Fatal("the model should be told the result was cut")
	}

	// An ordinary run is untouched.
	d2 := &costDriver{replies: loopOf(1, "done"), in: 10, out: 5}
	e2, _, _ := journalDirExecutorStore2(t, d2, toolResultBytes(200_000))
	if _, _, err := e2.Execute(context.Background(), a, "go", resolvedWithTools(orderTool()), nil, false); err != nil {
		t.Fatal(err)
	}
	for _, m := range d2.requests[len(d2.requests)-1].Messages {
		if m.Role == "tool" && len(m.Content) != 200_000 {
			t.Fatalf("a plain run must keep the whole result, got %d bytes", len(m.Content))
		}
	}
}

func journalDirExecutorStore2(t *testing.T, d *costDriver, gw *fakeGateway) (*executor.Executor, *executor.FileJournal, string) {
	t.Helper()
	e, j, dir, _ := journalDirExecutorStore(t, d, gw)
	return e, j, dir
}

// ── compaction ────────────────────────────────────────────────

func compactingDriver(turns int, toolBytes int) *costDriver {
	var main int32
	d := &costDriver{in: 40, out: 20}
	d.fn = func(req *models.RouteRequest) turnReply {
		if isSummarizerRequest(req) {
			return turnReply{content: "SUMMARY: the user wants the long task done; orders A0.. were looked up and delivered."}
		}
		n := int(atomic.AddInt32(&main, 1))
		if n > turns {
			return turnReply{content: "all done"}
		}
		return turnReply{toolCalls: []models.ToolCallResult{toolCall(fmt.Sprint("c", n), "lookup_order", map[string]interface{}{"order_id": fmt.Sprint("A", n)})}}
	}
	return d
}

func compactingAgent() *models.Agent {
	a := testAgent()
	a.MaxTurns = 60
	a.ContextBudget = 1500 // tokens: small, so a few tool results fill it
	a.Budget = &models.RunBudget{MaxTokens: 1_000_000}
	return a
}

func TestALongRunsOldHistoryIsSummarizedAndTheRunKeepsGoing(t *testing.T) {
	d := compactingDriver(12, 3000)
	e, j, _ := journalDirExecutorStore2(t, d, toolResultBytes(3000))
	resp, trace, err := e.Execute(taskCtx(), compactingAgent(), "do the long task", resolvedP(orderTool()), nil, false)
	if err != nil || resp != "all done" {
		t.Fatalf("%q %v", resp, err)
	}

	summarizerCalls, compactedRequests, maxLen := 0, 0, 0
	for _, req := range d.requests {
		if isSummarizerRequest(req) {
			summarizerCalls++
			continue
		}
		wellFormedRequest(t, req.Messages)
		if len(req.Messages) > maxLen {
			maxLen = len(req.Messages)
		}
		for _, m := range req.Messages {
			if m.Role == "system" && strings.HasPrefix(m.Content, executor.SummaryPrefix) {
				compactedRequests++
				break
			}
		}
		// The task statement is never summarized away.
		first := ""
		for _, m := range req.Messages {
			if m.Role == "user" {
				first = m.Content
				break
			}
		}
		if first != "do the long task" {
			t.Fatalf("the first user message was lost: %q", first)
		}
	}
	if summarizerCalls == 0 || compactedRequests == 0 {
		t.Fatalf("expected the history to be compacted (summarizer calls %d, requests carrying a summary %d)", summarizerCalls, compactedRequests)
	}
	// 12 tool turns would be 2 + 24 = 26 messages without compaction.
	if maxLen >= 26 {
		t.Fatalf("the conversation was never shortened, longest request had %d messages", maxLen)
	}

	// The summarizing calls are paid for like any other: they are in the run's spend and in the journal.
	total := int64(d.calls()) * 60
	if trace.Budget.SpentTokens != total {
		t.Fatalf("the run's budget should include the summaries: spent %d, calls cost %d", trace.Budget.SpentTokens, total)
	}
	js, _ := j.Load(context.Background(), trace.TraceID)
	if js.Usage.TotalTokens != total {
		t.Fatalf("the journal should include the summaries' cost: %d vs %d", js.Usage.TotalTokens, total)
	}
	hasSummary := false
	for _, m := range js.Messages {
		if strings.HasPrefix(m.Content, executor.SummaryPrefix) {
			hasSummary = true
		}
	}
	if !hasSummary {
		t.Fatal("the compacted history is what the journal replays to")
	}
}

func TestACompactedRunResumesFromItsCompactedHistory(t *testing.T) {
	d := compactingDriver(12, 3000)
	var crashAfter int32
	inner := d.fn
	d.fn = func(req *models.RouteRequest) turnReply {
		if !isSummarizerRequest(req) && atomic.AddInt32(&crashAfter, 1) == 9 {
			// the process dies here: represented by the driver failing from now on
			d.failN, d.failErr = 1<<30, errors.New("process died")
		}
		return inner(req)
	}
	e, j, dir := journalDirExecutorStore2(t, d, toolResultBytes(3000))
	_, trace, err := e.Execute(taskCtx(), compactingAgent(), "do the long task", resolvedP(orderTool()), nil, false)
	if err == nil {
		t.Fatal("expected the simulated crash")
	}

	d2 := compactingDriver(0, 0)
	e2, _, _ := newBudgetExecutor(t, d2, toolResultBytes(3000), false)
	e2.SetJournal(mustJournal(t, dir))
	_ = j
	resp, _, err := e2.Resume(context.Background(), "default", trace.TraceID, nil)
	if err != nil || resp != "all done" {
		t.Fatalf("resume: %q %v", resp, err)
	}
	if len(d2.requests) != 1 {
		t.Fatalf("resuming a compacted run should not summarize it again straight away, saw %d model calls", len(d2.requests))
	}
	sawSummary := false
	for _, m := range d2.requests[0].Messages {
		if strings.HasPrefix(m.Content, executor.SummaryPrefix) {
			sawSummary = true
		}
	}
	if !sawSummary || len(d2.requests[0].Messages) >= 26 {
		t.Fatalf("the resumed call should start from the compacted history (%d messages, summary=%v)", len(d2.requests[0].Messages), sawSummary)
	}
	wellFormedRequest(t, d2.requests[0].Messages)
}

func TestWhenTheBudgetCannotAffordASummaryTheHistoryIsCondensedWithoutTheModel(t *testing.T) {
	d := compactingDriver(12, 3000)
	e, _, _ := journalDirExecutorStore2(t, d, toolResultBytes(3000))
	a := compactingAgent()
	// Room for the next ordinary call but not for a summarizing request (about 1,000 tokens
	// in and up to 1,500 out) on top.
	a.Budget = &models.RunBudget{MaxTokens: 1800}
	_, _, err := e.Execute(taskCtx(), a, "do the long task", resolvedP(orderTool()), nil, false)
	var pe *executor.PausedError
	if err != nil && !errors.As(err, &pe) {
		t.Fatalf("running out of budget is a pause, got %v", err)
	}
	condensed := false
	for _, req := range d.requests {
		if isSummarizerRequest(req) {
			t.Fatal("the summarizer was called although the budget could not afford it")
		}
		for _, m := range req.Messages {
			if strings.HasPrefix(m.Content, executor.SummaryPrefix) && strings.Contains(m.Content, "condensed without a model") {
				condensed = true
			}
		}
	}
	if !condensed {
		t.Fatal("the history should have been condensed without the model rather than left to grow")
	}
}

// ── working notes ─────────────────────────────────────────────

func notesDriver(plan string) *costDriver {
	var main int32
	d := &costDriver{in: 40, out: 20}
	d.fn = func(req *models.RouteRequest) turnReply {
		switch int(atomic.AddInt32(&main, 1)) {
		case 1:
			return turnReply{toolCalls: []models.ToolCallResult{toolCall("n1", "agentoven_notes", map[string]interface{}{"action": "set", "section": "plan", "text": plan})}}
		case 2:
			return turnReply{toolCalls: []models.ToolCallResult{toolCall("l1", "lookup_order", map[string]interface{}{"order_id": "A1"})}}
		default:
			return turnReply{content: "finished"}
		}
	}
	return d
}

func pinnedNotes(req *models.RouteRequest) string {
	for _, m := range req.Messages {
		if m.Role == "system" && strings.HasPrefix(m.Content, "## Working notes") {
			return m.Content
		}
	}
	return ""
}

func hasNotesTool(req *models.RouteRequest) bool {
	for _, td := range req.Tools {
		if td.Function.Name == "agentoven_notes" {
			return true
		}
	}
	return false
}

func TestATaskKeepsWorkingNotesThatAreAlwaysInFrontOfTheModel(t *testing.T) {
	d := notesDriver("1. look up A1\n2. report")
	e, _, _ := journalDirExecutorStore2(t, d, orderGateway())
	a := testAgent()
	a.MaxTurns = 10
	if _, _, err := e.Execute(taskCtx(), a, "go", resolvedWithTools(orderTool()), nil, false); err != nil {
		t.Fatal(err)
	}
	if !hasNotesTool(d.requests[0]) {
		t.Fatal("a task is offered the notes tool")
	}
	if !strings.Contains(pinnedNotes(d.requests[0]), "(empty)") {
		t.Fatal("the pinned block is there from the first turn, empty")
	}
	if p := pinnedNotes(d.requests[1]); !strings.Contains(p, "1. look up A1") {
		t.Fatalf("after the agent writes its plan, the next turn shows it:\n%s", p)
	}
}

func TestPlainRunsGetNeitherTheNotesToolNorTheBlock(t *testing.T) {
	d := &costDriver{replies: []turnReply{{content: "hi"}}, in: 10, out: 5}
	e, _, _ := journalDirExecutorStore2(t, d, orderGateway())
	if _, _, err := e.Execute(context.Background(), testAgent(), "go", resolvedWithTools(orderTool()), nil, false); err != nil {
		t.Fatal(err)
	}
	if hasNotesTool(d.requests[0]) || pinnedNotes(d.requests[0]) != "" {
		t.Fatal("an ordinary request must not change")
	}
}

func TestAPlanAndExecuteAgentGetsNotesWithoutBeingATask(t *testing.T) {
	d := notesDriver("a plan")
	e, _, _ := journalDirExecutorStore2(t, d, orderGateway())
	a := testAgent()
	a.MaxTurns = 10
	a.ReasoningStrategy = models.StrategyPlanAndExecute
	if _, _, err := e.Execute(context.Background(), a, "go", resolvedWithTools(orderTool()), nil, false); err != nil {
		t.Fatal(err)
	}
	if !hasNotesTool(d.requests[0]) || !strings.Contains(pinnedNotes(d.requests[0]), "write a plan") {
		t.Fatal("plan-and-execute means notes and a planning-first instruction")
	}
}

func TestNotesSurviveARestart(t *testing.T) {
	d := notesDriver("remember: the customer is Dana")
	var main int32
	inner := d.fn
	d.fn = func(req *models.RouteRequest) turnReply {
		if atomic.AddInt32(&main, 1) == 2 {
			d.failN, d.failErr = 1<<30, errors.New("process died") // the call after this one dies
		}
		return inner(req)
	}
	e, _, dir := journalDirExecutorStore2(t, d, orderGateway())
	a := testAgent()
	a.MaxTurns = 10
	_, trace, err := e.Execute(taskCtx(), a, "go", resolvedWithTools(orderTool()), nil, false)
	if err == nil {
		t.Fatal("expected the simulated crash")
	}

	d2 := &costDriver{replies: []turnReply{{content: "finished"}}, in: 10, out: 5}
	e2, _, _ := newBudgetExecutor(t, d2, orderGateway(), false)
	e2.SetJournal(mustJournal(t, dir))
	if _, _, err := e2.Resume(context.Background(), "default", trace.TraceID, nil); err != nil {
		t.Fatal(err)
	}
	if p := pinnedNotes(d2.requests[0]); !strings.Contains(p, "the customer is Dana") {
		t.Fatalf("the notes must be in front of a resumed run:\n%s", p)
	}
}

func TestNotesAreStillThereAfterTheHistoryIsCompacted(t *testing.T) {
	var main int32
	d := &costDriver{in: 40, out: 20}
	d.fn = func(req *models.RouteRequest) turnReply {
		if isSummarizerRequest(req) {
			return turnReply{content: "a summary"}
		}
		n := int(atomic.AddInt32(&main, 1))
		switch {
		case n == 1:
			return turnReply{toolCalls: []models.ToolCallResult{toolCall("n", "agentoven_notes", map[string]interface{}{"action": "set", "section": "goal", "text": "GOAL: ship the report"})}}
		case n <= 10:
			return turnReply{toolCalls: []models.ToolCallResult{toolCall(fmt.Sprint("c", n), "lookup_order", map[string]interface{}{"order_id": fmt.Sprint("A", n)})}}
		}
		return turnReply{content: "done"}
	}
	e, _, _ := journalDirExecutorStore2(t, d, toolResultBytes(3000))
	if _, _, err := e.Execute(taskCtx(), compactingAgent(), "go", resolvedP(orderTool()), nil, false); err != nil {
		t.Fatal(err)
	}
	compacted := false
	for _, req := range d.requests {
		if isSummarizerRequest(req) {
			compacted = true
			continue
		}
		if compacted && !strings.Contains(pinnedNotes(req), "GOAL: ship the report") {
			t.Fatal("the goal was lost after compaction")
		}
	}
	if !compacted {
		t.Fatal("setup: the run was never compacted")
	}
}

// ── repetition ────────────────────────────────────────────────

func repeatingDriver(times int) *costDriver {
	var n int32
	d := &costDriver{in: 20, out: 10}
	d.fn = func(req *models.RouteRequest) turnReply {
		if int(atomic.AddInt32(&n, 1)) > times {
			return turnReply{content: "gave up and answered"}
		}
		return turnReply{toolCalls: []models.ToolCallResult{toolCall(fmt.Sprint("r", n), "lookup_order", map[string]interface{}{"order_id": "SAME"})}}
	}
	return d
}

func TestARunWarnsThenPausesWhenItKeepsMakingTheSameCall(t *testing.T) {
	d := repeatingDriver(100)
	e, j, _ := journalDirExecutorStore2(t, d, orderGateway())
	a := testAgent()
	a.MaxTurns = 50

	var warnings int
	_, trace, err := e.ExecuteStream(taskCtx(), a, "go", resolvedWithTools(orderTool()), nil, false, func(ev executor.Event) error {
		if ev.Type == executor.EventStallWarning {
			warnings++
		}
		return nil
	})
	var pe *executor.PausedError
	if !errors.As(err, &pe) || pe.Kind != executor.PauseStall || pe.Stall == nil {
		t.Fatalf("expected a stall pause, got %v", err)
	}
	if pe.Stall.Tool != "lookup_order" || pe.Stall.Count != 5 || d.calls() != 5 {
		t.Fatalf("it should stop after the 5th identical call: %+v after %d calls", pe.Stall, d.calls())
	}
	if warnings != 3 { // the 3rd, 4th and 5th calls are each warned about
		t.Fatalf("warnings = %d", warnings)
	}

	// The model saw the warning in the result of the third call.
	var warned bool
	for _, m := range d.requests[3].Messages {
		if m.Role == "tool" && strings.Contains(m.Content, "exact lookup_order call 3 times") {
			warned = true
		}
	}
	if !warned {
		t.Fatal("the third identical call's result should carry the notice")
	}

	un, _ := e.ListUnfinished(context.Background(), "default")
	if len(un) != 1 || un[0].PauseKind != string(executor.PauseStall) {
		t.Fatalf("a stalled run is listed as paused for a stall: %+v", un)
	}
	_ = j
	_ = trace
}

func TestASentOnAfterAStallCarriesTheOperatorsGuidance(t *testing.T) {
	d := repeatingDriver(5)
	e, j, _ := journalDirExecutorStore2(t, d, orderGateway())
	a := testAgent()
	a.MaxTurns = 50
	_, trace, err := e.Execute(taskCtx(), a, "go", resolvedWithTools(orderTool()), nil, false)
	var pe *executor.PausedError
	if !errors.As(err, &pe) {
		t.Fatalf("setup: %v", err)
	}

	if _, _, err := e.Resume(context.Background(), "default", trace.TraceID, nil); err == nil {
		t.Fatal("a stalled run needs a decision")
	}
	resp, _, err := e.Resume(context.Background(), "default", trace.TraceID, &executor.ResumeDecision{Approved: true, Note: "the order id is B-77, not SAME"})
	if err != nil || resp != "gave up and answered" {
		t.Fatalf("%q %v", resp, err)
	}
	guided := false
	for _, m := range d.requests[len(d.requests)-1].Messages {
		if m.Role == "user" && strings.Contains(m.Content, "the order id is B-77") {
			guided = true
		}
	}
	if !guided {
		t.Fatal("the operator's note should reach the model")
	}
	if js, _ := j.Load(context.Background(), trace.TraceID); js.Outcome != executor.OutcomeCompleted {
		t.Fatalf("%+v", js.Outcome)
	}
}

func TestAPersonCanEndAStalledRun(t *testing.T) {
	d := repeatingDriver(100)
	e, j, _ := journalDirExecutorStore2(t, d, orderGateway())
	a := testAgent()
	a.MaxTurns = 50
	_, trace, _ := e.Execute(taskCtx(), a, "go", resolvedWithTools(orderTool()), nil, false)
	_, _, err := e.Resume(context.Background(), "default", trace.TraceID, &executor.ResumeDecision{Approved: false, Note: "not worth it"})
	if !errors.Is(err, executor.ErrStalled) {
		t.Fatalf("expected ErrStalled, got %v", err)
	}
	if js, _ := j.Load(context.Background(), trace.TraceID); js.Outcome != executor.OutcomeStalled || js.Status != executor.RunDone {
		t.Fatalf("%+v", js)
	}
}

func TestOrdinaryRunsMayRepeatACallAsOftenAsTheyLike(t *testing.T) {
	d := repeatingDriver(8)
	e, _, _ := journalDirExecutorStore2(t, d, orderGateway())
	a := testAgent()
	a.MaxTurns = 20
	resp, _, err := e.Execute(context.Background(), a, "poll until done", resolvedWithTools(orderTool()), nil, false)
	if err != nil || resp != "gave up and answered" {
		t.Fatalf("a plain run is not subject to stall detection: %q %v", resp, err)
	}
}

func TestAStalledTaskIsPausedAndCanBeEnded(t *testing.T) {
	d := repeatingDriver(100)
	e, _, _ := journalDirExecutorStore2(t, d, orderGateway())
	m := executor.NewTaskManager(e, executor.TaskManagerOptions{})
	a := testAgent()
	a.MaxTurns = 50
	snap, _ := m.Submit(context.Background(), taskSpec(a, resolvedWithTools(orderTool())))
	waitFor(t, "pause", func() bool { return status(t, m, snap.ID) == executor.TaskPaused })
	got, _ := m.Get(context.Background(), "default", snap.ID)
	if got.PauseKind != "stall" || got.Stall == nil || got.Stall.Count != 5 {
		t.Fatalf("%+v", got)
	}
	if _, err := m.Resume(context.Background(), "default", "test-agent", snap.ID, &executor.ResumeDecision{Approved: false}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "ended", func() bool { return status(t, m, snap.ID) == executor.TaskStalled })
}

// ── model errors ──────────────────────────────────────────────

func TestATaskRepeatsAFailedModelCallBeforeGivingUp(t *testing.T) {
	d := &costDriver{replies: []turnReply{{content: "worked"}}, in: 10, out: 5, failN: 2, failErr: errors.New("503 service unavailable")}
	e, _, _ := journalDirExecutorStore2(t, d, orderGateway())
	e.SetModelRetry(2, time.Millisecond)
	resp, trace, err := e.Execute(taskCtx(), testAgent(), "go", resolvedWithTools(), nil, false)
	if err != nil || resp != "worked" {
		t.Fatalf("%q %v", resp, err)
	}
	if trace.Usage.TotalTokens != 15 {
		t.Fatalf("failed attempts cost nothing, usage = %d", trace.Usage.TotalTokens)
	}
	d.mu.Lock()
	attempts := len(d.requests)
	d.mu.Unlock()
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
}

func TestRetriesAreBoundedAndLeaveTheRunResumable(t *testing.T) {
	d := &costDriver{in: 10, out: 5, failN: 1 << 30, failErr: errors.New("503 service unavailable")}
	e, _, _ := journalDirExecutorStore2(t, d, orderGateway())
	e.SetModelRetry(2, time.Millisecond)
	_, trace, err := e.Execute(taskCtx(), testAgent(), "go", resolvedWithTools(), nil, false)
	if err == nil {
		t.Fatal("expected the failure to surface once retries ran out")
	}
	d.mu.Lock()
	attempts := len(d.requests)
	d.mu.Unlock()
	if attempts != 3 {
		t.Fatalf("1 attempt + 2 retries, got %d", attempts)
	}
	if un, _ := e.ListUnfinished(context.Background(), "default"); len(un) != 1 || un[0].TraceID != trace.TraceID {
		t.Fatalf("the run stays resumable: %+v", un)
	}
}

func TestAnErrorThatCannotClearIsNotRetried(t *testing.T) {
	d := &costDriver{in: 10, out: 5, failN: 1 << 30, failErr: errors.New("401 unauthorized: invalid api key")}
	e, _, _ := journalDirExecutorStore2(t, d, orderGateway())
	e.SetModelRetry(3, time.Millisecond)
	_, _, _ = e.Execute(taskCtx(), testAgent(), "go", resolvedWithTools(), nil, false)
	d.mu.Lock()
	attempts := len(d.requests)
	d.mu.Unlock()
	if attempts != 1 {
		t.Fatalf("a bad key is not worth repeating, got %d attempts", attempts)
	}
}

func TestPlainRunsDoNotRetryAModelError(t *testing.T) {
	d := &costDriver{in: 10, out: 5, failN: 1 << 30, failErr: errors.New("503 service unavailable")}
	e, _, _ := journalDirExecutorStore2(t, d, orderGateway())
	e.SetModelRetry(3, time.Millisecond)
	_, _, _ = e.Execute(context.Background(), testAgent(), "go", resolvedWithTools(), nil, false)
	d.mu.Lock()
	attempts := len(d.requests)
	d.mu.Unlock()
	if attempts != 1 {
		t.Fatalf("a request someone is waiting on fails at once, got %d attempts", attempts)
	}
}

var _ sync.Mutex

func TestAStalledRunIsAudited(t *testing.T) {
	d := repeatingDriver(100)
	e, _, _ := journalDirExecutorStore2(t, d, orderGateway())
	audit := &auditLog{}
	e.SetAudit(audit.sink)
	a := testAgent()
	a.MaxTurns = 50
	_, _, _ = e.Execute(taskCtx(), a, "go", resolvedWithTools(orderTool()), nil, false)
	if len(audit.events) != 1 || audit.events[0] != "run.stalled" || audit.last["run.stalled"]["tool"] != "lookup_order" {
		t.Fatalf("%v %v", audit.events, audit.last)
	}
}
