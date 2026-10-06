package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/guardrails"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

type wsSource struct {
	rules []models.Guardrail
	ex    []models.WorkspaceGuardrailException
	err   error
}

func (w *wsSource) WorkspaceGuardrails(context.Context, string) ([]models.Guardrail, error) {
	return w.rules, w.err
}
func (w *wsSource) WorkspaceGuardrailExceptions(_ context.Context, _, agent string) ([]models.WorkspaceGuardrailException, error) {
	var out []models.WorkspaceGuardrailException
	for _, e := range w.ex {
		if e.AgentName == agent {
			out = append(out, e)
		}
	}
	return out, nil
}

func blockWord(id string, enabled bool) models.Guardrail {
	return models.Guardrail{ID: id, Kind: models.GuardrailContentFilter, Stage: models.GuardrailStageInput, Enabled: enabled,
		Config: map[string]interface{}{"blocked_words": []interface{}{"forbidden-topic"}}}
}

// invokeStatus sends a message containing the blocked word to an agent that attached NO guardrails.
func invokeStatus(t *testing.T, src *wsSource, agentName string) int {
	t.Helper()
	h, s := modalityHandlers(t)
	h.Guardrails = &guardrails.CommunityGuardrailService{}
	if src != nil {
		h.SetGuardrailPolicy(src)
	}
	ctx := context.Background()
	_ = s.CreateAgent(ctx, &models.Agent{Name: agentName, Kitchen: "default", Mode: models.AgentModeManaged, Status: models.AgentStatusDraft})
	req := withChiParam(withKitchen(httptest.NewRequest(http.MethodPost, "/invoke", strings.NewReader(`{"message":"tell me about forbidden-topic"}`)), "default"), "agentName", agentName)
	rec := httptest.NewRecorder()
	h.InvokeAgent(rec, req)
	return rec.Code
}

func TestAMandatoryWorkspaceRuleBlocksAnAgentThatAttachedNoGuardrails(t *testing.T) {
	if code := invokeStatus(t, nil, "plain"); code == http.StatusForbidden {
		t.Fatal("with no workspace source an agent with no guardrails is not blocked")
	}
	src := &wsSource{rules: []models.Guardrail{blockWord("ws-1", true)}}
	if code := invokeStatus(t, src, "plain"); code != http.StatusForbidden {
		t.Fatalf("the mandatory workspace rule must block it, got %d", code)
	}
}

func TestADisabledWorkspaceRuleBlocksNobody(t *testing.T) {
	src := &wsSource{rules: []models.Guardrail{blockWord("ws-1", false)}}
	if code := invokeStatus(t, src, "plain"); code == http.StatusForbidden {
		t.Fatal("a disabled workspace rule must not block anything")
	}
}

func TestAnApprovedExceptionExemptsOnlyThatAgent(t *testing.T) {
	src := &wsSource{
		rules: []models.Guardrail{blockWord("ws-1", true)},
		ex:    []models.WorkspaceGuardrailException{{GuardrailID: "ws-1", AgentName: "exempt"}},
	}
	if code := invokeStatus(t, src, "exempt"); code == http.StatusForbidden {
		t.Fatal("the agent with an approved exception must not be blocked")
	}
	if code := invokeStatus(t, src, "other"); code != http.StatusForbidden {
		t.Fatalf("an agent without an exception is still blocked, got %d", code)
	}
}

func TestAMandatoryOrOverridableWorkspaceRuleBothApply(t *testing.T) {
	r := blockWord("ws-1", true)
	r.Overridable = true // no longer means anything: a global rule is always applied
	if code := invokeStatus(t, &wsSource{rules: []models.Guardrail{r}}, "plain"); code != http.StatusForbidden {
		t.Fatalf("an enabled workspace rule applies whatever Overridable says, got %d", code)
	}
}

func TestTheAgentIsNotRunWhenTheWorkspaceGuardrailsCannotBeRead(t *testing.T) {
	src := &wsSource{err: errors.New("workspace guardrails unavailable")}
	if code := invokeStatus(t, src, "plain"); code != http.StatusServiceUnavailable {
		t.Fatalf("an unreadable global policy must stop the agent (503), got %d", code)
	}
}

func TestTheAgentIsNotRunWhenAnEnabledWorkspaceRuleCannotBeApplied(t *testing.T) {
	broken := models.Guardrail{ID: "ws-bad", Kind: models.GuardrailRegexFilter, Stage: models.GuardrailStageInput, Enabled: true,
		Config: map[string]interface{}{"pattern": "("}}
	if code := invokeStatus(t, &wsSource{rules: []models.Guardrail{broken}}, "plain"); code != http.StatusServiceUnavailable {
		t.Fatalf("an enabled global rule that cannot work must stop the agent (503), got %d", code)
	}
	broken.Enabled = false
	if code := invokeStatus(t, &wsSource{rules: []models.Guardrail{broken}}, "plain"); code == http.StatusServiceUnavailable {
		t.Fatal("a disabled rule is not applied, so it cannot stop the agent")
	}
}

// ── A2A: the path recipe steps take (/agents/{name}/a2a) ───────────────────

type a2aBackend struct {
	*httptest.Server
	mu    sync.Mutex
	calls int
}

// newA2ABackend stands in for a deployed agent process; it answers every call with reply.
func newA2ABackend(t *testing.T, reply string) *a2aBackend {
	t.Helper()
	b := &a2aBackend{}
	b.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.calls++
		b.mu.Unlock()
		w.Header().Set("Content-Type", "application/a2a+json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": 1, "result": map[string]interface{}{
			"status":    map[string]string{"state": "completed"},
			"artifacts": []map[string]interface{}{{"parts": []map[string]string{{"type": "text", "text": reply}}}},
		}})
	}))
	t.Cleanup(b.Close)
	return b
}

