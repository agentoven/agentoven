// Package executor implements the agentic loop for managed-mode agents.
//
// When an agent's Mode is "managed", AgentOven runs the agent directly:
//
//	prompt template → render with variables → build messages →
//	call Model Router → if tool_calls, execute each via MCP Gateway →
//	feed results back → repeat until text response or max_turns hit.
//
// For agentic-behavior agents (Behavior="agentic"), the executor also
// manages a sliding context window with session persistence:
//
//	load session → build sliding context (system + summary + recent) →
//	run agentic loop → persist session with updated messages/tokens.
//
// External-mode agents are proxied to the developer's A2A endpoint instead.
package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/mcpgw"
	"github.com/agentoven/agentoven/control-plane/internal/resolver"
	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// DefaultMaxTurns is the maximum number of LLM ↔ tool loops.
const DefaultMaxTurns = 10

// DefaultContextBudget is the default max tokens for the sliding context window.
const DefaultContextBudget = 16000

// SummaryPrefix is prepended to the compressed summary message.
const SummaryPrefix = "[Summary of earlier conversation]\n"

// ToolCall represents a tool invocation requested by the LLM.
type ToolCall struct {
	ID        string                 `json:"id"`
	Name      string                 `json:"name"`
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
	TraceID   string            `json:"trace_id"`
	SessionID string            `json:"session_id,omitempty"`
	AgentName string            `json:"agent_name"`
	Kitchen   string            `json:"kitchen"`
	Turns     []Turn            `json:"turns"`
	TotalMs   int64             `json:"total_ms"`
	Usage     models.TokenUsage `json:"usage"`
}

// Turn is one iteration of the agentic loop.
type Turn struct {
	Number         int                    `json:"number"`
	Request        []models.ChatMessage   `json:"request"`
	Response       string                 `json:"response,omitempty"`
	ToolCalls      []ToolCall             `json:"tool_calls,omitempty"`
	ToolResults    []ToolResult           `json:"tool_results,omitempty"`
	ThinkingBlocks []models.ThinkingBlock `json:"thinking_blocks,omitempty"`
	LatencyMs      int64                  `json:"latency_ms"`
	Usage          models.TokenUsage      `json:"usage"`
}

// Executor runs managed-mode agents through an agentic tool-use loop.
type Executor struct {
	store    store.Store
	router   *router.ModelRouter
	gateway  *mcpgw.Gateway
	sessions contracts.SessionStore
}

// NewExecutor creates a new managed-agent executor.
func NewExecutor(s store.Store, r *router.ModelRouter, gw *mcpgw.Gateway, sess contracts.SessionStore) *Executor {
	return &Executor{
		store:    s,
		router:   r,
		gateway:  gw,
		sessions: sess,
	}
}

