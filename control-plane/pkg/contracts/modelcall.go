package contracts

import (
	"context"

	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// AdHocModelRouter sends a request to one provider that is not registered in a kitchen (a judge or a checker
// the kitchen configures in its settings) through the model router's whole chain: the guardrails on the prompt
// and the reply, the spend gate and rate limits, the response cache, the circuit and a trace. An edition that
// has such a caller uses this instead of making the HTTP call itself, so the call cannot skip any of it.
// OSS implementation: internal/router.ModelRouter.
type AdHocModelRouter interface {
	RouteVia(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, error)
}

// WithTrustedSystemPrompt marks a model call whose system messages were written by the platform's operator, not
// by the caller (a hosted agent's process sends the prompt it was baked with): the router's guardrails then leave
// the system messages alone and judge everything else the call carries. Only an edition that has authenticated
// such a caller by a credential of its own may set it.
func WithTrustedSystemPrompt(ctx context.Context) context.Context {
	return router.ContextSystemPromptTrusted(ctx)
}
