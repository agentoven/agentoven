// Package executor implements the agentic loop for managed-mode agents.
//
// When an agent's Mode is "managed", AgentOven runs the agent directly:
//
//	prompt template → render with variables → build messages →
//	call Model Router → if tool_calls, execute each via MCP Gateway →
//	feed results back → repeat until text response or max_turns hit.
//
// External-mode agents are proxied to the developer's A2A endpoint instead.
package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/mcpgw"
	"github.com/agentoven/agentoven/control-plane/internal/resolver"
	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// DefaultMaxTurns is the maximum number of LLM ↔ tool loops.
const DefaultMaxTurns = 10

// ToolCall represents a tool invocation requested by the LLM.
type ToolCall struct {
	ID       string                 `json:"id"`
	Name     string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

// ToolResult represents the result of executing a tool call.
type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	IsError    bool   `json:"is_error"`
}

// ExecutionTrace records the full execution history.
type ExecutionTrace struct {
	TraceID   string       `json:"trace_id"`
	AgentName string       `json:"agent_name"`
	Kitchen   string       `json:"kitchen"`
	Turns     []Turn       `json:"turns"`
	TotalMs   int64        `json:"total_ms"`
	Usage     models.TokenUsage `json:"usage"`
}

// Turn is one iteration of the agentic loop.
type Turn struct {
	Number         int                   `json:"number"`
	Request        []models.ChatMessage  `json:"request"`
	Response       string                `json:"response,omitempty"`
	ToolCalls      []ToolCall            `json:"tool_calls,omitempty"`
	ToolResults    []ToolResult          `json:"tool_results,omitempty"`
	ThinkingBlocks []models.ThinkingBlock `json:"thinking_blocks,omitempty"`
	LatencyMs      int64                 `json:"latency_ms"`
	Usage          models.TokenUsage     `json:"usage"`
}

// Executor runs managed-mode agents through an agentic tool-use loop.
type Executor struct {
	store   store.Store
	router  *router.ModelRouter
	gateway *mcpgw.Gateway
}

// NewExecutor creates a new managed-agent executor.
func NewExecutor(s store.Store, r *router.ModelRouter, gw *mcpgw.Gateway) *Executor {
	return &Executor{
		store:   s,
		router:  r,
		gateway: gw,
	}
}

