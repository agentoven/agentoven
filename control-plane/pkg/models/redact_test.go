package models

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestIsSecretKeyIsNarrow(t *testing.T) {
	for _, k := range []string{"api_key", "API_KEY", "apiKey", "api-key", "azure_api_key", "aws_secret_access_key", "client_secret", "password", "db_password", "token", "access_token", "api_token", "auth_token", "key", "bearer", "authorization", "AccessKeyId", "private_key", "credentials"} {
		if !IsSecretKey(k) {
			t.Errorf("%q should be treated as a secret", k)
		}
	}
	for _, k := range []string{"max_tokens", "max_output_tokens", "top_k", "model", "provider", "temperature", "cost_per_1k_input", "modalities", "tokenizer", "keywords", "endpoint", "region", "batch_size"} {
		if IsSecretKey(k) {
			t.Errorf("%q is a setting, not a secret, and must stay readable", k)
		}
	}
}

func TestResolvedModelCarriesNoCredential(t *testing.T) {
	raw, err := json.Marshal(ResolvedIngredients{Model: &ResolvedModel{Provider: "p", Model: "m"}, Embeddings: []ResolvedEmbedding{{Provider: "openai"}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "api_key") {
		t.Errorf("a resolved model must not have a key field at all: %s", raw)
	}
	// An agent stored before this change still has the key in its JSON; reading it drops the key.
	var back ResolvedIngredients
	if err := json.Unmarshal([]byte(`{"model":{"provider":"p","model":"m","api_key":"sk-old-stored"}}`), &back); err != nil {
		t.Fatal(err)
	}
	again, _ := json.Marshal(back)
	if strings.Contains(string(again), "sk-old-stored") {
		t.Errorf("a key from an old record survived a read and write: %s", again)
	}
}

func TestRedactedAgentHidesSecretsAndKeepsTheOriginal(t *testing.T) {
	a := Agent{
		Name: "a",
		ResolvedConfig: &ResolvedIngredients{
			Model:        &ResolvedModel{Provider: "p", Model: "m", Config: map[string]interface{}{"model": "m", "max_tokens": 4096, "api_key": "sk-live-2", "nested": map[string]interface{}{"token": "t"}}},
			Embeddings:   []ResolvedEmbedding{{Provider: "openai", Config: map[string]interface{}{"dimensions": 3, "api_key": "sk-live-3"}}},
			VectorStores: []ResolvedVectorStore{{Index: "i", Config: map[string]interface{}{"password": "pw", "host": "h"}}},
		},
		Ingredients: []Ingredient{{Name: "m", Kind: "model", Config: map[string]interface{}{"provider": "p", "client_secret": "s"}}},
	}
	r := a.Redacted()
	m := r.ResolvedConfig.Model
	if m.Config["api_key"] != RedactedValue || m.Config["model"] != "m" || m.Config["max_tokens"] != 4096 {
		t.Errorf("model config: %+v", m.Config)
	}
	if m.Config["nested"].(map[string]interface{})["token"] != RedactedValue {
		t.Error("a secret inside a nested map was not masked")
	}
	if e := r.ResolvedConfig.Embeddings[0].Config; e["api_key"] != RedactedValue || e["dimensions"] != 3 {
		t.Errorf("embedding config: %+v", e)
	}
	if vs := r.ResolvedConfig.VectorStores[0].Config; vs["password"] != RedactedValue || vs["host"] != "h" {
		t.Errorf("vector store config: %v", vs)
	}
	if r.Ingredients[0].Config["client_secret"] != RedactedValue || r.Ingredients[0].Config["provider"] != "p" {
		t.Errorf("ingredient config: %v", r.Ingredients[0].Config)
	}
	// What the executor runs on is untouched.
	if a.ResolvedConfig.Model.Config["api_key"] != "sk-live-2" || a.Ingredients[0].Config["client_secret"] != "s" {
		t.Error("redacting changed the original agent")
	}
	if (*ResolvedIngredients)(nil).Redacted() != nil {
		t.Error("nil should stay nil")
	}
}

func TestRedactedProviderMasksEveryKeyItHolds(t *testing.T) {
	p := ModelProvider{
		Name: "g", Kind: "gemini", SecretRef: "env://GEMINI_KEY",
		Config: map[string]interface{}{"api_key": "AIzaSyD-live", "api_secret": "sec-live-1", "aws_secret_access_key": "aws-live", "max_tokens": 1000, "cost_per_1k_input": 0.5},
		APIKeys: []APIKeyEntry{
			{Key: "sk-pool-aaaa", Label: "prod", Enabled: true},
			{Key: "", Label: "from-ref", Enabled: true, SecretRef: "env://POOL_KEY"},
		},
	}
	r := p.Redacted()
	for k, want := range map[string]string{"api_key": "AIza****", "api_secret": "sec-****", "aws_secret_access_key": "aws-****"} {
		if r.Config[k] != want {
			t.Errorf("config[%s] = %v, want %s", k, r.Config[k], want)
		}
	}
	if r.Config["max_tokens"] != 1000 || r.Config["cost_per_1k_input"] != 0.5 {
		t.Errorf("settings must stay readable: %v", r.Config)
	}
	if r.APIKeys[0].Key != "sk-p****" || r.APIKeys[0].Label != "prod" {
		t.Errorf("a key in the rotation pool was not masked: %+v", r.APIKeys[0])
	}
	if r.APIKeys[1].Key != "" || r.APIKeys[1].SecretRef != "env://POOL_KEY" {
		t.Errorf("an external reference is not a secret and an empty key stays empty: %+v", r.APIKeys[1])
	}
	if r.SecretRef != "env://GEMINI_KEY" {
		t.Error("the secret reference names where a key lives; it is not itself hidden")
	}
	raw, _ := json.Marshal(r)
	for _, secret := range []string{"AIzaSyD-live", "sec-live-1", "aws-live", "sk-pool-aaaa"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("%q is still in the response: %s", secret, raw)
		}
	}
	if p.Config["api_key"] != "AIzaSyD-live" || p.APIKeys[0].Key != "sk-pool-aaaa" {
		t.Error("redacting changed the stored provider")
	}
}

func TestRestoreRedactedIngredientsKeepsStoredSecrets(t *testing.T) {
	stored := []Ingredient{{Name: "m", Kind: "model", Config: map[string]interface{}{"provider": "p", "api_key": "real", "nested": map[string]interface{}{"token": "real-t"}}}}
	// A client read the agent (masked) and writes it back, changing one visible setting.
	back := []Ingredient{{Name: "m", Kind: "model", Config: map[string]interface{}{"provider": "q", "api_key": RedactedValue, "nested": map[string]interface{}{"token": RedactedValue}}}}
	got := RestoreRedactedIngredients(back, stored)[0].Config
	if got["api_key"] != "real" || got["nested"].(map[string]interface{})["token"] != "real-t" || got["provider"] != "q" {
		t.Errorf("restore: %v", got)
	}
	// A secret the caller really changed is kept as sent.
	changed := []Ingredient{{Name: "m", Kind: "model", Config: map[string]interface{}{"api_key": "new"}}}
	if RestoreRedactedIngredients(changed, stored)[0].Config["api_key"] != "new" {
		t.Error("a changed secret was overwritten")
	}
	// A mask with nothing stored to restore stays as the caller sent it.
	fresh := []Ingredient{{Name: "other", Kind: "model", Config: map[string]interface{}{"api_key": RedactedValue}}}
	if RestoreRedactedIngredients(fresh, stored)[0].Config["api_key"] != RedactedValue {
		t.Error("unexpected restore for an unrelated ingredient")
	}
}

func TestRestoreRedactedConfigKeepsAProvidersKeyWhenOnlyTheEndpointChanges(t *testing.T) {
	stored := map[string]interface{}{"api_key": "sk-real-key", "region": "eu"}
	// What the dashboard sends back after reading the masked provider and editing the region.
	sent := map[string]interface{}{"api_key": "sk-r****", "region": "us"}
	got := RestoreRedactedConfig(sent, stored)
	if got["api_key"] != "sk-real-key" || got["region"] != "us" {
		t.Errorf("got %v", got)
	}
	// A new key is a new key, and a non-secret that happens to end in **** is just text.
	got = RestoreRedactedConfig(map[string]interface{}{"api_key": "sk-new-key", "note": "stars ****"}, stored)
	if got["api_key"] != "sk-new-key" || got["note"] != "stars ****" {
		t.Errorf("got %v", got)
	}
}
