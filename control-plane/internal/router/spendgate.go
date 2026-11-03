package router

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// SpendGate is the router's view of a spend gate; see contracts.SpendGate for what it means.
type SpendGate interface {
	Allow(ctx context.Context, q models.SpendQuery) error
	Record(ctx context.Context, r models.SpendRecord)
}

// SpendReserver is an optional extension of SpendGate for a gate that must not let parallel
// callers oversubscribe a budget. Allow and Record are separate calls with nothing linking them, so a
// gate that only has those can only check what has already been spent; many callers that check at
// once all see the same headroom. A gate that also implements SpendReserver is asked to Reserve
// the worst case of a call before it is made, and the router then settles or cancels exactly that
// reservation, so the budget is held for calls in flight. When a gate implements it, the router uses
// Reserve instead of Allow and Settle instead of Record.
type SpendReserver interface {
	// Reserve admits the call or refuses it (see SpendGate.Allow for what an error means).
	Reserve(ctx context.Context, q models.SpendQuery) (SpendReservation, error)
}

// SpendReservation is a held part of a budget for one call in flight.
type SpendReservation interface {
	// Settle ends the reservation with what the call actually used.
	Settle(ctx context.Context, r models.SpendRecord)
	// Cancel ends it for a call that failed or was never made; nothing is charged.
	Cancel(ctx context.Context)
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
	q := spendQueryOf(provider, req)
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

func spendQueryOf(provider *models.ModelProvider, req *models.RouteRequest) models.SpendQuery {
	q := models.SpendQuery{Kitchen: req.Kitchen, Agent: req.AgentRef, Provider: provider.Name, Model: req.Model}
	if req.MaxTokens != nil {
		q.MaxOutputTokens = *req.MaxTokens
	}
	q.EstimatedInputTokens = estimateInputTokens(req)
	return q
}

// estimateInputTokens is the prompt's size at about four characters a token, tool definitions
// included. It only sizes a reservation; the bill is the usage the provider reports.
func estimateInputTokens(req *models.RouteRequest) int {
	n := 0
	for _, m := range req.Messages {
		n += len(m.Content) + len(m.Role) + 8
		for _, tc := range m.ToolCalls {
			n += len(tc.Function.Name) + len(tc.Function.Arguments) + 8
		}
		for _, p := range m.ContentParts {
			n += len(p.Text) + len(p.Content) + 8 // text only; media is sized by the provider, not here
		}
	}
	for _, t := range req.Tools {
		n += len(t.Function.Name) + len(t.Function.Description) + 64
		if b, err := json.Marshal(t.Function.Parameters); err == nil {
			n += len(b)
		}
	}
	return n/4 + 1
}

// spendBegin is what a call goes through: it asks the gate (reserving, when the gate can) and
// returns the function that ends the gate's part in the call. Pass the response of a call that
// completed, nil for one that failed. A refusal is returned as spendAllow returns it.
func (mr *ModelRouter) spendBegin(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest) (func(*models.RouteResponse), error) {
	box := mr.spendGate.Load()
	if box == nil {
		return func(*models.RouteResponse) {}, nil
	}
	if rg, ok := box.g.(SpendReserver); ok {
		res, err := rg.Reserve(ctx, spendQueryOf(provider, req))
		if err != nil {
			return nil, fmt.Errorf("spend gate: %w", err)
		}
		return func(resp *models.RouteResponse) {
			// The reservation is ended even when the caller's context is gone: a held budget
			// that is never released would shrink the budget for everyone.
			ectx := context.WithoutCancel(ctx)
			if resp == nil {
				res.Cancel(ectx)
				return
			}
			res.Settle(ectx, models.SpendRecord{Kitchen: req.Kitchen, Agent: req.AgentRef, Provider: provider.Name, Model: req.Model, Usage: resp.Usage})
		}, nil
	}
	if err := mr.spendAllow(ctx, provider, req); err != nil {
		return nil, err
	}
	return func(resp *models.RouteResponse) { mr.spendRecord(ctx, provider, req, resp) }, nil
}

// streamCharge is the response a streamed call is settled with. The usage the provider reported is used
// when there is one (and priced when the driver did not price it). When the stream ended without it
// (a mid-stream error, a client that went away, a provider that does not send usage), the missing
// part is estimated from what was received, at the same four characters a token the reservation was
// sized with, so the charge errs towards the real bill instead of towards zero. nil means nothing was
// generated and the reservation is released.
func (mr *ModelRouter) streamCharge(provider *models.ModelProvider, req *models.RouteRequest, reported *models.TokenUsage, receivedChars int) *models.RouteResponse {
	if reported == nil && receivedChars == 0 {
		return nil
	}
	var u models.TokenUsage
	if reported != nil {
		u = *reported
	}
	estimated := false
	if u.InputTokens == 0 {
		u.InputTokens, estimated = int64(estimateInputTokens(req)), true
	}
	if u.OutputTokens == 0 && u.ThinkingTokens == 0 && receivedChars > 0 {
		u.OutputTokens, estimated = int64(receivedChars/4+1), true
	}
	if u.TotalTokens < u.InputTokens+u.OutputTokens+u.ThinkingTokens {
		u.TotalTokens = u.InputTokens + u.OutputTokens + u.ThinkingTokens
	}
	if estimated || (u.PriceSource == "" && u.EstimatedCost == 0) {
		mr.settle(provider, req.Model, &u)
	}
	return &models.RouteResponse{Usage: u}
}
