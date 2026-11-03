package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// respondRunStopped answers a request whose run stopped for a reason other than finishing
// or failing, and reports whether it did. Callers that return the run's output call it
// first, so a paused or capped run never gets treated as an answer:
//
//   - paused at its budget: 402, with the dimension and numbers and where to resume it
//   - paused for approval of a tool call: 202, with what is pending
//   - ended at its budget (nobody to ask, or a person said no): 402
//   - a dollar cap on a model with no known price: 422
//
// trace may be nil.
func (h *Handlers) respondRunStopped(w http.ResponseWriter, agentName string, trace *executor.ExecutionTrace, err error) bool {
	if err == nil {
		return false
	}
	traceID := ""
	if trace != nil {
		traceID = trace.TraceID
	}

	// A model call that a rate limit refused is a 429 that says when to come back, never a 500.
	if respondRateLimited(w, err) {
		return true
	}

	var paused *executor.PausedError
	switch {
	case errors.As(err, &paused) && paused.Stall != nil:
		body := map[string]interface{}{
			"error":    "run_stalled",
			"message":  err.Error(),
			"status":   "paused",
			"agent":    agentName,
			"trace_id": traceID,
			"stall":    paused.Stall,
		}
		if traceID != "" {
			body["resume"] = fmt.Sprintf("POST /api/v1/agents/%s/runs/%s/resume", agentName, traceID)
		}
		respondJSON(w, http.StatusConflict, body)
		return true

	case errors.Is(err, executor.ErrStalled):
		respondJSON(w, http.StatusConflict, map[string]interface{}{
			"error": "run_stalled", "message": err.Error(), "status": "stopped", "agent": agentName, "trace_id": traceID,
		})
		return true

	case errors.As(err, &paused) && paused.Budget != nil:
		b := paused.Budget
		body := map[string]interface{}{
			"error":     "run_budget_exhausted",
			"message":   err.Error(),
			"status":    "paused",
			"agent":     agentName,
			"trace_id":  traceID,
			"scope":     b.Scope,
			"dimension": b.Dimension,
			"spent":     b.Spent,
			"cap":       b.Cap,
			"projected": b.Projected,
			"turn":      b.Turn,
		}
		if traceID != "" {
			body["resume"] = fmt.Sprintf("POST /api/v1/agents/%s/runs/%s/resume", agentName, traceID)
		}
		if trace != nil && trace.Budget != nil {
			body["budget"] = trace.Budget
		}
		respondJSON(w, http.StatusPaymentRequired, body)
		return true

	case errors.Is(err, executor.ErrPausedForApproval):
		body := map[string]interface{}{
			"status":   "paused",
			"agent":    agentName,
			"trace_id": traceID,
			"resume":   fmt.Sprintf("POST /api/v1/agents/%s/runs/%s/resume", agentName, traceID),
		}
		if trace != nil {
			body["pending"] = trace.Pending
		}
		respondJSON(w, http.StatusAccepted, body)
		return true

	case errors.Is(err, executor.ErrBudgetExhausted):
		body := map[string]interface{}{
			"error":    "run_budget_exhausted",
			"message":  err.Error(),
			"status":   "stopped",
			"agent":    agentName,
			"trace_id": traceID,
		}
		if trace != nil && trace.BudgetPause != nil {
			body["scope"] = trace.BudgetPause.Scope
			body["dimension"] = trace.BudgetPause.Dimension
			body["spent"] = trace.BudgetPause.Spent
			body["cap"] = trace.BudgetPause.Cap
		}
		respondJSON(w, http.StatusPaymentRequired, body)
		return true

	case errors.Is(err, executor.ErrUnpricedModel):
		respondJSON(w, http.StatusUnprocessableEntity, map[string]interface{}{
			"error":   "unpriced_model",
			"message": err.Error(),
		})
		return true
	}
	return false
}

// authorizeExtension applies the edition's rule for who may raise a budget, and reports
// whether the request may go on. It answers the 403 itself.
func (h *Handlers) authorizeExtension(w http.ResponseWriter, r *http.Request, ext *models.BudgetExtension) bool {
	if ext == nil || h.AuthorizeBudgetExtension == nil {
		return true
	}
	if err := h.AuthorizeBudgetExtension(r); err != nil {
		respondJSON(w, http.StatusForbidden, map[string]interface{}{
			"error":   "permission_denied",
			"message": "raising a run's budget needs the budget:extend permission: " + err.Error(),
		})
		return false
	}
	return true
}

// auditRun records a run event (see Executor.SetAudit) in the audit log.
func (h *Handlers) auditRun(ctx context.Context, kitchen, action string, details map[string]interface{}) {
	if h.Store == nil {
		return
	}
	userID, userEmail, displayName, provider, actorType := auditActorFromIdentity(ctx)
	if details == nil {
		details = map[string]interface{}{}
	}
	details["provider"] = provider
	resourceID, _ := details["trace_id"].(string)
	_ = h.Store.CreateAuditEvent(ctx, &models.AuditEvent{
		ID: uuid.New().String(), Timestamp: time.Now().UTC(),
		UserID: userID, UserEmail: userEmail, ActorType: actorType, ActorDisplayName: displayName,
		Action: action, Resource: "run", ResourceID: resourceID, Kitchen: kitchen,
		Details: details, ResponseStatus: http.StatusOK,
		RegulationTags: []string{"cost-control"}, DataClassification: "internal",
	})
}
