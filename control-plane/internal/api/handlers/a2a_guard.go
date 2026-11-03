package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/google/uuid"
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
	text      string   // the text parts of the message, assembled as the agent receives them (a2aJoinText)
	parts     []string // the same text parts one by one, each judged on its own as well
	hasMsg    bool     // the method sends a message to the agent
	streaming bool     // the response is a stream the proxy cannot buffer
}

// maxA2ATextParts bounds how many text parts of one message are judged one by one. A message with more
// is refused: judging each part costs a guardrail evaluation, and no real caller splits text that finely.
const maxA2ATextParts = 256

// a2aJoinText is the one way the text parts of a message become the agent's input: concatenated, with
// nothing between them. The pod does exactly this (agent_runner.py) and so does the in-process handler
// (handleA2ATaskSend); the guard judges this text so that it judges what the agent receives. A guard that
// joined with a newline missed every pattern (a blocked word, a card number, an injection phrase) cut at a
// part boundary, which reached the model whole.
func a2aJoinText(parts []string) string { return strings.Join(parts, "") }

func parseA2ARequest(body []byte) a2aRequest {
	var env struct {
		ID     interface{} `json:"id"`
		Method string      `json:"method"`
		Params struct {
			Message struct {
				Parts []struct {
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
	// Any part that carries text counts, whatever its declared type: the pod reads every part with a
	// "text" field, so a part typed "data" with text in it reaches the agent too.
	for _, p := range env.Params.Message.Parts {
		if p.Text != "" {
			req.parts = append(req.parts, p.Text)
		}
	}
	req.text = a2aJoinText(req.parts)
	return req
}

// joinSeparator is what the evaluated text of a stage was joined with: a request's text parts as the agent
// receives them (a2aJoinText), a result's parts as a2aResponseText lays them out.
func joinSeparator(stage models.GuardrailStage) string {
	if stage == models.GuardrailStageInput {
		return ""
	}
	return "\n"
}

// firstBlockedPart judges every text part of a message on its own, on top of the joined text the agent
// receives, and returns the first evaluation that blocks. A rule anchored to the start or end of the text
// (or one that measures length) sees a part that the join would bury in the middle of another. A message
// of a single part is the joined text itself and is judged once, by the caller. An evaluation that fails
// is skipped here: the joined text is judged by the caller, which handles that failure.
func (h *Handlers) firstBlockedPart(ctx context.Context, gr []models.Guardrail, parts []string) *models.GuardrailEvaluation {
	if h.Guardrails == nil || len(gr) == 0 || len(parts) < 2 {
		return nil
	}
	for _, part := range parts {
		if eval, err := h.Guardrails.EvaluateInput(ctx, gr, part); err == nil && !eval.Passed {
			return eval
		}
	}
	return nil
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
// A response is checked before the caller sees any of it. A streamed response
// (message/stream, tasks/sendSubscribe) is held back and checked too when the agent has
// output rules, so such an agent does not stream token by token; with no output rules the
// stream is relayed as it arrives.
func (h *Handlers) proxyA2AGuarded(w http.ResponseWriter, r *http.Request, backendURL string, agent *models.Agent, own []models.Guardrail, envSlug string) {
	if h.a2aFilesInProcess(w, r, agent, envSlug) {
		return
	}
	// The call to the process is accounted to a run, so its model calls can be held to a budget.
	hostedCtx, endRun := h.beginHostedRun(r.Context(), agent)
	defer endRun()
	toolCtx, closeTools := h.openToolScope(hostedCtx, agent)
	defer closeTools()
	r = r.WithContext(toolCtx)

	// Every run of a bot is registered with the bot manager before it starts. A message that starts
	// work (send, stream) is registered here under the run ID the process will be sent; the other
	// methods (get, cancel) start nothing and pass through.
	if agent.IsAgenticBot() {
		raw, ok := readA2ABody(w, r)
		if !ok {
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		if req := parseA2ARequest(raw); req.hasMsg {
			runID := hostedRunFrom(r.Context())
			if runID == "" {
				runID = uuid.New().String()
				r = r.WithContext(context.WithValue(r.Context(), hostedRunKey{}, runID))
			}
			text := req.text
			if text == "" {
				text = string(raw)
			}
			if err := h.admitBotInvoke(r.Context(), agent, runID, text); err != nil {
				a2aRPCError(w, req.rpcID, -32007, "The bot could not be started: "+err.Error(), nil)
				return
			}
		}
	}

	gr, err := h.guardrailsFor(r.Context(), agent, own)
	if err != nil {
		log.Error().Err(err).Str("agent", agent.Name).Msg("refusing A2A call: workspace guardrails could not be applied")
		h.emitGuardrailAudit(r.Context(), agent.Kitchen, agent.Name, "policy", nil, err)
		a2aRPCError(w, nil, -32004, "The guardrail policy for this agent could not be applied, so the agent was not run", nil)
		return
	}
	if h.Guardrails == nil || len(gr) == 0 {
		h.proxyA2ARequest(w, r, backendURL, agent.Name, agent.Kitchen, agent.IsManaged())
		return
	}

	body, ok := readA2ABody(w, r)
	if !ok {
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	req := parseA2ARequest(body)
	if len(req.parts) > maxA2ATextParts {
		a2aRPCError(w, req.rpcID, -32602, "A message may have at most 256 text parts", nil)
		return
	}

	audit := func(stage string, eval *models.GuardrailEvaluation, gErr error) {
		if envSlug != "" {
			h.emitGuardrailAuditEnv(r.Context(), agent.Kitchen, agent.Name, envSlug, stage, eval, gErr)
		} else {
			h.emitGuardrailAudit(r.Context(), agent.Kitchen, agent.Name, stage, eval, gErr)
		}
	}

	// A method that sends no message (tasks/get, tasks/cancel) has no input to judge, but what it
	// returns is a task's content, and a pod may have stored it before any output rule redacted it.
	if req.hasMsg {
		if eval := h.firstBlockedPart(r.Context(), gr, req.parts); eval != nil {
			audit("input", eval, nil)
			a2aRPCError(w, req.rpcID, -32001, "Input blocked by guardrails", eval.Results)
			return
		}
		input := req.text
		if input == "" {
			input = string(body) // an unfamiliar message shape: check the whole envelope rather than nothing
		} else if extra := a2aNonTextRequest(body); extra != "" {
			// File names and text files, data parts: judged with the text. A redaction in them cannot be
			// written back part by part, so redactA2A refuses the message then (fails closed).
			input += "\n" + extra
		}
		if eval, gErr := h.Guardrails.EvaluateInput(r.Context(), gr, input); gErr != nil {
			log.Warn().Err(gErr).Str("agent", agent.Name).Msg("Input guardrail evaluation error (A2A)")
			audit("input", nil, gErr)
		} else if !eval.Passed {
			audit("input", eval, nil)
			a2aRPCError(w, req.rpcID, -32001, "Input blocked by guardrails", eval.Results)
			return
		} else if eval.Redacted > 0 {
			rewritten, ok := h.redactA2A(body, req.text == "", eval, gr, models.GuardrailStageInput, a2aRequestParts)
			if !ok {
				refused := *eval
				refused.Passed = false // the redaction could not be applied to the message, so it is refused
				audit("input", &refused, nil)
				a2aRPCError(w, req.rpcID, -32001, "Input blocked by guardrails", eval.Results)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(rewritten))
			audit("input", eval, nil)
		} else if eval.Fired() {
			audit("input", eval, nil)
		}
	}

	if (req.streaming || !req.hasMsg) && !outputStageEnabled(gr) {
		// Nothing to check on the way out: relay the stream as it arrives.
		h.proxyA2ARequest(w, r, backendURL, agent.Name, agent.Kitchen, agent.IsManaged())
		return
	}

	rec := newBufferedResponse()
	h.proxyA2ARequest(rec, r, backendURL, agent.Name, agent.Kitchen, agent.IsManaged())
	out := a2aResponseText(rec.body.Bytes())
	if extra := a2aNonTextResponse(rec.body.Bytes()); extra != "" && !req.streaming {
		out = joinNonEmpty([]string{out, extra})
	}
	if req.streaming {
		// An agent with output rules does not stream: the frames are held back, checked,
		// then released (stream_guard.go).
		out = a2aStreamText(rec.body.Bytes())
	}
	if out != "" {
		if eval, gErr := h.Guardrails.EvaluateOutput(r.Context(), gr, out); gErr != nil {
			log.Warn().Err(gErr).Str("agent", agent.Name).Msg("Output guardrail evaluation error (A2A)")
			audit("output", nil, gErr)
		} else if !eval.Passed {
			audit("output", eval, nil)
			a2aRPCError(w, req.rpcID, -32001, "Output blocked by guardrails", eval.Results)
			return
		} else if eval.Redacted > 0 {
			// Frames of a held-back stream cannot be rewritten; a whole response can.
			var rewritten []byte
			ok := false
			if !req.streaming {
				rewritten, ok = h.redactA2A(rec.body.Bytes(), false, eval, gr, models.GuardrailStageOutput, a2aResponseParts)
			}
			if !ok {
				refused := *eval
				refused.Passed = false
				audit("output", &refused, nil)
				a2aRPCError(w, req.rpcID, -32001, "Output blocked by guardrails", eval.Results)
				return
			}
			rec.body.Reset()
			rec.body.Write(rewritten)
			audit("output", eval, nil)
		} else if eval.Fired() {
			audit("output", eval, nil)
		}
	}
	rec.replay(w)
}

// a2aFilesInProcess answers a tasks/send that carries file parts for an agent on AgentOven's
// own loop. The pod that agent runs in reads the text of a message and nothing else, so
// forwarding the files would drop them without a word. The control plane's own executor
// takes them (the same path POST /a2a with metadata.agent_name uses). It reports whether it
// answered; false means the request has no files, or the agent is not one of these, and the
// proxy goes ahead as before.
func (h *Handlers) a2aFilesInProcess(w http.ResponseWriter, r *http.Request, agent *models.Agent, envSlug string) bool {
	if !agent.IsManaged() || agent.IsFrameworkNative() || agent.IsAgenticBot() || r.Body == nil {
		return false
	}
	raw, ok := readA2ABody(w, r)
	if !ok {
		return true // answered: too large
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var env struct {
		ID     interface{}            `json:"id"`
		Method string                 `json:"method"`
		Params map[string]interface{} `json:"params"`
	}
	if json.Unmarshal(raw, &env) != nil || !a2aHasFileParts(env.Params) {
		return false
	}
	if env.Method != "tasks/send" {
		a2aRPCError(w, env.ID, -32602, "File parts are accepted on tasks/send", nil)
		return true
	}
	if envSlug != "" {
		// The environment's workload reads text only and the control plane's executor is not
		// that workload: say so rather than answer from a different deployment.
		a2aRPCError(w, env.ID, -32602, "File parts are not supported on environment-scoped endpoints; call /agents/"+agent.Name+"/a2a", nil)
		return true
	}
	// The route named the agent: a caller cannot use it to reach another one.
	meta, _ := env.Params["metadata"].(map[string]interface{})
	if meta == nil {
		meta = map[string]interface{}{}
	}
	meta["agent_name"] = agent.Name
	env.Params["metadata"] = meta
	params, err := json.Marshal(env.Params)
	if err != nil {
		return false
	}
	h.handleA2ATaskSend(w, r, params, env.ID)
	return true
}

// a2aHasFileParts reports whether an A2A message's parts include a file part.
func a2aHasFileParts(params map[string]interface{}) bool {
	msg, _ := params["message"].(map[string]interface{})
	parts, _ := msg["parts"].([]interface{})
	for _, p := range parts {
		pm, _ := p.(map[string]interface{})
		if t, _ := pm["type"].(string); t == "file" {
			return true
		}
		if k, _ := pm["kind"].(string); k == "file" {
			return true
		}
	}
	return false
}

// a2aRequestParts and a2aResponseParts find the text parts of a JSON-RPC A2A request and
// response: the places a redaction has to be written back to.
func a2aRequestParts(doc map[string]interface{}) []map[string]interface{} {
	params, _ := doc["params"].(map[string]interface{})
	msg, _ := params["message"].(map[string]interface{})
	return textParts(msg["parts"])
}

func a2aResponseParts(doc map[string]interface{}) []map[string]interface{} {
	// The order of a2aResponseText, which is the order the evaluated text was joined in.
	res, _ := doc["result"].(map[string]interface{})
	var out []map[string]interface{}
	if arts, ok := res["artifacts"].([]interface{}); ok {
		for _, a := range arts {
			if am, ok := a.(map[string]interface{}); ok {
				out = append(out, textParts(am["parts"])...)
			}
		}
	}
	if status, ok := res["status"].(map[string]interface{}); ok {
		if msg, ok := status["message"].(map[string]interface{}); ok {
			out = append(out, textParts(msg["parts"])...)
		}
	}
	return append(out, textParts(res["parts"])...)
}

func textParts(v interface{}) []map[string]interface{} {
	list, _ := v.([]interface{})
	var out []map[string]interface{}
	for _, p := range list {
		if pm, ok := p.(map[string]interface{}); ok {
			if _, isText := pm["text"].(string); isText {
				out = append(out, pm)
			}
		}
	}
	return out
}

// redactA2A writes an evaluation's redactions into a JSON-RPC body. Each text part is scrubbed
// on its own with the same rules, which gives the same result as redacting the joined text
// when the rules are local detectors. If the parts do not add up to what the evaluation
// produced (a custom endpoint rewrote the text, or a match spanned two parts) the redaction
// cannot be applied faithfully and ok is false: the caller refuses the message. whole is the
// case of a message with no text parts, where the evaluation was of the entire body.
func (h *Handlers) redactA2A(body []byte, whole bool, eval *models.GuardrailEvaluation, gr []models.Guardrail, stage models.GuardrailStage, find func(map[string]interface{}) []map[string]interface{}) ([]byte, bool) {
	if whole {
		if json.Valid([]byte(eval.Text)) {
			return []byte(eval.Text), true
		}
		return nil, false
	}
	svc, ok := h.Guardrails.(contracts.StageGuardrailService)
	if !ok {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber() // ids and numbers come back exactly as they went in
	var doc map[string]interface{}
	if dec.Decode(&doc) != nil {
		return nil, false
	}
	var joined []string
	for _, part := range find(doc) {
		text, _ := part["text"].(string)
		scrubbed, _ := svc.Scrub(gr, text, stage)
		part["text"] = scrubbed
		if scrubbed != "" {
			joined = append(joined, scrubbed)
		}
	}
	// The evaluated text was assembled the way the agent receives it (a request, a2aJoinText) or the way
	// a2aResponseText joins a result; the scrubbed parts must add up to the same text.
	if sep := joinSeparator(stage); strings.Join(joined, sep) != eval.Text {
		return nil, false
	}
	out, err := json.Marshal(doc)
	return out, err == nil
}
