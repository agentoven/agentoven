package executor

// What the executor does with a guardrail evaluation beyond pass or fail (ADR-0032): redact the
// arguments of a tool call and the result it returns, hold a call for a person's approval, and
// check the chunks retrieved for the prompt. The handlers do the same for what enters and
// leaves an agent; these are the hops in between.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/rs/zerolog/log"
)

// GuardrailCounts is what guardrail rules changed during a run: spans they redacted and rules
// that warned, across tool-call arguments, tool results and retrieved chunks. A trace carries
// the counts, never the text that was taken out.
type GuardrailCounts struct {
	Redacted int `json:"redacted,omitempty"`
	Warned   int `json:"warned,omitempty"`
}

type guardCounts struct {
	mu               sync.Mutex
	redacted, warned int
}

func (c *guardCounts) add(eval *models.GuardrailEvaluation) {
	if c == nil || eval == nil {
		return
	}
	c.mu.Lock()
	c.redacted += eval.Redacted
	c.warned += eval.Warned
	c.mu.Unlock()
}

// snapshot is nil when no rule changed anything, so a trace without guardrail activity does not
// grow a field.
func (c *guardCounts) snapshot() *GuardrailCounts {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.redacted == 0 && c.warned == 0 {
		return nil
	}
	return &GuardrailCounts{Redacted: c.redacted, Warned: c.warned}
}

type guardCountsKey struct{}

func withGuardCounts(ctx context.Context, c *guardCounts) context.Context {
	return context.WithValue(ctx, guardCountsKey{}, c)
}

func guardCountsFrom(ctx context.Context) *guardCounts {
	c, _ := ctx.Value(guardCountsKey{}).(*guardCounts)
	return c
}

// guardrailApprovedKey marks a tool call a person has approved. A rule whose action is
// require_approval does not hold such a call a second time.
type guardrailApprovedKey struct{}

func withGuardrailApproval(ctx context.Context) context.Context {
	return context.WithValue(ctx, guardrailApprovedKey{}, true)
}

func guardrailApproved(ctx context.Context) bool {
	v, _ := ctx.Value(guardrailApprovedKey{}).(bool)
	return v
}

// GuardrailHold says why a tool call is waiting for a person: the rules that asked for it.
// It carries rule names and messages only, never the arguments that matched.
type GuardrailHold struct {
	CallID string   `json:"call_id"`
	Tool   string   `json:"tool"`
	Rules  []string `json:"rules"`
	Reason string   `json:"reason,omitempty"`
}

// withheldArguments is what the arguments of a call a rule blocked are replaced with everywhere they
// are kept. It is valid JSON, so the assistant message that carries it can be sent back to the model.
const withheldArguments = `{"withheld":"the arguments were blocked by a guardrail"}`

// withheldCall is what the arguments of a blocked call become in the call itself.
func withheldCall() map[string]interface{} {
	return map[string]interface{}{"withheld": "the arguments were blocked by a guardrail"}
}

