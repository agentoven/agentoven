package router

import (
	"context"
	"fmt"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// SpendGate is the router's view of a spend gate; see contracts.SpendGate for what it means.
type SpendGate interface {
	Allow(ctx context.Context, q models.SpendQuery) error
	Record(ctx context.Context, r models.SpendRecord)
}

type gateBox struct{ g SpendGate }

// SetSpendGate installs the gate consulted before every model call and told about every one
// that completes (see contracts.SpendGate). Pass nil to remove it. It is safe to call while the
// router is in use.
func (mr *ModelRouter) SetSpendGate(g SpendGate) {
	if g == nil {
		mr.spendGate.Store(nil)
		return
	}
	mr.spendGate.Store(&gateBox{g: g})
}

// spendAllow asks the gate, if there is one, whether the call may go out.
func (mr *ModelRouter) spendAllow(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest) error {
	box := mr.spendGate.Load()
	if box == nil {
		return nil
	}
	q := models.SpendQuery{Kitchen: req.Kitchen, Agent: req.AgentRef, Provider: provider.Name, Model: req.Model}
	if req.MaxTokens != nil {
		q.MaxOutputTokens = *req.MaxTokens
	}
	if err := box.g.Allow(ctx, q); err != nil {
		// A gate that errors for its own reasons still stops the call; wrapping keeps a
		// budget refusal recognisable to the caller.
		return fmt.Errorf("spend gate: %w", err)
	}
	return nil
}

// spendRecord tells the gate about a completed call.
func (mr *ModelRouter) spendRecord(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest, resp *models.RouteResponse) {
	if box := mr.spendGate.Load(); box != nil && resp != nil {
		box.g.Record(ctx, models.SpendRecord{Kitchen: req.Kitchen, Agent: req.AgentRef, Provider: provider.Name, Model: req.Model, Usage: resp.Usage})
	}
}
