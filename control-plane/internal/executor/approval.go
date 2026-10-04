package executor

import (
	"errors"

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
}

// ResumeDecision is a human's answer to a PendingApproval.
type ResumeDecision struct {
	Approved bool `json:"approved"`
	// Note, on denial, becomes the tool result every gated call in the turn
	// sees in place of running — "Human denied this tool call: <note>" — so
	// the agent can react to *why*, not just that it was refused.
	Note string `json:"note,omitempty"`
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
