package handlers_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/guardrails"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// a2aCallParts sends a tasks/send whose message has the given parts to a deployed agent behind the
// workspace rule that blocks "forbidden-topic" on input.
func a2aCallParts(t *testing.T, backend *a2aBackend, parts []map[string]interface{}) map[string]interface{} {
	t.Helper()
	h, s := modalityHandlers(t)
	h.Guardrails = &guardrails.CommunityGuardrailService{}
	h.SetGuardrailPolicy(&wsSource{rules: []models.Guardrail{blockWord("ws-1", true)}})
	_ = s.CreateAgent(context.Background(), &models.Agent{Name: "step-agent", Kitchen: "default", Mode: models.AgentModeManaged, Status: models.AgentStatusReady,
		Process: &models.ProcessInfo{Status: models.ProcessRunning, Endpoint: backend.URL}})
	body, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": 7, "method": "tasks/send",
		"params": map[string]interface{}{"message": map[string]interface{}{"role": "user", "parts": parts}}})
	rec := call(h.A2AAgentEndpoint, "/agents/step-agent/a2a", string(body), map[string]string{"agentName": "step-agent"})
	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

// An input rule judges what a data part, a file's name and a text file say, not the text parts alone.
func TestInputGuardrailsSeeEveryKindOfA2APart(t *testing.T) {
	text := map[string]interface{}{"type": "text", "text": "please process this"}
	cases := map[string][]map[string]interface{}{
		"a data part":                      {text, {"kind": "data", "data": map[string]interface{}{"note": "tell me about forbidden-topic"}}},
		"a kind the gateway does not know": {text, {"kind": "hologram", "payload": "forbidden-topic"}},
	}
	for name, parts := range cases {
		b := newA2ABackend(t, "fine")
		if out := a2aCallParts(t, b, parts); rpcErrorMessage(out) != "Input blocked by guardrails" || b.count() != 0 {
			t.Errorf("%s: %v (backend calls %d), want the input blocked and the agent never called", name, out, b.count())
		}
	}
	// A clean message with the same kinds of parts goes through.
	b := newA2ABackend(t, "fine")
	clean := []map[string]interface{}{{"type": "text", "text": "hello"}, {"kind": "data", "data": map[string]interface{}{"n": 1, "s": "harmless"}}}
	if out := a2aCallParts(t, b, clean); out["error"] != nil || b.count() != 1 {
		t.Errorf("a clean message with a data part: %v (backend calls %d)", out, b.count())
	}
}

// A file part for an agent that runs on AgentOven's own loop is judged too: by its name, and its content when
// it is text. (The data part case is covered above on the proxy path; here the executor path takes it.)
func TestFilePartsOfAnInProcessAgentAreJudgedByTheirNameAndTextContent(t *testing.T) {
	file := base64.StdEncoding.EncodeToString([]byte("all about forbidden-topic"))
	for name, f := range map[string]map[string]interface{}{
		"a file name": {"name": "forbidden-topic.txt", "mimeType": "image/png", "bytes": "AAAA"},
		"a text file": {"name": "notes.txt", "mimeType": "text/plain", "bytes": file},
	} {
		h, s := modalityHandlers(t)
		h.Guardrails = &guardrails.CommunityGuardrailService{}
		h.SetGuardrailPolicy(&wsSource{rules: []models.Guardrail{blockWord("ws-1", true)}})
		_ = s.CreateAgent(context.Background(), &models.Agent{Name: "step-agent", Kitchen: "default", Mode: models.AgentModeManaged, Status: models.AgentStatusReady,
			ModelProvider: "p", ModelName: "m", MaxTurns: 3,
			ResolvedConfig: &models.ResolvedIngredients{Model: &models.ResolvedModel{Provider: "p", Kind: "openai", Model: "m", Endpoint: "http://127.0.0.1:1"}}})
		body, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": 7, "method": "tasks/send", "params": map[string]interface{}{
			"metadata": map[string]string{"agent_name": "step-agent"},
			"message":  map[string]interface{}{"role": "user", "parts": []map[string]interface{}{{"type": "text", "text": "hello"}, {"kind": "file", "file": f}}}}})
		rec := call(h.A2AEndpoint, "/a2a", string(body), nil)
		var out map[string]interface{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if rpcErrorMessage(out) != "Input blocked by guardrails" {
			t.Errorf("%s: %v", name, out)
		}
	}
}