// Execute runs the agentic loop for a managed agent.
//
// Flow:
//  1. Render the prompt template with variables
//  2. Build system + user messages
//  3. Call Model Router
//  4. If LLM returns tool_calls → execute each via MCP Gateway → add results → goto 3
//  5. If LLM returns text → return as final response
//  6. If max_turns reached → return with "max turns exceeded" warning
func (e *Executor) Execute(ctx context.Context, agent *models.Agent, userMessage string, resolved *models.ResolvedIngredients, promptVars map[string]string, thinkingEnabled bool) (string, *ExecutionTrace, error) {
	traceID := uuid.New().String()
	trace := &ExecutionTrace{
		TraceID:   traceID,
		AgentName: agent.Name,
		Kitchen:   agent.Kitchen,
	}

	start := time.Now()
	maxTurns := agent.MaxTurns
	if maxTurns <= 0 {
		maxTurns = DefaultMaxTurns
	}

	// Build initial messages
	messages := e.buildInitialMessages(agent, resolved, userMessage, promptVars)

	// Get available tool definitions for the LLM
	toolDefs := e.buildToolDefinitions(resolved.Tools)

	var totalUsage models.TokenUsage
	var err error

	for turn := 1; turn <= maxTurns; turn++ {
		turnStart := time.Now()

		// Call Model Router with tool definitions
		routeReq := &models.RouteRequest{
			Messages:        messages,
			Model:           resolved.Model.Model,
			Strategy:        models.RoutingFallback,
			Kitchen:         agent.Kitchen,
			AgentRef:        agent.Name,
			ThinkingEnabled: thinkingEnabled,
		}

		// Use backup provider failover if configured on the agent
		var routeResp *models.RouteResponse
		if agent.BackupProvider != "" {
			routeResp, err = e.router.RouteWithBackup(ctx, routeReq, agent.BackupProvider, agent.BackupModel)
		} else {
			routeResp, err = e.router.Route(ctx, routeReq)
		}
		if err != nil {
			return "", trace, fmt.Errorf("model router call failed (turn %d): %w", turn, err)
		}

		// Accumulate token usage
		totalUsage.InputTokens += routeResp.Usage.InputTokens
		totalUsage.OutputTokens += routeResp.Usage.OutputTokens
		totalUsage.TotalTokens += routeResp.Usage.TotalTokens
		totalUsage.ThinkingTokens += routeResp.Usage.ThinkingTokens
		totalUsage.EstimatedCost += routeResp.Usage.EstimatedCost

		// Parse the response for tool calls
		toolCalls := e.parseToolCalls(routeResp.Content)

		turnRecord := Turn{
			Number:         turn,
			Request:        messages,
			ThinkingBlocks: routeResp.ThinkingBlocks,
			Usage:          routeResp.Usage,
		}

		if len(toolCalls) == 0 {
			// No tool calls — LLM gave a direct text response
			turnRecord.Response = routeResp.Content
			turnRecord.LatencyMs = time.Since(turnStart).Milliseconds()
			trace.Turns = append(trace.Turns, turnRecord)
			trace.TotalMs = time.Since(start).Milliseconds()
			trace.Usage = totalUsage

			log.Info().
				Str("agent", agent.Name).
				Int("turns", turn).
				Int64("total_ms", trace.TotalMs).
				Msg("Managed agent execution complete")

			return routeResp.Content, trace, nil
		}

		// Execute tool calls via MCP Gateway
		turnRecord.ToolCalls = toolCalls
		var toolResults []ToolResult

		for _, tc := range toolCalls {
			result := e.executeTool(ctx, agent.Kitchen, tc)
			toolResults = append(toolResults, result)
		}

		turnRecord.ToolResults = toolResults
		turnRecord.LatencyMs = time.Since(turnStart).Milliseconds()
		trace.Turns = append(trace.Turns, turnRecord)

		// Add assistant message with tool calls info
		messages = append(messages, models.ChatMessage{
			Role:    "assistant",
			Content: routeResp.Content,
		})

		// Add tool results as messages
		for _, tr := range toolResults {
			messages = append(messages, models.ChatMessage{
				Role:    "tool",
				Content: fmt.Sprintf("[Tool: %s] %s", tr.Name, tr.Content),
			})
		}

		_ = toolDefs // tool definitions would be sent with the route request in a full implementation

		log.Debug().
			Str("agent", agent.Name).
			Int("turn", turn).
			Int("tool_calls", len(toolCalls)).
			Msg("Agentic loop continuing")
	}

	// Max turns exceeded
	trace.TotalMs = time.Since(start).Milliseconds()
	trace.Usage = totalUsage

	lastContent := ""
	if len(trace.Turns) > 0 {
		lastTurn := trace.Turns[len(trace.Turns)-1]
		lastContent = lastTurn.Response
		if lastContent == "" && len(lastTurn.ToolResults) > 0 {
			lastContent = lastTurn.ToolResults[len(lastTurn.ToolResults)-1].Content
		}
	}

	log.Warn().
		Str("agent", agent.Name).
		Int("max_turns", maxTurns).
		Msg("Managed agent hit max turns")

	return fmt.Sprintf("[Max turns (%d) reached] %s", maxTurns, lastContent), trace, nil
}

// buildInitialMessages constructs the system prompt and user message.
func (e *Executor) buildInitialMessages(agent *models.Agent, resolved *models.ResolvedIngredients, userMessage string, promptVars map[string]string) []models.ChatMessage {
	messages := make([]models.ChatMessage, 0, 3)

	// System prompt from resolved prompt ingredient
	systemPrompt := ""
	if resolved.Prompt != nil {
		systemPrompt = resolver.RenderPrompt(resolved.Prompt.Template, promptVars)
	} else if agent.Description != "" {
		// Fallback: use agent description as system prompt
		systemPrompt = agent.Description
	}

	// Add tool descriptions to system prompt
	if len(resolved.Tools) > 0 {
		toolList := "\n\nAvailable tools:\n"
		for _, t := range resolved.Tools {
			toolList += fmt.Sprintf("- %s: %s\n", t.Name, describeSchema(t.Schema))
		}
		toolList += "\nTo use a tool, respond with a JSON block: {\"tool_calls\": [{\"name\": \"tool_name\", \"arguments\": {...}}]}"
		systemPrompt += toolList
	}

	if systemPrompt != "" {
		messages = append(messages, models.ChatMessage{
			Role:    "system",
			Content: systemPrompt,
		})
	}

	// User message
	messages = append(messages, models.ChatMessage{
		Role:    "user",
		Content: userMessage,
	})

	return messages
}

