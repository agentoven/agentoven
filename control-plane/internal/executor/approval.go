package executor

import (
	"context"
	"errors"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// ErrPausedForApproval is returned by Execute/ExecuteStream when the loop
// stops to wait for a human decision on a tool call named in
// Agent.ApprovalTools, instead of running it. The trace's Pending field
// carries the call waiting on a decision; call Resume with that trace's ID
// once a person has approved or denied it.
//
// This is deliberately a real error, not a special-cased success value: a
// caller that does not know about approval gates (an older integration, a
// script) gets a clear failure instead of silently treating "paused" as
// "finished" and reading a half-answer as the whole one.
var ErrPausedForApproval = errors.New("executor: paused, waiting for human approval of a tool call")

// PendingApproval is what a paused run is waiting on: every tool call the
// model asked for in the turn that triggered the pause (approval-gated or
// not — none of them ran yet), and which of their names are the ones
// actually gated. A turn pauses as a whole the moment any one of its calls is
// gated: ResumeDecision's answer applies to every gated call in the turn, not
// to one call at a time. That is a deliberate boundary, not an oversight —
// per-call partial approval within a single turn is a real feature a future
// pass can add; this pass makes the common case (one sensitive call gates
// the turn) correct and auditable.
type PendingApproval struct {
	Turn       int        `json:"turn"`
	ToolCalls  []ToolCall `json:"tool_calls"`
	GatedNames []string   `json:"gated_names"`
	// GuardrailHolds says, for the calls a guardrail rule holds for approval, which rules asked.
	GuardrailHolds []GuardrailHold `json:"guardrail_holds,omitempty"`
	// Usage and LatencyMs are the model call that asked for these tools. They are
	// journaled with the pause so the call is not lost from the run's spend.
	Usage     models.TokenUsage `json:"usage,omitempty"`
	LatencyMs int64             `json:"latency_ms,omitempty"`
}

// ResumeDecision is a human's answer to a PendingApproval.
type ResumeDecision struct {
	Approved bool `json:"approved"`
	// Note, on denial, becomes the tool result every gated call in the turn
	// sees in place of running — "Human denied this tool call: <note>" — so
	// the agent can react to *why*, not just that it was refused.
	Note string `json:"note,omitempty"`

	// Extend, on a budget pause, is the budget a person grants so the run can continue.
	// Approving a budget pause without one is an error: there is nothing to approve.
	Extend *models.BudgetExtension `json:"extend,omitempty"`
}

// requiresApproval reports whether name is one of agent's approval-gated
// tools.
func requiresApproval(agent *models.Agent, name string) bool {
	for _, n := range agent.ApprovalTools {
		if n == name {
			return true
		}
	}
	return false
}

// SetToolApprovalPolicy installs a policy that gates tools in every agent, beyond the ones an
// agent lists in ApprovalTools: an installed tool pack's write tools are held for a person by
// default. The policy is asked on each call, so changing it needs no re-bake. Pass nil to remove it.
func (e *Executor) SetToolApprovalPolicy(p contracts.ToolApprovalPolicy) {
	e.toolApproval = p
}

// policyGates reports whether the installed policy holds the tool for approval.
func (e *Executor) policyGates(ctx context.Context, kitchen, name string) bool {
	return e.toolApproval != nil && e.toolApproval.RequiresApproval(ctx, kitchen, name)
}

// withPolicyGated returns agent as it is for approval purposes: its own ApprovalTools plus every
// tool in resolved that the policy gates. A subagent cannot pause for a person, so the pool it may
// draw tools from is built from this, and a gated tool is never handed to one.
func (e *Executor) withPolicyGated(ctx context.Context, agent *models.Agent, resolved *models.ResolvedIngredients) *models.Agent {
	if e.toolApproval == nil || resolved == nil {
		return agent
	}
	cp := *agent
	cp.ApprovalTools = append([]string(nil), agent.ApprovalTools...)
	for _, t := range allResolvedTools(resolved) {
		if e.policyGates(ctx, agent.Kitchen, t.Name) {
			cp.ApprovalTools = append(cp.ApprovalTools, t.Name)
		}
	}
	return &cp
}
