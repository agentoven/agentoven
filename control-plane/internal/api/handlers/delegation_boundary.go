package handlers

import (
	"context"
	"fmt"

	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// InstallDelegationBoundary holds every delegation between agents to the target's own guardrails
// and lets an agent delegate to a framework-native one. New calls it; a Handlers built by hand (a
// test) must.
//
// Only the invoke routes used to apply a target's input and output rules, so an agent that
// delegated to another reached it with neither. Now the message goes through the target's input
// guardrails (workspace rules included, fail closed) before it is sent, and the answer through its
// output guardrails before it comes back to the delegating agent's model. A framework-native target
// (LangChain, LangGraph, Deep Agents) is reached through its process, as an invoke from outside
// reaches it, instead of being run on the Go loop in its place.
func (h *Handlers) InstallDelegationBoundary() {
	if h.Executor == nil {
		return
	}
	h.Executor.SetDelegationBoundary(&executor.DelegationBoundary{
		Input: func(ctx context.Context, target *models.Agent, message string) error {
			gr, err := h.guardrailsFor(ctx, target, target.Guardrails)
			if err != nil {
				return fmt.Errorf("%s's guardrails could not be established, so nothing was sent", target.Name)
			}
			if h.Guardrails == nil || len(gr) == 0 {
				return nil
			}
			eval, gErr := h.Guardrails.EvaluateInput(ctx, gr, message)
			if gErr != nil {
				h.emitGuardrailAudit(ctx, target.Kitchen, target.Name, "input", nil, gErr)
				return fmt.Errorf("%s's input guardrails could not be evaluated, so nothing was sent", target.Name)
			}
			if !eval.Passed {
				h.emitGuardrailAudit(ctx, target.Kitchen, target.Name, "input", eval, nil)
				return fmt.Errorf("the message was blocked by %s's input guardrails", target.Name)
			}
			return nil
		},
		Output: func(ctx context.Context, target *models.Agent, response string) (string, error) {
			gr, err := h.guardrailsFor(ctx, target, target.Guardrails)
			if err != nil {
				return "", fmt.Errorf("%s's guardrails could not be established, so its answer was withheld", target.Name)
			}
			if pass, _ := h.outputVerdict(ctx, target, gr, response); !pass {
				return "", fmt.Errorf("%s's answer was withheld by its output guardrails", target.Name)
			}
			return response, nil
		},
		Native: func(ctx context.Context, target *models.Agent, message string) (string, error) {
			resp, _, err := h.proxyToProcess(ctx, target, message, nil, nil, target.Kitchen)
			return resp, err
		},
	})
}
