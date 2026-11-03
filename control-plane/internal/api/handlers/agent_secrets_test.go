package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/internal/guardrails"
	"github.com/agentoven/agentoven/control-plane/internal/secretref"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

const (
	ruleSecret   = "K-LLAMAGUARD-RULE-SECRET-123"
	urlSecret    = "T-INGREDIENT-URL-TOKEN-456"
	agentPayload = `{"name":"sec","mode":"managed","guardrails":[{"id":"g1","kind":"llamaguard","stage":"input","enabled":true,` +
		`"config":{"endpoint":"https://guard.example/v1","api_key":"` + ruleSecret + `"}}],` +
		`"ingredients":[{"id":"i1","name":"docs","kind":"vectorstore","config":{"url":"https://vs.example/q?token=` + urlSecret + `&x=1","index":"idx"}}]}`
)

func sealingHandlers(t *testing.T) (*handlers.Handlers, store.Store, *memVault) {
	t.Helper()
	h, s := modalityHandlers(t)
	res := secretref.New()
	v := newMemVault()
	res.SetVault(v)
	h.SecretResolver = res
	h.Resolver.UseVault(res.Vault)
	guardrails.UseVault(res.Vault)
	t.Cleanup(func() { guardrails.UseVault(nil) })
	return h, s, v
}

func register(t *testing.T, h *handlers.Handlers, body string) {
	t.Helper()
	if w := callAgentAPI(h.RegisterAgent, http.MethodPost, "/api/v1/agents", nil, body); w.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", w.Code, w.Body.String())
	}
}

func storedJSON(t *testing.T, s store.Store, name string) string {
	t.Helper()
	a, err := s.GetAgent(context.Background(), "default", name)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(a)
	return string(b)
}

// Review 5, finding 12: a Viewer read a guardrail's api_key and a URL token in clear from every agent route.
func TestAgentRoutesMaskGuardrailAndIngredientSecrets(t *testing.T) {
	h, s := modalityHandlers(t) // no vault: the community edition, the values stay in the record
	register(t, h, agentPayload)
	p := map[string]string{"agentName": "sec"}
	for name, w := range map[string]*httptest.ResponseRecorder{
		"list":    callAgentAPI(h.ListAgents, http.MethodGet, "/api/v1/agents", nil, ""),
		"get":     callAgentAPI(h.GetAgent, http.MethodGet, "/api/v1/agents/sec", p, ""),
		"config":  callAgentAPI(h.GetAgentConfig, http.MethodGet, "/api/v1/agents/sec/config", p, ""),
		"update":  callAgentAPI(h.UpdateAgent, http.MethodPut, "/api/v1/agents/sec", p, `{"description":"d"}`),
		"history": callAgentAPI(h.ListAgentVersions, http.MethodGet, "/api/v1/agents/sec/versions", p, ""),
	} {
		body := w.Body.String()
		if strings.Contains(body, ruleSecret) || strings.Contains(body, urlSecret) {
			t.Errorf("%s returned a secret: %.400s", name, body)
		}
	}
	if !strings.Contains(storedJSON(t, s, "sec"), ruleSecret) {
		t.Errorf("without a vault the stored rule must keep its key (it is what runs)")
	}
}

// A client that reads an agent and writes it back (the dashboard does) does not overwrite the secrets with masks.
func TestWritingAMaskedAgentBackKeepsTheStoredSecrets(t *testing.T) {
	h, s := modalityHandlers(t)
	register(t, h, agentPayload)
	p := map[string]string{"agentName": "sec"}
	got := callAgentAPI(h.GetAgent, http.MethodGet, "/api/v1/agents/sec", p, "").Body.String()
	var read struct {
		Guardrails  []models.Guardrail  `json:"guardrails"`
		Ingredients []models.Ingredient `json:"ingredients"`
	}
	_ = json.Unmarshal([]byte(got), &read)
	body, _ := json.Marshal(read)
	if w := callAgentAPI(h.UpdateAgent, http.MethodPut, "/api/v1/agents/sec", p, string(body)); w.Code != http.StatusOK {
		t.Fatalf("write back: %d %s", w.Code, w.Body.String())
	}
	stored := storedJSON(t, s, "sec")
	if !strings.Contains(stored, ruleSecret) || !strings.Contains(stored, urlSecret) {
		t.Errorf("a masked write-back replaced a stored secret: %s", stored)
	}
}

