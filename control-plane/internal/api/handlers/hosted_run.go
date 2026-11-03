package handlers

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// RunIDHeader carries a run's ID from the control plane to a hosted process on every call it
// relays. The process presents it on its model calls, so a metered model gateway can tell which
// run a call belongs to and hold it to that run's budget.
const RunIDHeader = "X-AgentOven-Run-Id"

type hostedRunKey struct{}

func hostedRunFrom(ctx context.Context) string {
	id, _ := ctx.Value(hostedRunKey{}).(string)
	return id
}

// beginHostedRun brackets one call to a hosted process (a LangChain, LangGraph or Deep Agents
// process, which calls its model itself) for spend accounting. When the edition has run scopes
// and the agent has a budget, it opens a scope and returns a context carrying the run's ID,
// which the proxy sends to the process; the returned func closes the scope and reports what the
// process's model calls cost and whether the budget stopped them. Otherwise it does nothing and
// the func returns nil.
func (h *Handlers) beginHostedRun(ctx context.Context, agent *models.Agent) (context.Context, func() *models.RunOutcome) {
	if h.RunScopes == nil || agent.Budget == nil || agent.Budget.IsZero() {
		return ctx, func() *models.RunOutcome { return nil }
	}
	runID := uuid.New().String()
	scope := h.RunScopes.Open(ctx, agent.Kitchen, agent.Name, runID, *agent.Budget)
	ctx = context.WithValue(ctx, hostedRunKey{}, runID)
	var out *models.RunOutcome
	return ctx, func() *models.RunOutcome {
		if out == nil {
			o := scope.Close()
			out = &o
		}
		return out
	}
}

// respondHostedStopped answers a call to a hosted process that its run's budget stopped. The
// process cannot be checkpointed from outside, so unlike an agent run by the control plane's own
// loop it is not paused and resumed: the call ended, and what it spent up to then is reported.
func (h *Handlers) respondHostedStopped(w http.ResponseWriter, agentName, runID string, out *models.RunOutcome) {
	p := out.Stopped
	respondJSON(w, http.StatusPaymentRequired, map[string]interface{}{
		"error":     "run_budget_exhausted",
		"message":   fmt.Sprintf("the agent's run hit its budget (%s: %.4g of %.4g) and its model calls were refused", p.Dimension, p.Spent, p.Cap),
		"status":    "stopped",
		"agent":     agentName,
		"trace_id":  runID,
		"scope":     "gateway",
		"dimension": p.Dimension,
		"spent":     p.Spent,
		"cap":       p.Cap,
		"usage":     out.Usage,
	})
}

// newScopeRunID names the run of a tool scope when no model-gateway run scope is open for the call.
func newScopeRunID() string { return uuid.New().String() }
