package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

const (
	cfgSecret  = "sk-live-CONFIG-KEY-123456"
	poolSecret = "sk-live-POOL-KEY-7890123"
	awsSecret  = "aws-live-SECRET-ACCESS-1"
)

func seedKeyedProvider(t *testing.T) *handlers.Handlers {
	t.Helper()
	h, s := newTestHandlers(t)
	h.Router = router.NewModelRouter(s)
	if err := s.CreateProvider(context.Background(), &models.ModelProvider{
		ID: "p1", Name: "openai", Kitchen: "default", Kind: "openai", Models: []string{"gpt-4o"},
		Config:  map[string]interface{}{"api_key": cfgSecret, "aws_secret_access_key": awsSecret, "max_tokens": 2048},
		APIKeys: []models.APIKeyEntry{{Key: poolSecret, Label: "prod", Enabled: true}},
	}); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestProviderEndpointsMaskEveryKeyTheyHold(t *testing.T) {
	h := seedKeyedProvider(t)
	params := map[string]string{"providerName": "openai"}
	get := callAgentAPI(h.GetProvider, http.MethodGet, "/api/v1/providers/openai", params, "")
	responses := map[string]string{
		"list":   callAgentAPI(h.ListProviders, http.MethodGet, "/api/v1/providers", nil, "").Body.String(),
		"get":    get.Body.String(),
		"update": callAgentAPI(h.UpdateProvider, http.MethodPut, "/api/v1/providers/openai", params, `{"endpoint":"https://proxy.example/v1"}`).Body.String(),
	}
	for name, body := range responses {
		for _, secret := range []string{cfgSecret, poolSecret, awsSecret} {
			if strings.Contains(body, secret) {
				t.Errorf("%s: %q is in the response: %s", name, secret, body)
			}
		}
	}
	if !strings.Contains(responses["get"], `"max_tokens":2048`) {
		t.Errorf("settings must stay readable: %s", responses["get"])
	}
	if !strings.Contains(responses["get"], "sk-l****") {
		t.Errorf("a key should show as a short prefix: %s", responses["get"])
	}
}

func TestEditingAProviderWithItsMaskedKeysKeepsTheRealKeys(t *testing.T) {
	h := seedKeyedProvider(t)
	read := callAgentAPI(h.GetProvider, http.MethodGet, "/api/v1/providers/openai", map[string]string{"providerName": "openai"}, "")
	var shown models.ModelProvider
	if err := json.Unmarshal(read.Body.Bytes(), &shown); err != nil {
		t.Fatal(err)
	}
	// The dashboard sends back the config it was shown, with one setting changed.
	shown.Config["max_tokens"] = 4096
	body, _ := json.Marshal(map[string]interface{}{"config": shown.Config})
	if w := callAgentAPI(h.UpdateProvider, http.MethodPut, "/api/v1/providers/openai", map[string]string{"providerName": "openai"}, string(body)); w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	stored, err := h.Store.GetProvider(context.Background(), "default", "openai")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Config["api_key"] != cfgSecret || stored.Config["aws_secret_access_key"] != awsSecret {
		t.Errorf("saving the masked provider replaced a real key: %v", stored.Config)
	}
	if stored.Config["max_tokens"] != float64(4096) {
		t.Errorf("the change the caller made was lost: %v", stored.Config["max_tokens"])
	}

	// A caller who really sets a new key still can.
	if w := callAgentAPI(h.UpdateProvider, http.MethodPut, "/api/v1/providers/openai", map[string]string{"providerName": "openai"}, `{"config":{"api_key":"sk-live-NEW-KEY-999999"}}`); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	stored, _ = h.Store.GetProvider(context.Background(), "default", "openai")
	if stored.Config["api_key"] != "sk-live-NEW-KEY-999999" {
		t.Errorf("a new key was not stored: %v", stored.Config["api_key"])
	}
}

func TestAProviderCanBePointedAtAnExternalSecret(t *testing.T) {
	h := seedKeyedProvider(t)
	w := callAgentAPI(h.UpdateProvider, http.MethodPut, "/api/v1/providers/openai", map[string]string{"providerName": "openai"}, `{"secret_ref":"env://OPENAI_API_KEY"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	stored, _ := h.Store.GetProvider(context.Background(), "default", "openai")
	if stored.SecretRef != "env://OPENAI_API_KEY" {
		t.Errorf("secret_ref = %q", stored.SecretRef)
	}
	if !strings.Contains(w.Body.String(), "env://OPENAI_API_KEY") {
		t.Error("the reference names where the key lives and is shown")
	}
}
