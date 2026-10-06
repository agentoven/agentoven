package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/rs/zerolog/log"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// DefaultToolTimeout bounds one tool call. A model that can call several
// tools per turn can also wait on several at once if nothing bounds any one
// of them — 120s is generous for a well-behaved MCP tool and short enough
// that a hung one does not stall an episode indefinitely.
const DefaultToolTimeout = 120 * time.Second

// executeToolCallsParallel runs every tool call in one turn concurrently,
// each under its own timeout, and returns results in the same order as
// calls regardless of which finished first — the model's next turn needs
// tool_call_id-correlated results in a stable order, not finish order.
//
// Parallel, not sequential, because the model already decided these calls
// are independent of each other by asking for them in the same turn; running
// them one at a time only adds up every tool's latency instead of taking the
// slowest one.
func (e *Executor) executeToolCallsParallel(ctx context.Context, agent *models.Agent, calls []ToolCall, schemas map[string]map[string]interface{}, sink *eventSink) []ToolResult {
	results := make([]ToolResult, len(calls))
	var wg sync.WaitGroup
	for i, tc := range calls {
		wg.Add(1)
		go func(i int, tc ToolCall) {
			defer wg.Done()
			sink.emit(Event{Type: EventToolCallStart, ToolCall: &tc})
			result := e.dispatchToolCall(ctx, agent, tc, schemas[tc.Name])
			sink.emit(Event{Type: EventToolCallEnd, ToolCall: &tc, ToolResult: &result})
			results[i] = result
		}(i, tc)
	}
	wg.Wait()
	return results
}

// dispatchToolCall validates tc's arguments against schema, then runs it
// through executeTool under a per-call timeout. A schema failure never
// reaches the gateway at all — the model gets a precise correction instead of
// whatever the real tool happens to do with malformed input, and the tool is
// never invoked with arguments it did not ask to receive.
func (e *Executor) dispatchToolCall(ctx context.Context, agent *models.Agent, tc ToolCall, schema map[string]interface{}) ToolResult {
	_, toolSpan := tracer.Start(ctx, "tool."+tc.Name,
		oteltrace.WithSpanKind(oteltrace.SpanKindClient),
		oteltrace.WithAttributes(
			attribute.String("tool.name", tc.Name),
			attribute.String("tool.call_id", tc.ID),
		),
	)
	defer toolSpan.End()

	// schema is nil for the virtual agentoven_delegate tool (it isn't one of
	// resolved.Tools), and validateArgs treats a nil schema as unconstrained —
	// so this naturally only ever rejects a real MCP tool's malformed call.
	if err := validateArgs(schema, tc.Arguments); err != nil {
		toolSpan.SetStatus(codes.Error, err.Error())
		toolSpan.SetAttributes(attribute.Bool("tool.is_error", true), attribute.Bool("tool.validation_failed", true))
		return ToolResult{ToolCallID: tc.ID, Name: tc.Name, IsError: true, Content: fmt.Sprintf("invalid arguments: %s", err.Error())}
	}

	// Guardrails run mid-loop, not just around the whole request: the
	// arguments the model generated are its output heading out to an external
	// system (same direction, same checks, as its final text response), and a
	// tool's result is untrusted external content about to enter the model's
	// context (same direction as a user message — and the classic vector for
	// a malicious MCP server or scraped page to smuggle in a prompt
	// injection, which is exactly what the "input" stage's heuristics cover).
	guards, gerr := e.guardrailsFor(ctx, agent)
	if gerr != nil {
		log.Error().Err(gerr).Str("agent", agent.Name).Str("tool", tc.Name).Msg("tool call not run: workspace guardrails could not be applied")
		toolSpan.SetStatus(codes.Error, "guardrail policy unavailable")
		toolSpan.SetAttributes(attribute.Bool("tool.is_error", true), attribute.Bool("tool.guardrail_blocked", true))
		return ToolResult{ToolCallID: tc.ID, Name: tc.Name, IsError: true, Content: "Error: tool call not run, the guardrail policy could not be applied"}
	}
	if e.guardrails != nil && len(guards) > 0 {
		argsJSON, _ := json.Marshal(tc.Arguments)
		if eval, gErr := e.guardrails.EvaluateOutput(ctx, guards, string(argsJSON)); gErr == nil && eval != nil && !eval.Passed {
			toolSpan.SetStatus(codes.Error, "tool call arguments blocked by guardrails")
			toolSpan.SetAttributes(attribute.Bool("tool.is_error", true), attribute.Bool("tool.guardrail_blocked", true))
			return ToolResult{ToolCallID: tc.ID, Name: tc.Name, IsError: true, Content: "Error: tool call arguments blocked by guardrails"}
		}
	}

	timeout := e.toolTimeout
	if timeout <= 0 {
		timeout = DefaultToolTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result := e.executeTool(callCtx, agent, tc)

	if !result.IsError && e.guardrails != nil && len(guards) > 0 {
		if eval, gErr := e.guardrails.EvaluateInput(ctx, guards, result.Content); gErr == nil && eval != nil && !eval.Passed {
			result = ToolResult{ToolCallID: tc.ID, Name: tc.Name, IsError: true, Content: "Error: tool result blocked by guardrails"}
		}
	}

	if result.IsError {
		toolSpan.SetStatus(codes.Error, result.Content)
	} else {
		toolSpan.SetStatus(codes.Ok, "")
	}
	toolSpan.SetAttributes(attribute.Bool("tool.is_error", result.IsError))
	return result
}
