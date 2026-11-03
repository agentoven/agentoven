package workflow

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/rs/zerolog/log"
)

// SetApprovalObserver names who is told when a human gate starts waiting. Nil removes it. With an
// observer installed the observer owns reaching people through the kitchen's channels (messages,
// reminders, escalation), so a gate's gate_waiting event goes only to the MCP tools the step names.
func (e *Engine) SetApprovalObserver(o contracts.ApprovalObserver) { e.approvals = o }

// observeGate tells the observer that the gate is waiting. It never fails the gate: the record is
// stored and a person can still decide it from the queue.
func (e *Engine) observeGate(ctx context.Context, kitchen, gateKey string, maxWait time.Duration) {
	if e.approvals == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Error().Interface("panic", r).Str("gate_key", gateKey).Msg("approval observer panicked; the gate keeps waiting")
		}
	}()
	e.approvals.ApprovalRequested(ctx, contracts.ApprovalRequest{Kitchen: kitchen, Gate: contracts.GateRecipeGate, RecordID: gateKey, Deadline: maxWait})
}

// decideMu serialises the read-check-write of a gate's record so a decision and an expiry that arrive
// together in this process cannot both win. Across replicas the window is one store round trip.
var decideMu sync.Mutex

// ExpireGate ends a gate nobody answered by recording it as timed out. The waiting step takes the
// path its recipe gave for "nobody answered" (on_timeout: branch) or fails; it is never approved. It
// reports false, changing nothing, when the gate is not pending in that kitchen.
func (e *Engine) ExpireGate(ctx context.Context, kitchen, gateKey, reason string) (bool, error) {
	decideMu.Lock()
	defer decideMu.Unlock()
	record, err := e.store.GetApproval(ctx, gateKey)
	if err != nil || record == nil || record.Kitchen != kitchen || record.Status != "pending" {
		return false, nil
	}
	now := time.Now().UTC()
	record.Status = "timed_out"
	record.ResolvedAt = &now
	record.Comments = reason
	if err := e.store.UpdateApproval(ctx, record); err != nil {
		return false, fmt.Errorf("record the timeout of gate %s: %w", gateKey, err)
	}
	return true, nil
}

// gateTimedOut is what a human gate does when nobody answered: carry on along the recipe's
// on_timeout path when it has one, otherwise fail the step.
func (e *Engine) gateTimedOut(ctx context.Context, run *models.RecipeRun, step *models.Step, result *models.StepResult, why string) error {
	result.GateStatus = "timed_out"
	if ctx.Err() == nil && gateFollowsPath(step, "on_timeout") {
		result.Output = map[string]interface{}{"approved": false, "decision": "timed_out"}
		run.Status = models.RecipeRunRunning
		e.store.UpdateRecipeRun(ctx, run)
		return nil
	}
	return fmt.Errorf("human gate '%s' %s", step.Name, why)
}