// guardToolCalls runs on the model's tool calls before anything else touches them: before they
// are journaled, shown to an approver or dispatched. It redacts their arguments (in the calls, and
// in the assistant message the model sees on its next turn) and reports the calls that a
// require_approval rule holds, so the loop can pause for a person with the redacted arguments on
// the screen. A call that a rule blocks outright is refused here and its arguments are replaced by a
// placeholder in every copy that is kept (the response, the journal, the capture, the session, the
// trace): a block means the text is not stored, and it used to be the one thing a redaction could not
// reach. The call is marked Refused, so dispatch never runs it with the placeholder.
//
// Every call is judged here, once; dispatch does not judge it again. It returns the response with the
// redacted or withheld arguments in it.
func (e *Executor) guardToolCalls(ctx context.Context, agent *models.Agent, resp *models.RouteResponse, calls []ToolCall) (_ *models.RouteResponse, names []string, holds []GuardrailHold) {
	if e.guardrails == nil || len(calls) == 0 {
		return resp, nil, nil
	}
	guards, err := e.guardrailsFor(ctx, agent)
	if err != nil || len(guards) == 0 {
		return resp, nil, nil // dispatch refuses a call whose policy could not be established
	}
	// The response may be shared (the router's response cache hands out what it stored), so what is
	// changed here is a copy.
	own := *resp
	own.ToolCalls = append([]models.ToolCallResult(nil), resp.ToolCalls...)
	resp = &own
	for i := range calls {
		raw, _ := json.Marshal(calls[i].Arguments)
		eval, gErr := e.guardrails.EvaluateOutput(ctx, guards, string(raw))
		if gErr != nil || eval == nil {
			continue
		}
		if !eval.Passed && !eval.ApprovalRequired {
			calls[i].Arguments, calls[i].Refused, calls[i].guarded = withheldCall(), true, true
			if len(resp.ToolCalls) == len(calls) {
				resp.ToolCalls[i].Function.Arguments = withheldArguments
			}
			e.auditGuardrail(ctx, agent, "tool_args", calls[i].Name, eval)
			continue
		}
		if eval.Redacted > 0 {
			var args map[string]interface{}
			if json.Unmarshal([]byte(eval.Text), &args) != nil {
				continue // the redaction broke the JSON: dispatch refuses the call
			}
			calls[i].Arguments = args
			if len(resp.ToolCalls) == len(calls) {
				resp.ToolCalls[i].Function.Arguments = eval.Text
			}
		}
		calls[i].guarded = true
		guardCountsFrom(ctx).add(eval)
		e.auditGuardrail(ctx, agent, "tool_args", calls[i].Name, eval)
		if eval.ApprovalRequired {
			names = append(names, calls[i].Name)
			holds = append(holds, GuardrailHold{CallID: calls[i].ID, Tool: calls[i].Name, Rules: firingRules(eval), Reason: firstMessage(eval)})
		}
	}
	return resp, names, holds
}

// judgeReply judges the text of one assistant turn, whether the turn ends the run or goes on to call tools
// (text beside a tool call used to go unjudged), before anything keeps it: the capture, the journal, the
// trace, the session and the event stream all get what it returns. It returns the reply with redactions
// applied. A reply a rule blocks, or that could not be judged (closed, as every output check is), comes back
// as a *models.GuardrailBlockedError together with a copy of the reply emptied of its text, which is what a
// capture of the refused call may hold. judged says whether any rule was applied.
func (e *Executor) judgeReply(ctx context.Context, agent *models.Agent, resp *models.RouteResponse) (out *models.RouteResponse, judged bool, err error) {
	if e.guardrails == nil || resp == nil || strings.TrimSpace(resp.Content) == "" {
		return resp, false, nil
	}
	guards, gerr := e.guardrailsFor(ctx, agent)
	if gerr != nil {
		return withheldReply(resp), false, &models.GuardrailBlockedError{Stage: "policy"}
	}
	if len(guards) == 0 {
		return resp, false, nil
	}
	eval, eerr := e.guardrails.EvaluateOutput(ctx, guards, resp.Content)
	if eerr != nil || eval == nil {
		log.Error().Err(eerr).Str("agent", agent.Name).Msg("the model's reply could not be judged by the guardrails; it is withheld")
		return withheldReply(resp), true, &models.GuardrailBlockedError{Stage: "output"}
	}
	e.auditGuardrail(ctx, agent, "output", "", eval)
	if !eval.Passed {
		return withheldReply(resp), true, &models.GuardrailBlockedError{Stage: "output", Results: eval.Results}
	}
	guardCountsFrom(ctx).add(eval)
	if eval.Redacted == 0 {
		return resp, true, nil
	}
	// The response may be shared (the router's response cache hands out what it stored): change a copy.
	own := *resp
	own.Content = eval.Text
	return &own, true, nil
}

