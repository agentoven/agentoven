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
	"slices"
	"strings"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/ctxwindow"
	grails "github.com/agentoven/agentoven/control-plane/internal/guardrails"
	"github.com/agentoven/agentoven/control-plane/internal/resolver"
	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"go.opentelemetry.io/otel"
)

// DefaultMaxTurns is the maximum number of LLM ↔ tool loops.
const DefaultMaxTurns = 10

// DefaultContextBudget is the default max tokens for the sliding context window.
const DefaultContextBudget = 16000

// SummaryPrefix is prepended to the compressed summary message.
const SummaryPrefix = "[Summary of earlier conversation]\n"

// tracer is the OpenTelemetry tracer for the executor package.
// Spans emitted here appear as children of the outer HTTP span in Jaeger / Tempo.
var tracer = otel.Tracer("agentoven/executor")

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
	// Pending is set when the run stopped for human approval of a tool call —
	// see ErrPausedForApproval. Absent on every other trace.
	Pending *PendingApproval `json:"pending,omitempty"`
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

// ToolGateway is the executor's view of the MCP gateway: it dispatches a tool
// call and returns the result.
//
// Narrow on purpose. Anything that can answer a tools/call can stand in, which
// is what lets a scenario episode route the agent's tool calls into its own
// isolated world instead of the real gateway — without the executor knowing a
// scenario exists. *mcpgw.Gateway satisfies it, so ordinary wiring is unchanged.
type ToolGateway interface {
	HandleJSONRPC(ctx context.Context, kitchen string, req *models.MCPRequest) *models.MCPResponse
}

// Executor runs managed-mode agents through an agentic tool-use loop.
type Executor struct {
	store       store.Store
	router      *router.ModelRouter
	gateway     ToolGateway
	sessions    contracts.SessionStore
	ragRegistry RAGRegistry // optional: enables retriever ingredient consumption

	// journal, when set, makes runs durable — see Journal, SetJournal, Resume.
	// nil by default: journaling is opt-in, so an embedder that has no use
	// for resumability pays nothing for it.
	journal Journal
	// toolTimeout bounds one tool call; DefaultToolTimeout when zero.
	toolTimeout time.Duration

	// guardrails, when set, is evaluated around every tool call, not just the
	// outer user message/final response the HTTP handlers already gate — see
	// SetGuardrails and dispatchToolCall.
	guardrails contracts.GuardrailService
	// guardrailPolicy, when set, adds the kitchen's workspace guardrails to the
	// agent's own for those tool-call checks. See SetGuardrailPolicy.
	guardrailPolicy contracts.WorkspaceGuardrailSource
}

// RAGRegistry provides access to registered RAG services.
// Defined as interface to avoid circular import with internal/rag package.
type RAGRegistry interface {
	Get(name string) (contracts.RAGService, error)
	Default() (contracts.RAGService, error)
}

// NewExecutor creates a new managed-agent executor.
func NewExecutor(s store.Store, r *router.ModelRouter, gw ToolGateway, sess contracts.SessionStore) *Executor {
	return &Executor{
		store:       s,
		router:      r,
		gateway:     gw,
		sessions:    sess,
		toolTimeout: DefaultToolTimeout,
	}
}

// SetRAGRegistry sets the RAG service registry for retriever ingredient consumption.
// Called after server initialization to avoid circular dependencies.
func (e *Executor) SetRAGRegistry(reg RAGRegistry) {
	e.ragRegistry = reg
}

// SetJournal makes every run through this Executor durable: each turn is
// checkpointed, so an interrupted run can be continued with Resume instead of
// starting over. Pass nil to disable journaling again.
func (e *Executor) SetJournal(j Journal) {
	e.journal = j
}

// SetToolTimeout overrides DefaultToolTimeout for every tool call this
// Executor dispatches.
func (e *Executor) SetToolTimeout(d time.Duration) {
	e.toolTimeout = d
}

