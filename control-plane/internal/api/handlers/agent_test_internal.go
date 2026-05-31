package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// RunAgentTest executes the managed-agent test path without HTTP/auth middleware.
// Used by Pro test suite runner and other in-process callers.
func (h *Handlers) RunAgentTest(ctx context.Context, kitchen, agentName, message string) (*models.AgentTestResult, error) {
	if message == "" {
		return nil, fmt.Errorf("message is required")
	}

	agent, err := h.Store.GetAgent(ctx, kitchen, agentName)
	if err != nil {
		return nil, err
	}

	if agent.ModelProvider == "" && !agent.IsFrameworkNative() {
		return nil, fmt.Errorf("agent has no model provider configured")
	}

	if h.Guardrails != nil && len(agent.Guardrails) > 0 {
		eval, gErr := h.Guardrails.EvaluateInput(ctx, agent.Guardrails, message)
		if gErr != nil {
			log.Warn().Err(gErr).Str("agent", agentName).Msg("Input guardrail evaluation error (internal test runner)")
		} else if !eval.Passed {
			return nil, fmt.Errorf("input blocked by guardrails")
		}
	}

	if agent.IsFrameworkNative() {
		if agent.Status != models.AgentStatusReady {
			return nil, fmt.Errorf("agent %q is not ready (status: %s)", agentName, agent.Status)
		}
		response, traceRecord, err := h.proxyToProcess(ctx, agent, message, nil, kitchen)
		if err != nil {
			return nil, err
		}
		usage := models.TokenUsage{}
		if traceRecord.Usage != nil {
			usage = *traceRecord.Usage
		}
		return &models.AgentTestResult{
			Response: response,
			TraceID:  traceRecord.ID,
			Usage:    usage,
		}, nil
	}

	messages := []models.ChatMessage{}
	for _, ing := range agent.Ingredients {
		if ing.Kind == models.IngredientPrompt {
			if text, ok := ing.Config["text"].(string); ok && text != "" {
				messages = append(messages, models.ChatMessage{Role: "system", Content: text})
			}
		}
	}
	messages = append(messages, models.ChatMessage{Role: "user", Content: message})

	start := time.Now()
	routeReq := &models.RouteRequest{
		Messages: messages,
		Model:    agent.ModelName,
		Strategy: models.RoutingFallback,
		Kitchen:  kitchen,
		AgentRef: agentName,
	}
	resp, err := h.Router.Route(router.ContextSkipTrace(ctx), routeReq)
	duration := time.Since(start)
	if err != nil {
		return nil, err
	}

	if h.Guardrails != nil && len(agent.Guardrails) > 0 {
		eval, gErr := h.Guardrails.EvaluateOutput(ctx, agent.Guardrails, resp.Content)
		if gErr != nil {
			log.Warn().Err(gErr).Str("agent", agentName).Msg("Output guardrail evaluation error (internal test runner)")
		} else if !eval.Passed {
			return nil, fmt.Errorf("output blocked by guardrails")
		}
	}

	traceUsage := resp.Usage
	trace := &models.Trace{
		ID:          uuid.New().String(),
		AgentName:   agentName,
		Kitchen:     kitchen,
		Status:      "completed",
		DurationMs:  duration.Milliseconds(),
		TotalTokens: resp.Usage.TotalTokens,
		CostUSD:     resp.Usage.EstimatedCost,
		InputText:   message,
		OutputText:  resp.Content,
		Usage:       &traceUsage,
		Metadata: map[string]interface{}{
			"provider": resp.Provider,
			"model":    resp.Model,
			"type":     "test",
			"source":   "internal_test_runner",
		},
		CreatedAt: time.Now().UTC(),
	}
	h.Store.CreateTrace(ctx, trace)

	now := time.Now().UTC()
	llmStart := now.Add(-duration)
	reqJSON, _ := json.Marshal(messages)
	respJSON, _ := json.Marshal(map[string]interface{}{
		"content":         resp.Content,
		"thinking_blocks": resp.ThinkingBlocks,
	})
	llmSpan := models.Span{
		ID:         uuid.New().String(),
		TraceID:    trace.ID,
		Name:       fmt.Sprintf("llm.%s", resp.Model),
		Kind:       models.SpanKindLLM,
		Status:     "completed",
		StartTime:  llmStart,
		EndTime:    now,
		DurationMs: duration.Milliseconds(),
		Input:      reqJSON,
		Output:     respJSON,
		Usage:      &traceUsage,
		Model:      resp.Model,
		Provider:   resp.Provider,
	}
	h.Store.CreateSpan(ctx, &llmSpan)

	return &models.AgentTestResult{
		Response: resp.Content,
		TraceID:  trace.ID,
		Usage:    resp.Usage,
	}, nil
}
