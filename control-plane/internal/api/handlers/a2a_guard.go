package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/rs/zerolog/log"
)

// Agents are reached over A2A by recipe steps, other agents and external callers, either
// in-process (the executor) or on a separately deployed process. Both go through the same
// guardrails: workspace rules on top of the agent's own, checked on what comes in and, for a
// response that is returned whole, on what goes out. These helpers do that for the proxied
// path; handleA2ATaskSend does the same around the executor.

// a2aRequest is the part of a JSON-RPC A2A request the guardrails care about.
type a2aRequest struct {
	rpcID     interface{}
	text      string // the text parts of the message
	hasMsg    bool   // the method sends a message to the agent
	streaming bool   // the response is a stream the proxy cannot buffer
}

func parseA2ARequest(body []byte) a2aRequest {
	var env struct {
		ID     interface{} `json:"id"`
		Method string      `json:"method"`
		Params struct {
			Message struct {
				Parts []struct {
					Type string `json:"type"`
					Kind string `json:"kind"`
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"message"`
		} `json:"params"`
	}
	if json.Unmarshal(body, &env) != nil {
		return a2aRequest{}
	}
	m := strings.ToLower(env.Method)
	req := a2aRequest{rpcID: env.ID}
	req.hasMsg = strings.Contains(m, "send") || strings.Contains(m, "stream") || strings.Contains(m, "subscribe")
	req.streaming = strings.Contains(m, "stream") || strings.Contains(m, "subscribe")
	var texts []string
	for _, p := range env.Params.Message.Parts {
		if (p.Type == "text" || p.Kind == "text" || (p.Type == "" && p.Kind == "")) && p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	req.text = strings.Join(texts, "\n")
	return req
}

// a2aResponseText collects the text an A2A result carries: artifacts, the status message,
// or a direct message.
func a2aResponseText(body []byte) string {
	var env struct {
		Result struct {
			Artifacts []struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"artifacts"`
			Status struct {
				Message struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"message"`
			} `json:"status"`
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"result"`
	}
	if json.Unmarshal(body, &env) != nil {
		return ""
	}
	var texts []string
	for _, a := range env.Result.Artifacts {
		for _, p := range a.Parts {
			if p.Text != "" {
				texts = append(texts, p.Text)
			}
		}
	}
	for _, p := range env.Result.Status.Message.Parts {
		if p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	for _, p := range env.Result.Parts {
		if p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func a2aRPCError(w http.ResponseWriter, id interface{}, code int, message string, data interface{}) {
	w.Header().Set("Content-Type", "application/a2a+json")
	e := map[string]interface{}{"code": code, "message": message}
	if data != nil {
		e["data"] = data
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "error": e, "id": id})
}

// bufferedResponse holds a proxied response so it can be checked before the caller sees it.
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newBufferedResponse() *bufferedResponse {
	return &bufferedResponse{header: http.Header{}, status: http.StatusOK}
}
func (b *bufferedResponse) Header() http.Header         { return b.header }
func (b *bufferedResponse) WriteHeader(code int)        { b.status = code }
func (b *bufferedResponse) Write(p []byte) (int, error) { return b.body.Write(p) }

func (b *bufferedResponse) replay(w http.ResponseWriter) {
	for k, vals := range b.header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(b.status)
	_, _ = w.Write(b.body.Bytes())
}

// proxyA2AGuarded proxies an A2A call to the agent's backend with its guardrails around it.
// own is the agent's list, or that list after an environment policy; the workspace
// guardrails are applied on top. If they cannot be established the call is refused and
// nothing is sent to the agent.
//
// A response is checked only when it is returned whole (tasks/send). A streamed response
// (message/stream, tasks/sendSubscribe) has its input checked but is passed through
// unchecked: it cannot be held back without breaking the stream.
func (h *Handlers) proxyA2AGuarded(w http.ResponseWriter, r *http.Request, backendURL string, agent *models.Agent, own []models.Guardrail, envSlug string) {
	gr, err := h.guardrailsFor(r.Context(), agent, own)
	if err != nil {
		log.Error().Err(err).Str("agent", agent.Name).Msg("refusing A2A call: workspace guardrails could not be applied")
		h.emitGuardrailAudit(r.Context(), agent.Kitchen, agent.Name, "policy", nil, err)
		a2aRPCError(w, nil, -32004, "The guardrail policy for this agent could not be applied, so the agent was not run", nil)
		return
	}
	if h.Guardrails == nil || len(gr) == 0 {
		h.proxyA2ARequest(w, r, backendURL, agent.Name)
		return
	}

	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	req := parseA2ARequest(body)
	if !req.hasMsg {
		h.proxyA2ARequest(w, r, backendURL, agent.Name)
		return
	}

	audit := func(stage string, eval *models.GuardrailEvaluation, gErr error) {
		if envSlug != "" {
			h.emitGuardrailAuditEnv(r.Context(), agent.Kitchen, agent.Name, envSlug, stage, eval, gErr)
		} else {
			h.emitGuardrailAudit(r.Context(), agent.Kitchen, agent.Name, stage, eval, gErr)
		}
	}

	input := req.text
	if input == "" {
		input = string(body) // an unfamiliar message shape: check the whole envelope rather than nothing
	}
	if eval, gErr := h.Guardrails.EvaluateInput(r.Context(), gr, input); gErr != nil {
		log.Warn().Err(gErr).Str("agent", agent.Name).Msg("Input guardrail evaluation error (A2A)")
		audit("input", nil, gErr)
	} else if !eval.Passed {
		audit("input", eval, nil)
		a2aRPCError(w, req.rpcID, -32001, "Input blocked by guardrails", eval.Results)
		return
	}

	if req.streaming {
		h.proxyA2ARequest(w, r, backendURL, agent.Name)
		return
	}

	rec := newBufferedResponse()
	h.proxyA2ARequest(rec, r, backendURL, agent.Name)
	if out := a2aResponseText(rec.body.Bytes()); out != "" {
		if eval, gErr := h.Guardrails.EvaluateOutput(r.Context(), gr, out); gErr != nil {
			log.Warn().Err(gErr).Str("agent", agent.Name).Msg("Output guardrail evaluation error (A2A)")
			audit("output", nil, gErr)
		} else if !eval.Passed {
			audit("output", eval, nil)
			a2aRPCError(w, req.rpcID, -32001, "Output blocked by guardrails", eval.Results)
			return
		}
	}
	rec.replay(w)
}
