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

// needsPrepass reports whether any rule that applies to a tool call's arguments does more than
// block: redacts, asks for approval, or is a custom endpoint. With only blocking rules (every rule
// written before actions existed) there is nothing to do before dispatch, and nothing is evaluated
// twice.
func needsPrepass(rules []models.Guardrail) bool {
	for _, g := range rules {
		if !g.Enabled || g.Kind == "" {
			continue
		}
		switch g.EffectiveAction() {
		case models.GuardrailActionRedact, models.GuardrailActionMask, models.GuardrailActionRequireApproval:
			return true
		}
		if g.Kind == models.GuardrailCustom {
			return true
		}
	}
	return false
}

// guardToolCalls runs on the model's tool calls before anything else touches them: before they
// are journaled, shown to an approver or dispatched. It redacts their arguments (in the calls, and
// in the assistant message the model sees on its next turn) and reports the calls that a
// require_approval rule holds, so the loop can pause for a person with the redacted arguments on
// the screen. A call that a rule blocks outright is left as it is: dispatch refuses it, on the
// original text, which is what the rule judged.
//
// It does nothing unless some rule redacts or asks for approval (needsPrepass). It returns the response
// with the redacted arguments in it.
func (e *Executor) guardToolCalls(ctx context.Context, agent *models.Agent, resp *models.RouteResponse, calls []ToolCall) (_ *models.RouteResponse, names []string, holds []GuardrailHold) {
	if e.guardrails == nil || len(calls) == 0 {
		return resp, nil, nil
	}
	guards, err := e.guardrailsFor(ctx, agent)
	if err != nil || !needsPrepass(guards) {
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
