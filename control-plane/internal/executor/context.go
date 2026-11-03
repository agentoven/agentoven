package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog/log"

	"github.com/agentoven/agentoven/control-plane/internal/ctxwindow"
	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// Long runs fail in a few well-known ways that have nothing to do with the model being
// wrong: the context fills with old tool output, a single enormous result swamps it, the run
// forgets what it was doing when the window is cut or the process restarted, or it keeps
// making the same call hoping for a different answer. This file is the loop's defence against
// each. All of it applies to background tasks (WithTaskMode); an ordinary short request,
// budgeted or not, behaves as it always did. Compaction also applies to an agent that sets a
// ContextBudget, and working notes to one that asks for the plan-and-execute strategy.

// ErrStalled ends a run that repeated itself and had nobody to ask: it has no journal to
// pause into, or it is a subagent whose parent is waiting on it.
var ErrStalled = errors.New("executor: run stopped, it kept repeating the same tool call")

// StallInfo describes the repetition that stopped a run.
type StallInfo struct {
	Turn  int    `json:"turn"`
	Tool  string `json:"tool"`
	Args  string `json:"args,omitempty"`
	Count int    `json:"count"`
	Limit int    `json:"limit"`
}

// ── tool results ──────────────────────────────────────────────

// DefaultToolResultBytes is how much of one tool result a budgeted run keeps. A result is
// cut in the middle, keeping its start and its end (where tools tend to put the summary or
// the error), and says how much was left out.
const DefaultToolResultBytes = 32 * 1024

type toolLimitKey struct{}

type taskModeKey struct{}

// WithTaskMode marks the run started with ctx as a background task, which gets the long-run
// defences in this file. The task manager sets it; so does anything that starts a run meant to
// outlive its caller.
func WithTaskMode(ctx context.Context) context.Context {
	return context.WithValue(ctx, taskModeKey{}, true)
}

func taskModeFrom(ctx context.Context) bool {
	v, _ := ctx.Value(taskModeKey{}).(bool)
	return v
}

func withToolLimit(ctx context.Context, n int) context.Context {
	return context.WithValue(ctx, toolLimitKey{}, n)
}

func toolLimitFrom(ctx context.Context) int {
	n, _ := ctx.Value(toolLimitKey{}).(int)
	return n
}

// toolResultLimit is the cap for a run: DefaultToolResultBytes (or
// AGENTOVEN_TOOL_RESULT_MAX_BYTES; 0 means no cap) for a background task, none for a plain run.
func toolResultLimit(longRun bool) int {
	if !longRun {
		return 0
	}
	if v := os.Getenv("AGENTOVEN_TOOL_RESULT_MAX_BYTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return DefaultToolResultBytes
}

// truncateMiddle keeps the start and end of s within limit bytes, on rune boundaries.
func truncateMiddle(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	marker := func(omitted int) string {
		return fmt.Sprintf("\n[… %d bytes omitted from the middle of this result …]\n", omitted)
	}
	keep := limit - len(marker(len(s)))
	if keep < 64 {
		keep = limit / 2
	}
	headN, tailN := keep*2/3, keep-keep*2/3
	head := s[:headN]
	for len(head) > 0 && !utf8.ValidString(head) {
		head = head[:len(head)-1]
	}
	tail := s[len(s)-tailN:]
	for len(tail) > 0 && !utf8.ValidString(tail) {
		tail = tail[1:]
	}
	return head + marker(len(s)-len(head)-len(tail)) + tail
}

// capResult applies the run's cap to a tool result.
func capResult(ctx context.Context, r ToolResult) ToolResult {
	if limit := toolLimitFrom(ctx); limit > 0 {
		r.Content = truncateMiddle(r.Content, limit)
	}
	return r
}

// ── compaction ────────────────────────────────────────────────

// taskContextBudget is the context budget of a budgeted run whose agent sets none. The
// general default (16K tokens) suits a chat; a run that works for hours needs room to work in.
const taskContextBudget = 64000

const (
	compactAt   = 0.80 // compact when the conversation reaches this share of the budget
	compactKeep = 0.40 // and keep about this share, the most recent part, verbatim
)

// estimateConversation counts tokens the way a request will be billed more closely than
// ctxwindow does: it includes the arguments of tool calls, which the content field omits.
func estimateConversation(msgs []models.ChatMessage) int {
	n := ctxwindow.EstimateTokensForMessages(msgs)
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			n += (len(tc.Function.Name) + len(tc.Function.Arguments)) / 4
		}
	}
	return n
}