// SetGuardrails makes every tool call an agent with Guardrails configured
// dispatches also pass through them — not just the whole-request input/output
// gating the HTTP handlers already do around the outer user message and final
// response. Pass nil to disable (the default): an Executor with no guardrail
// service configured skips the check entirely rather than failing closed.
func (e *Executor) SetGuardrails(g contracts.GuardrailService) {
	e.guardrails = g
}

// SetGuardrailPolicy makes the kitchen's workspace guardrails apply to tool calls
// too, combined with the agent's own (guardrails.Effective). nil: only the agent's own.
func (e *Executor) SetGuardrailPolicy(src contracts.WorkspaceGuardrailSource) {
	e.guardrailPolicy = src
}

// guardrailsFor returns the guardrails to enforce for agent: its own with the kitchen's
// workspace guardrails applied on top. It fails closed: an error means the workspace
// guardrails could not be established and the agent must not run (see guardrails.Effective).
func (e *Executor) guardrailsFor(ctx context.Context, agent *models.Agent) ([]models.Guardrail, error) {
	return grails.Effective(ctx, e.guardrailPolicy, agent.Kitchen, agent.Name, agent.Guardrails)
}

// requireGuardrailPolicy refuses to start a run when the workspace guardrails that govern
// the agent cannot be established. It is a no-op when no workspace source is configured.
func (e *Executor) requireGuardrailPolicy(ctx context.Context, agent *models.Agent) error {
	if e.guardrailPolicy == nil {
		return nil
	}
	if _, err := e.guardrailsFor(ctx, agent); err != nil {
		log.Error().Err(err).Str("agent", agent.Name).Msg("refusing to run: workspace guardrails could not be applied")
		return fmt.Errorf("executor: agent not run, its guardrail policy could not be applied: %w", err)
	}
	return nil
}

// buildInitialMessages constructs the system prompt and user message for reactive agents.
func (e *Executor) buildInitialMessages(ctx context.Context, agent *models.Agent, resolved *models.ResolvedIngredients, userMsg models.ChatMessage, promptVars map[string]string) []models.ChatMessage {
	messages := make([]models.ChatMessage, 0, 3)

	systemPrompt := e.buildSystemPrompt(ctx, agent, resolved, userMsg.Content, promptVars)
	if systemPrompt != "" {
		messages = append(messages, models.ChatMessage{
			Role:    "system",
			Content: systemPrompt,
		})
	}

	messages = append(messages, userMsg)

	return messages
}

// buildSystemPrompt constructs the system prompt from agent config, tools, and RAG context.
func (e *Executor) buildSystemPrompt(ctx context.Context, agent *models.Agent, resolved *models.ResolvedIngredients, userMessage string, promptVars map[string]string) string {
	systemPrompt := ""
	if resolved.Prompt != nil {
		systemPrompt = resolver.RenderPrompt(resolved.Prompt.Template, promptVars)
	} else if agent.Description != "" {
		systemPrompt = agent.Description
	}

	// ── RAG retrieval injection ──────────────────────────────
	// If the agent has retriever ingredients and a RAG registry is available,
	// perform retrieval and inject context into the system prompt.
	if len(resolved.Retrievers) > 0 && e.ragRegistry != nil && userMessage != "" {
		ragContext := e.retrieveContext(ctx, agent.Kitchen, resolved.Retrievers, userMessage)
		if ragContext != "" {
			systemPrompt += "\n\n" + ragContext
		}
	}

	// ── Skills ─────────────────────────────────────────────────
	// A skill's SKILL.md instructions are the one piece of an Agent Skills
	// bundle that only ever reaches the model as text, never as a tool
	// definition — this is "activation" in the format's progressive
	// disclosure model (discovery already happened at bake time, when the
	// skill was attached as an ingredient at all).
	if len(resolved.Skills) > 0 {
		var sb strings.Builder
		sb.WriteString("\n\n## Skills\n")
		for _, sk := range resolved.Skills {
			fmt.Fprintf(&sb, "\n### %s\n%s\n", sk.Name, sk.Instructions)
		}
		systemPrompt += sb.String()
	}

	// Add tool instructions to system prompt (fallback for models without native tool calling)
	allTools := allResolvedTools(resolved)
	if len(allTools) > 0 {
		toolList := "\n\nAvailable tools:\n"
		for _, t := range allTools {
			toolList += fmt.Sprintf("- %s: %s\n", t.Name, describeSchema(t.Schema))
		}
		toolList += "\nTo use a tool, respond with a JSON block: {\"tool_calls\": [{\"name\": \"tool_name\", \"arguments\": {...}}]}"
		systemPrompt += toolList
	}

	return systemPrompt
}

