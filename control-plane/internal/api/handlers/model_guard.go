package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/pkg/middleware"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// modelGuard is the router's ContentGuard: the kitchen's workspace guardrails, applied to every model call that
// does not already come with the judgement of an agent run (router/guard.go). A person with the right to call a
// model directly, a hosted process behind the model gateway, a recipe step, a document reader: what the kitchen
// forbids sending to a model, or accepting back from one, is forbidden for all of them.
type modelGuard struct{ h *Handlers }

// ModelGuard returns the guard to install on the router (Router.SetContentGuard).
func (h *Handlers) ModelGuard() router.ContentGuard { return modelGuard{h} }

// policyUnavailable is the refusal for rules that could not be established: the call is not made, as every
// route that applies the rules refuses (guardrails.Effective fails closed). It is a 503, not a 403.
func policyUnavailable() error { return &models.GuardrailBlockedError{Stage: "policy"} }

func (g modelGuard) rules(ctx context.Context, kitchen, agent string) ([]models.Guardrail, error) {
	if g.h.Guardrails == nil {
		return nil, nil
	}
	rules, err := g.h.workspaceRules(ctx, kitchen)
	if err != nil {
		g.h.emitGuardrailAudit(ctx, kitchen, agent, "policy", nil, err)
		return nil, policyUnavailable()
	}
	return rules, nil
}

func (g modelGuard) HoldsOutput(ctx context.Context, req *models.RouteRequest) (bool, error) {
	rules, err := g.rules(ctx, req.Kitchen, req.AgentRef)
	if err != nil {
		return false, err
	}
	return outputStageEnabled(rules), nil
}

// Input judges every text the caller sent: messages of any role (the system prompt too, unless the caller says it
// wrote it itself), the text parts of rich messages, tool results and the arguments of earlier tool calls.
func (g modelGuard) Input(ctx context.Context, req *models.RouteRequest) (*models.RouteRequest, error) {
	rules, err := g.rules(ctx, req.Kitchen, req.AgentRef)
	if err != nil || len(rules) == 0 {
		return req, err
	}
	trustSystem := router.SystemPromptTrusted(ctx)
	var out *models.RouteRequest
	for i := range req.Messages {
		m := req.Messages[i]
		if m.Role == "system" && trustSystem {
			continue
		}
		changed := false
		var blocked error
		judge := func(text string) string {
			if text == "" || blocked != nil {
				return text
			}
			eval, err := g.h.Guardrails.EvaluateInput(ctx, rules, text)
			if err != nil || eval == nil {
				g.h.emitGuardrailAudit(ctx, req.Kitchen, req.AgentRef, "input", nil, err)
				blocked = policyUnavailable()
				return text
			}
			if !eval.Passed {
				g.h.emitGuardrailAudit(ctx, req.Kitchen, req.AgentRef, "input", eval, nil)
				blocked = &models.GuardrailBlockedError{Stage: "input", Results: eval.Results}
				return text
			}
			g.h.noteGuardrails(ctx, req.Kitchen, req.AgentRef, "input", eval)
			if eval.Redacted > 0 {
				changed = true
			}
			return eval.Apply(text)
		}
		m.Content = judge(m.Content)
		if len(m.ContentParts) > 0 {
			parts := append([]models.ContentPart(nil), m.ContentParts...)
			for j := range parts {
				parts[j].Text = judge(parts[j].Text)
				parts[j].Content = judge(parts[j].Content)
			}
			m.ContentParts = parts
		}
		if len(m.ToolCalls) > 0 {
			calls := append([]models.ToolCallResult(nil), m.ToolCalls...)
			for j := range calls {
				calls[j].Function.Arguments = judge(calls[j].Function.Arguments)
				if changed && !json.Valid([]byte(calls[j].Function.Arguments)) {
					blocked = &models.GuardrailBlockedError{Stage: "input"}
				}
			}
			m.ToolCalls = calls
		}
		if blocked != nil {
			return nil, blocked
		}
		if changed {
			if out == nil {
				cp := *req
				cp.Messages = append([]models.ChatMessage(nil), req.Messages...)
				out = &cp
			}
			out.Messages[i] = m
		}
	}
	if out != nil {
		return out, nil
	}
	return req, nil
}