func isSummary(m models.ChatMessage) bool {
	return m.Role == "system" && strings.HasPrefix(m.Content, SummaryPrefix)
}

// compactionSplit divides a conversation into what is always kept (the system prompt and the
// first user message, which states the task), what is summarized, and the recent tail kept
// verbatim. The tail starts on a message that is not a tool result: a tool result without the
// assistant message that requested it is a malformed conversation to every provider, so a cut
// never separates them. A previous summary is returned separately so the new one replaces it
// rather than stacking beside it. ok is false when there is nothing worth summarizing.
func compactionSplit(msgs []models.ChatMessage, budget int) (base []models.ChatMessage, firstUser *models.ChatMessage, oldSummary string, middle, tail []models.ChatMessage, ok bool) {
	i := 0
	for i < len(msgs) && msgs[i].Role == "system" {
		if isSummary(msgs[i]) {
			oldSummary = strings.TrimPrefix(msgs[i].Content, SummaryPrefix)
		} else {
			base = append(base, msgs[i])
		}
		i++
	}
	if i < len(msgs) && msgs[i].Role == "user" {
		u := msgs[i]
		firstUser = &u
		i++
	}
	rest := msgs[i:]
	keep := int(float64(budget) * compactKeep)
	cut := len(rest)
	used := 0
	for cut > 0 {
		t := estimateConversation(rest[cut-1 : cut])
		if used+t > keep && cut < len(rest) {
			break
		}
		used += t
		cut--
	}
	// Never start the tail on a tool result: back up to the assistant message that asked.
	for cut > 0 && cut < len(rest) && rest[cut].Role == "tool" {
		cut--
	}
	if cut <= 0 {
		return nil, nil, "", nil, nil, false
	}
	return base, firstUser, oldSummary, rest[:cut], rest[cut:], true
}

// summaryPrompt asks for the things a run needs to keep going.
const summaryPrompt = "You are compacting the history of a long-running agent so it can continue with less context. " +
	"Write a summary of the conversation below that preserves: the goal and any constraints, what has been done and found " +
	"(with the concrete values: names, IDs, paths, URLs, numbers), decisions made and why, what failed and should not be tried again, " +
	"and what remains. Be specific rather than general. Under 600 words.\n\n"

// renderForSummary turns messages into text for the summarizer, clipping each so one huge
// message cannot make the summary request itself too large.
func renderForSummary(oldSummary string, msgs []models.ChatMessage) string {
	var sb strings.Builder
	if oldSummary != "" {
		sb.WriteString("[previous summary]: " + oldSummary + "\n")
	}
	for _, m := range msgs {
		line := m.Content
		for _, tc := range m.ToolCalls {
			line += fmt.Sprintf(" [called %s(%s)]", tc.Function.Name, truncateMiddle(tc.Function.Arguments, 300))
		}
		sb.WriteString("[" + m.Role + "]: " + truncateMiddle(line, 1500) + "\n")
	}
	const total = 60000
	return truncateMiddle(sb.String(), total)
}

// fallbackSummary is used when the model cannot be asked (the call failed, or the budget
// cannot afford it): a terse record of what happened, which beats losing the history outright.
func fallbackSummary(oldSummary string, msgs []models.ChatMessage) string {
	var sb strings.Builder
	if oldSummary != "" {
		sb.WriteString(truncateMiddle(oldSummary, 3000) + "\n")
	}
	sb.WriteString("(Earlier steps, condensed without a model:)\n")
	for _, m := range msgs {
		line := strings.ReplaceAll(strings.TrimSpace(m.Content), "\n", " ")
		for _, tc := range m.ToolCalls {
			line += " [called " + tc.Function.Name + "]"
		}
		if line == "" {
			continue
		}
		sb.WriteString("- " + m.Role + ": " + truncateMiddle(line, 200) + "\n")
	}
	return truncateMiddle(sb.String(), 6000)
}

