package handlers

import (
	"context"
	"net/http"

	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/rs/zerolog/log"
)

// What a guardrail evaluation changes besides pass or fail (ADR-0032): text a rule redacted
// continues in place of the original, and a rule that warned or redacted is audited even though
// the request went on. Every place that used to read only eval.Passed does both through here, so
// a redact rule is never a rule that quietly does nothing on one route.

// noteGuardrails audits an evaluation that let the text through but fired: a warning, or text
// that was redacted. A pass that did nothing is not an event, and a block is audited by its own
// caller. The audit event carries rule ids, types and counts, never the text.
func (h *Handlers) noteGuardrails(ctx context.Context, kitchen, agent, stage string, eval *models.GuardrailEvaluation) {
	if eval != nil && eval.Passed && eval.Fired() {
		h.emitGuardrailAudit(ctx, kitchen, agent, stage, eval, nil)
	}
}

// guardrailOutcome names the audit action and response status of an evaluation: a block, or
// (the text went on) a redaction or a warning.
func guardrailOutcome(stage string, eval *models.GuardrailEvaluation) (string, int) {
	if eval != nil && eval.Passed {
		if eval.Redacted > 0 {
			return "guardrail." + stage + "_redacted", http.StatusOK
		}
		return "guardrail." + stage + "_warned", http.StatusOK
	}
	return "guardrail." + stage + "_blocked", http.StatusForbidden
}

// guardTally is the redaction and warning counts a run's traces carry, so a reader can see
// that text was altered without seeing what it was.
type guardTally struct{ redacted, warned int }

func (t *guardTally) add(eval *models.GuardrailEvaluation) {
	if eval != nil {
		t.redacted += eval.Redacted
		t.warned += eval.Warned
	}
}

// addRun adds what the executor's rules changed during a run: tool arguments, tool results and
// retrieved chunks.
func (t *guardTally) addRun(tr *executor.ExecutionTrace) {
	if tr != nil && tr.Guardrails != nil {
		t.redacted += tr.Guardrails.Redacted
		t.warned += tr.Guardrails.Warned
	}
}

// meta is the trace metadata for the tally, or nil when no rule fired.
func (t guardTally) meta() map[string]interface{} {
	if t.redacted == 0 && t.warned == 0 {
		return nil
	}
	return map[string]interface{}{"redacted": t.redacted, "warned": t.warned}
}

// withGuardrailMeta adds the tally to a trace's metadata under "guardrails".
func withGuardrailMeta(md map[string]interface{}, t guardTally) map[string]interface{} {
	if m := t.meta(); m != nil {
		md["guardrails"] = m
	}
	return md
}

// outputText checks a reply against the output rules and returns the text that may continue: the
// reply with redactions applied. ok is false when it was blocked or the rules could not be
// evaluated (closed, as outputVerdict has always done).
func (h *Handlers) outputText(ctx context.Context, agent *models.Agent, gr []models.Guardrail, text string) (out string, ok bool, eval *models.GuardrailEvaluation) {
	pass, eval := h.outputEvaluation(ctx, agent, gr, text)
	if !pass {
		return "", false, eval
	}
	h.noteGuardrails(ctx, agent.Kitchen, agent.Name, "output", eval)
	return eval.Apply(text), true, eval
}

// redactedTurn puts a redacted reply into the last turn of the run's trace, so the trace the
// caller gets and the spans written from it never hold the text the rule took out.
func redactedTurn(trace *executor.ExecutionTrace, original, redacted string) {
	if trace == nil || original == redacted || len(trace.Turns) == 0 {
		return
	}
	if last := &trace.Turns[len(trace.Turns)-1]; last.Response == original {
		last.Response = redacted
	}
}

// scrubForStorage applies the redact and mask rules of the local detectors to text that is stored
// before the guardrails of the route that produced it have run: the trace of a call to an agent's
// own process is written when the call returns, and the reply may still be refused or redacted
// after. Only redactions are applied (never a block, never a network call), so what is stored is
// at worst the redacted text; a rule that blocks is still enforced by the route. If the rules
// cannot be established the text is returned as it is: the route refuses the reply in that case.
func (h *Handlers) scrubForStorage(ctx context.Context, agent *models.Agent, stage models.GuardrailStage, text string) string {
	svc, ok := h.Guardrails.(contracts.StageGuardrailService)
	if !ok || text == "" {
		return text
	}
	gr, err := h.guardrailsFor(ctx, agent, agent.Guardrails)
	if err != nil {
		return text
	}
	out, _ := svc.Scrub(gr, text, stage)
	return out
}

// redactSessionReply replaces, in the stored session, the assistant reply the executor saved
// before the output guardrails ran. The session is replayed to the model on the next turn and
// readable through the sessions API, so a reply a rule redacted must not stay in it whole.
func (h *Handlers) redactSessionReply(ctx context.Context, trace *executor.ExecutionTrace, original, redacted string) {
	if h.Sessions == nil || trace == nil || trace.SessionID == "" || original == redacted {
		return
	}
	session, err := h.Sessions.GetSession(ctx, trace.SessionID)
	if err != nil || session == nil {
		return
	}
	for i := len(session.Messages) - 1; i >= 0; i-- {
		if m := &session.Messages[i]; m.Role == "assistant" && m.Content == original {
			m.Content = redacted
			if err := h.Sessions.UpdateSession(ctx, session); err != nil {
				log.Error().Err(err).Str("session", session.ID).Msg("the redacted reply could not be written to the session, which still holds the original")
			}
			return
		}
	}
}

// recordGuardrailsOnTrace brings a trace that was written before the route's guardrails ran (the
// call to an agent's own process stores its trace when the call returns) in line with what the
// route returned: the redacted reply, and the counts of what the rules changed.
func (h *Handlers) recordGuardrailsOnTrace(ctx context.Context, trace *models.Trace, reply string, t guardTally) {
	if trace == nil || (t.meta() == nil && trace.OutputText == reply) {
		return
	}
	trace.OutputText = reply
	if trace.Metadata == nil {
		trace.Metadata = map[string]interface{}{}
	}
	withGuardrailMeta(trace.Metadata, t)
	if err := h.Store.UpdateTrace(ctx, trace); err != nil {
		log.Warn().Err(err).Str("trace", trace.ID).Msg("could not record the guardrail counts on the trace")
	}
}
