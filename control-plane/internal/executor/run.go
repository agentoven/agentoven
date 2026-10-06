package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/router"
	inttelemetry "github.com/agentoven/agentoven/control-plane/internal/telemetry"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// runState is everything the shared loop core needs, however the run came to
// exist: built fresh by Execute/ExecuteStream, or reconstructed from a
// journal by Resume. Separating "how this run came to be" from "what happens
// each turn" is what lets all three entry points share one implementation of
// the turn loop instead of three copies of it drifting apart.
type runState struct {
	traceID    string
	trace      *ExecutionTrace
	agent      *models.Agent
	resolved   *models.ResolvedIngredients
	toolDefs   []models.ToolDefinition
	toolSchema map[string]map[string]interface{} // tool name → JSON Schema, for arg validation
	messages   []models.ChatMessage
	session    *models.Session
	startTurn  int // 1 for a fresh run, completedTurns+1 for a resumed one
	totalUsage models.TokenUsage
	start      time.Time
}

// loopOpts configures one pass through the shared loop core. Both fields are
// optional: nil onEvent means no streaming (the non-streaming Execute path),
// nil journal means no durability (an Executor with none configured).
type loopOpts struct {
	onEvent *eventSink
	journal Journal
}

// newRunState builds a fresh run: a new trace ID, session handling identical
// to the original single-function Execute, and initial messages for either a
// reactive (flat) or agentic (sliding-window) agent.
func (e *Executor) newRunState(ctx context.Context, agent *models.Agent, userMsg models.ChatMessage, resolved *models.ResolvedIngredients, promptVars map[string]string, sessionID ...string) *runState {
	traceID := uuid.New().String()
	st := &runState{
		traceID: traceID,
		trace: &ExecutionTrace{
			TraceID:   traceID,
			AgentName: agent.Name,
			Kitchen:   agent.Kitchen,
		},
		agent:     agent,
		resolved:  resolved,
		toolDefs:  e.buildToolDefinitions(agent, allResolvedTools(resolved)),
		startTurn: 1,
		start:     time.Now(),
	}
	st.toolSchema = make(map[string]map[string]interface{}, len(resolved.Tools))
	for _, t := range allResolvedTools(resolved) {
		st.toolSchema[t.Name] = t.Schema
	}

	// Session continuity is not limited to agentic behavior: a reactive agent
	// runs no autonomous tool loop, but still deserves to remember a
	// conversation across separate calls when a caller supplies a sessionID.
	// buildSlidingContext (used below regardless of Behavior) degrades to
	// "just the full history" when it fits under budget, and only compresses
	// once a session genuinely needs it — not an agentic-exclusive concern.
	var session *models.Session
	if e.sessions != nil {
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
			st.trace.SessionID = session.ID
		}
	}
	st.session = session

	if session != nil && len(session.Messages) > 0 {
		st.messages = e.buildSlidingContext(ctx, agent, resolved, session, userMsg, promptVars)
	} else {
		st.messages = e.buildInitialMessages(ctx, agent, resolved, userMsg, promptVars)
	}
	if session != nil {
		session.Messages = append(session.Messages, userMsg)
	}
	return st
}

// outputResponseFormat builds the ResponseFormat that gives agent.OutputSchema
// to the model, in the shape router.go's OpenAI/Ollama drivers pass straight
// through and its Anthropic driver unwraps for its forced-tool path.
func outputResponseFormat(agent *models.Agent) *models.ResponseFormat {
	if agent.OutputSchema == nil {
		return nil
	}
	return &models.ResponseFormat{
		Type: "json_schema",
		JSONSchema: map[string]interface{}{
			"name":   "output",
			"strict": true,
			"schema": agent.OutputSchema,
		},
	}
}

// Execute runs the agentic loop for a managed agent to completion.
//
// Flow:
//  1. Load or create session (if sessionID provided and agent is agentic)
//  2. Render the prompt template with variables
//  3. Build messages — sliding context window for agentic agents, flat for reactive
//  4. Call Model Router with native tool definitions
//  5. If LLM returns tool_calls → validate args → execute each (in parallel) → add results → goto 4
//  6. If LLM returns text → return as final response
//  7. If max_turns reached → return with "max turns exceeded" warning
//  8. Persist session with updated messages and token counts
//
// A tool named in agent.ApprovalTools pauses the loop instead of running:
// Execute returns ErrPausedForApproval and trace.Turns' last entry has no
// result for it yet. Resume continues from there once a human has decided.
func (e *Executor) Execute(ctx context.Context, agent *models.Agent, userMessage string, resolved *models.ResolvedIngredients, promptVars map[string]string, thinkingEnabled bool, sessionID ...string) (string, *ExecutionTrace, error) {
	return e.ExecuteMessage(ctx, agent, textMessage(userMessage), resolved, promptVars, thinkingEnabled, sessionID...)
}