// Output judges what the model said: its text, its reasoning and the arguments of the tool calls it made. The
// reply handed on has the redactions applied; a reply a rule blocks is not handed on at all.
func (g modelGuard) Output(ctx context.Context, req *models.RouteRequest, resp *models.RouteResponse) (*models.RouteResponse, error) {
	rules, err := g.rules(ctx, req.Kitchen, req.AgentRef)
	if err != nil || len(rules) == 0 {
		return resp, err
	}
	out := *resp
	var blocked error
	judge := func(text string) (string, bool) {
		if text == "" || blocked != nil {
			return text, false
		}
		eval, err := g.h.Guardrails.EvaluateOutput(ctx, rules, text)
		if err != nil || eval == nil {
			g.h.emitGuardrailAudit(ctx, req.Kitchen, req.AgentRef, "output", nil, err)
			blocked = policyUnavailable()
			return text, false
		}
		if !eval.Passed {
			g.h.emitGuardrailAudit(ctx, req.Kitchen, req.AgentRef, "output", eval, nil)
			blocked = &models.GuardrailBlockedError{Stage: "output", Results: eval.Results}
			return text, false
		}
		g.h.noteGuardrails(ctx, req.Kitchen, req.AgentRef, "output", eval)
		return eval.Apply(text), eval.Redacted > 0
	}
	out.Content, _ = judge(resp.Content)
	if len(resp.ThinkingBlocks) > 0 {
		out.ThinkingBlocks = append([]models.ThinkingBlock(nil), resp.ThinkingBlocks...)
		for i := range out.ThinkingBlocks {
			out.ThinkingBlocks[i].Content, _ = judge(out.ThinkingBlocks[i].Content)
		}
	}
	if len(resp.ToolCalls) > 0 {
		out.ToolCalls = append([]models.ToolCallResult(nil), resp.ToolCalls...)
		for i := range out.ToolCalls {
			args, changed := judge(strings.TrimSpace(out.ToolCalls[i].Function.Arguments))
			if changed && !json.Valid([]byte(args)) {
				blocked = &models.GuardrailBlockedError{Stage: "output"}
			}
			if changed {
				out.ToolCalls[i].Function.Arguments = args
			}
		}
	}
	if len(resp.ContentParts) > 0 {
		out.ContentParts = append([]models.ContentPart(nil), resp.ContentParts...)
		for i := range out.ContentParts {
			out.ContentParts[i].Text, _ = judge(out.ContentParts[i].Text)
		}
	}
	if blocked != nil {
		return nil, blocked
	}
	return &out, nil
}

// maxRawRouteBody bounds what /models/route accepts: prompts with media parts are large, nothing legitimate
// is this large.
const maxRawRouteBody = 32 << 20

// rawRouteRequest reads the body of /models/route and /models/route/stream into the request the router gets.
// What the caller may not choose is not taken from the body: the kitchen is the one the caller authenticated
// into (a body that names another one is overwritten, never honoured, since it would spend that kitchen's
// providers, budget, rate limits and cache), and the agent a call is attributed to has to be an agent of that
// kitchen the caller may use, so nobody can charge another agent's budget or invent names in the ledger.
func (h *Handlers) rawRouteRequest(w http.ResponseWriter, r *http.Request) (*models.RouteRequest, bool) {
	var req models.RouteRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRawRouteBody)).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return nil, false
	}
	kitchen := middleware.GetKitchen(r.Context())
	if kitchen == "" {
		kitchen = "default"
	}
	req.Kitchen = kitchen
	if req.AgentRef != "" {
		if _, err := h.Store.GetAgent(r.Context(), kitchen, req.AgentRef); err != nil {
			respondError(w, http.StatusNotFound, "agent_ref names no agent in this kitchen")
			return nil, false
		}
		if !h.canAccessAgent(r.Context(), kitchen, req.AgentRef) {
			respondError(w, http.StatusForbidden, "this credential may not use that agent")
			return nil, false
		}
	}
	return &req, true
}

// replyJudge judges a reply with the rules given (an agent's own and the kitchen's), for a handler that calls
// the router itself (router.ContextReplyJudge). A reply a rule blocks, or that cannot be judged, is refused;
// redactions are applied before the router records the call.
func (h *Handlers) replyJudge(kitchen, agent string, gr []models.Guardrail) router.ReplyJudge {
	return func(ctx context.Context, _ *models.RouteRequest, resp *models.RouteResponse) (*models.RouteResponse, error) {
		if h.Guardrails == nil || len(gr) == 0 || strings.TrimSpace(resp.Content) == "" {
			return resp, nil
		}
		eval, err := h.Guardrails.EvaluateOutput(ctx, gr, resp.Content)
		if err != nil || eval == nil {
			log.Error().Err(err).Str("agent", agent).Msg("the reply could not be judged by the guardrails; it is withheld")
			h.emitGuardrailAudit(ctx, kitchen, agent, "output", nil, err)
			return nil, &models.GuardrailBlockedError{Stage: "policy"}
		}
		if !eval.Passed {
			h.emitGuardrailAudit(ctx, kitchen, agent, "output", eval, nil)
			return nil, &models.GuardrailBlockedError{Stage: "output", Results: eval.Results}
		}
		h.noteGuardrails(ctx, kitchen, agent, "output", eval)
		if eval.Redacted == 0 {
			return resp, nil
		}
		own := *resp
		own.Content = eval.Text
		return &own, nil
	}
}