// buildToolDefinitions creates OpenAI-compatible tool definitions for the LLM.
func (e *Executor) buildToolDefinitions(tools []models.ResolvedTool) []map[string]interface{} {
	defs := make([]map[string]interface{}, 0, len(tools))
	for _, t := range tools {
		def := map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":       t.Name,
				"parameters": t.Schema,
			},
		}
		if desc, ok := t.Schema["description"].(string); ok {
			def["function"].(map[string]interface{})["description"] = desc
		}
		defs = append(defs, def)
	}
	return defs
}

// parseToolCalls attempts to extract tool calls from the LLM response.
// Supports two formats:
//  1. JSON block with {"tool_calls": [...]} in the response text
//  2. Direct JSON array of tool call objects
func (e *Executor) parseToolCalls(content string) []ToolCall {
	if content == "" {
		return nil
	}

	// Try to find JSON in the response
	type toolCallsWrapper struct {
		ToolCalls []ToolCall `json:"tool_calls"`
	}

	// Try wrapper format: {"tool_calls": [...]}
	var wrapper toolCallsWrapper
	if err := json.Unmarshal([]byte(content), &wrapper); err == nil && len(wrapper.ToolCalls) > 0 {
		// Assign IDs if missing
		for i := range wrapper.ToolCalls {
			if wrapper.ToolCalls[i].ID == "" {
				wrapper.ToolCalls[i].ID = fmt.Sprintf("call_%d", i)
			}
		}
		return wrapper.ToolCalls
	}

	// Try direct array: [{"name": "...", "arguments": {...}}]
	var calls []ToolCall
	if err := json.Unmarshal([]byte(content), &calls); err == nil && len(calls) > 0 {
		for i := range calls {
			if calls[i].ID == "" {
				calls[i].ID = fmt.Sprintf("call_%d", i)
			}
		}
		return calls
	}

	// No tool calls found
	return nil
}

// executeTool calls an MCP tool via the Gateway and returns the result.
func (e *Executor) executeTool(ctx context.Context, kitchen string, tc ToolCall) ToolResult {
	paramsJSON, _ := json.Marshal(models.MCPToolCallParams{
		Name:      tc.Name,
		Arguments: tc.Arguments,
	})

	mcpReq := &models.MCPRequest{
		Jsonrpc: "2.0",
		Method:  "tools/call",
		Params:  paramsJSON,
		ID:      tc.ID,
	}

	mcpResp := e.gateway.HandleJSONRPC(ctx, kitchen, mcpReq)

	if mcpResp.Error != nil {
		return ToolResult{
			ToolCallID: tc.ID,
			Name:       tc.Name,
			Content:    fmt.Sprintf("Error: %s", mcpResp.Error.Message),
			IsError:    true,
		}
	}

	// Extract text content from the result
	resultJSON, _ := json.Marshal(mcpResp.Result)
	var toolResult models.MCPToolResult
	if err := json.Unmarshal(resultJSON, &toolResult); err == nil {
		var text string
		for _, c := range toolResult.Content {
			if c.Type == "text" {
				text += c.Text
			}
		}
		return ToolResult{
			ToolCallID: tc.ID,
			Name:       tc.Name,
			Content:    text,
			IsError:    toolResult.IsError,
		}
	}

	// Fallback: return raw JSON
	return ToolResult{
		ToolCallID: tc.ID,
		Name:       tc.Name,
		Content:    string(resultJSON),
	}
}

// describeSchema creates a one-line description of a tool's JSON schema.
func describeSchema(schema map[string]interface{}) string {
	if schema == nil {
		return "(no parameters)"
	}

	desc, _ := schema["description"].(string)
	if desc != "" {
		return desc
	}

	// Summarize properties
	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		return "(no description)"
	}

	var parts []string
	for key := range props {
		parts = append(parts, key)
	}
	if len(parts) > 5 {
		return fmt.Sprintf("params: %v... (%d total)", parts[:5], len(parts))
	}
	return fmt.Sprintf("params: %v", parts)
}