// ExecuteMessage is Execute for a user turn that may carry media: images,
// PDFs, or audio as ContentParts alongside its text.
func (e *Executor) ExecuteMessage(ctx context.Context, agent *models.Agent, userMsg models.ChatMessage, resolved *models.ResolvedIngredients, promptVars map[string]string, thinkingEnabled bool, sessionID ...string) (string, *ExecutionTrace, error) {
	ctx = ensureDelegationRoot(ctx, agent.Name)
	userMessage := userMsg.Content
	st := e.newRunState(ctx, agent, userMsg, resolved, promptVars, sessionID...)
	if e.journal != nil {
		if err := e.journal.Start(ctx, JournalHeader{
			TraceID: st.traceID, Kitchen: agent.Kitchen, Agent: agent, Resolved: resolved,
			UserMessage: userMessage, PromptVars: promptVars, ThinkingEnabled: thinkingEnabled,
			SessionID: st.trace.SessionID, StartedAt: st.start,
		}); err != nil {
			log.Warn().Err(err).Str("trace_id", st.traceID).Msg("Failed to start journal for run; continuing without durability for this run")
		}
	}
	return e.runLoop(ctx, st, thinkingEnabled, loopOpts{journal: e.journal})
}

// ExecuteStream is Execute, plus an event for every token, tool call and turn
// boundary as the loop runs — the primitive a caller streams to a user over
// SSE/websocket from, instead of waiting for the whole run to finish.
func (e *Executor) ExecuteStream(ctx context.Context, agent *models.Agent, userMessage string, resolved *models.ResolvedIngredients, promptVars map[string]string, thinkingEnabled bool, onEvent func(Event) error, sessionID ...string) (string, *ExecutionTrace, error) {
	return e.ExecuteStreamMessage(ctx, agent, textMessage(userMessage), resolved, promptVars, thinkingEnabled, onEvent, sessionID...)
}

// ExecuteStreamMessage is ExecuteStream for a user turn that may carry media.
func (e *Executor) ExecuteStreamMessage(ctx context.Context, agent *models.Agent, userMsg models.ChatMessage, resolved *models.ResolvedIngredients, promptVars map[string]string, thinkingEnabled bool, onEvent func(Event) error, sessionID ...string) (string, *ExecutionTrace, error) {
	ctx = ensureDelegationRoot(ctx, agent.Name)
	userMessage := userMsg.Content
	st := e.newRunState(ctx, agent, userMsg, resolved, promptVars, sessionID...)
	if e.journal != nil {
		if err := e.journal.Start(ctx, JournalHeader{
			TraceID: st.traceID, Kitchen: agent.Kitchen, Agent: agent, Resolved: resolved,
			UserMessage: userMessage, PromptVars: promptVars, ThinkingEnabled: thinkingEnabled,
			SessionID: st.trace.SessionID, StartedAt: st.start,
		}); err != nil {
			log.Warn().Err(err).Str("trace_id", st.traceID).Msg("Failed to start journal for run; continuing without durability for this run")
		}
	}
	return e.runLoop(ctx, st, thinkingEnabled, loopOpts{onEvent: newEventSink(onEvent), journal: e.journal})
}

func textMessage(text string) models.ChatMessage {
	return models.ChatMessage{Role: "user", Content: text}
}