// refuseReply ends a run whose reply a guardrail refused. What was said is kept nowhere: the capture of the
// call holds the emptied reply, the journal gets no assistant message and no final text, and the session
// is left as it was before the turn, so the refused text cannot be replayed to the model on the next one.
// The turn itself (its usage, the provider that answered) is recorded, since it was paid for.
func (e *Executor) refuseReply(ctx context.Context, st *runState, opts loopOpts, trace *ExecutionTrace, agent *models.Agent, req *models.RouteRequest, withheld *models.RouteResponse, turn int, turnStart time.Time, cause error) (string, *ExecutionTrace, error) {
	rec := Turn{Number: turn, Request: st.messages, Usage: withheld.Usage, Provider: withheld.Provider, Model: withheld.Model, Attempts: withheld.Attempts,
		CacheHit: withheld.Cached, LatencyMs: time.Since(turnStart).Milliseconds(),
		SpanID: e.captureCall(ctx, st.traceID, agent, turn, turnStart, req, withheld)}
	trace.Turns = appendTurn(trace.Turns, rec)
	trace.TotalMs = time.Since(st.start).Milliseconds()
	trace.Usage = st.totalUsage
	if opts.journal != nil {
		if err := opts.journal.CommitTurn(ctx, st.traceID, rec, st.messages); err != nil {
			log.Warn().Err(err).Str("trace_id", st.traceID).Msg("Failed to journal the turn of a refused reply")
		}
		if err := opts.journal.MarkOutcome(ctx, st.traceID, OutcomeFailed, "", cause); err != nil {
			log.Warn().Err(err).Str("trace_id", st.traceID).Msg("Failed to mark the run with a refused reply done in the journal")
		}
	}
	opts.onEvent.emit(Event{Type: EventError, Turn: turn, Err: cause.Error()})
	return "", trace, cause
}

// withheldReply is a copy of a reply with nothing a rule judged left in it; its usage stays, since the
// provider was paid for the call.
func withheldReply(resp *models.RouteResponse) *models.RouteResponse {
	w := *resp
	w.Content, w.ToolCalls, w.ContentParts = "", nil, nil
	return &w
}

// mergeNames appends the names in extra that are not already in names, keeping call order.
func mergeNames(names, extra []string) []string {
	for _, n := range extra {
		dup := false
		for _, have := range names {
			if have == n {
				dup = true
				break
			}
		}
		if !dup {
			names = append(names, n)
		}
	}
	return names
}

// checkToolArgs is the dispatch-time check of a call's arguments. It returns the arguments to
// run with (redacted), and a reason when the call must not run. A call the pre-pass already
// judged is not judged again.
func (e *Executor) checkToolArgs(ctx context.Context, agent *models.Agent, guards []models.Guardrail, tc ToolCall) (map[string]interface{}, string) {
	if tc.Refused {
		return nil, "Error: tool call arguments blocked by guardrails"
	}
	if tc.guarded {
		return tc.Arguments, ""
	}
	raw, _ := json.Marshal(tc.Arguments)
	eval, gErr := e.guardrails.EvaluateOutput(ctx, guards, string(raw))
	if gErr != nil || eval == nil {
		return tc.Arguments, "" // as before: an evaluator that errors does not stop the call here
	}
	if !eval.Passed {
		approved := eval.ApprovalRequired && guardrailApproved(ctx)
		if !approved {
			e.auditGuardrail(ctx, agent, "tool_args", tc.Name, eval)
			if eval.ApprovalRequired {
				return nil, "Error: tool call needs a person's approval under a guardrail"
			}
			return nil, "Error: tool call arguments blocked by guardrails"
		}
	}
	args := tc.Arguments
	if eval.Redacted > 0 {
		if json.Unmarshal([]byte(eval.Text), &args) != nil {
			return nil, "Error: tool call arguments blocked by guardrails: the redaction could not be applied"
		}
	}
	guardCountsFrom(ctx).add(eval)
	e.auditGuardrail(ctx, agent, "tool_args", tc.Name, eval)
	return args, ""
}

// checkToolResult is the check of what a tool returned, before the model sees it: a rule that
// blocks withholds the result, one that redacts replaces it. A result cannot wait for approval, so
// require_approval blocks here.
func (e *Executor) checkToolResult(ctx context.Context, agent *models.Agent, guards []models.Guardrail, result ToolResult) ToolResult {
	eval, gErr := e.guardrails.EvaluateInput(ctx, guards, result.Content)
	if gErr != nil || eval == nil {
		return result
	}
	if !eval.Passed {
		e.auditGuardrail(ctx, agent, "tool_result", result.Name, eval)
		return ToolResult{ToolCallID: result.ToolCallID, Name: result.Name, IsError: true, Content: "Error: tool result blocked by guardrails"}
	}
	result.Content = eval.Apply(result.Content)
	result.Redacted += eval.Redacted
	guardCountsFrom(ctx).add(eval)
	e.auditGuardrail(ctx, agent, "tool_result", result.Name, eval)
	return result
}