// With a vault the record holds references; the rule and the ingredient get their secrets back where they are used.
func TestSecretsAreSealedIntoTheVaultAndReadBackWhereUsed(t *testing.T) {
	h, s, v := sealingHandlers(t)
	register(t, h, agentPayload)
	stored := storedJSON(t, s, "sec")
	if strings.Contains(stored, ruleSecret) || strings.Contains(stored, urlSecret) {
		t.Fatalf("the stored agent still holds a secret: %s", stored)
	}
	if !strings.Contains(stored, "vault://seal-") {
		t.Fatalf("the stored agent holds no reference: %s", stored)
	}
	body := callAgentAPI(h.GetAgent, http.MethodGet, "/api/v1/agents/sec", map[string]string{"agentName": "sec"}, "").Body.String()
	if strings.Contains(body, ruleSecret) || strings.Contains(body, urlSecret) {
		t.Errorf("the API returned a secret: %s", body)
	}

	a, _ := s.GetAgent(context.Background(), "default", "sec")
	rules, err := guardrails.Effective(context.Background(), nil, "default", "sec", a.Guardrails)
	if err != nil || rules[0].Config["api_key"] != ruleSecret {
		t.Errorf("Effective must read the rule's key back: %v %v", rules, err)
	}
	if a.Guardrails[0].Config["api_key"] == ruleSecret {
		t.Errorf("Effective changed the stored rule")
	}
	resolved, err := h.Resolver.Resolve(context.Background(), a)
	_ = err // the ingredient has no backing store here; only the unsealed config matters
	if resolved != nil && len(resolved.VectorStores) > 0 {
		if u, _ := resolved.VectorStores[0].Config["url"].(string); !strings.Contains(u, urlSecret) {
			t.Errorf("the resolver must hand the ingredient its url back: %v", u)
		}
	}
	if len(v.creds) != 2 {
		t.Errorf("want 2 credentials in the vault, have %d", len(v.creds))
	}
}

// A rule that points at another agent's credential is refused when it is applied: the name of a credential
// can be worked out by anyone, so ownership is what protects it.
func TestARuleCannotReadAnotherAgentsSealedCredential(t *testing.T) {
	h, s, _ := sealingHandlers(t)
	register(t, h, agentPayload)
	a, _ := s.GetAgent(context.Background(), "default", "sec")
	ref, _ := a.Guardrails[0].Config["api_key"].(string)
	thief := []models.Guardrail{{ID: "t", Kind: models.GuardrailLlamaGuard, Stage: models.GuardrailStageInput, Enabled: true,
		Config: map[string]interface{}{"endpoint": "https://evil.example", "api_key": ref}}}
	if _, err := guardrails.Effective(context.Background(), nil, "default", "other-agent", thief); err == nil {
		t.Fatal("another agent's rule was given the credential")
	}
}

