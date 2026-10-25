package contracts

import (
	"context"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

type budgetKey struct{}

// WithBudget attaches a per-request run budget to ctx. The executor combines it with the
// agent's own Budget by tightening only: a request can ask for a smaller allowance than the
// agent is configured with, never a larger one. It travels in the context so that the public
// Runner.Execute signature, which Pro depends on, does not have to change.
func WithBudget(ctx context.Context, b models.RunBudget) context.Context {
	if b.IsZero() {
		return ctx
	}
	return context.WithValue(ctx, budgetKey{}, &b)
}

// WithoutBudget hides any per-request budget from ctx. The executor uses it when it hands
// a context to a subagent: the request's allowance belongs to the run that received it,
// and the subagent is bounded by the share its parent grants.
func WithoutBudget(ctx context.Context) context.Context {
	return context.WithValue(ctx, budgetKey{}, (*models.RunBudget)(nil))
}

// BudgetFrom returns the per-request budget on ctx, or nil.
func BudgetFrom(ctx context.Context) *models.RunBudget {
	b, _ := ctx.Value(budgetKey{}).(*models.RunBudget)
	return b
}
