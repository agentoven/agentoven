package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/router"
	inttelemetry "github.com/agentoven/agentoven/control-plane/internal/telemetry"
	pkgmw "github.com/agentoven/agentoven/control-plane/pkg/middleware"
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
	// guard counts what guardrail rules changed during this run (see GuardrailCounts).
	guard *guardCounts

	// budget is nil for a run with no limits, which then behaves exactly as it always
	// did. baseBudget is the limits the run started under, before any extension.
	budget     *runBudget
	baseBudget models.RunBudget

	// Long-run state (see context.go).
	longRun        bool // a background task: see context.go
	notes          *notesStore
	notesOn        bool
	stall          *stallTracker
	stalled        *StallInfo
	compactOn      bool
	lastCompactLen int
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
	if id, ok := ctx.Value(traceIDKey{}).(string); ok && id != "" {
		traceID = id
	}
	st := &runState{
		traceID: traceID,
		notes:   newNotes(nil),
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
		guard:     &guardCounts{},
	}
	ctx = withGuardCounts(ctx, st.guard) // chunks retrieved for the first prompt are counted too
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
	if err := e.requireGuardrailPolicy(ctx, agent); err != nil {
		return "", nil, err
	}
	ctx = ensureDelegationRoot(ctx, agent.Name)
	// Media bytes go to the blob store before anything records the message: the
	// session, the run journal and the trace then hold a blob_ref, not the bytes.
	userMsg = e.router.ExternalizeMedia(ctx, agent.Kitchen, userMsg)
	userMessage := userMsg.Content
	base := baseBudget(ctx, agent)
	bud, err := e.buildBudget(ctx, agent, resolved, base, nil, models.TokenUsage{}, 0, 0)
	if err != nil {
		return "", nil, err
	}
	st := e.newRunState(ctx, agent, userMsg, resolved, promptVars, sessionID...)
	st.budget, st.baseBudget = bud, base
	st.longRun = taskModeFrom(ctx)
	if e.journal != nil {
		if err := e.journal.Start(ctx, JournalHeader{
			TraceID: st.traceID, Kitchen: agent.Kitchen, Agent: agent, Resolved: resolved,
			UserMessage: userMessage, PromptVars: promptVars, ThinkingEnabled: thinkingEnabled,
			SessionID: st.trace.SessionID, StartedAt: st.start,
			Budget: budgetPtr(base), ParentTraceID: parentRunID(ctx),
			Schema: journalSchema, InitialMessages: st.messages, LongRun: st.longRun,
			StartedBy: startedBy(ctx),
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
	if err := e.requireGuardrailPolicy(ctx, agent); err != nil {
		return "", nil, err
	}
	ctx = ensureDelegationRoot(ctx, agent.Name)
	// Media bytes go to the blob store before anything records the message: the
	// session, the run journal and the trace then hold a blob_ref, not the bytes.
	userMsg = e.router.ExternalizeMedia(ctx, agent.Kitchen, userMsg)
	userMessage := userMsg.Content
	base := baseBudget(ctx, agent)
	bud, err := e.buildBudget(ctx, agent, resolved, base, nil, models.TokenUsage{}, 0, 0)
	if err != nil {
		return "", nil, err
	}
	st := e.newRunState(ctx, agent, userMsg, resolved, promptVars, sessionID...)
	st.budget, st.baseBudget = bud, base
	st.longRun = taskModeFrom(ctx)
	if e.journal != nil {
		if err := e.journal.Start(ctx, JournalHeader{
			TraceID: st.traceID, Kitchen: agent.Kitchen, Agent: agent, Resolved: resolved,
			UserMessage: userMessage, PromptVars: promptVars, ThinkingEnabled: thinkingEnabled,
			SessionID: st.trace.SessionID, StartedAt: st.start,
			Budget: budgetPtr(base), ParentTraceID: parentRunID(ctx),
			Schema: journalSchema, InitialMessages: st.messages, LongRun: st.longRun,
			StartedBy: startedBy(ctx),
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
	return e.resume(ctx, kitchen, traceID, decision, nil)
}

// ResumeStream is Resume with an event for everything the resumed run does, as ExecuteStream
// gives a fresh one. A background task is resumed through it so a person watching sees the
// run carry on.
func (e *Executor) ResumeStream(ctx context.Context, kitchen, agentName, traceID string, decision *ResumeDecision, onEvent func(Event) error) (string, *ExecutionTrace, error) {
	if err := e.checkRunAgent(ctx, kitchen, agentName, traceID); err != nil {
		return "", nil, err
	}
	return e.resume(ctx, kitchen, traceID, decision, onEvent)
}

func (e *Executor) resume(ctx context.Context, kitchen, traceID string, decision *ResumeDecision, onEvent func(Event) error) (string, *ExecutionTrace, error) {
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
	if err := e.requireGuardrailPolicy(ctx, agent); err != nil {
		return "", nil, err
	}
	ctx = ensureDelegationRoot(ctx, agent.Name)
	resolved := js.Header.Resolved

	if js.Claimed {
		return "", nil, fmt.Errorf("executor: run %s was being resumed when it stopped, and whether its approved tool calls already ran is unknown, so it is not resumed automatically", traceID)
	}

	budgetPaused := js.Status == RunPaused && js.BudgetPause != nil
	stallPaused := js.Status == RunPaused && js.StallPause != nil
	var ext *models.BudgetExtension
	if js.Status == RunPaused {
		if decision == nil {
			return "", nil, fmt.Errorf("executor: run %s is paused, a ResumeDecision is required", traceID)
		}
		if stallPaused && !decision.Approved {
			// A person ending a run that was going in circles is a decision, not a failure.
			runErr := fmt.Errorf("%w: ended by a person after it repeated %s %d times (%s)", ErrStalled, js.StallPause.Tool, js.StallPause.Count, decision.Note)
			if err := e.journal.MarkOutcome(ctx, traceID, OutcomeStalled, "", runErr); err != nil {
				log.Warn().Err(err).Str("trace_id", traceID).Msg("Failed to mark run ended after a stall")
			}
			return "", nil, runErr
		}
		if budgetPaused {
			if !decision.Approved {
				// A person ending the run is a decision, not a failure to resume.
				if err := e.journal.MarkDone(ctx, traceID, "", ErrBudgetExhausted); err != nil {
					log.Warn().Err(err).Str("trace_id", traceID).Msg("Failed to mark run ended at its budget")
				}
				return "", nil, fmt.Errorf("%w: ended by a person (%s)", ErrBudgetExhausted, decision.Note)
			}
			shared := js.BudgetPause.Scope != "" && js.BudgetPause.Scope != "run"
			switch {
			case decision.Extend != nil && !decision.Extend.IsZero():
				grant := *decision.Extend
				if grant.ID == "" {
					grant.ID = uuid.New().String()
				}
				ext = &grant
			case shared:
				// A budget outside the run (the kitchen's, a provider's) is raised or reset
				// elsewhere; resuming just tries again, and pauses again if it is still used up.
			default:
				return "", nil, fmt.Errorf("executor: run %s is paused at its budget, approving it needs an extension that grants something", traceID)
			}
		}
	}

	var base models.RunBudget
	if js.Base != nil {
		base = *js.Base
	}
	exts := js.Extensions
	if ext != nil {
		exts = append(append([]models.BudgetExtension{}, exts...), *ext)
	}
	// Built before the pause is claimed: a refusal (the model has lost its price since the
	// run began) must not consume the pause.
	bud, err := e.buildBudget(ctx, agent, resolved, base, exts, js.Usage, time.Duration(js.ActiveMs)*time.Millisecond, js.CompletedTurns)
	if err != nil {
		return "", nil, err
	}

	pending := js.Pending
	if js.Status == RunPaused {
		claimed, err := e.journal.ClaimResume(ctx, traceID, ext)
		if err != nil {
			return "", nil, err
		}
		if !claimed {
			return "", nil, fmt.Errorf("executor: run %s is already being resumed", traceID)
		}
		if ext != nil {
			e.auditRun(ctx, kitchen, "run.budget_extended", map[string]interface{}{
				"agent": agent.Name, "trace_id": traceID, "extension_id": ext.ID,
				"add_cost_usd": ext.AddCostUSD, "add_tokens": ext.AddTokens,
				"add_active_seconds": ext.AddActiveSeconds, "add_turns": ext.AddTurns,
				"reason": ext.Reason, "by": ext.By,
			})
		}
	}

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
		budget:     bud,
		baseBudget: base,
		notes:      newNotes(js.Notes),
		longRun:    js.Header.LongRun,
	}
	st.lastCompactLen = len(js.Messages) // a resumed run was already compacted as far as it could be
	// Tools finished here (a call approved after a pause, one recovered after a crash) run in
	// the same context as the main loop gives them.
	ctx = e.bindRun(ctx, st, loopOpts{journal: e.journal})
	st.toolSchema = make(map[string]map[string]interface{}, len(resolved.Tools))
	for _, t := range allResolvedTools(resolved) {
		st.toolSchema[t.Name] = t.Schema
	}
	if js.Header.SessionID != "" && e.sessions != nil {
		st.session, _ = e.sessions.GetSession(ctx, js.Header.SessionID)
	}

	// A run sent on after a stall carries the person's note as guidance: the one chance to
	// tell it what it was missing.
	if stallPaused && strings.TrimSpace(decision.Note) != "" {
		st.messages = append(append([]models.ChatMessage{}, st.messages...), models.ChatMessage{
			Role: "user", Content: "Operator guidance after the run repeated itself: " + strings.TrimSpace(decision.Note)})
		if err := e.journal.Snapshot(ctx, traceID, st.messages, models.TokenUsage{}); err != nil {
			log.Warn().Err(err).Str("trace_id", traceID).Msg("Failed to journal operator guidance")
		}
	}

	if pending != nil && !budgetPaused {
		record := func(r ToolResult) {
			if err := e.journal.AppendToolResult(ctx, traceID, pending.Turn, r); err != nil {
				log.Warn().Err(err).Str("trace_id", traceID).Str("tool", r.Name).Msg("Failed to journal tool result")
			}
		}
		results := e.resolvePendingTurn(ctx, agent, *pending, *decision, st.toolSchema, record)
		st.messages = appendToolResults(st.messages, results)
		turn := Turn{Number: pending.Turn, ToolCalls: pending.ToolCalls, ToolResults: results}
		st.trace.Turns = append(st.trace.Turns, turn)
		// The paused turn is now resolved — continue at the next one. This
		// overrides the CompletedTurns-based startTurn set above, because a
		// paused turn was never journaled as a completed "turn" record (there
		// was nothing to record until just now), so CompletedTurns alone
		// would have the loop repeat the turn that just got resolved.
		st.startTurn = pending.Turn + 1
		if e.journal != nil {
			if err := e.journal.CommitTurn(ctx, traceID, turn, st.messages); err != nil {
				log.Warn().Err(err).Str("trace_id", traceID).Msg("Failed to journal resumed turn")
			}
		}
	}

	// A run that died between turns is picked up at the next one. One that died inside a
	// turn has its model reply (and any tool results) on record, so the turn is finished
	// from the record rather than asked of the model again.
	if js.InFlight != nil && js.Pending == nil && !budgetPaused {
		if final, finished := e.finishInFlight(ctx, st, js.InFlight); finished {
			st.trace.Usage = js.Usage
			st.trace.TotalMs = time.Since(st.start).Milliseconds()
			if err := e.journal.MarkOutcome(ctx, traceID, OutcomeCompleted, final, nil); err != nil {
				log.Warn().Err(err).Str("trace_id", traceID).Msg("Failed to mark recovered run done")
			}
			return final, st.trace, nil
		}
	}

	return e.runLoop(ctx, st, js.Header.ThinkingEnabled, loopOpts{journal: e.journal, onEvent: newEventSink(onEvent)})
}

// ResumeFor is Resume for a caller that addressed the run through an agent (the URL
// /agents/{agent}/runs/{id}/resume): the run must belong to that agent. Without the check a
// scoped key limited to one agent could resume another agent's run by its trace ID. A
// mismatch reports "not found", as a kitchen mismatch does.
func (e *Executor) ResumeFor(ctx context.Context, kitchen, agentName, traceID string, decision *ResumeDecision) (string, *ExecutionTrace, error) {
	if err := e.checkRunAgent(ctx, kitchen, agentName, traceID); err != nil {
		return "", nil, err
	}
	return e.resume(ctx, kitchen, traceID, decision, nil)
}

func (e *Executor) checkRunAgent(ctx context.Context, kitchen, agentName, traceID string) error {
	if e.journal == nil {
		return fmt.Errorf("executor: no journal configured, nothing is resumable")
	}
	js, err := e.journal.Load(ctx, traceID)
	if err != nil {
		return err
	}
	if js.Header.Kitchen != kitchen || js.Header.Agent == nil || js.Header.Agent.Name != agentName {
		return fmt.Errorf("executor: run %s not found", traceID)
	}
	return nil
}

// startedBy is who started the run: the e-mail of the authenticated caller, else their subject.
// A GxP kitchen bars that person from signing the run's approvals (Pro ADR-0041).
func startedBy(ctx context.Context) string {
	id := pkgmw.GetIdentity(ctx)
	if id == nil {
		return ""
	}
	if id.Email != "" {
		return id.Email
	}
	return id.Subject
}

// RunState returns a run's journaled state, if the run is in kitchen. A run in another
// kitchen reports "not found", the same as one that does not exist.
func (e *Executor) RunState(ctx context.Context, kitchen, traceID string) (*JournalState, error) {
	if e.journal == nil {
		return nil, fmt.Errorf("executor: no journal configured")
	}
	js, err := e.journal.Load(ctx, traceID)
	if err != nil {
		return nil, fmt.Errorf("executor: run %s not found", traceID)
	}
	if js.Header.Kitchen != kitchen {
		return nil, fmt.Errorf("executor: run %s not found", traceID)
	}
	return js, nil
}

// Journal returns the executor's journal, or nil.
func (e *Executor) Journal() Journal { return e.journal }

// ValidateBudget reports whether a run of agent could start under its budget, without
// starting one. It is how a request that would be refused (a dollar cap on a model with no
// known price) is refused up front, with nothing created.
func (e *Executor) ValidateBudget(ctx context.Context, agent *models.Agent, resolved *models.ResolvedIngredients) error {
	_, err := e.buildBudget(ctx, agent, resolved, baseBudget(ctx, agent), nil, models.TokenUsage{}, 0, 0)
	return err
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

	bud := st.budget
	budgeted := st.longRun
	ctx = e.bindRun(ctx, st, opts)
	defer func() {
		if bud != nil {
			trace.Budget = bud.status(len(trace.Turns))
		}
		trace.Guardrails = st.guard.snapshot()
	}()

	// With a budget that sets its own turn limit, the gate decides when to stop (and pauses
	// for a decision) instead of the legacy "[Max turns reached]" answer below, so the loop
	// is allowed one turn past the limit to let the gate speak.
	loopBound := func() int {
		if bud != nil {
			if m := bud.limits().MaxTurns; m > 0 {
				return m + 1
			}
		}
		return maxTurns
	}

	for turn := st.startTurn; turn <= loopBound(); turn++ {
		turnStart := time.Now()

		// A caller that has gone away must stop costing money. Without this check a
		// disconnected request keeps running to completion.
		if err := ctx.Err(); err != nil {
			runErr := fmt.Errorf("run stopped before turn %d: %w", turn, err)
			opts.onEvent.emit(Event{Type: EventError, Turn: turn, Err: runErr.Error()})
			return "", trace, runErr
		}

		// A run that kept repeating itself stops here, between turns, where the last commit
		// is a clean checkpoint.
		if st.stalled != nil {
			return e.pauseForStall(ctx, st, opts, *st.stalled)
		}
		e.compactIfNeeded(ctx, st, opts, turn)

		headroom := 0
		if bud != nil {
			blk, h := bud.gate(turn, e.estimateInput(st, turn))
			if blk != nil {
				return e.pauseForBudget(ctx, st, opts, blk.pause)
			}
			headroom = h
		}

		opts.onEvent.emit(Event{Type: EventTurnStart, Turn: turn})

		routeReq := &models.RouteRequest{
			Messages:        e.requestMessages(st, turn),
			Model:           resolved.Model.Model,
			Strategy:        models.RoutingFallback,
			Kitchen:         agent.Kitchen,
			AgentRef:        agent.Name,
			ThinkingEnabled: thinkingEnabled,
			Tools:           st.toolDefs,
			ToolChoice:      "auto",
			ResponseFormat:  responseFormat,
		}
		if headroom > 0 {
			// The most the call may produce within what is left of the budget, so the run
			// can overshoot by at most the prompt it already sent.
			n := headroom
			routeReq.MaxTokens = &n
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
		callModel := func() (*models.RouteResponse, error) {
			routeCtx := router.ContextSkipTrace(llmCtx)
			cancelCall := func() {}
			if bud != nil {
				if d := bud.deadline(); d > 0 {
					routeCtx, cancelCall = context.WithTimeout(routeCtx, d)
				}
			}
			defer cancelCall()
			if agent.BackupProvider != "" {
				return e.router.RouteWithBackup(routeCtx, routeReq, agent.BackupProvider, agent.BackupModel)
			}
			return e.router.Route(routeCtx, routeReq)
		}
		routeResp, err := callModel()
		// A budgeted run is usually unattended, so a transient provider error (a rate limit,
		// an overload) is repeated a few times with growing delays before the run is left
		// for a person to resume. A failed call produced no usage, so it costs nothing.
		for attempt := 0; err != nil && budgeted && attempt < e.retry.max && retriable(err) && ctx.Err() == nil; attempt++ {
			if bud != nil && errors.Is(err, context.DeadlineExceeded) {
				break // that is the active-time cap, handled below
			}
			wait := e.retry.base << uint(attempt)
			log.Warn().Err(err).Str("trace_id", st.traceID).Int("turn", turn).Int("attempt", attempt+1).Dur("wait", wait).Msg("Model call failed; retrying")
			select {
			case <-time.After(wait):
			case <-ctx.Done():
			}
			routeResp, err = callModel()
		}
		if err != nil {
			llmSpan.RecordError(err)
			llmSpan.SetStatus(codes.Error, err.Error())
			inttelemetry.RecordProviderCall(ctx, agent.Kitchen, "unknown", routeReq.Model, "error", time.Since(turnStart), 0, 0, 0)
			llmSpan.End()
			if bud != nil && errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				// The call ran into the active-time cap, not a provider fault.
				if p := bud.activeExceeded(); p != nil {
					p.Turn = turn
					return e.pauseForBudget(ctx, st, opts, *p)
				}
			}
			var refused *models.SpendRefusedError
			if errors.As(err, &refused) {
				// A budget shared beyond this run (the kitchen's, a provider's) is used up. The
				// run waits, as it does at its own budget, and carries on when a person resumes
				// it once that budget has been raised or has reset.
				return e.pauseForBudget(ctx, st, opts, models.BudgetPause{
					Turn: turn, Dimension: models.BudgetDimCost, Spent: refused.Spent, Cap: refused.Cap, Scope: refused.Scope})
			}
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
			attribute.String("llm.model", routeResp.Model),
			attribute.Int64("llm.thinking_tokens", routeResp.Usage.ThinkingTokens),
			attribute.Int("llm.thinking_blocks", len(routeResp.ThinkingBlocks)),
			attribute.String("llm.price_source", routeResp.Usage.PriceSource),
		)
		llmSpan.End()

		st.totalUsage.InputTokens += routeResp.Usage.InputTokens
		st.totalUsage.OutputTokens += routeResp.Usage.OutputTokens
		st.totalUsage.TotalTokens += routeResp.Usage.TotalTokens
		st.totalUsage.ThinkingTokens += routeResp.Usage.ThinkingTokens
		st.totalUsage.EstimatedCost += routeResp.Usage.EstimatedCost
		if bud != nil {
			for _, w := range bud.charge(routeResp.Usage) {
				w := w
				opts.onEvent.emit(Event{Type: EventBudgetWarning, Turn: turn, BudgetWarning: &w})
				log.Warn().Str("agent", agent.Name).Str("trace_id", st.traceID).Str("dimension", w.Dimension).Int("pct", w.Pct).Msg("Run budget threshold reached")
			}
			bud.addTurn()
		}

		if routeResp.Content != "" {
			opts.onEvent.emit(Event{Type: EventToken, Turn: turn, Content: routeResp.Content})
		}

		toolCalls := e.extractToolCalls(routeResp)
		// Redact the calls' arguments and find the ones a guardrail holds for a person, before the
		// calls are captured, journaled, shown to an approver or run.
		routeResp, guardHeld, guardHolds := e.guardToolCalls(ctx, agent, routeResp, toolCalls)
		turnRecord := Turn{Number: turn, Request: st.messages, ThinkingBlocks: routeResp.ThinkingBlocks, Usage: routeResp.Usage,
			Provider: routeResp.Provider, Model: routeResp.Model, Attempts: routeResp.Attempts,
			SpanID: e.captureCall(ctx, st.traceID, agent, turn, turnStart, routeReq, routeResp)}

		// The model's reply is journaled before anything acts on it. It was paid for, and
		// a crash while its tools run must not lose it or make a resume ask (and pay) again.
		assistantToolCalls := routeResp.ToolCalls
		if len(assistantToolCalls) == 0 && len(toolCalls) > 0 {
			assistantToolCalls = synthesizeToolCallResults(toolCalls)
		}
		assistantMsg := models.ChatMessage{Role: "assistant", Content: routeResp.Content, ToolCalls: assistantToolCalls, Thinking: routeResp.ThinkingBlocks}
		if opts.journal != nil {
			if err := opts.journal.AppendAssistant(ctx, st.traceID, turn, assistantMsg, toolCalls, routeResp.Usage, time.Since(turnStart).Milliseconds()); err != nil {
				log.Warn().Err(err).Str("trace_id", st.traceID).Msg("Failed to journal the model's reply")
			}
		}

		if len(toolCalls) == 0 {
			turnRecord.Response = routeResp.Content
			turnRecord.LatencyMs = time.Since(turnStart).Milliseconds()
			trace.Turns = appendTurn(trace.Turns, turnRecord)
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
				if err := opts.journal.CommitTurn(ctx, st.traceID, turnRecord, st.messages); err != nil {
					log.Warn().Err(err).Str("trace_id", st.traceID).Msg("Failed to journal final turn")
				}
				if err := opts.journal.MarkOutcome(ctx, st.traceID, OutcomeCompleted, routeResp.Content, nil); err != nil {
					log.Warn().Err(err).Str("trace_id", st.traceID).Msg("Failed to mark run done in journal")
				}
			}
			opts.onEvent.emit(e.turnEndEvent(st, turn, len(trace.Turns)))
			opts.onEvent.emit(Event{Type: EventDone, Turn: turn, Content: routeResp.Content, Trace: trace})

			log.Info().Str("agent", agent.Name).Int("turns", turn).Int64("total_ms", trace.TotalMs).Msg("Managed agent execution complete")
			return routeResp.Content, trace, nil
		}

		turnRecord.ToolCalls = toolCalls

		// Build the assistant message this turn's tool calls hang off of —
		// needed in the conversation (and in the journal, if we pause) before
		// any tool actually runs, since the provider protocol requires tool
		// results to correlate to a preceding assistant tool_calls message.
		messagesWithAssistant := append(append([]models.ChatMessage{}, st.messages...), assistantMsg)

		if gated := mergeNames(e.gatedNames(ctx, agent, toolCalls), guardHeld); len(gated) > 0 {
			pending := PendingApproval{Turn: turn, ToolCalls: toolCalls, GatedNames: gated, GuardrailHolds: guardHolds,
				Usage: routeResp.Usage, LatencyMs: time.Since(turnStart).Milliseconds()}
			trace.Turns = appendTurn(trace.Turns, turnRecord) // tool results not yet known
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

		// A call repeated enough is warned about in its own result (where the model will see
		// it), and repeated further it stops the run at the top of the next turn.
		warnings := map[string]string{}
		if st.stall != nil {
			for _, tc := range toolCalls {
				if tc.Name == notesToolName {
					continue
				}
				n := st.stall.note(callSignature(tc))
				switch {
				case n >= st.stall.limit && st.stalled == nil:
					args, _ := json.Marshal(tc.Arguments)
					st.stalled = &StallInfo{Turn: turn, Tool: tc.Name, Args: truncateMiddle(string(args), 300), Count: n, Limit: st.stall.limit}
					fallthrough
				case n >= st.stall.warnAt():
					warnings[tc.ID] = fmt.Sprintf("\n\n[Notice: you have made this exact %s call %d times with the same arguments and it has not changed the outcome. Do something different: change the arguments, use another tool, revise your plan in your notes, or finish with what you have. Repeating it again will stop this run.]", tc.Name, n)
					opts.onEvent.emit(Event{Type: EventStallWarning, Turn: turn, Stall: &StallInfo{Turn: turn, Tool: tc.Name, Count: n, Limit: st.stall.limit}})
				}
			}
		}
		decorate := func(r ToolResult) ToolResult {
			if note, ok := warnings[r.ToolCallID]; ok {
				r.Content += note
			}
			return r
		}
		toolResults := e.executeToolCallsParallel(ctx, agent, toolCalls, st.toolSchema, opts.onEvent, decorate, func(r ToolResult) {
			if opts.journal == nil {
				return
			}
			if err := opts.journal.AppendToolResult(ctx, st.traceID, turn, r); err != nil {
				log.Warn().Err(err).Str("trace_id", st.traceID).Str("tool", r.Name).Msg("Failed to journal tool result")
			}
		})
		turnRecord.ToolResults = toolResults
		turnRecord.LatencyMs = time.Since(turnStart).Milliseconds()
		trace.Turns = appendTurn(trace.Turns, turnRecord)

		// What subagents spent this turn belongs to this run's total (their charge to the
		// budget already happened live), and to the journal so a resume still sees it.
		turnUsage := routeResp.Usage
		for _, tr := range toolResults {
			turnUsage = addUsage(turnUsage, tr.ChildUsage)
			st.totalUsage = addUsage(st.totalUsage, tr.ChildUsage)
		}

		st.messages = appendToolResults(messagesWithAssistant, toolResults)

		if st.session != nil {
			st.session.Messages = append(st.session.Messages, assistantMsg)
			for _, tr := range toolResults {
				st.session.Messages = append(st.session.Messages, models.ChatMessage{Role: "tool", Content: tr.Content, Name: tr.Name, ToolCallID: tr.ToolCallID})
			}
			st.session.TotalTokens += turnUsage.TotalTokens
			st.session.TotalCost += turnUsage.EstimatedCost
		}
		if opts.journal != nil {
			if err := opts.journal.CommitTurn(ctx, st.traceID, turnRecord, st.messages); err != nil {
				log.Warn().Err(err).Str("trace_id", st.traceID).Msg("Failed to journal turn")
			}
		}
		opts.onEvent.emit(e.turnEndEvent(st, turn, len(trace.Turns)))

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
	trace.Outcome = OutcomeMaxTurns
	if opts.journal != nil {
		if err := opts.journal.MarkOutcome(ctx, st.traceID, OutcomeMaxTurns, final, nil); err != nil {
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
func (e *Executor) resolvePendingTurn(ctx context.Context, agent *models.Agent, pending PendingApproval, decision ResumeDecision, schemas map[string]map[string]interface{}, onResult func(ToolResult)) []ToolResult {
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
			if onResult != nil {
				onResult(results[i])
			}
			continue
		}
		toRun = append(toRun, i)
	}
	for _, i := range toRun {
		callCtx := ctx
		if gated[pending.ToolCalls[i].Name] {
			callCtx = withGuardrailApproval(ctx) // a person said yes: a require_approval rule does not hold it again
		}
		results[i] = e.dispatchToolCall(callCtx, agent, pending.ToolCalls[i], schemas[pending.ToolCalls[i].Name])
		if onResult != nil {
			onResult(results[i])
		}
	}
	return results
}

// outcomeUnknown is what the model is told about a tool call that was running when the
// process died and is not known to be safe to repeat.
const outcomeUnknown = "Outcome unknown: the process stopped while this tool call was running, so it may or may not have taken effect. Check the current state before repeating it."

// finishInFlight completes a turn that was interrupted after the model replied. Calls that
// already have a recorded result keep it. A call without one is run again only if its tool
// is marked idempotent; otherwise the model is told the outcome is unknown, which is the
// safe reading for anything that may have changed the world. It returns finished=true when
// the interrupted turn was the final answer, so there is nothing left to run.
func (e *Executor) finishInFlight(ctx context.Context, st *runState, inf *InFlightTurn) (final string, finished bool) {
	if len(inf.Calls) == 0 {
		return inf.Assistant.Content, true
	}
	results := make([]ToolResult, len(inf.Calls))
	for i, tc := range inf.Calls {
		if r, ok := inf.Results[tc.ID]; ok {
			results[i] = r
			continue
		}
		if toolIsIdempotent(st.resolved, tc.Name) {
			results[i] = e.dispatchToolCall(ctx, st.agent, tc, st.toolSchema[tc.Name])
		} else {
			results[i] = ToolResult{ToolCallID: tc.ID, Name: tc.Name, IsError: true, Content: outcomeUnknown}
		}
		if err := e.journal.AppendToolResult(ctx, st.traceID, inf.Turn, results[i]); err != nil {
			log.Warn().Err(err).Str("trace_id", st.traceID).Str("tool", tc.Name).Msg("Failed to journal recovered tool result")
		}
	}
	msgs := append(append([]models.ChatMessage{}, st.messages...), inf.Assistant)
	st.messages = appendToolResults(msgs, results)
	turn := Turn{Number: inf.Turn, ToolCalls: inf.Calls, ToolResults: results}
	st.trace.Turns = append(st.trace.Turns, turn)
	if err := e.journal.CommitTurn(ctx, st.traceID, turn, st.messages); err != nil {
		log.Warn().Err(err).Str("trace_id", st.traceID).Msg("Failed to journal recovered turn")
	}
	st.startTurn = inf.Turn + 1
	return "", false
}

func toolIsIdempotent(resolved *models.ResolvedIngredients, name string) bool {
	for _, t := range allResolvedTools(resolved) {
		if t.Name == name {
			return t.Idempotent
		}
	}
	return false
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
// agent.ApprovalTools or that the installed tool-approval policy holds. Empty when none do.
func (e *Executor) gatedNames(ctx context.Context, agent *models.Agent, toolCalls []ToolCall) []string {
	var runtimeGated map[string]bool
	if e.subagents != nil {
		for _, n := range e.subagents.GatedTools(agent) {
			if runtimeGated == nil {
				runtimeGated = map[string]bool{}
			}
			runtimeGated[n] = true
		}
	}
	var out []string
	for _, tc := range toolCalls {
		if requiresApproval(agent, tc.Name) || runtimeGated[tc.Name] || e.policyGates(ctx, agent.Kitchen, tc.Name) {
			out = append(out, tc.Name)
		}
	}
	return out
}

// keepTurnRequests is how many of a run's most recent turns keep the full message list they
// were sent. Every turn's request is the whole history so far, so keeping all of them makes
// a trace grow with the square of the run; older turns keep their response and tool calls.
const keepTurnRequests = 10

func appendTurn(turns []Turn, t Turn) []Turn {
	turns = append(turns, t)
	if i := len(turns) - 1 - keepTurnRequests; i >= 0 {
		turns[i].Request = nil
	}
	return turns
}

// traceIDKey carries a run ID chosen by the caller (see WithTraceID).
type traceIDKey struct{}

// WithTraceID makes the next run started with ctx use id as its trace ID, so a caller that
// must hand an ID back before the run starts (a background task) can. The ID becomes a
// journal file name, so it must be generated by the server, never taken from a client.
func WithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceIDKey{}, id)
}

func (e *Executor) turnEndEvent(st *runState, turn, turnsDone int) Event {
	u := st.totalUsage
	ev := Event{Type: EventTurnEnd, Turn: turn, Usage: &u}
	if st.budget != nil {
		ev.Budget = st.budget.status(turnsDone)
	}
	return ev
}

// bindRun gives ctx everything a run's tools need to behave as part of that run, and sets up
// the run's long-run state. It is how a subagent's spend reaches this run's budget, how its
// journal names this run as its parent, and how the notes tool finds the notes. Every path that
// executes a run's tools goes through it, including the ones that run outside the main loop (a
// tool call approved after a pause, one finished after a crash), so a tool behaves the same
// wherever the run happens to be executing it.
func (e *Executor) bindRun(ctx context.Context, st *runState, opts loopOpts) context.Context {
	agent, resolved := st.agent, st.resolved

	if st.guard == nil {
		st.guard = &guardCounts{} // a run read back from a journal
	}
	ctx = withGuardCounts(ctx, st.guard)
	ctx = withRunBudget(ctx, st.budget)
	ctx = WithTraceID(ctx, "") // a hint names this run only, never a subagent's
	ctx = withGrant(ctx, nil)  // this run's own grant was applied when it started; it is not inherited downward
	ctx = withRunID(ctx, st.traceID)

	// Defences for long runs (context.go) apply to background tasks. A plain request keeps its
	// original behaviour.
	st.compactOn = st.longRun || agent.ContextBudget > 0
	ctx = withToolLimit(ctx, toolResultLimit(st.longRun))
	if st.longRun && st.stall == nil {
		if lim := stallLimit(); lim > 0 {
			st.stall = &stallTracker{limit: lim}
		}
	}

	ctx = withParentScope(ctx, &parentScope{agent: agent, resolved: resolved, traceID: st.traceID})
	if isSubagent(ctx) {
		st.toolDefs = stripDelegation(st.toolDefs)
	} else if e.subagentsOffered(ctx, agent) && !hasTool(st.toolDefs, subagentToolName) {
		st.toolDefs = append(st.toolDefs, subagentToolDefs()...)
	}

	st.notesOn = st.longRun || agent.ReasoningStrategy == models.StrategyPlanAndExecute
	if st.notesOn {
		if st.notes == nil {
			st.notes = newNotes(nil)
		}
		st.notes.journal, st.notes.traceID = opts.journal, st.traceID
		ctx = withNotes(ctx, st.notes)
		if !hasTool(st.toolDefs, notesToolName) {
			st.toolDefs = append(st.toolDefs, notesToolDef())
		}
	}
	return ctx
}

// captureCall hands the model call that was just answered, whole, to the router's capture hook (when
// one is installed) and returns the ID of the span it will be recorded under. The run's spans are
// written when it ends, by which time the oldest turns' requests have been dropped to bound memory, so
// the capture has to happen here, at the call.
func (e *Executor) captureCall(ctx context.Context, traceID string, agent *models.Agent, turn int, started time.Time, req *models.RouteRequest, resp *models.RouteResponse) string {
	spanID := uuid.New().String()
	c := e.router.CallCapturer()
	if c == nil {
		return spanID
	}
	snap := *req
	snap.Messages = append([]models.ChatMessage(nil), req.Messages...)
	snap.Tools = append([]models.ToolDefinition(nil), req.Tools...)
	err := c.CaptureLLMCall(ctx, models.LLMCall{
		TraceID: traceID, SpanID: spanID, Kitchen: agent.Kitchen, AgentName: agent.Name,
		Turn: turn, StartedAt: started.UTC(), Request: &snap, Response: resp,
	})
	if err != nil {
		log.Warn().Err(err).Str("trace_id", traceID).Int("turn", turn).Msg("Failed to capture model call")
	}
	return spanID
}