func (b *a2aBackend) count() int { b.mu.Lock(); defer b.mu.Unlock(); return b.calls }

func a2aCall(t *testing.T, src *wsSource, ownRules []models.Guardrail, backend *a2aBackend, method, text string) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()
	h, s := modalityHandlers(t)
	h.Guardrails = &guardrails.CommunityGuardrailService{}
	if src != nil {
		h.SetGuardrailPolicy(src)
	}
	_ = s.CreateAgent(context.Background(), &models.Agent{Name: "step-agent", Kitchen: "default", Mode: models.AgentModeManaged, Status: models.AgentStatusReady,
		Guardrails: ownRules, Process: &models.ProcessInfo{Status: models.ProcessRunning, Endpoint: backend.URL}})
	body, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": 7, "method": method,
		"params": map[string]interface{}{"message": map[string]interface{}{"role": "user", "parts": []map[string]string{{"type": "text", "text": text}}}}})
	rec := call(h.A2AAgentEndpoint, "/agents/step-agent/a2a", string(body), map[string]string{"agentName": "step-agent"})
	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func rpcErrorMessage(out map[string]interface{}) string {
	e, _ := out["error"].(map[string]interface{})
	m, _ := e["message"].(string)
	return m
}

func TestARecipeStepToADeployedAgentIsGuardedByTheWorkspaceRule(t *testing.T) {
	src := &wsSource{rules: []models.Guardrail{blockWord("ws-1", true)}}

	b := newA2ABackend(t, "fine")
	_, out := a2aCall(t, src, nil, b, "tasks/send", "tell me about forbidden-topic")
	if rpcErrorMessage(out) != "Input blocked by guardrails" || out["id"] != float64(7) {
		t.Fatalf("a blocked input must come back as a JSON-RPC error carrying the request id, got %v", out)
	}
	if b.count() != 0 {
		t.Fatal("a blocked input must never reach the deployed agent")
	}

	b = newA2ABackend(t, "fine")
	rec, out := a2aCall(t, src, nil, b, "tasks/send", "hello")
	if out["error"] != nil || b.count() != 1 || !strings.Contains(rec.Body.String(), "fine") {
		t.Fatalf("a clean call passes through unchanged: %s (backend calls %d)", rec.Body.String(), b.count())
	}
}

func TestAWorkspaceRuleAlsoGuardsWhatADeployedAgentSaysBack(t *testing.T) {
	out := models.Guardrail{ID: "ws-out", Kind: models.GuardrailContentFilter, Stage: models.GuardrailStageOutput, Enabled: true,
		Config: map[string]interface{}{"blocked_words": []interface{}{"secret-sauce"}}}
	b := newA2ABackend(t, "the secret-sauce recipe is...")
	rec, resp := a2aCall(t, &wsSource{rules: []models.Guardrail{out}}, nil, b, "tasks/send", "what is the recipe?")
	if rpcErrorMessage(resp) != "Output blocked by guardrails" {
		t.Fatalf("the agent's reply must be blocked, got %v", resp)
	}
	if strings.Contains(rec.Body.String(), "secret-sauce") {
		t.Fatalf("the blocked reply must never reach the caller: %s", rec.Body.String())
	}
}

func TestAWorkspaceRuleAppliesOnTopOfTheAgentsOwnRuleOfTheSameKind(t *testing.T) {
	own := []models.Guardrail{{ID: "own", Kind: models.GuardrailContentFilter, Stage: models.GuardrailStageInput, Enabled: true,
		Config: map[string]interface{}{"blocked_words": []interface{}{"only-the-agent-blocks-this"}}}}
	src := &wsSource{rules: []models.Guardrail{blockWord("ws-1", true)}}
	b := newA2ABackend(t, "fine")
	_, out := a2aCall(t, src, own, b, "tasks/send", "tell me about forbidden-topic")
	if rpcErrorMessage(out) != "Input blocked by guardrails" || b.count() != 0 {
		t.Fatal("the agent's own content filter must not displace the global one")
	}
	b = newA2ABackend(t, "fine")
	_, out = a2aCall(t, src, own, b, "tasks/send", "only-the-agent-blocks-this")
	if rpcErrorMessage(out) != "Input blocked by guardrails" || b.count() != 0 {
		t.Fatal("and the agent's own rule still applies as well")
	}
}

func TestADeployedAgentIsNotCalledWhenTheWorkspacePolicyCannotBeApplied(t *testing.T) {
	b := newA2ABackend(t, "fine")
	_, out := a2aCall(t, &wsSource{err: errors.New("unavailable")}, nil, b, "tasks/send", "hello")
	if !strings.Contains(rpcErrorMessage(out), "could not be applied") || b.count() != 0 {
		t.Fatalf("an unreadable global policy must stop the call (calls %d): %v", b.count(), out)
	}
}

func TestAStreamedA2ACallHasItsInputCheckedButIsPassedThrough(t *testing.T) {
	src := &wsSource{rules: []models.Guardrail{blockWord("ws-1", true)}}
	b := newA2ABackend(t, "fine")
	_, out := a2aCall(t, src, nil, b, "message/stream", "about forbidden-topic")
	if rpcErrorMessage(out) != "Input blocked by guardrails" || b.count() != 0 {
		t.Fatal("a streamed call's input must be checked before the agent is called")
	}
}
