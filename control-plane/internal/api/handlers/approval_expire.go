package handlers

import (
	"context"
	"fmt"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
)

// Approval delivery, reminders and escalation are an edition's business (Pro ADR-0050); the control
// plane only says when a person is asked (an ApprovalObserver) and can end what nobody answered (an
// ApprovalExpirer). Neither can approve anything.

var _ contracts.ApprovalExpirer = (*Handlers)(nil)

// SetApprovalObserver tells o whenever a recipe gate or a run starts waiting for a person: a tool call,
// a budget cap, a run repeating itself. Nil removes it.
func (h *Handlers) SetApprovalObserver(o contracts.ApprovalObserver) {
	if h.Workflow != nil {
		h.Workflow.SetApprovalObserver(o)
	}
	if h.Executor != nil {
		h.Executor.SetApprovalObserver(o)
	}
}

// ExpireApproval ends a request nobody answered, as a refusal. A recipe gate is recorded as timed out
// and its step takes the recipe's on_timeout path or fails; a paused run is cancelled. Whatever the
// request was waiting to do is not done. It reports false, changing nothing, when the request is no
// longer pending (someone decided first) or is not of the gate named.
func (h *Handlers) ExpireApproval(ctx context.Context, kitchen, gate, recordID, reason string) (bool, error) {
	switch gate {
	case contracts.GateRecipeGate:
		if h.Workflow == nil {
			return false, fmt.Errorf("recipes are not available")
		}
		return h.Workflow.ExpireGate(ctx, kitchen, recordID, reason)
	case contracts.GateToolApproval, contracts.GateBudgetExtension, contracts.GateResumeGuidance:
		if h.Executor == nil {
			return false, fmt.Errorf("runs are not available")
		}
		js, err := h.Executor.RunState(ctx, kitchen, recordID)
		if err != nil || gateOfRun(js) != gate {
			return false, nil // gone, decided, or waiting for something else now
		}
		expired, err := h.Executor.ExpirePause(ctx, kitchen, recordID, reason)
		if expired && h.Tasks != nil {
			// A task held in this process still says "paused"; Cancel finds the run already ended and
			// brings the task's own status into line.
			_, _ = h.Tasks.Cancel(ctx, kitchen, recordID)
		}
		return expired, err
	}
	return false, fmt.Errorf("a %s request cannot be expired here", gate)
}
