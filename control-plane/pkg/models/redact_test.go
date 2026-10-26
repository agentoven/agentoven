package models

import "testing"

func TestRedactedAgentHidesKeysAndKeepsTheOriginal(t *testing.T) {
	a := Agent{
		Name: "a",
		ResolvedConfig: &ResolvedIngredients{
			Model:        &ResolvedModel{Provider: "p", Model: "m", APIKey: "sk-live-1", Config: map[string]interface{}{"model": "m", "api_key": "sk-live-2", "nested": map[string]interface{}{"token": "t"}}},
			Embeddings:   []ResolvedEmbedding{{Provider: "openai", APIKey: "sk-live-3", Config: map[string]interface{}{"dimensions": 3}}},
			VectorStores: []ResolvedVectorStore{{Index: "i", Config: map[string]interface{}{"password": "pw", "host": "h"}}},
		},
		Ingredients: []Ingredient{{Name: "m", Kind: "model", Config: map[string]interface{}{"provider": "p", "secret": "s"}}},
	}
	r := a.Redacted()
	m := r.ResolvedConfig.Model
	if m.APIKey != RedactedValue || m.Config["api_key"] != RedactedValue || m.Config["model"] != "m" {
		t.Errorf("model not redacted as expected: %+v", m)
	}
	if m.Config["nested"].(map[string]interface{})["token"] != RedactedValue {
		t.Error("a secret inside a nested map was not masked")
	}
	if r.ResolvedConfig.Embeddings[0].APIKey != RedactedValue || r.ResolvedConfig.Embeddings[0].Config["dimensions"] != 3 {
		t.Errorf("embedding: %+v", r.ResolvedConfig.Embeddings[0])
	}
	if vs := r.ResolvedConfig.VectorStores[0].Config; vs["password"] != RedactedValue || vs["host"] != "h" {
		t.Errorf("vector store config: %v", vs)
	}
	if r.Ingredients[0].Config["secret"] != RedactedValue || r.Ingredients[0].Config["provider"] != "p" {
		t.Errorf("ingredient config: %v", r.Ingredients[0].Config)
	}
	// What the executor runs on is untouched.
	if a.ResolvedConfig.Model.APIKey != "sk-live-1" || a.ResolvedConfig.Model.Config["api_key"] != "sk-live-2" || a.Ingredients[0].Config["secret"] != "s" {
		t.Error("redacting changed the original agent")
	}
}

func TestRedactedKeepsAnEmptyKeyEmpty(t *testing.T) {
	r := (&ResolvedIngredients{Model: &ResolvedModel{Provider: "ollama", Model: "llama"}}).Redacted()
	if r.Model.APIKey != "" {
		t.Errorf("an unset key should stay empty, got %q", r.Model.APIKey)
	}
	if (*ResolvedIngredients)(nil).Redacted() != nil {
		t.Error("nil should stay nil")
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
