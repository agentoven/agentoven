package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/rs/zerolog/log"
)

// Output guardrails need the whole reply before a caller may see any of it, so a stream
// from an agent that has any enabled output rule is held back, checked, then released.
// The alternative, checking after the tokens are already on the wire, can only record a
// violation, not prevent it. The cost is that a guarded agent does not stream token by
// token; an agent with no output rules streams exactly as before.
//
// This applies the same way to every runtime: the Go executor, a hosted LangChain,
// LangGraph or Deep Agents process behind /invoke/stream, and an A2A streaming call.

// outputStageEnabled reports whether any enabled guardrail applies to output. A rule with
// no stage applies to every stage (guardrails.appliesToStage).
func outputStageEnabled(gr []models.Guardrail) bool {
	for _, g := range gr {
		if !g.Enabled {
			continue
		}
		switch g.Stage {
		case models.GuardrailStageOutput, models.GuardrailStageBoth, "":
			return true
		}
	}
	return false
}

// holdBackNeeded is true when a stream must be buffered and checked before release.
func (h *Handlers) holdBackNeeded(gr []models.Guardrail) bool {
	return h.Guardrails != nil && outputStageEnabled(gr)
}

// outputVerdict checks text against the output guardrails. It fails closed: if the
// guardrails cannot be evaluated the text is treated as blocked, because a guardrail that
// errors out and lets the content through is a guardrail that was skipped.
func (h *Handlers) outputVerdict(ctx context.Context, agent *models.Agent, gr []models.Guardrail, text string) (bool, *models.GuardrailEvaluation) {
	if h.Guardrails == nil || len(gr) == 0 || strings.TrimSpace(text) == "" {
		return true, nil
	}
	eval, err := h.Guardrails.EvaluateOutput(ctx, gr, text)
	if err != nil {
		log.Error().Err(err).Str("agent", agent.Name).Msg("output guardrail evaluation failed; blocking the reply")
		h.emitGuardrailAudit(ctx, agent.Kitchen, agent.Name, "output", nil, err)
		return false, nil
	}
	if !eval.Passed {
		h.emitGuardrailAudit(ctx, agent.Kitchen, agent.Name, "output", eval, nil)
		return false, eval
	}
	return true, eval
}

// respondOutputBlocked is the 403 a held-back stream gets instead of its body. It carries
// the rule results but none of the blocked text.
func respondOutputBlocked(w http.ResponseWriter, eval *models.GuardrailEvaluation) {
	body := map[string]interface{}{"error": "Output blocked by guardrails"}
	if eval != nil {
		body["guardrails"] = eval.Results
	}
	respondJSON(w, http.StatusForbidden, body)
}

// sseVisibleTexts returns the texts a caller would see in an SSE stream, one entry per
// channel: the streamed chunks joined into one reply, then each final or summary frame
// (a "done" that repeats the whole reply, a "result", and so on) on its own. They must be
// checked separately. Joined, a reply that appears twice runs together ("...EXAMPLEAKIA...")
// and the word boundary a pattern like \bAKIA[0-9A-Z]{16}\b relies on is gone, so the rule
// stops matching text the caller received in full.
//
// It reads only top-level string fields of each frame, so a final frame that carries a whole
// trace (and with it the user's own input and the system prompt) is not mistaken for the
// agent's reply.
func sseVisibleTexts(body []byte) []string {
	var chunks strings.Builder
	var finals []string
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var frame map[string]interface{}
		if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &frame) != nil {
			continue
		}
		typ, _ := frame["type"].(string)
		isChunk := typ == "" || typ == "token" || typ == "text" || typ == "delta" || typ == "content"
		var parts []string
		for _, key := range []string{"content", "text", "response", "delta", "result", "output"} {
			if s, ok := frame[key].(string); ok && s != "" {
				parts = append(parts, s)
			}
		}
		// OpenAI-style frames nest the text under choices[].delta.content.
		if choices, ok := frame["choices"].([]interface{}); ok {
			isChunk = true
			for _, c := range choices {
				if cm, ok := c.(map[string]interface{}); ok {
					if d, ok := cm["delta"].(map[string]interface{}); ok {
						if s, ok := d["content"].(string); ok && s != "" {
							parts = append(parts, s)
						}
					}
				}
			}
		}
		if isChunk {
			chunks.WriteString(strings.Join(parts, ""))
		} else {
			finals = append(finals, parts...)
		}
	}
	out := finals
	if chunks.Len() > 0 {
		out = append([]string{chunks.String()}, finals...)
	}
	return out
}

