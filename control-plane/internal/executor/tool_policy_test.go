package executor_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// policyGate holds the named tools for approval, the way an installed tool pack does.
type policyGate map[string]bool

func (p policyGate) RequiresApproval(_ context.Context, _, tool string) bool { return p[tool] }

// A tool the policy gates pauses the run although the agent never listed it, runs only after a
// person approves, and the gateway is told the call passed the gate.
func TestPolicyGatedToolPausesAndRunsOnlyAfterApproval(t *testing.T) {
	d := &scriptedDriver{replies: []turnReply{
		{toolCalls: []models.ToolCallResult{toolCall("1", "send_mail", map[string]interface{}{"to": "a@example.com"})}},
		{content: "sent"},
	}}
	gw := newFakeGateway()
	var sent int32
	gw.register("send_mail", func(map[string]interface{}) (string, bool) { atomic.AddInt32(&sent, 1); return "ok", false })
	e := newTestExecutor(t, d, gw)
	j, err := executor.NewFileJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e.SetJournal(j)
	e.SetToolApprovalPolicy(policyGate{"send_mail": true})

	tool := models.ResolvedTool{Name: "send_mail", Schema: map[string]interface{}{"type": "object"}}
	_, trace, err := e.Execute(context.Background(), testAgent(), "mail them", resolvedWithTools(tool), nil, false)
	if err != executor.ErrPausedForApproval {
		t.Fatalf("a policy-gated tool must pause the run, got %v", err)
	}
	if atomic.LoadInt32(&sent) != 0 {
		t.Fatal("a gated tool must not run before approval")
	}
	if trace.Pending == nil || len(trace.Pending.GatedNames) != 1 || trace.Pending.GatedNames[0] != "send_mail" {
		t.Fatalf("the pending approval should name send_mail: %+v", trace.Pending)
	}

	if _, _, err := e.Resume(context.Background(), "default", trace.TraceID, &executor.ResumeDecision{Approved: true}); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&sent) != 1 {
		t.Fatalf("an approved tool runs once, ran %d times", sent)
	}
	if len(gw.approvedCalls) != 1 || !gw.approvedCalls[0] {
		t.Fatalf("the gateway must be told the call passed the approval gate: %v", gw.approvedCalls)
	}
}

// A tool the policy does not name is not held, and a denial never reaches the gateway.
func TestPolicyDoesNotHoldOtherToolsAndADenialRunsNothing(t *testing.T) {
	d := &scriptedDriver{replies: []turnReply{
		{toolCalls: []models.ToolCallResult{toolCall("1", "lookup_order", map[string]interface{}{"order_id": "A-1"})}},
		{content: "found"},
	}}
	gw := newFakeGateway()
	gw.register("lookup_order", func(map[string]interface{}) (string, bool) { return "A-1 shipped", false })
	e := newTestExecutor(t, d, gw)
	e.SetToolApprovalPolicy(policyGate{"send_mail": true})

	resp, _, err := e.Execute(context.Background(), testAgent(), "where is it", resolvedWithTools(orderTool()), nil, false)
	if err != nil || resp != "found" {
		t.Fatalf("an ungated tool runs without a pause: %q, %v", resp, err)
	}

	d2 := &scriptedDriver{replies: []turnReply{
		{toolCalls: []models.ToolCallResult{toolCall("1", "send_mail", map[string]interface{}{})}},
		{content: "ok, not sending"},
	}}
	gw2 := newFakeGateway()
	gw2.register("send_mail", func(map[string]interface{}) (string, bool) { t.Error("a denied tool ran"); return "", false })
	e2 := newTestExecutor(t, d2, gw2)
	j, _ := executor.NewFileJournal(t.TempDir())
	e2.SetJournal(j)
	e2.SetToolApprovalPolicy(policyGate{"send_mail": true})
	tool := models.ResolvedTool{Name: "send_mail", Schema: map[string]interface{}{"type": "object"}}
	_, trace, err := e2.Execute(context.Background(), testAgent(), "mail", resolvedWithTools(tool), nil, false)
	if err != executor.ErrPausedForApproval {
		t.Fatal(err)
	}
	if _, _, err := e2.Resume(context.Background(), "default", trace.TraceID, &executor.ResumeDecision{Approved: false, Note: "no"}); err != nil {
		t.Fatal(err)
	}
	if len(gw2.calls) != 0 {
		t.Fatalf("a denied call must not reach the gateway: %v", gw2.calls)
	}
}