// Resume continues a run from its journal: a plain interrupted run (process
// died mid-loop, no pause involved) picks up at its next turn; a run paused
// on ErrPausedForApproval applies decision to every gated call in the turn
// that paused it, executes the turn's tool calls for real, and continues.
//
// Resume needs a Journal — without one there is nothing to resume from, which
// is the honest reflection of what "durable" means here: a run is only ever
// resumable because its journal said so.
//
// kitchen must match the run's own kitchen (its journal header, set from the
// agent that started it). Journal.Load takes only a traceID, so without this
// check any caller who guessed or observed another tenant's traceID could
// resume — and see the content of — a run that isn't theirs. Reporting the
// mismatch as "not found" rather than "forbidden" avoids confirming that the
// traceID exists at all to a caller who isn't its owner.
func (e *Executor) Resume(ctx context.Context, kitchen, traceID string, decision *ResumeDecision) (string, *ExecutionTrace, error) {
	if e.journal == nil {
		return "", nil, fmt.Errorf("executor: no journal configured, nothing is resumable")
	}
	js, err := e.journal.Load(ctx, traceID)
	if err != nil {
		return "", nil, err
	}
	if js.Header.Kitchen != kitchen {
		return "", nil, fmt.Errorf("executor: run %s not found", traceID)
	}
	if js.Status == RunDone {
		return js.FinalContent, nil, fmt.Errorf("executor: run %s already finished", traceID)
	}

	agent := js.Header.Agent
	ctx = ensureDelegationRoot(ctx, agent.Name)
	resolved := js.Header.Resolved
	st := &runState{
		traceID: traceID,
		trace: &ExecutionTrace{
			TraceID: traceID, AgentName: agent.Name, Kitchen: agent.Kitchen, SessionID: js.Header.SessionID,
		},
		agent:      agent,
		resolved:   resolved,
		toolDefs:   e.buildToolDefinitions(agent, allResolvedTools(resolved)),
		messages:   js.Messages,
		startTurn:  js.CompletedTurns + 1,
		totalUsage: js.Usage,
		start:      js.Header.StartedAt,
	}
	st.toolSchema = make(map[string]map[string]interface{}, len(resolved.Tools))
	for _, t := range allResolvedTools(resolved) {
		st.toolSchema[t.Name] = t.Schema
	}
	if js.Header.SessionID != "" && e.sessions != nil {
		st.session, _ = e.sessions.GetSession(ctx, js.Header.SessionID)
	}

	if js.Status == RunPaused {
		if decision == nil {
			return "", nil, fmt.Errorf("executor: run %s is paused for approval, a ResumeDecision is required", traceID)
		}
		results := e.resolvePendingTurn(ctx, agent, *js.Pending, *decision, st.toolSchema)
		st.messages = appendToolResults(st.messages, results)
		turn := Turn{Number: js.Pending.Turn, ToolCalls: js.Pending.ToolCalls, ToolResults: results}
		st.trace.Turns = append(st.trace.Turns, turn)
		// The paused turn is now resolved — continue at the next one. This
		// overrides the CompletedTurns-based startTurn set above, because a
		// paused turn was never journaled as a completed "turn" record (there
		// was nothing to record until just now), so CompletedTurns alone
		// would have the loop repeat the turn that just got resolved.
		st.startTurn = js.Pending.Turn + 1
		if e.journal != nil {
			if err := e.journal.AppendTurn(ctx, traceID, turn, st.messages, models.TokenUsage{}); err != nil {
				log.Warn().Err(err).Str("trace_id", traceID).Msg("Failed to journal resumed turn")
			}
		}
	}

	return e.runLoop(ctx, st, js.Header.ThinkingEnabled, loopOpts{journal: e.journal})
}

// ListUnfinished reports runs that stopped without finishing — crashed,
// restarted, or paused for approval — so an embedder can decide which, if
// any, to Resume. Requires a journal; returns an error without one, the same
// way Resume does.
func (e *Executor) ListUnfinished(ctx context.Context, kitchen string) ([]JournalSummary, error) {
	if e.journal == nil {
		return nil, fmt.Errorf("executor: no journal configured, nothing to list")
	}
	return e.journal.ListUnfinished(ctx, kitchen)
}