// A vault that cannot be written refuses the save and stores nothing: a secret is never left in the record
// because sealing it was inconvenient.
func TestAFailedSealRefusesTheSave(t *testing.T) {
	h, s, v := sealingHandlers(t)
	v.fail = true
	if w := callAgentAPI(h.RegisterAgent, http.MethodPost, "/api/v1/agents", nil, agentPayload); w.Code != http.StatusInternalServerError {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if _, err := s.GetAgent(context.Background(), "default", "sec"); err == nil {
		t.Error("the agent was stored although its secrets could not be")
	}
}

// Removing a rule removes its credential, and a PUT that keeps the rule keeps the credential.
func TestRemovingARuleRemovesItsCredential(t *testing.T) {
	h, _, v := sealingHandlers(t)
	register(t, h, agentPayload)
	p := map[string]string{"agentName": "sec"}
	if len(v.creds) != 2 {
		t.Fatalf("want 2 credentials, have %d", len(v.creds))
	}
	if w := callAgentAPI(h.UpdateAgent, http.MethodPut, "/api/v1/agents/sec", p, `{"guardrails":[]}`); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if len(v.creds) != 1 {
		t.Errorf("want the rule's credential gone and the ingredient's kept, have %d credentials", len(v.creds))
	}
}

// The migration: agents stored before sealing existed are moved over, a dry run changes nothing, a second run
// finds nothing.
func TestSealAllAgentsMovesStoredPlaintext(t *testing.T) {
	h, s, v := sealingHandlers(t)
	var a models.Agent
	_ = json.Unmarshal([]byte(agentPayload), &a)
	a.Kitchen, a.ID, a.Status = "default", "id-1", models.AgentStatusDraft
	_ = s.CreateKitchen(context.Background(), &models.Kitchen{ID: "k-default", Name: "default"}) // the memory store seeds none
	if err := s.CreateAgent(context.Background(), &a); err != nil {
		t.Fatal(err)
	}
	if rep, err := h.SealAllAgents(context.Background(), true); err != nil || len(rep.Moved) != 1 || len(v.creds) != 0 {
		t.Fatalf("dry run: %+v %v creds=%d", rep, err, len(v.creds))
	}
	if rep, err := h.SealAllAgents(context.Background(), false); err != nil || len(rep.Moved) != 1 || len(rep.Failed) != 0 {
		t.Fatalf("seal: %+v %v", rep, err)
	}
	if strings.Contains(storedJSON(t, s, "sec"), ruleSecret) {
		t.Error("the stored agent still holds the rule's key")
	}
	if rep, _ := h.SealAllAgents(context.Background(), false); len(rep.Moved) != 0 {
		t.Errorf("a second run moved %v", rep.Moved)
	}
}

// The validation key follows the same rules: sealed, masked, never overwritten by its mask, read back for the validator.
func TestValidationKeyIsSealedMaskedAndResolved(t *testing.T) {
	h, s, v := sealingHandlers(t)
	put := func(body string) string {
		w := callAgentAPI(h.UpdateKitchenSettings, http.MethodPut, "/api/v1/settings", nil, body)
		if w.Code != http.StatusOK {
			t.Fatalf("put: %d %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	if out := put(`{"validation_provider":"openai","validation_model":"m","validation_api_key":"sk-VALIDATION-KEY-789"}`); strings.Contains(out, "VALIDATION-KEY") {
		t.Errorf("the key was echoed: %s", out)
	}
	st, _ := s.GetKitchenSettings(context.Background(), "default")
	if strings.Contains(st.ValidationAPIKey, "VALIDATION-KEY") || !strings.HasPrefix(st.ValidationAPIKey, "vault://") {
		t.Fatalf("the settings hold %q", st.ValidationAPIKey)
	}
	if got, _ := v.Read(context.Background(), "default", "kitchen-validation-key"); got != "sk-VALIDATION-KEY-789" {
		t.Errorf("the vault holds %q", got)
	}
	// What a client read, written back.
	get := callAgentAPI(h.GetKitchenSettings, http.MethodGet, "/api/v1/settings", nil, "").Body.String()
	if strings.Contains(get, "VALIDATION-KEY") {
		t.Errorf("GET returned the key: %s", get)
	}
	put(get)
	if got, _ := v.Read(context.Background(), "default", "kitchen-validation-key"); got != "sk-VALIDATION-KEY-789" {
		t.Errorf("writing the read settings back changed the key to %q", got)
	}
}
