package executor

import (
	"context"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// DelegationBoundary is what stands between an agent and the agent it delegates to. The target is
// held to its own rules even though the request comes from another agent and not from outside: what
// the model of the delegating agent wrote is untrusted input to the target, and what the target
// answers is output that is about to leave it. Without a boundary a delegation skips the target's
// input and output guardrails, which only the invoke routes applied.
//
// The control plane's handlers install one (Handlers.New); an executor used on its own has none.
type DelegationBoundary struct {
	// Input is asked before the message goes to target. An error refuses the delegation; the
	// delegating agent's model is told why (the message must not carry the blocked content).
	Input func(ctx context.Context, target *models.Agent, message string) error
	// Output is asked about the target's answer. It returns the answer to hand back, or an error to
	// withhold it.
	Output func(ctx context.Context, target *models.Agent, response string) (string, error)
	// Native runs a message on a framework-native agent's own process (LangChain, LangGraph, Deep
	// Agents), the way an invoke from outside does. Without it such an agent cannot be delegated to:
	// its definition is not a configuration the Go loop can run in its place.
	Native func(ctx context.Context, target *models.Agent, message string) (string, error)
}

// SetDelegationBoundary installs the boundary. Pass nil to remove it.
func (e *Executor) SetDelegationBoundary(b *DelegationBoundary) { e.boundary = b }
