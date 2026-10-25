package contracts

import (
	"context"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// RunScopes accounts for the model calls a hosted process (LangChain, LangGraph, Deep Agents,
// ...) makes while it handles one call. The control plane's own agent loop meters its calls
// itself; a hosted process calls its model directly, so the control plane can only cap its
// spend if the process's model calls pass through something that knows which run they belong
// to. An edition with a metered model gateway installs one of these: the control plane opens a
// scope when it relays a call to the process, tells the process its run ID, and closes the scope
// when the call returns.
//
// Without one (the community edition), hosted processes' spend is reported but not capped.
type RunScopes interface {
	// Open starts a scope for one call to a hosted agent, under budget. The run ID is passed to
	// the process, which presents it on its model calls.
	Open(ctx context.Context, kitchen, agent, runID string, budget models.RunBudget) RunScope
}

// RunScope is one open scope.
type RunScope interface {
	// Close ends the scope and reports what happened inside it.
	Close() models.RunOutcome
}