// compactIfNeeded replaces the older part of a long conversation with a summary. The run's
// opening (system prompt, task) and its most recent turns stay verbatim; the notes, which are
// injected fresh on every call, are unaffected. The rewrite is written to the journal as a
// snapshot, and the summarizing call is charged to the run's budget like any other model call.
func (e *Executor) compactIfNeeded(ctx context.Context, st *runState, opts loopOpts, turn int) {
	if !st.compactOn {
		return
	}
	budget := ctxwindow.EffectiveBudget(st.agent.ContextBudget, taskContextBudget)
	if float64(estimateConversation(st.messages)) < compactAt*float64(budget) {
		return
	}
	if len(st.messages) < st.lastCompactLen+6 {
		return // compacting again straight away would only churn
	}
	base, firstUser, oldSummary, middle, tail, ok := compactionSplit(st.messages, budget)
	if !ok || len(middle) == 0 {
		return
	}

	summary, usage := e.summarize(ctx, st, oldSummary, middle, turn)

	next := make([]models.ChatMessage, 0, len(base)+len(tail)+2)
	next = append(next, base...)
	next = append(next, models.ChatMessage{Role: "system", Content: SummaryPrefix + summary})
	if firstUser != nil {
		next = append(next, *firstUser)
	}
	next = append(next, tail...)

	before := len(st.messages)
	st.messages = next
	st.lastCompactLen = len(next)
	st.totalUsage = addUsage(st.totalUsage, usage)
	if opts.journal != nil {
		if err := opts.journal.Snapshot(ctx, st.traceID, st.messages, usage); err != nil {
			log.Warn().Err(err).Str("trace_id", st.traceID).Msg("Failed to journal the compacted history")
		}
	}
	opts.onEvent.emit(Event{Type: EventContextCompacted, Turn: turn, Content: fmt.Sprintf("%d earlier messages summarized into one", len(middle))})
	log.Info().Str("agent", st.agent.Name).Str("trace_id", st.traceID).Int("before", before).Int("after", len(next)).Int("summarized", len(middle)).Msg("Context compacted")
}

// summarize asks the model for the summary, within the run's budget.
func (e *Executor) summarize(ctx context.Context, st *runState, oldSummary string, middle []models.ChatMessage, turn int) (string, models.TokenUsage) {
	model := st.agent.SummaryModel
	if model == "" && st.resolved != nil && st.resolved.Model != nil {
		model = st.resolved.Model.Model
	}
	if model == "" {
		return fallbackSummary(oldSummary, middle), models.TokenUsage{}
	}
	prompt := summaryPrompt + renderForSummary(oldSummary, middle)

	if st.budget != nil {
		if blk, _ := st.budget.gate(turn, ctxwindow.EstimateTokens(prompt)+1500); blk != nil {
			log.Info().Str("trace_id", st.traceID).Str("dimension", blk.pause.Dimension).Msg("Budget cannot afford a summarizing call; condensing without the model")
			return fallbackSummary(oldSummary, middle), models.TokenUsage{}
		}
	}
	maxOut := 1500
	resp, err := e.router.Route(router.ContextGuardrailsApplied(router.ContextSkipTrace(ctx)), &models.RouteRequest{
		Messages:  []models.ChatMessage{{Role: "user", Content: prompt}},
		Model:     model,
		Kitchen:   st.agent.Kitchen,
		Strategy:  models.RoutingFallback,
		AgentRef:  st.agent.Name + "_summarizer",
		MaxTokens: &maxOut,
	})
	if err != nil || strings.TrimSpace(resp.Content) == "" {
		log.Warn().Err(err).Str("trace_id", st.traceID).Msg("Summarizing call failed; condensing without the model")
		return fallbackSummary(oldSummary, middle), models.TokenUsage{}
	}
	if st.budget != nil {
		st.budget.charge(resp.Usage)
	}
	return strings.TrimSpace(resp.Content), resp.Usage
}