// runLoop is the one implementation of the agentic loop every entry point
// drives through. st.startTurn is 1 for a fresh run and completedTurns+1 for
// a resumed one; everything else about a turn is identical either way.
func (e *Executor) runLoop(ctx context.Context, st *runState, thinkingEnabled bool, opts loopOpts) (string, *ExecutionTrace, error) {
	agent, resolved, trace := st.agent, st.resolved, st.trace

	ctx, rootSpan := tracer.Start(ctx, "agent.run",
		oteltrace.WithSpanKind(oteltrace.SpanKindInternal),
		oteltrace.WithAttributes(
			attribute.String("agent.name", agent.Name),
			attribute.String("agent.kitchen", agent.Kitchen),
			attribute.String("agent.mode", string(agent.Mode)),
			attribute.String("agent.behavior", string(agent.Behavior)),
			attribute.String("agentoven.trace_id", st.traceID),
		),
	)
	defer func() {
		rootSpan.SetAttributes(
			attribute.Int64("agent.total_ms", trace.TotalMs),
			attribute.Int("agent.turns", len(trace.Turns)),
			attribute.Int64("agent.total_tokens", trace.Usage.TotalTokens),
			attribute.Float64("agent.cost_usd", trace.Usage.EstimatedCost),
		)
		rootSpan.End()
	}()

	maxTurns := agent.MaxTurns
	if maxTurns <= 0 {
		maxTurns = DefaultMaxTurns
	}
	responseFormat := outputResponseFormat(agent)

	for turn := st.startTurn; turn <= maxTurns; turn++ {
		turnStart := time.Now()
		opts.onEvent.emit(Event{Type: EventTurnStart, Turn: turn})

		routeReq := &models.RouteRequest{
			Messages:        st.messages,
			Model:           resolved.Model.Model,
			Strategy:        models.RoutingFallback,
			Kitchen:         agent.Kitchen,
			AgentRef:        agent.Name,
			ThinkingEnabled: thinkingEnabled,
			Tools:           st.toolDefs,
			ToolChoice:      "auto",
			ResponseFormat:  responseFormat,
		}
		if st.session != nil {
			routeReq.SessionID = st.session.ID
		}

		llmCtx, llmSpan := tracer.Start(ctx, fmt.Sprintf("llm.turn_%d", turn),
			oteltrace.WithSpanKind(oteltrace.SpanKindClient),
			oteltrace.WithAttributes(
				attribute.String("llm.model", routeReq.Model),
				attribute.Int("llm.turn", turn),
				attribute.Int("llm.tools_available", len(st.toolDefs)),
			),
		)
		routeCtx := router.ContextSkipTrace(llmCtx)
		var routeResp *models.RouteResponse
		var err error
		if agent.BackupProvider != "" {
			routeResp, err = e.router.RouteWithBackup(routeCtx, routeReq, agent.BackupProvider, agent.BackupModel)
		} else {
			routeResp, err = e.router.Route(routeCtx, routeReq)
		}
		if err != nil {
			llmSpan.RecordError(err)
			llmSpan.SetStatus(codes.Error, err.Error())
			inttelemetry.RecordProviderCall(ctx, agent.Kitchen, "unknown", routeReq.Model, "error", time.Since(turnStart), 0, 0, 0)
			llmSpan.End()
			runErr := fmt.Errorf("model router call failed (turn %d): %w", turn, err)
			// Deliberately not journal.MarkDone: a model/router failure is
			// exactly the kind of interruption durability exists for — a
			// timeout, a transient provider outage, the process losing the
			// call mid-flight. The run stays "running" in the journal with
			// whatever turns already completed, so a future Resume retries
			// this turn's model call rather than finding a run durability
			// already gave up on. MarkDone is reserved for a genuine
			// terminal state: a final answer, or hitting max_turns.
			opts.onEvent.emit(Event{Type: EventError, Turn: turn, Err: runErr.Error()})
			return "", trace, runErr
		}
		inttelemetry.RecordProviderCall(ctx, agent.Kitchen, routeResp.Provider, routeReq.Model, "ok",
			time.Since(turnStart), routeResp.Usage.InputTokens, routeResp.Usage.OutputTokens, routeResp.Usage.EstimatedCost)
		llmSpan.SetAttributes(
			attribute.Int64("llm.input_tokens", routeResp.Usage.InputTokens),
			attribute.Int64("llm.output_tokens", routeResp.Usage.OutputTokens),
			attribute.Float64("llm.cost_usd", routeResp.Usage.EstimatedCost),
			attribute.String("llm.finish_reason", routeResp.FinishReason),
			attribute.String("llm.provider", routeResp.Provider),
		)
		llmSpan.End()

		st.totalUsage.InputTokens += routeResp.Usage.InputTokens
		st.totalUsage.OutputTokens += routeResp.Usage.OutputTokens
		st.totalUsage.TotalTokens += routeResp.Usage.TotalTokens
		st.totalUsage.ThinkingTokens += routeResp.Usage.ThinkingTokens
		st.totalUsage.EstimatedCost += routeResp.Usage.EstimatedCost

		if routeResp.Content != "" {
			opts.onEvent.emit(Event{Type: EventToken, Turn: turn, Content: routeResp.Content})
		}

		toolCalls := e.extractToolCalls(routeResp)
		turnRecord := Turn{Number: turn, Request: st.messages, ThinkingBlocks: routeResp.ThinkingBlocks, Usage: routeResp.Usage}

		if len(toolCalls) == 0 {
			turnRecord.Response = routeResp.Content
			turnRecord.LatencyMs = time.Since(turnStart).Milliseconds()
			trace.Turns = append(trace.Turns, turnRecord)
			trace.TotalMs = time.Since(st.start).Milliseconds()
			trace.Usage = st.totalUsage

			if st.session != nil {
				st.session.Messages = append(st.session.Messages, models.ChatMessage{Role: "assistant", Content: routeResp.Content})
				st.session.TurnCount++
				st.session.TotalTokens += routeResp.Usage.TotalTokens
				st.session.TotalCost += routeResp.Usage.EstimatedCost
				st.session.UpdatedAt = time.Now().UTC()
				if updateErr := e.sessions.UpdateSession(ctx, st.session); updateErr != nil {
					log.Error().Err(updateErr).Str("session", st.session.ID).Msg("Failed to persist session")
				}
			}
			if opts.journal != nil {
				if err := opts.journal.AppendTurn(ctx, st.traceID, turnRecord, st.messages, routeResp.Usage); err != nil {
					log.Warn().Err(err).Str("trace_id", st.traceID).Msg("Failed to journal final turn")
				}
				if err := opts.journal.MarkDone(ctx, st.traceID, routeResp.Content, nil); err != nil {
					log.Warn().Err(err).Str("trace_id", st.traceID).Msg("Failed to mark run done in journal")
				}
			}
			opts.onEvent.emit(Event{Type: EventTurnEnd, Turn: turn})
			opts.onEvent.emit(Event{Type: EventDone, Turn: turn, Content: routeResp.Content, Trace: trace})

			log.Info().Str("agent", agent.Name).Int("turns", turn).Int64("total_ms", trace.TotalMs).Msg("Managed agent execution complete")
			return routeResp.Content, trace, nil
		}

		turnRecord.ToolCalls = toolCalls

		// Build the assistant message this turn's tool calls hang off of —
		// needed in the conversation (and in the journal, if we pause) before
		// any tool actually runs, since the provider protocol requires tool
		// results to correlate to a preceding assistant tool_calls message.
		assistantToolCalls := routeResp.ToolCalls
		if len(assistantToolCalls) == 0 && len(toolCalls) > 0 {
			assistantToolCalls = synthesizeToolCallResults(toolCalls)
		}
		assistantMsg := models.ChatMessage{Role: "assistant", Content: routeResp.Content, ToolCalls: assistantToolCalls}
		messagesWithAssistant := append(append([]models.ChatMessage{}, st.messages...), assistantMsg)

		if gated := gatedNames(agent, toolCalls); len(gated) > 0 {
			pending := PendingApproval{Turn: turn, ToolCalls: toolCalls, GatedNames: gated}
			trace.Turns = append(trace.Turns, turnRecord) // tool results not yet known
			trace.TotalMs = time.Since(st.start).Milliseconds()
			trace.Usage = st.totalUsage
			trace.Pending = &pending
			if st.session != nil {
				st.session.Messages = append(st.session.Messages, assistantMsg)
				st.session.UpdatedAt = time.Now().UTC()
				_ = e.sessions.UpdateSession(ctx, st.session)
			}
			if opts.journal != nil {
				if err := opts.journal.MarkPaused(ctx, st.traceID, pending, messagesWithAssistant); err != nil {
					log.Warn().Err(err).Str("trace_id", st.traceID).Msg("Failed to journal paused run")
				}
			}
			opts.onEvent.emit(Event{Type: EventPaused, Turn: turn, Pending: &pending, Trace: trace})
			log.Info().Str("agent", agent.Name).Int("turn", turn).Strs("gated_tools", gated).Msg("Agentic loop paused for approval")
			return "", trace, ErrPausedForApproval
		}

		toolResults := e.executeToolCallsParallel(ctx, agent, toolCalls, st.toolSchema, opts.onEvent)
		turnRecord.ToolResults = toolResults
		turnRecord.LatencyMs = time.Since(turnStart).Milliseconds()
		trace.Turns = append(trace.Turns, turnRecord)

		st.messages = appendToolResults(messagesWithAssistant, toolResults)

		if st.session != nil {
			st.session.Messages = append(st.session.Messages, assistantMsg)
			for _, tr := range toolResults {
				st.session.Messages = append(st.session.Messages, models.ChatMessage{Role: "tool", Content: tr.Content, Name: tr.Name, ToolCallID: tr.ToolCallID})
			}
			st.session.TotalTokens += routeResp.Usage.TotalTokens
			st.session.TotalCost += routeResp.Usage.EstimatedCost
		}
		if opts.journal != nil {
			if err := opts.journal.AppendTurn(ctx, st.traceID, turnRecord, st.messages, routeResp.Usage); err != nil {
				log.Warn().Err(err).Str("trace_id", st.traceID).Msg("Failed to journal turn")
			}
		}
		opts.onEvent.emit(Event{Type: EventTurnEnd, Turn: turn})

		log.Debug().Str("agent", agent.Name).Int("turn", turn).Int("tool_calls", len(toolCalls)).Msg("Agentic loop continuing")
	}

	// Max turns exceeded.
	trace.TotalMs = time.Since(st.start).Milliseconds()
	trace.Usage = st.totalUsage

	if st.session != nil {
		st.session.TurnCount += maxTurns
		st.session.UpdatedAt = time.Now().UTC()
		if updateErr := e.sessions.UpdateSession(ctx, st.session); updateErr != nil {
			log.Error().Err(updateErr).Str("session", st.session.ID).Msg("Failed to persist session at max turns")
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
	final := fmt.Sprintf("[Max turns (%d) reached] %s", maxTurns, lastContent)
	if opts.journal != nil {
		if err := opts.journal.MarkDone(ctx, st.traceID, final, nil); err != nil {
			log.Warn().Err(err).Str("trace_id", st.traceID).Msg("Failed to mark max-turns run done in journal")
		}
	}
	opts.onEvent.emit(Event{Type: EventDone, Content: final, Trace: trace})

	log.Warn().Str("agent", agent.Name).Int("max_turns", maxTurns).Msg("Managed agent hit max turns")
	return final, trace, nil
}

// resolvePendingTurn applies a human decision to a paused turn's tool calls:
// every gated call either runs for real (approved) or gets a synthesized
// denial result; any call in the same turn that was never gated runs for
// real regardless, since pausing happened before anything in the turn ran.
func (e *Executor) resolvePendingTurn(ctx context.Context, agent *models.Agent, pending PendingApproval, decision ResumeDecision, schemas map[string]map[string]interface{}) []ToolResult {
	gated := make(map[string]bool, len(pending.GatedNames))
	for _, n := range pending.GatedNames {
		gated[n] = true
	}

	results := make([]ToolResult, len(pending.ToolCalls))
	var toRun []int
	for i, tc := range pending.ToolCalls {
		if gated[tc.Name] && !decision.Approved {
			note := decision.Note
			if note == "" {
				note = "no reason given"
			}
			results[i] = ToolResult{ToolCallID: tc.ID, Name: tc.Name, IsError: true, Content: fmt.Sprintf("Human denied this tool call: %s", note)}
			continue
		}
		toRun = append(toRun, i)
	}
	for _, i := range toRun {
		results[i] = e.dispatchToolCall(ctx, agent, pending.ToolCalls[i], schemas[pending.ToolCalls[i].Name])
	}
	return results
}

func synthesizeToolCallResults(calls []ToolCall) []models.ToolCallResult {
	out := make([]models.ToolCallResult, 0, len(calls))
	for _, tc := range calls {
		argsJSON, _ := json.Marshal(tc.Arguments)
		out = append(out, models.ToolCallResult{
			ID: tc.ID, Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: tc.Name, Arguments: string(argsJSON)},
		})
	}
	return out
}

func appendToolResults(messages []models.ChatMessage, results []ToolResult) []models.ChatMessage {
	out := append([]models.ChatMessage{}, messages...)
	for _, tr := range results {
		out = append(out, models.ChatMessage{Role: "tool", Content: tr.Content, Name: tr.Name, ToolCallID: tr.ToolCallID})
	}
	return out
}

// gatedNames returns, in call order, the names from toolCalls that appear in
// agent.ApprovalTools. Empty when none do.
func gatedNames(agent *models.Agent, toolCalls []ToolCall) []string {
	if len(agent.ApprovalTools) == 0 {
		return nil
	}
	var out []string
	for _, tc := range toolCalls {
		if requiresApproval(agent, tc.Name) {
			out = append(out, tc.Name)
		}
	}
	return out
}
