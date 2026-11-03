package handlers

import (
	"context"

	grails "github.com/agentoven/agentoven/control-plane/internal/guardrails"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// workspaceRules is what the kitchen's workspace guardrails alone require: the rules that apply to a
// caller that is no agent (a person or key calling a tool or a model directly). Like guardrailsFor it
// fails closed, so an error means the rules could not be established and the caller must be refused.
func (h *Handlers) workspaceRules(ctx context.Context, kitchen string) ([]models.Guardrail, error) {
	return grails.Effective(ctx, h.GuardrailPolicy, kitchen, "", nil)
}