// a2aStreamText collects the text parts of an A2A streaming response (SSE frames or
// newline-delimited JSON-RPC). It looks only at parts arrays under a frame's result and
// skips history and metadata, which hold the caller's own message.
func a2aStreamText(body []byte) string {
	var out []string
	var walk func(v interface{}, depth int)
	walk = func(v interface{}, depth int) {
		if depth > 8 {
			return
		}
		switch t := v.(type) {
		case map[string]interface{}:
			for k, child := range t {
				switch k {
				case "history", "metadata", "request", "params":
					continue
				case "parts":
					if arr, ok := child.([]interface{}); ok {
						for _, p := range arr {
							if pm, ok := p.(map[string]interface{}); ok {
								if s, ok := pm["text"].(string); ok && s != "" {
									out = append(out, s)
								}
							}
						}
					}
				default:
					walk(child, depth+1)
				}
			}
		case []interface{}:
			for _, child := range t {
				walk(child, depth+1)
			}
		}
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if line == "" {
			continue
		}
		var frame map[string]interface{}
		if json.Unmarshal([]byte(line), &frame) != nil {
			continue
		}
		if res, ok := frame["result"]; ok {
			walk(res, 0)
		}
	}
	return strings.Join(out, "\n")
}

// streamProcess serves /invoke/stream for an agent running as a separate process (a
// LangChain, LangGraph, Deep Agents or custom runtime). With no output rules the stream is
// relayed as it arrives. With output rules it is buffered, checked, and only then released.
func (h *Handlers) streamProcess(w http.ResponseWriter, r *http.Request, agent *models.Agent, gr []models.Guardrail, message string, vars map[string]string, sessionID string) {
	ctx, endRun := h.beginHostedRun(withCallerSession(r.Context(), sessionID), agent)
	defer endRun()
	if !h.holdBackNeeded(gr) {
		if err := h.proxyToProcessStream(ctx, w, agent, message, vars); err != nil {
			respondError(w, http.StatusBadGateway, err.Error())
		}
		return
	}

	held := newBufferedResponse()
	if err := h.proxyToProcessStream(ctx, held, agent, message, vars); err != nil {
		respondError(w, http.StatusBadGateway, err.Error())
		return
	}
	for _, text := range sseVisibleTexts(held.body.Bytes()) {
		if ok, eval := h.outputVerdict(r.Context(), agent, gr, text); !ok {
			respondOutputBlocked(w, eval)
			return
		}
	}
	held.replay(w)
}

// heldEvents buffers the Go executor's stream events until the reply has been checked.
type heldEvents struct {
	frames [][]byte
	text   strings.Builder
}

func (e *heldEvents) add(ev executor.Event) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	e.frames = append(e.frames, payload)
	if ev.Type == executor.EventToken {
		e.text.WriteString(ev.Content)
	}
	return nil
}

// release checks everything the caller would have seen (the streamed tokens and the final
// answer), then writes the buffered frames. It returns false, having already answered the
// request with a 403, if the reply is blocked.
func (h *Handlers) releaseHeld(w http.ResponseWriter, r *http.Request, agent *models.Agent, gr []models.Guardrail, e *heldEvents, final string) bool {
	for _, text := range []string{e.text.String(), final} {
		if ok, eval := h.outputVerdict(r.Context(), agent, gr, text); !ok {
			respondOutputBlocked(w, eval)
			return false
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	for _, f := range e.frames {
		if _, err := fmt.Fprintf(w, "data: %s\n\n", f); err != nil {
			return true
		}
	}
	if fl, ok := w.(http.Flusher); ok {
		fl.Flush()
	}
	return true
}

// ── Session identity for hosted frameworks ────────────────────

type proxySessionKey struct{}

// withProxySession tells proxyToProcess which conversation a call belongs to. A hosted
// framework keeps its own memory (LangGraph and Deep Agents key a checkpointer on a thread
// ID), so the session ID is passed through as both session_id and thread_id.
func withProxySession(ctx context.Context, sessionID string) context.Context {
	if sessionID == "" {
		return ctx
	}
	return context.WithValue(ctx, proxySessionKey{}, sessionID)
}

// validSessionID is what a caller-supplied session ID may look like before it is handed to a
// hosted process as a thread ID: it ends up in the framework's checkpointer, so it should be a
// plain token, not arbitrary text.
var validSessionID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// withCallerSession is withProxySession for an ID that came from the request body. An ID that is
// not a plain token is dropped, and the call runs without a conversation.
func withCallerSession(ctx context.Context, sessionID string) context.Context {
	if !validSessionID.MatchString(sessionID) {
		return ctx
	}
	return withProxySession(ctx, sessionID)
}

func proxySessionFrom(ctx context.Context) string {
	s, _ := ctx.Value(proxySessionKey{}).(string)
	return s
}
