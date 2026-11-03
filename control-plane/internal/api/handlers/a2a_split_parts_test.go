package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/guardrails"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// a pod stands in for an agent process: it keeps what it was sent, and assembles the text the way the
// real pod does (agent_runner.py joins the text parts with nothing).
type a2aPod struct {
	*httptest.Server
	mu       sync.Mutex
	received []string
}

func newA2APod(t *testing.T, reply string) *a2aPod {
	t.Helper()
	p := &a2aPod{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var env struct {
			Params struct {
				Message struct {
					Parts []map[string]interface{} `json:"parts"`
				} `json:"message"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&env)
		var text string
		for _, part := range env.Params.Message.Parts {
			if s, ok := part["text"].(string); ok {
				text += s
			}
		}
		p.mu.Lock()
		p.received = append(p.received, text)
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/a2a+json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": 1, "result": map[string]interface{}{
			"status":    map[string]string{"state": "completed"},
			"artifacts": []map[string]interface{}{{"parts": []map[string]string{{"type": "text", "text": reply}}}},
		}})
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *a2aPod) texts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.received...)
}

func a2aSplit(t *testing.T, rules []models.Guardrail, pod *a2aPod, texts ...string) map[string]interface{} {
	t.Helper()
	h, s := modalityHandlers(t)
	h.Guardrails = &guardrails.CommunityGuardrailService{}
	h.SetGuardrailPolicy(&wsSource{rules: rules})
	_ = s.CreateAgent(context.Background(), &models.Agent{Name: "step-agent", Kitchen: "default", Mode: models.AgentModeManaged, Status: models.AgentStatusReady,
		Process: &models.ProcessInfo{Status: models.ProcessRunning, Endpoint: pod.URL}})
	var parts []map[string]string
	for _, tx := range texts {
		parts = append(parts, map[string]string{"type": "text", "text": tx})
	}
	body, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": 7, "method": "tasks/send",
		"params": map[string]interface{}{"message": map[string]interface{}{"role": "user", "parts": parts}}})
	rec := call(h.A2AAgentEndpoint, "/agents/step-agent/a2a", string(body), map[string]string{"agentName": "step-agent"})
	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

// A blocked word cut over two text parts is what the agent receives once the pod joins them, so the guard
// judges that joined text (finding 7 of review 5: the guard joined with a newline and the model got the word).
func TestA2AGuardJudgesTheTextTheAgentReceives(t *testing.T) {
	pod := newA2APod(t, "fine")
	out := a2aSplit(t, []models.Guardrail{blockWord("ws-1", true)}, pod, "say forbid", "den-topic")
	if rpcErrorMessage(out) != "Input blocked by guardrails" || len(pod.texts()) != 0 {
		t.Fatalf("split blocked word: %v, the pod received %q; want it blocked and the pod never called", out, pod.texts())
	}
}

// Every part is judged on its own as well: a rule anchored to the start of the text sees a part that starts a
// message even when the join would bury it behind the part before it.
func TestA2AGuardJudgesEachPartOnItsOwn(t *testing.T) {
	rule := models.Guardrail{ID: "ws-re", Kind: models.GuardrailRegexFilter, Stage: models.GuardrailStageInput, Enabled: true,
		Config: map[string]interface{}{"pattern": `^IGNORE$`}}
	pod := newA2APod(t, "fine")
	out := a2aSplit(t, []models.Guardrail{rule}, pod, "hello ", "IGNORE")
	if rpcErrorMessage(out) != "Input blocked by guardrails" || len(pod.texts()) != 0 {
		t.Fatalf("anchored rule on one part: %v, the pod received %q", out, pod.texts())
	}
}

// Clean parts still go through, and arrive exactly as the guard judged them.
func TestA2ACleanSplitPartsReachThePod(t *testing.T) {
	pod := newA2APod(t, "fine")
	out := a2aSplit(t, []models.Guardrail{blockWord("ws-1", true)}, pod, "hello ", "world")
	if out["error"] != nil || len(pod.texts()) != 1 || pod.texts()[0] != "hello world" {
		t.Fatalf("clean parts: %v, pod received %q", out, pod.texts())
	}
}

// A card number cut in two is redacted on the joined text; a redaction that cannot be written back part by part
// refuses the message instead of letting the cut number through.
func TestA2ASplitCardIsNotForwarded(t *testing.T) {
	rule := models.Guardrail{ID: "ws-pii", Kind: models.GuardrailPIIDetection, Stage: models.GuardrailStageInput, Enabled: true,
		Config: map[string]interface{}{"patterns": []interface{}{"credit_card"}, "action": "redact"}}
	pod := newA2APod(t, "fine")
	_ = a2aSplit(t, []models.Guardrail{rule}, pod, "card 4111 1111 ", "1111 1111")
	for _, got := range pod.texts() {
		if strings.Contains(got, "4111 1111 1111 1111") || strings.Contains(got, "4111 1111 1111") {
			t.Fatalf("the pod received the card number: %q", got)
		}
	}
}

// tasks/get sends no message but returns a task's content, which a pod may have stored before an output rule
// ran: it passes the output rules like a send does.
func TestA2ATasksGetResultPassesTheOutputRules(t *testing.T) {
	rule := blockWord("ws-out", true)
	rule.Stage = models.GuardrailStageOutput
	pod := newA2APod(t, "the stored answer is forbidden-topic")
	h, s := modalityHandlers(t)
	h.Guardrails = &guardrails.CommunityGuardrailService{}
	h.SetGuardrailPolicy(&wsSource{rules: []models.Guardrail{rule}})
	_ = s.CreateAgent(context.Background(), &models.Agent{Name: "step-agent", Kitchen: "default", Mode: models.AgentModeManaged, Status: models.AgentStatusReady,
		Process: &models.ProcessInfo{Status: models.ProcessRunning, Endpoint: pod.URL}})
	body := `{"jsonrpc":"2.0","id":7,"method":"tasks/get","params":{"id":"t1"}}`
	rec := call(h.A2AAgentEndpoint, "/agents/step-agent/a2a", body, map[string]string{"agentName": "step-agent"})
	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rpcErrorMessage(out) != "Output blocked by guardrails" {
		t.Fatalf("tasks/get result: %v, want the output blocked", out)
	}
}
