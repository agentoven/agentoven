package guardrails

import (
	"context"
	"sync/atomic"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// OwnEndpointPolicy decides whether the custom endpoints an agent names in its own guardrails may be used.
// An edition that evaluates custom rules installs one (Pro: a Head Chef approves each endpoint for the agent,
// owner decision S7, 2026-11-03). It returns an error that says what is missing; the agent is then refused,
// as an agent whose workspace rule cannot be applied is.
type OwnEndpointPolicy func(ctx context.Context, kitchen, agent string, own []models.Guardrail) error

var ownEndpoints atomic.Pointer[OwnEndpointPolicy]

// UseOwnEndpointPolicy installs the policy. Call it before serving.
func UseOwnEndpointPolicy(p OwnEndpointPolicy) { ownEndpoints.Store(&p) }

// CheckOwnEndpoints asks the installed policy about an agent's own rules. With none installed (the community
// edition, which evaluates no custom rule) nothing is asked and nothing is refused. Effective calls it before
// every run; a bake calls it before it marks the agent ready.
func CheckOwnEndpoints(ctx context.Context, kitchen, agent string, own []models.Guardrail) error {
	p := ownEndpoints.Load()
	if p == nil || *p == nil {
		return nil
	}
	for _, r := range own {
		if r.Enabled && r.Kind == models.GuardrailCustom {
			return (*p)(ctx, kitchen, agent, own)
		}
	}
	return nil
}