// allResolvedTools flattens an agent's own tools together with every
// attached skill's bundled tools into the one list buildToolDefinitions and
// the system prompt's tool listing both need — a skill's tools are
// dispatched through the exact same gateway path a plain Tool ingredient's
// are, so nothing downstream needs to know the difference.
func allResolvedTools(resolved *models.ResolvedIngredients) []models.ResolvedTool {
	if len(resolved.Skills) == 0 {
		return resolved.Tools
	}
	all := make([]models.ResolvedTool, 0, len(resolved.Tools))
	all = append(all, resolved.Tools...)
	for _, sk := range resolved.Skills {
		all = append(all, sk.Tools...)
	}
	return all
}

// retrieveContext performs RAG retrieval using resolved retriever ingredients
// and returns formatted context text for injection into the system prompt.
func (e *Executor) retrieveContext(ctx context.Context, kitchen string, retrievers []models.ResolvedRetriever, userMessage string) string {
	var contextParts []string

	for _, ret := range retrievers {
		providerName := ret.Provider
		if providerName == "" {
			providerName = "built-in"
		}

		svc, err := e.ragRegistry.Get(providerName)
		if err != nil {
			// Try default provider
			svc, err = e.ragRegistry.Default()
			if err != nil {
				log.Warn().
					Str("provider", providerName).
					Msg("RAG provider not found for retriever, skipping")
				continue
			}
		}

		strategy := ret.Strategy
		if strategy == "" {
			strategy = models.RAGNaive
		}

		req := models.RAGQueryRequest{
			Kitchen:   kitchen,
			Question:  userMessage,
			Strategy:  strategy,
			TopK:      ret.TopK,
			MinScore:  ret.ScoreThreshold,
			Namespace: ret.Namespace,
		}

		result, err := svc.Query(ctx, kitchen, req)
		if err != nil {
			log.Warn().Err(err).
				Str("provider", providerName).
				Msg("RAG retrieval failed for retriever, skipping")
			continue
		}

		if len(result.Sources) == 0 {
			continue
		}

		// Format retrieved sources as context
		var sb strings.Builder
		sb.WriteString("## Retrieved Knowledge\n\n")
		sb.WriteString("Use the following retrieved information to help answer the user's question:\n\n")
		for i, s := range result.Sources {
			sb.WriteString(fmt.Sprintf("### Source %d", i+1))
			if src, ok := s.Doc.Metadata["source"]; ok {
				sb.WriteString(fmt.Sprintf(" (%s)", src))
			}
			sb.WriteString(fmt.Sprintf(" [score: %.3f]\n", s.Score))
			sb.WriteString(s.Doc.Content)
			sb.WriteString("\n\n")
		}

		contextParts = append(contextParts, sb.String())
	}

	return strings.Join(contextParts, "\n")
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
func (e *Executor) buildSlidingContext(ctx context.Context, agent *models.Agent, resolved *models.ResolvedIngredients, session *models.Session, userMsg models.ChatMessage, promptVars map[string]string) []models.ChatMessage {
	budget := ctxwindow.EffectiveBudget(agent.ContextBudget, 0)

	// Build the system prompt (includes RAG retrieval for retriever ingredients)
	systemPrompt := e.buildSystemPrompt(ctx, agent, resolved, userMsg.Content, promptVars)
	systemMsg := models.ChatMessage{Role: "system", Content: systemPrompt}

	// Start with system + all session history + new user message
	allMessages := make([]models.ChatMessage, 0, len(session.Messages)+2)
	allMessages = append(allMessages, systemMsg)
	allMessages = append(allMessages, session.Messages...)
	allMessages = append(allMessages, userMsg)

	// Estimate total tokens
	totalTokens := ctxwindow.EstimateTokensForMessages(allMessages)

	if totalTokens <= budget {
		// Under budget — use everything as-is
		return allMessages
	}

	// Over budget — compress older messages into a summary
	systemTokens := ctxwindow.EstimateTokens(systemMsg.Content)
	userTokens := ctxwindow.EstimateTokens(userMsg.Content)
	reservedTokens := systemTokens + userTokens + 500 // 500 tokens buffer for summary overhead

	// Find how many recent messages we can keep within budget
	availableForRecent := budget - reservedTokens
	recentMessages := make([]models.ChatMessage, 0)
	recentTokens := 0

	// Walk backwards through session.Messages to keep recent ones
	for i := len(session.Messages) - 1; i >= 0; i-- {
		msgTokens := ctxwindow.EstimateTokens(session.Messages[i].Content)
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
	result = append(result, userMsg)

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

	// Suppress router trace — summary calls are internal, not user-facing.
	resp, err := e.router.Route(router.ContextSkipTrace(ctx), routeReq)
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
// Delegates to the shared ctxwindow package.
func estimateTokens(text string) int {
	return ctxwindow.EstimateTokens(text)
}

// estimateTokensForMessages returns the total estimated tokens across all messages.
// Delegates to the shared ctxwindow package.
func estimateTokensForMessages(messages []models.ChatMessage) int {
	return ctxwindow.EstimateTokensForMessages(messages)
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
func (e *Executor) buildToolDefinitions(agent *models.Agent, tools []models.ResolvedTool) []models.ToolDefinition {
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

	// agentoven_delegate is offered in two cases: the original orchestrator
	// pattern (an agent with no tools of its own — adding delegate alongside
	// real tools otherwise causes the LLM to prefer delegation over direct
	// execution), or when Subagents explicitly names who this agent may hand
	// work to, which stays useful even for an agent that also has real tools.
	agentProp := map[string]interface{}{
		"type":        "string",
		"description": "Name of the agent to delegate to",
	}
	if len(agent.Subagents) > 0 {
		agentProp["enum"] = toInterfaceSlice(agent.Subagents)
	}
	if len(tools) == 0 || len(agent.Subagents) > 0 {
		defs = append(defs, models.ToolDefinition{
			Type: "function",
			Function: models.ToolFunction{
				Name:        "agentoven_delegate",
				Description: "Delegate a subtask to another agent in the same kitchen. Use this when a task is better handled by a specialized agent.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"agent": agentProp,
						"message": map[string]interface{}{
							"type":        "string",
							"description": "The task or question to send to the delegate agent",
						},
					},
					"required": []interface{}{"agent", "message"},
				},
			},
		})
	}

	return defs
}

func toInterfaceSlice(s []string) []interface{} {
	out := make([]interface{}, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
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
// Intercepts the virtual "agentoven_delegate" tool for agent-to-agent delegation.
func (e *Executor) executeTool(ctx context.Context, agent *models.Agent, tc ToolCall) ToolResult {
	// Handle virtual delegation tool
	if tc.Name == "agentoven_delegate" {
		return e.executeDelegation(ctx, agent, tc)
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

	mcpResp := e.gateway.HandleJSONRPC(ctx, agent.Kitchen, mcpReq)

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

// MaxDelegationDepth bounds how many agentoven_delegate hops a single
// invocation can chain through. It is the primary safety net against runaway
// recursion — a cycle (A delegates to B delegates back to A) is also detected
// explicitly below for a clearer error, but the depth cap alone is what
// guarantees termination even for a long chain that never repeats an agent.
const MaxDelegationDepth = 5

type delegationChainKey struct{}

// delegationChain returns the agent names already in this call's delegation
// stack, oldest first — empty for a top-level invocation that has never
// delegated.
func delegationChain(ctx context.Context) []string {
	chain, _ := ctx.Value(delegationChainKey{}).([]string)
	return chain
}

// ensureDelegationRoot seeds the delegation chain with the top-level agent's
// name the first time a run starts (Execute/ExecuteStream/Resume), so the
// very first delegate call already has one entry — the root — to detect a
// direct A-delegates-to-A cycle against. It is a no-op on a context that
// already carries a chain, which is exactly the case for a recursive call
// executeDelegation makes into Execute for its target.
func ensureDelegationRoot(ctx context.Context, rootAgentName string) context.Context {
	if _, ok := ctx.Value(delegationChainKey{}).([]string); ok {
		return ctx
	}
	return context.WithValue(ctx, delegationChainKey{}, []string{rootAgentName})
}

// executeDelegation handles the agentoven_delegate virtual tool call.
// It invokes another agent in the same kitchen and returns its response.
func (e *Executor) executeDelegation(ctx context.Context, agent *models.Agent, tc ToolCall) ToolResult {
	targetAgent, _ := tc.Arguments["agent"].(string)
	message, _ := tc.Arguments["message"].(string)
	kitchen := agent.Kitchen

	if targetAgent == "" || message == "" {
		return ToolResult{
			ToolCallID: tc.ID,
			Name:       tc.Name,
			Content:    "Error: agentoven_delegate requires 'agent' and 'message' arguments",
			IsError:    true,
		}
	}

	// An explicit Subagents allowlist is enforced server-side, not just shown
	// to the model as a schema enum — the model's output is untrusted input.
	if len(agent.Subagents) > 0 && !slices.Contains(agent.Subagents, targetAgent) {
		return ToolResult{
			ToolCallID: tc.ID,
			Name:       tc.Name,
			Content:    fmt.Sprintf("Error: '%s' is not in this agent's allowed subagents (%s)", targetAgent, strings.Join(agent.Subagents, ", ")),
			IsError:    true,
		}
	}

	chain := delegationChain(ctx)
	if len(chain) >= MaxDelegationDepth {
		return ToolResult{
			ToolCallID: tc.ID,
			Name:       tc.Name,
			Content:    fmt.Sprintf("Error: delegation depth exceeded (max %d): %s -> %s", MaxDelegationDepth, strings.Join(chain, " -> "), targetAgent),
			IsError:    true,
		}
	}
	if slices.Contains(chain, targetAgent) {
		return ToolResult{
			ToolCallID: tc.ID,
			Name:       tc.Name,
			Content:    fmt.Sprintf("Error: delegation cycle detected: %s -> %s", strings.Join(chain, " -> "), targetAgent),
			IsError:    true,
		}
	}

	// Look up the target agent
	target, err := e.store.GetAgent(ctx, kitchen, targetAgent)
	if err != nil {
		return ToolResult{
			ToolCallID: tc.ID,
			Name:       tc.Name,
			Content:    fmt.Sprintf("Error: agent '%s' not found in kitchen '%s'", targetAgent, kitchen),
			IsError:    true,
		}
	}

	if target.Status != models.AgentStatusReady {
		return ToolResult{
			ToolCallID: tc.ID,
			Name:       tc.Name,
			Content:    fmt.Sprintf("Error: agent '%s' is not ready (status: %s)", targetAgent, target.Status),
			IsError:    true,
		}
	}

	// For managed agents, resolve and execute directly
	if target.Mode == models.AgentModeManaged {
		resolved := target.ResolvedConfig
		if resolved == nil {
			return ToolResult{
				ToolCallID: tc.ID,
				Name:       tc.Name,
				Content:    fmt.Sprintf("Error: agent '%s' has no resolved config — bake it first", targetAgent),
				IsError:    true,
			}
		}

		delegateCtx := context.WithValue(ctx, delegationChainKey{}, append(append([]string{}, chain...), targetAgent))
		response, _, delegateErr := e.Execute(delegateCtx, target, message, resolved, nil, false)
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