// ── working notes ─────────────────────────────────────────────

// notesToolName is the virtual tool a long-running agent keeps its own memory in. A run's
// context is cut, summarized and sometimes restarted from a checkpoint; the notes are the
// one thing guaranteed to be in front of the model at every turn, and they are journaled,
// so a resume or a compaction cannot lose them.
const notesToolName = "agentoven_notes"

var notesSections = []string{"goal", "plan", "progress", "decisions"}

const maxNotesBytes = 8 * 1024

type notesStore struct {
	mu       sync.Mutex
	sections map[string]string
	journal  Journal
	traceID  string
}

func newNotes(initial map[string]string) *notesStore {
	n := &notesStore{sections: map[string]string{}}
	for k, v := range initial {
		n.sections[k] = v
	}
	return n
}

type notesCtxKey struct{}

func withNotes(ctx context.Context, n *notesStore) context.Context {
	return context.WithValue(ctx, notesCtxKey{}, n)
}

func notesFrom(ctx context.Context) *notesStore {
	n, _ := ctx.Value(notesCtxKey{}).(*notesStore)
	return n
}

func (n *notesStore) sizeLocked() int {
	t := 0
	for _, v := range n.sections {
		t += len(v)
	}
	return t
}

func (n *notesStore) snapshotLocked() map[string]string {
	out := make(map[string]string, len(n.sections))
	for k, v := range n.sections {
		out[k] = v
	}
	return out
}

// planEmpty reports whether the agent has written no plan.
func (n *notesStore) planEmpty() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return strings.TrimSpace(n.sections["plan"]) == ""
}

// render is the block put in front of the model on every call.
func (n *notesStore) render(turn int, planFirst bool) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var sb strings.Builder
	sb.WriteString("## Working notes (pinned; always shown)\n")
	sb.WriteString("This run may last a long time. Your earlier messages can be summarized away, and the run can be stopped and resumed with no memory of them; these notes are the only thing guaranteed to survive. ")
	sb.WriteString("Keep them current with the " + notesToolName + " tool: goal, plan, progress (what is done, what is next), decisions (and why). Read them first whenever you start from them.\n")
	for _, s := range notesSections {
		v := strings.TrimSpace(n.sections[s])
		if v == "" {
			v = "(empty)"
		}
		sb.WriteString("\n### " + s + "\n" + v + "\n")
	}
	if strings.TrimSpace(n.sections["plan"]) == "" {
		switch {
		case planFirst && turn <= 2:
			sb.WriteString("\nBefore acting, write a plan: " + notesToolName + " with action set, section plan.\n")
		case turn >= 3:
			sb.WriteString("\nYou have no plan in your notes yet. Write one now (" + notesToolName + ", action set, section plan) so the work can be picked up if it is interrupted.\n")
		}
	}
	return sb.String()
}

func notesToolDef() models.ToolDefinition {
	return models.ToolDefinition{
		Type: "function",
		Function: models.ToolFunction{
			Name: notesToolName,
			Description: "Your durable working notes for this task. They are shown to you at the top of every turn and survive context summarization and restarts. " +
				"Use sections goal, plan, progress and decisions. Total size is limited to 8KB, so keep entries short and replace stale ones.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"action":  map[string]interface{}{"type": "string", "enum": []interface{}{"set", "append", "read"}, "description": "set replaces a section, append adds to it, read returns the notes"},
					"section": map[string]interface{}{"type": "string", "enum": toInterfaceSlice(notesSections)},
					"text":    map[string]interface{}{"type": "string", "description": "the text to set or append"},
				},
				"required": []interface{}{"action"},
			},
		},
	}
}