// auditGuardrail records a guardrail decision made inside a run: a block, a redaction, a warning
// or a hold for approval. Rule ids, types and counts only, never the text.
func (e *Executor) auditGuardrail(ctx context.Context, agent *models.Agent, hop, subject string, eval *models.GuardrailEvaluation) {
	if !eval.Fired() {
		return
	}
	outcome := "blocked"
	switch {
	case eval.ApprovalRequired:
		outcome = "approval_required"
	case eval.Passed && eval.Redacted > 0:
		outcome = "redacted"
	case eval.Passed:
		outcome = "warned"
	}
	e.auditRun(ctx, agent.Kitchen, "guardrail."+hop+"_"+outcome, map[string]interface{}{
		"agent": agent.Name, "subject": subject, "hop": hop, "results": eval.Results,
		"redacted": eval.Redacted, "warned": eval.Warned, "passed": eval.Passed,
	})
}

func firingRules(eval *models.GuardrailEvaluation) []string {
	var out []string
	for _, r := range eval.Results {
		if !r.Passed {
			name := r.RuleName
			if name == "" {
				name = r.RuleID
			}
			out = append(out, name)
		}
	}
	return out
}

func firstMessage(eval *models.GuardrailEvaluation) string {
	for _, r := range eval.Results {
		if !r.Passed && r.Message != "" {
			return r.Message
		}
	}
	return ""
}

// ── Retrieved chunks ────────────────────────────────────────

// guardChunks checks the text of each retrieved chunk against the rules whose stage is
// retrieval (or all), before it enters the prompt. A chunk a rule blocks is dropped, not sent
// with a hole in it; a chunk a rule redacts goes in redacted. Retrieved text is external content
// in the same way a tool result is (a poisoned document is the classic route for a prompt
// injection), so it gets the same treatment. If the rules cannot be established, or evaluating
// one fails, no chunk goes in: nothing unchecked enters the prompt.
//
// It returns the text to use for each chunk, in the order given, and whether the chunk may be
// used at all (false: dropped).
func (e *Executor) guardChunks(ctx context.Context, agent *models.Agent, provider string, chunks []string) (texts []string, usable []bool) {
	texts, usable = append([]string(nil), chunks...), make([]bool, len(chunks))
	allow := func(v bool) ([]string, []bool) {
		for i := range usable {
			usable[i] = v
		}
		return texts, usable
	}
	svc, ok := e.guardrails.(contracts.StageGuardrailService)
	if !ok {
		return allow(true)
	}
	guards, err := e.guardrailsFor(ctx, agent)
	if err != nil {
		log.Error().Err(err).Str("agent", agent.Name).Msg("retrieved chunks not used: workspace guardrails could not be applied")
		return allow(false)
	}
	if !hasRetrievalRules(guards) {
		return allow(true)
	}
	var redacted, warned, dropped int
	var rules []string
	for i, text := range chunks {
		eval, err := svc.Evaluate(ctx, guards, text, models.GuardrailStageRetrieval)
		if err != nil || eval == nil || !eval.Passed {
			dropped++
			if eval != nil {
				rules = append(rules, firingRules(eval)...)
			}
			continue
		}
		redacted += eval.Redacted
		warned += eval.Warned
		texts[i], usable[i] = eval.Apply(text), true
	}
	counts := &models.GuardrailEvaluation{Passed: true, Redacted: redacted, Warned: warned}
	guardCountsFrom(ctx).add(counts)
	if dropped > 0 || counts.Fired() {
		outcome := "redacted"
		if dropped > 0 {
			outcome = "blocked"
		}
		e.auditRun(ctx, agent.Kitchen, "guardrail.retrieval_"+outcome, map[string]interface{}{
			"agent": agent.Name, "hop": "retrieval", "provider": provider, "chunks": len(chunks),
			"dropped": dropped, "redacted": redacted, "warned": warned, "rules": strings.Join(rules, ", "),
		})
	}
	return texts, usable
}

func hasRetrievalRules(guards []models.Guardrail) bool {
	for _, g := range guards {
		if g.Enabled && (g.Stage == models.GuardrailStageRetrieval || g.Stage == models.GuardrailStageAll) {
			return true
		}
	}
	return false
}
