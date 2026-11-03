package executor

import (
	"context"
	"errors"
	"fmt"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/rs/zerolog/log"
)

// SetApprovalObserver names who is told when a run pauses for a person: an approval-gated tool call,
// a budget cap, a run repeating itself. Nil removes it.
func (e *Executor) SetApprovalObserver(o contracts.ApprovalObserver) { e.approvals = o }

// pausedFor tells the observer that the run is now waiting for a decision of the given kind. The run
// is already paused and journaled when this is called, so an observer that fails (or panics) leaves a
// pause that a person can still decide from the queue; it never changes what happens to the run.
func (e *Executor) pausedFor(ctx context.Context, st *runState, kind PauseKind) {
	if e.approvals == nil {
		return
	}
	gate := map[PauseKind]string{PauseApproval: contracts.GateToolApproval, PauseBudget: contracts.GateBudgetExtension, PauseStall: contracts.GateResumeGuidance}[kind]
	kitchen, traceID := st.agent.Kitchen, st.traceID
	defer func() {
		if r := recover(); r != nil {
			log.Error().Interface("panic", r).Str("trace_id", traceID).Msg("approval observer panicked; the run stays paused")
		}
	}()
	e.approvals.ApprovalRequested(ctx, contracts.ApprovalRequest{Kitchen: kitchen, Gate: gate, RecordID: traceID})
}

// ExpirePause ends a run that is still paused because nobody decided in time. It is a refusal: the
// run is cancelled with reason as its error and nothing it was waiting to do is done. It reports false
// when the run was not paused any more (a person decided first, or it finished). The pause is
// claimed through the journal the way a person's decision is, so exactly one of a late approval and
// the expiry wins, on any replica.
func (e *Executor) ExpirePause(ctx context.Context, kitchen, traceID, reason string) (bool, error) {
	js, err := e.RunState(ctx, kitchen, traceID)
	if err != nil {
		return false, err
	}
	if js.Status != RunPaused {
		return false, nil
	}
	claimed, err := e.journal.ClaimResume(ctx, traceID, nil)
	if err != nil || !claimed {
		return false, err
	}
	if err := e.journal.MarkOutcome(ctx, traceID, OutcomeCancelled, "", errors.New(reason)); err != nil {
		return true, fmt.Errorf("run %s was claimed for expiry but its ending could not be recorded: %w", traceID, err)
	}
	e.auditRun(ctx, kitchen, "run.approval_expired", map[string]interface{}{"trace_id": traceID, "pause_kind": PauseKindOf(js), "reason": reason})
	return true, nil
}