// executeNotes runs one call of the notes tool.
func (e *Executor) executeNotes(ctx context.Context, tc ToolCall) ToolResult {
	fail := func(msg string) ToolResult {
		return ToolResult{ToolCallID: tc.ID, Name: tc.Name, IsError: true, Content: "Error: " + msg}
	}
	n := notesFrom(ctx)
	if n == nil {
		return fail("working notes are not available in this run")
	}
	action, _ := tc.Arguments["action"].(string)
	section, _ := tc.Arguments["section"].(string)
	text, _ := tc.Arguments["text"].(string)

	n.mu.Lock()
	defer n.mu.Unlock()
	valid := func(s string) bool {
		for _, v := range notesSections {
			if v == s {
				return true
			}
		}
		return false
	}

	switch action {
	case "read":
		if section != "" {
			if !valid(section) {
				return fail("unknown section; use one of " + strings.Join(notesSections, ", "))
			}
			return ToolResult{ToolCallID: tc.ID, Name: tc.Name, Content: n.sections[section]}
		}
		keys := make([]string, 0, len(n.sections))
		for k := range n.sections {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var sb strings.Builder
		for _, k := range keys {
			sb.WriteString("### " + k + "\n" + n.sections[k] + "\n")
		}
		return ToolResult{ToolCallID: tc.ID, Name: tc.Name, Content: sb.String()}
	case "set", "append":
		if !valid(section) {
			return fail("section is required and must be one of " + strings.Join(notesSections, ", "))
		}
		if strings.TrimSpace(text) == "" && action == "append" {
			return fail("text is required")
		}
		old := n.sections[section]
		next := text
		if action == "append" && old != "" {
			next = old + "\n" + text
		}
		if n.sizeLocked()-len(old)+len(next) > maxNotesBytes {
			return fail(fmt.Sprintf("the notes would exceed %d bytes; shorten or replace entries with action set", maxNotesBytes))
		}
		n.sections[section] = next
		if n.journal != nil {
			if err := n.journal.AppendNotes(ctx, n.traceID, n.snapshotLocked()); err != nil {
				log.Warn().Err(err).Str("trace_id", n.traceID).Msg("Failed to journal the working notes")
			}
		}
		return ToolResult{ToolCallID: tc.ID, Name: tc.Name, Content: "saved"}
	}
	return fail("action must be set, append or read")
}

// ── repetition ────────────────────────────────────────────────

const (
	defaultStallLimit = 5
	stallWindow       = 16
)

// stallLimit is how many times an identical tool call may recur within the recent calls
// before the run is stopped (AGENTOVEN_STALL_LIMIT; 0 turns the check off). The model is
// warned at half that.
func stallLimit() int {
	if v := os.Getenv("AGENTOVEN_STALL_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return defaultStallLimit
}

type stallTracker struct {
	limit  int
	recent []string
}

func callSignature(tc ToolCall) string {
	raw, _ := json.Marshal(tc.Arguments) // map keys are written sorted, so equal arguments compare equal
	return tc.Name + "\x00" + string(raw)
}

// note records a call and returns how many times it now appears among the recent ones.
func (s *stallTracker) note(sig string) int {
	s.recent = append(s.recent, sig)
	if len(s.recent) > stallWindow {
		s.recent = s.recent[len(s.recent)-stallWindow:]
	}
	n := 0
	for _, r := range s.recent {
		if r == sig {
			n++
		}
	}
	return n
}

func (s *stallTracker) reset() { s.recent = nil }

func (s *stallTracker) warnAt() int {
	w := (s.limit + 1) / 2
	if w < 2 {
		w = 2
	}
	return w
}

// ── model errors ──────────────────────────────────────────────

// retriable says whether a failed model call is worth repeating: a rate limit, an overload or
// a dropped connection usually clears; a bad key or a malformed request never will.
func retriable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, models.ErrSpendRefused) {
		return false
	}
	if errors.Is(err, models.ErrRateLimited) {
		return true // our own limit, and the one error whose message says when to come back
	}
	msg := strings.ToLower(err.Error())
	for _, never := range []string{"401", "403", "unauthorized", "invalid api key", "invalid_api_key", "authentication", "permission", "400", "invalid_request", "context length", "context_length"} {
		if strings.Contains(msg, never) {
			return false
		}
	}
	return true
}

// modelRetry bounds how a budgeted run repeats a failed model call.
type modelRetry struct {
	max  int
	base time.Duration
}

// maxRateLimitWait bounds how long a run waits for a rate limit to clear before the retry: a limit
// that wants more than this is left to a person (or the caller) to come back to.
const maxRateLimitWait = time.Minute

