package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/internal/resolver"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

const providerSecret = "sk-live-NEVER-RETURN-ME-123456"
const ingredientSecret = "tok-ingredient-NEVER-RETURN-ME"

// An agent that has been baked against a provider that has a key, as it is after a real bake. The agent
// keeps the provider's name; the key stays with the provider.
func seedBakedAgent(t *testing.T) (*handlers.Handlers, context.Context) {
	t.Helper()
	h, s := newTestHandlers(t)
	ctx := context.Background()
	if err := s.CreateProvider(ctx, &models.ModelProvider{
		Name: "openai", Kind: "openai", Kitchen: "default", Models: []string{"gpt-4o"},
		Config: map[string]interface{}{"api_key": providerSecret},
	}); err != nil {
		t.Fatal(err)
	}
	agent := &models.Agent{
		Name: "keeper", Kitchen: "default", Status: models.AgentStatusReady, Mode: models.AgentModeManaged,
		Ingredients: []models.Ingredient{
			{ID: "m", Name: "model", Kind: models.IngredientModel, Required: true, Config: map[string]interface{}{"provider": "openai", "model": "gpt-4o", "api_token": ingredientSecret}},
		},
	}
	h.Resolver = resolver.NewResolver(s)
	resolved, err := h.Resolver.Resolve(ctx, agent)
	if err != nil {
		t.Fatal(err)
	}
	agent.ResolvedConfig = resolved
	if err := s.CreateAgent(ctx, agent); err != nil {
		t.Fatal(err)
	}
	return h, ctx
}

func callAgentAPI(h http.HandlerFunc, method, path string, params map[string]string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = withKitchen(req, "default")
	for k, v := range params {
		req = withChiParam(req, k, v)
	}
	w := httptest.NewRecorder()
	h(w, req)
	return w
}

func TestAgentEndpointsNeverReturnTheProviderKey(t *testing.T) {
	h, _ := seedBakedAgent(t)
	cases := map[string]*httptest.ResponseRecorder{
		"list":    callAgentAPI(h.ListAgents, http.MethodGet, "/api/v1/agents", nil, ""),
		"get":     callAgentAPI(h.GetAgent, http.MethodGet, "/api/v1/agents/keeper", map[string]string{"agentName": "keeper"}, ""),
		"config":  callAgentAPI(h.GetAgentConfig, http.MethodGet, "/api/v1/agents/keeper/config", map[string]string{"agentName": "keeper"}, ""),
		"update":  callAgentAPI(h.UpdateAgent, http.MethodPut, "/api/v1/agents/keeper", map[string]string{"agentName": "keeper"}, `{"description":"changed"}`),
		"history": callAgentAPI(h.ListAgentVersions, http.MethodGet, "/api/v1/agents/keeper/versions", map[string]string{"agentName": "keeper"}, ""),
	}
	for name, w := range cases {
		t.Run(name, func(t *testing.T) {
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			body := w.Body.String()
			if strings.Contains(body, providerSecret) {
				t.Errorf("the provider key was returned: %s", body)
			}
			if strings.Contains(body, ingredientSecret) {
				t.Errorf("a secret in an ingredient's config was returned: %s", body)
			}
		})
	}
	// The agent no longer has a key field at all; the provider holds it.
	if body := cases["get"].Body.String(); strings.Contains(body, `"api_key"`) {
		t.Errorf("an agent should carry no api_key field: %s", body)
	}
}

func TestReadingTheMaskedAgentAndWritingItBackKeepsTheSecrets(t *testing.T) {
	h, _ := seedBakedAgent(t)
	got := callAgentAPI(h.GetAgent, http.MethodGet, "/api/v1/agents/keeper", map[string]string{"agentName": "keeper"}, "")
	// A client that round-trips the agent sends back what it read, mask included.
	body := `{"ingredients":[{"id":"m","name":"model","kind":"model","required":true,"config":{"provider":"openai","model":"gpt-4o","api_token":"****"}}]}`
	if w := callAgentAPI(h.UpdateAgent, http.MethodPut, "/api/v1/agents/keeper", map[string]string{"agentName": "keeper"}, body); w.Code != http.StatusOK {
		t.Fatalf("update: %d %s (read was %s)", w.Code, w.Body.String(), got.Body.String())
	}
	stored, err := h.Store.GetAgent(context.Background(), "default", "keeper")
	if err != nil {
		t.Fatal(err)
	}
	if v := stored.Ingredients[0].Config["api_token"]; v != ingredientSecret {
		t.Errorf("writing the masked agent back replaced the stored secret with %v", v)
	}
	if raw, _ := json.Marshal(stored); strings.Contains(string(raw), providerSecret) {
		t.Errorf("the stored agent holds the provider's key: %s", raw)
	}
	if stored.ResolvedConfig == nil || stored.ResolvedConfig.Model == nil || stored.ResolvedConfig.Model.Provider != "openai" {
		t.Error("the stored agent should still name its provider")
	}
}
