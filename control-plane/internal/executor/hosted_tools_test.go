package executor_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func mailTool() models.ResolvedTool {
	return models.ResolvedTool{Name: "send_mail", Schema: map[string]interface{}{
		"type": "object", "required": []interface{}{"to"},
		"properties": map[string]interface{}{"to": map[string]interface{}{"type": "string"}},
	}}
}

type hostedFixture struct {
	e    *executor.Executor
	gw   *fakeGateway
	sent *int32
	req  executor.HostedToolRequest
}

func newHostedFixture(t *testing.T, withJournal bool) *hostedFixture {
	t.Helper()
	gw := newFakeGateway()
	var sent int32
	gw.register("send_mail", func(map[string]interface{}) (string, bool) { atomic.AddInt32(&sent, 1); return "sent", false })
	gw.register("lookup_order", func(map[string]interface{}) (string, bool) { return "A-1 shipped", false })
	e := newTestExecutor(t, &scriptedDriver{}, gw)
	if withJournal {
		j, err := executor.NewFileJournal(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		e.SetJournal(j)
	}
	e.SetToolApprovalPolicy(policyGate{"send_mail": true})
	return &hostedFixture{e: e, gw: gw, sent: &sent, req: executor.HostedToolRequest{
		Agent: testAgent(), Resolved: resolvedWithTools(mailTool(), orderTool()), Run: "run-1", CallID: "c1",
	}}
}

// heldRun waits for the paused run a held call created and returns its id.
func heldRun(t *testing.T, e *executor.Executor) string {
	t.Helper()
	for i := 0; i < 100; i++ {
		runs, err := e.ListUnfinished(context.Background(), "default")
		if err == nil {
			for _, r := range runs {
				if r.Status == executor.RunPaused {
					return r.TraceID
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("no run was held for approval")
	return ""
}

func (f *hostedFixture) call(name string, args map[string]interface{}) executor.HostedToolOutcome {
	r := f.req
	r.Name, r.Arguments = name, args
	return f.e.RunHostedToolCall(context.Background(), r)
}

func TestHostedUngatedToolRunsThroughTheGateway(t *testing.T) {
	f := newHostedFixture(t, true)
	out := f.call("lookup_order", map[string]interface{}{"order_id": "A-1"})
	if out.Kind != "" || out.Result.IsError || out.Result.Content != "A-1 shipped" || out.Approval != "not_required" {
		t.Fatalf("an ungated tool runs and answers: %+v", out)
	}
	if len(f.gw.calls) != 1 {
		t.Fatalf("one gateway call, got %v", f.gw.calls)
	}
}

func TestHostedCallOutsideTheAllowlistNeverReachesTheGateway(t *testing.T) {
	f := newHostedFixture(t, true)
	f.gw.register("drop_table", func(map[string]interface{}) (string, bool) { t.Error("an unknown tool ran"); return "", false })
	for _, name := range []string{"drop_table", "agentoven_delegate", "agentoven_notes"} {
		out := f.call(name, map[string]interface{}{})
		if out.Kind != executor.HostedUnknownTool || !out.Result.IsError {
			t.Fatalf("%s must be refused as unknown: %+v", name, out)
		}
	}
	if len(f.gw.calls) != 0 {
		t.Fatalf("nothing may reach the gateway: %v", f.gw.calls)
	}
}

func TestHostedInvalidArgumentsAreRefusedByTheSchema(t *testing.T) {
	f := newHostedFixture(t, true)
	out := f.call("lookup_order", map[string]interface{}{"order_id": 7})
	if !out.Result.IsError || len(f.gw.calls) != 0 {
		t.Fatalf("a call off its schema must not run: %+v %v", out, f.gw.calls)
	}
}

func TestHostedGatedToolWaitsAndRunsOnceAfterAnApproval(t *testing.T) {
	f := newHostedFixture(t, true)
	done := make(chan executor.HostedToolOutcome, 1)
	go func() { done <- f.call("send_mail", map[string]interface{}{"to": "a@example.com"}) }()
	id := heldRun(t, f.e)
	if atomic.LoadInt32(f.sent) != 0 {
		t.Fatal("a gated tool must not run before a decision")
	}
	js, err := f.e.RunState(context.Background(), "default", id)
	if err != nil || js.Pending == nil || len(js.Pending.ToolCalls) != 1 || js.Pending.ToolCalls[0].Name != "send_mail" || js.Header.Hosted == nil {
		t.Fatalf("the held run must look like any paused run: %+v %v", js, err)
	}
	if executor.PauseKindOf(js) != string(executor.PauseApproval) {
		t.Fatalf("the pause kind is approval, got %q", executor.PauseKindOf(js))
	}
	if _, _, err := f.e.ResumeFor(context.Background(), "default", "test-agent", id, &executor.ResumeDecision{Approved: true}); !errors.Is(err, executor.ErrHostedDecisionRecorded) {
		t.Fatalf("a decision on a held call is recorded, not resumed: %v", err)
	}
	select {
	case out := <-done:
		if out.Approval != "approved" || out.Kind != "" || out.Result.IsError || out.HeldRun != id {
			t.Fatalf("approved: %+v", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the held call did not return after the approval")
	}
	if atomic.LoadInt32(f.sent) != 1 || len(f.gw.approvedCalls) != 1 || !f.gw.approvedCalls[0] {
		t.Fatalf("an approved call runs once and the gateway is told it passed the gate: sent=%d %v", *f.sent, f.gw.approvedCalls)
	}
	// A second decision on the same call finds nothing to decide.
	if _, _, err := f.e.ResumeFor(context.Background(), "default", "test-agent", id, &executor.ResumeDecision{Approved: true}); err == nil || errors.Is(err, executor.ErrHostedDecisionRecorded) {
		t.Fatalf("a decided call cannot be decided again: %v", err)
	}
	if atomic.LoadInt32(f.sent) != 1 {
		t.Fatal("a second decision ran the tool again")
	}
}

func TestHostedDenialRunsNothingAndSaysWhy(t *testing.T) {
	f := newHostedFixture(t, true)
	done := make(chan executor.HostedToolOutcome, 1)
	go func() { done <- f.call("send_mail", map[string]interface{}{"to": "a@example.com"}) }()
	id := heldRun(t, f.e)
	_, _, err := f.e.Resume(context.Background(), "default", id, &executor.ResumeDecision{Approved: false, Note: "not this customer"})
	if !errors.Is(err, executor.ErrHostedDecisionRecorded) {
		t.Fatal(err)
	}
	out := <-done
	if out.Approval != "denied" || out.Kind != executor.HostedApprovalDenied || !out.Result.IsError || out.Result.Content != "Human denied this tool call: not this customer" {
		t.Fatalf("denied: %+v", out)
	}
	if atomic.LoadInt32(f.sent) != 0 || len(f.gw.calls) != 0 {
		t.Fatal("a denied call must not reach the gateway")
	}
}

func TestHostedHoldExpiresAndTheToolDoesNotRun(t *testing.T) {
	t.Setenv("AGENTOVEN_TOOL_APPROVAL_WAIT", "1s")
	f := newHostedFixture(t, true)
	out := f.call("send_mail", map[string]interface{}{"to": "a@example.com"})
	if out.Approval != "expired" || out.Kind != executor.HostedApprovalExpired || atomic.LoadInt32(f.sent) != 0 {
		t.Fatalf("expired: %+v", out)
	}
	// A late approval of the expired call has nothing to act on and runs nothing.
	if _, _, err := f.e.Resume(context.Background(), "default", out.HeldRun, &executor.ResumeDecision{Approved: true}); err == nil || errors.Is(err, executor.ErrHostedDecisionRecorded) {
		t.Fatalf("an expired call cannot be approved: %v", err)
	}
	if atomic.LoadInt32(f.sent) != 0 {
		t.Fatal("a late approval ran the tool")
	}
}

func TestHostedHoldEndsWhenTheAskingProcessLeaves(t *testing.T) {
	f := newHostedFixture(t, true)
	var open atomic.Bool
	open.Store(true)
	f.req.StillOpen = open.Load
	done := make(chan executor.HostedToolOutcome, 1)
	go func() { done <- f.call("send_mail", map[string]interface{}{"to": "a@example.com"}) }()
	heldRun(t, f.e)
	open.Store(false)
	select {
	case out := <-done:
		if out.Approval != "expired" || atomic.LoadInt32(f.sent) != 0 {
			t.Fatalf("the hold ends with its asker: %+v", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the hold outlived its asker")
	}
}

func TestHostedApprovalAfterTheAskerLeftDoesNotRun(t *testing.T) {
	f := newHostedFixture(t, true)
	var open atomic.Bool
	open.Store(true)
	f.req.StillOpen = open.Load
	// The decision lands between the asker leaving and the call noticing it.
	f.gw.register("send_mail", func(map[string]interface{}) (string, bool) { atomic.AddInt32(f.sent, 1); return "sent", false })
	done := make(chan executor.HostedToolOutcome, 1)
	go func() { done <- f.call("send_mail", map[string]interface{}{"to": "a@example.com"}) }()
	id := heldRun(t, f.e)
	open.Store(false)
	_, _, _ = f.e.Resume(context.Background(), "default", id, &executor.ResumeDecision{Approved: true})
	out := <-done
	if atomic.LoadInt32(f.sent) != 0 {
		t.Fatalf("a call whose asker is gone must not run, even approved: %+v", out)
	}
	if !out.Result.IsError {
		t.Fatalf("the refusal is an error result: %+v", out)
	}
}

func TestHostedGatedToolWithNoJournalIsRefusedNotLeftHanging(t *testing.T) {
	f := newHostedFixture(t, false)
	out := f.call("send_mail", map[string]interface{}{"to": "a@example.com"})
	if out.Kind != executor.HostedApprovalNeeded || atomic.LoadInt32(f.sent) != 0 {
		t.Fatalf("without a journal nobody can be asked, so the call is refused: %+v", out)
	}
}

func TestHostedAgentListedApprovalToolIsHeldToo(t *testing.T) {
	f := newHostedFixture(t, true)
	f.e.SetToolApprovalPolicy(nil)
	a := testAgent()
	a.ApprovalTools = []string{"lookup_order"}
	f.req.Agent = a
	t.Setenv("AGENTOVEN_TOOL_APPROVAL_WAIT", "1s")
	out := f.call("lookup_order", map[string]interface{}{"order_id": "A-1"})
	if out.Approval != "expired" || len(f.gw.calls) != 0 {
		t.Fatalf("Agent.ApprovalTools gates a brokered call as it gates a loop's: %+v", out)
	}
}