// waitFor is how long to wait before the retry after attempt (0-based) failed with err: the usual
// growing delay, or, for a rate limit, at least as long as the limit says it needs.
func (r modelRetry) waitFor(attempt int, err error) time.Duration {
	wait := r.base << uint(attempt)
	var rl *models.RateLimitedError
	if errors.As(err, &rl) {
		if rl.RetryAfter > wait {
			wait = rl.RetryAfter
		}
		if wait > maxRateLimitWait {
			wait = maxRateLimitWait
		}
	}
	return wait
}

func defaultModelRetry() modelRetry {
	r := modelRetry{max: 2, base: 2 * time.Second}
	if v := os.Getenv("AGENTOVEN_MODEL_RETRIES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			r.max = n
		}
	}
	return r
}

// SetModelRetry sets how many times a budgeted run repeats a failed model call and the delay
// before the first repeat (it doubles each time). 0 retries turns it off.
func (e *Executor) SetModelRetry(max int, base time.Duration) {
	e.retry = modelRetry{max: max, base: base}
}

func hasTool(defs []models.ToolDefinition, name string) bool {
	for _, d := range defs {
		if d.Function.Name == name {
			return true
		}
	}
	return false
}

// requestMessages is the conversation as the model sees it this turn: the stored history
// with, when the run keeps notes, the pinned notes block placed after the system prompt. The
// block is rebuilt every turn and never stored, so it is always current and compaction can
// never summarize it away.
func (e *Executor) requestMessages(st *runState, turn int) []models.ChatMessage {
	if !st.notesOn || st.notes == nil {
		return st.messages
	}
	block := models.ChatMessage{Role: "system", Content: st.notes.render(turn, st.agent.ReasoningStrategy == models.StrategyPlanAndExecute)}
	i := 0
	for i < len(st.messages) && st.messages[i].Role == "system" {
		i++
	}
	out := make([]models.ChatMessage, 0, len(st.messages)+1)
	out = append(out, st.messages[:i]...)
	out = append(out, block)
	out = append(out, st.messages[i:]...)
	return out
}

// pauseForStall stops a run that kept repeating itself. It waits for a person, who can send
// it on with guidance or end it; a run that cannot wait (no journal, or a subagent whose
// parent is mid-tool-call) is ended instead.
func (e *Executor) pauseForStall(ctx context.Context, st *runState, opts loopOpts, info StallInfo) (string, *ExecutionTrace, error) {
	trace := st.trace
	trace.TotalMs = time.Since(st.start).Milliseconds()
	trace.Usage = st.totalUsage

	isChild := st.budget != nil && st.budget.parent != nil
	if opts.journal != nil && !isChild {
		if err := opts.journal.MarkStalled(ctx, st.traceID, info); err == nil {
			e.auditRun(ctx, st.agent.Kitchen, "run.stalled", map[string]interface{}{
				"agent": st.agent.Name, "trace_id": st.traceID, "tool": info.Tool, "count": info.Count,
			})
			opts.onEvent.emit(Event{Type: EventStallPaused, Turn: info.Turn, Stall: &info})
			e.pausedFor(ctx, st, PauseStall)
			log.Warn().Str("agent", st.agent.Name).Str("trace_id", st.traceID).Str("tool", info.Tool).Int("count", info.Count).Msg("Run paused: it kept repeating the same tool call")
			return "", trace, &PausedError{Kind: PauseStall, Stall: &info}
		} else {
			log.Warn().Err(err).Str("trace_id", st.traceID).Msg("Failed to journal the stall pause; ending the run instead")
		}
	}
	runErr := fmt.Errorf("%w: %s, %d times", ErrStalled, info.Tool, info.Count)
	if opts.journal != nil {
		if err := opts.journal.MarkOutcome(ctx, st.traceID, OutcomeStalled, "", runErr); err != nil {
			log.Warn().Err(err).Str("trace_id", st.traceID).Msg("Failed to mark stalled run done")
		}
	}
	opts.onEvent.emit(Event{Type: EventError, Turn: info.Turn, Err: runErr.Error(), Stall: &info})
	return "", trace, runErr
}