// Execute runs the agentic loop for a managed agent.
//
// Flow:
//  1. Load or create session (if sessionID provided and agent is agentic)
//  2. Render the prompt template with variables
//  3. Build messages — sliding context window for agentic agents, flat for reactive
//  4. Call Model Router with native tool definitions
//  5. If LLM returns tool_calls → execute each via MCP Gateway → add results → goto 4
//  6. If LLM returns text → return as final response
//  7. If max_turns reached → return with "max turns exceeded" warning
//  8. Persist session with updated messages and token counts
func (e *Executor) Execute(ctx context.Context, agent *models.Agent, userMessage string, resolved *models.ResolvedIngredients, promptVars map[string]string, thinkingEnabled bool, sessionID ...string) (string, *ExecutionTrace, error) {
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

	isAgentic := agent.Behavior == models.BehaviorAgentic

	// ── Session management (agentic agents) ─────────────────
	var session *models.Session
	if isAgentic && e.sessions != nil {
		var err error
		sid := ""
		if len(sessionID) > 0 && sessionID[0] != "" {
			sid = sessionID[0]
		}
		if sid != "" {
			session, err = e.sessions.GetSession(ctx, sid)
			if err != nil {
				log.Warn().Err(err).Str("session_id", sid).Msg("Session not found, creating new")
				session = nil
			}
		}
		if session == nil {
			session = &models.Session{
				ID:        uuid.New().String(),
				AgentName: agent.Name,
				Kitchen:   agent.Kitchen,
				Status:    models.SessionActive,
				Messages:  []models.ChatMessage{},
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			}
			if createErr := e.sessions.CreateSession(ctx, session); createErr != nil {
				log.Warn().Err(createErr).Msg("Failed to create session, proceeding without persistence")
				session = nil
			}
		}
		if session != nil {
			trace.SessionID = session.ID
		}
	}

	// Build tool definitions for native tool calling
	toolDefs := e.buildToolDefinitions(resolved.Tools)

	// Build initial messages — sliding context for agentic, flat for reactive
	var messages []models.ChatMessage
	if isAgentic && session != nil && len(session.Messages) > 0 {
		messages = e.buildSlidingContext(ctx, agent, resolved, session, userMessage, promptVars)
	} else {
		messages = e.buildInitialMessages(agent, resolved, userMessage, promptVars)
	}

	// Append current user message to session history
	if session != nil {
		session.Messages = append(session.Messages, models.ChatMessage{
			Role:    "user",
			Content: userMessage,
		})
	}

	var totalUsage models.TokenUsage
	var err error

	for turn := 1; turn <= maxTurns; turn++ {
		turnStart := time.Now()

		// Call Model Router with native tool definitions
		routeReq := &models.RouteRequest{
			Messages:        messages,
			Model:           resolved.Model.Model,
			Strategy:        models.RoutingFallback,
			Kitchen:         agent.Kitchen,
			AgentRef:        agent.Name,
			ThinkingEnabled: thinkingEnabled,
			Tools:           toolDefs,
			ToolChoice:      "auto",
		}
		if session != nil {
			routeReq.SessionID = session.ID
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

		// Prefer native tool calls from RouteResponse, fall back to text parsing
		toolCalls := e.extractToolCalls(routeResp)

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

			// Persist assistant response in session
			if session != nil {
				session.Messages = append(session.Messages, models.ChatMessage{
					Role:    "assistant",
					Content: routeResp.Content,
				})
				session.TurnCount++
				session.TotalTokens += routeResp.Usage.TotalTokens
				session.TotalCost += routeResp.Usage.EstimatedCost
				session.UpdatedAt = time.Now().UTC()
				if updateErr := e.sessions.UpdateSession(ctx, session); updateErr != nil {
					log.Error().Err(updateErr).Str("session", session.ID).Msg("Failed to persist session")
				}
			}

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

		// Add assistant message with tool call info to conversation
		assistantMsg := models.ChatMessage{
			Role:    "assistant",
			Content: routeResp.Content,
		}
		messages = append(messages, assistantMsg)

		// Add tool results as messages
		for _, tr := range toolResults {
			toolMsg := models.ChatMessage{
				Role:    "tool",
				Content: fmt.Sprintf("[Tool: %s] %s", tr.Name, tr.Content),
				Name:    tr.Name,
			}
			messages = append(messages, toolMsg)
		}

		// Persist tool exchange in session
		if session != nil {
			session.Messages = append(session.Messages, assistantMsg)
			for _, tr := range toolResults {
				session.Messages = append(session.Messages, models.ChatMessage{
					Role:    "tool",
					Content: fmt.Sprintf("[Tool: %s] %s", tr.Name, tr.Content),
					Name:    tr.Name,
				})
			}
			session.TotalTokens += routeResp.Usage.TotalTokens
			session.TotalCost += routeResp.Usage.EstimatedCost
		}

		log.Debug().
			Str("agent", agent.Name).
			Int("turn", turn).
			Int("tool_calls", len(toolCalls)).
			Msg("Agentic loop continuing")
	}

	// Max turns exceeded
	trace.TotalMs = time.Since(start).Milliseconds()
	trace.Usage = totalUsage

	// Persist session at max turns
	if session != nil {
		session.TurnCount += maxTurns
		session.UpdatedAt = time.Now().UTC()
		if updateErr := e.sessions.UpdateSession(ctx, session); updateErr != nil {
			log.Error().Err(updateErr).Str("session", session.ID).Msg("Failed to persist session at max turns")
		}
	}

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

// buildInitialMessages constructs the system prompt and user message for reactive agents.
func (e *Executor) buildInitialMessages(agent *models.Agent, resolved *models.ResolvedIngredients, userMessage string, promptVars map[string]string) []models.ChatMessage {
	messages := make([]models.ChatMessage, 0, 3)

	systemPrompt := e.buildSystemPrompt(agent, resolved, promptVars)
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

// buildSystemPrompt constructs the system prompt from agent config and tools.
func (e *Executor) buildSystemPrompt(agent *models.Agent, resolved *models.ResolvedIngredients, promptVars map[string]string) string {
	systemPrompt := ""
	if resolved.Prompt != nil {
		systemPrompt = resolver.RenderPrompt(resolved.Prompt.Template, promptVars)
	} else if agent.Description != "" {
		systemPrompt = agent.Description
	}

	// Add tool instructions to system prompt (fallback for models without native tool calling)
	if len(resolved.Tools) > 0 {
		toolList := "\n\nAvailable tools:\n"
		for _, t := range resolved.Tools {
			toolList += fmt.Sprintf("- %s: %s\n", t.Name, describeSchema(t.Schema))
		}
		toolList += "\nTo use a tool, respond with a JSON block: {\"tool_calls\": [{\"name\": \"tool_name\", \"arguments\": {...}}]}"
		systemPrompt += toolList
	}

	return systemPrompt
}

// ── Sliding Context Window ──────────────────────────────────

// buildSlidingContext constructs a context window for agentic agents with session history.
//
// The sliding context has three tiers:
//  1. System prompt (always included, never compressed)
//  2. Summary buffer — compressed summary of older messages
//  3. Recent window — most recent N messages kept verbatim
//
// When the total token count exceeds ContextBudget, the oldest non-system
// messages are summarized using the agent's SummaryModel (or the primary
// model as fallback), and replaced with a single summary message.
func (e *Executor) buildSlidingContext(ctx context.Context, agent *models.Agent, resolved *models.ResolvedIngredients, session *models.Session, userMessage string, promptVars map[string]string) []models.ChatMessage {
	budget := agent.ContextBudget
	if budget <= 0 {
		budget = DefaultContextBudget
	}

	// Build the system prompt
	systemPrompt := e.buildSystemPrompt(agent, resolved, promptVars)
	systemMsg := models.ChatMessage{Role: "system", Content: systemPrompt}

	// Start with system + all session history + new user message
	allMessages := make([]models.ChatMessage, 0, len(session.Messages)+2)
	allMessages = append(allMessages, systemMsg)
	allMessages = append(allMessages, session.Messages...)
	allMessages = append(allMessages, models.ChatMessage{Role: "user", Content: userMessage})

	// Estimate total tokens
	totalTokens := estimateTokensForMessages(allMessages)

	if totalTokens <= budget {
		// Under budget — use everything as-is
		return allMessages
	}

	// Over budget — compress older messages into a summary
	systemTokens := estimateTokens(systemMsg.Content)
	userTokens := estimateTokens(userMessage)
	reservedTokens := systemTokens + userTokens + 500 // 500 tokens buffer for summary overhead

	// Find how many recent messages we can keep within budget
	availableForRecent := budget - reservedTokens
	recentMessages := make([]models.ChatMessage, 0)
	recentTokens := 0

	// Walk backwards through session.Messages to keep recent ones
	for i := len(session.Messages) - 1; i >= 0; i-- {
		msgTokens := estimateTokens(session.Messages[i].Content)
		if recentTokens+msgTokens > availableForRecent {
			break
		}
		recentMessages = append([]models.ChatMessage{session.Messages[i]}, recentMessages...)
		recentTokens += msgTokens
	}

	// Messages to summarize = session history that didn't make it into recent
	numRecent := len(recentMessages)
	numToSummarize := len(session.Messages) - numRecent
	if numToSummarize <= 0 {
		// Everything fits in recent — shouldn't happen but handle gracefully
		return allMessages
	}

	toSummarize := session.Messages[:numToSummarize]

	// Generate summary via Model Router
	summary := e.summarizeMessages(ctx, agent, toSummarize)

	// Build final context: [system] + [summary] + [recent...] + [user]
	result := make([]models.ChatMessage, 0, len(recentMessages)+3)
	result = append(result, systemMsg)
	if summary != "" {
		result = append(result, models.ChatMessage{
			Role:    "system",
			Content: SummaryPrefix + summary,
		})
	}
	result = append(result, recentMessages...)
	result = append(result, models.ChatMessage{Role: "user", Content: userMessage})

	log.Debug().
		Str("agent", agent.Name).
		Int("session_msgs", len(session.Messages)).
		Int("summarized", numToSummarize).
		Int("recent_kept", numRecent).
		Int("budget", budget).
		Int("total_estimated", totalTokens).
		Msg("Sliding context built")

	return result
}

// summarizeMessages compresses a slice of messages into a concise summary
// using the agent's SummaryModel (or the primary model as fallback).
func (e *Executor) summarizeMessages(ctx context.Context, agent *models.Agent, messages []models.ChatMessage) string {
	if len(messages) == 0 {
		return ""
	}

	// Build the conversation text to summarize
	var sb strings.Builder
	for _, msg := range messages {
		sb.WriteString(fmt.Sprintf("[%s]: %s\n", msg.Role, msg.Content))
	}

	summaryPrompt := fmt.Sprintf(
		"Summarize the following conversation concisely, preserving key facts, decisions, "+
			"and context needed for continuation. Keep it under 500 words.\n\n%s", sb.String())

	model := agent.SummaryModel
	if model == "" {
		model = agent.ModelName
	}
	if model == "" {
		// Last resort: just truncate
		text := sb.String()
		if len(text) > 2000 {
			return text[:2000] + "..."
		}
		return text
	}

	routeReq := &models.RouteRequest{
		Messages: []models.ChatMessage{
			{Role: "user", Content: summaryPrompt},
		},
		Model:    model,
		Kitchen:  agent.Kitchen,
		Strategy: models.RoutingFallback,
		AgentRef: agent.Name + "_summarizer",
	}

	resp, err := e.router.Route(ctx, routeReq)
	if err != nil {
		log.Warn().Err(err).Str("agent", agent.Name).Msg("Summary generation failed, using truncation fallback")
		text := sb.String()
		if len(text) > 2000 {
			return text[:2000] + "..."
		}
		return text
	}

	return resp.Content
}

// estimateTokens returns a rough token count for a string.
// Uses the ~4 chars per token heuristic. Replace with tiktoken-go
// for production accuracy.
func estimateTokens(text string) int {
	return len(text) / 4
}

// estimateTokensForMessages returns the total estimated tokens across all messages.
func estimateTokensForMessages(messages []models.ChatMessage) int {
	total := 0
	for _, msg := range messages {
		total += estimateTokens(msg.Content) + 4 // 4 tokens overhead per message (role, delimiters)
	}
	return total
}

// ── Native Tool Calling ─────────────────────────────────────

// extractToolCalls extracts tool calls from the RouteResponse.
// Prefers native structured tool calls (RouteResponse.ToolCalls) when available,
// falls back to text-based JSON parsing for models that embed tool calls in content.
func (e *Executor) extractToolCalls(resp *models.RouteResponse) []ToolCall {
	// Prefer native tool calls from the model response
	if len(resp.ToolCalls) > 0 {
		calls := make([]ToolCall, 0, len(resp.ToolCalls))
		for _, tc := range resp.ToolCalls {
			var args map[string]interface{}
			if tc.Function.Arguments != "" {
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
					log.Warn().Err(err).Str("tool", tc.Function.Name).Msg("Failed to parse native tool call arguments")
					args = map[string]interface{}{"raw": tc.Function.Arguments}
				}
			}
			id := tc.ID
			if id == "" {
				id = fmt.Sprintf("call_%s", uuid.New().String()[:8])
			}
			calls = append(calls, ToolCall{
				ID:        id,
				Name:      tc.Function.Name,
				Arguments: args,
			})
		}
		return calls
	}

	// Check finish_reason hint
	if resp.FinishReason == "tool_calls" {
		// Model signaled tool calls but they weren't in ToolCalls field —
		// try to parse from content
		return e.parseToolCalls(resp.Content)
	}

	// Fall back to text-based tool call parsing
	return e.parseToolCalls(resp.Content)
}

// buildToolDefinitions creates native tool definitions for the LLM.
func (e *Executor) buildToolDefinitions(tools []models.ResolvedTool) []models.ToolDefinition {
	defs := make([]models.ToolDefinition, 0, len(tools)+1)

	// Add real MCP tools
	for _, t := range tools {
		desc, _ := t.Schema["description"].(string)
		def := models.ToolDefinition{
			Type: "function",
			Function: models.ToolFunction{
				Name:        t.Name,
				Description: desc,
				Parameters:  t.Schema,
			},
		}
		defs = append(defs, def)
	}

	// Add agentoven.delegate virtual tool for agent-to-agent delegation
	defs = append(defs, models.ToolDefinition{
		Type: "function",
		Function: models.ToolFunction{
			Name:        "agentoven.delegate",
			Description: "Delegate a subtask to another agent in the same kitchen. Use this when a task is better handled by a specialized agent.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"agent": map[string]interface{}{
						"type":        "string",
						"description": "Name of the agent to delegate to",
					},
					"message": map[string]interface{}{
						"type":        "string",
						"description": "The task or question to send to the delegate agent",
					},
				},
				"required": []interface{}{"agent", "message"},
			},
		},
	})

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
// Intercepts the virtual "agentoven.delegate" tool for agent-to-agent delegation.
func (e *Executor) executeTool(ctx context.Context, kitchen string, tc ToolCall) ToolResult {
	// Handle virtual delegation tool
	if tc.Name == "agentoven.delegate" {
		return e.executeDelegation(ctx, kitchen, tc)
	}

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

// executeDelegation handles the agentoven.delegate virtual tool call.
// It invokes another agent in the same kitchen and returns its response.
func (e *Executor) executeDelegation(ctx context.Context, kitchen string, tc ToolCall) ToolResult {
	targetAgent, _ := tc.Arguments["agent"].(string)
	message, _ := tc.Arguments["message"].(string)

	if targetAgent == "" || message == "" {
		return ToolResult{
			ToolCallID: tc.ID,
			Name:       tc.Name,
			Content:    "Error: agentoven.delegate requires 'agent' and 'message' arguments",
			IsError:    true,
		}
	}

	// Look up the target agent
	agent, err := e.store.GetAgent(ctx, kitchen, targetAgent)
	if err != nil {
		return ToolResult{
			ToolCallID: tc.ID,
			Name:       tc.Name,
			Content:    fmt.Sprintf("Error: agent '%s' not found in kitchen '%s'", targetAgent, kitchen),
			IsError:    true,
		}
	}

	if agent.Status != models.AgentStatusReady {
		return ToolResult{
			ToolCallID: tc.ID,
			Name:       tc.Name,
			Content:    fmt.Sprintf("Error: agent '%s' is not ready (status: %s)", targetAgent, agent.Status),
			IsError:    true,
		}
	}

	// For managed agents, resolve and execute directly
	if agent.Mode == models.AgentModeManaged {
		resolved := agent.ResolvedConfig
		if resolved == nil {
			return ToolResult{
				ToolCallID: tc.ID,
				Name:       tc.Name,
				Content:    fmt.Sprintf("Error: agent '%s' has no resolved config — bake it first", targetAgent),
				IsError:    true,
			}
		}

		response, _, delegateErr := e.Execute(ctx, agent, message, resolved, nil, false)
		if delegateErr != nil {
			return ToolResult{
				ToolCallID: tc.ID,
				Name:       tc.Name,
				Content:    fmt.Sprintf("Error delegating to '%s': %s", targetAgent, delegateErr.Error()),
				IsError:    true,
			}
		}

		log.Info().
			Str("from_kitchen", kitchen).
			Str("to_agent", targetAgent).
			Msg("Agent delegation completed")

		return ToolResult{
			ToolCallID: tc.ID,
			Name:       tc.Name,
			Content:    response,
		}
	}

	// For external agents, we'd relay via A2A — for now return an error
	return ToolResult{
		ToolCallID: tc.ID,
		Name:       tc.Name,
		Content:    fmt.Sprintf("Delegation to external agent '%s' not yet supported — use managed-mode agents", targetAgent),
		IsError:    true,
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
