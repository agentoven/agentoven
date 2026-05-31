package handlers

import (
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func TestSanitizeResolvedIngredients_RedactsModelAPIKey(t *testing.T) {
	original := &models.ResolvedIngredients{
		Model: &models.ResolvedModel{
			Provider: "my-openai",
			Kind:     "openai",
			Model:    "gpt-5-mini",
			APIKey:   "sk-live-secret-key",
			Config: map[string]interface{}{
				"api_key": "sk-config-secret",
				"model":   "gpt-5-mini",
			},
		},
	}

	sanitized := sanitizeResolvedIngredients(original)

	if original.Model.APIKey != "sk-live-secret-key" {
		t.Fatal("sanitizer mutated the original resolved model")
	}
	if sanitized.Model.APIKey != redactedSecret {
		t.Errorf("sanitized api_key = %q, want %q", sanitized.Model.APIKey, redactedSecret)
	}
	if cfgKey, ok := sanitized.Model.Config["api_key"].(string); !ok || cfgKey != redactedSecret {
		t.Errorf("sanitized config api_key = %v, want %q", sanitized.Model.Config["api_key"], redactedSecret)
	}
	if strings.Contains(sanitized.Model.APIKey, "sk-") {
		t.Error("sanitized api_key still contains provider secret prefix")
	}
}

func TestSanitizeResolvedIngredients_RedactsDataAndVectorStoreConfigs(t *testing.T) {
	original := &models.ResolvedIngredients{
		Data: []models.ResolvedData{
			{
				Name: "warehouse",
				URI:  "snowflake://example",
				Config: map[string]interface{}{
					"api_key": "secret-data-key",
					"region":  "us-east-1",
				},
			},
		},
		VectorStores: []models.ResolvedVectorStore{
			{
				Backend: models.VectorStorePinecone,
				Index:   "docs",
				Config: map[string]interface{}{
					"token": "pinecone-secret",
				},
			},
		},
		Tools: []models.ResolvedTool{
			{
				Name: "Weather",
				Schema: map[string]interface{}{
					"auth": map[string]interface{}{
						"api_key": "tool-secret",
					},
				},
			},
		},
	}

	sanitized := sanitizeResolvedIngredients(original)

	if sanitized.Data[0].Config["api_key"] != redactedSecret {
		t.Fatalf("data config api_key = %v", sanitized.Data[0].Config["api_key"])
	}
	if sanitized.VectorStores[0].Config["token"] != redactedSecret {
		t.Fatalf("vector store token = %v", sanitized.VectorStores[0].Config["token"])
	}
	auth, ok := sanitized.Tools[0].Schema["auth"].(map[string]interface{})
	if !ok || auth["api_key"] != redactedSecret {
		t.Fatalf("tool schema auth api_key not redacted: %#v", sanitized.Tools[0].Schema["auth"])
	}
	if original.Data[0].Config["api_key"] != "secret-data-key" {
		t.Fatal("sanitizer mutated original data config")
	}
}

func TestSanitizeResolvedIngredients_RedactsEmbeddingSecrets(t *testing.T) {
	original := &models.ResolvedIngredients{
		Embeddings: []models.ResolvedEmbedding{
			{
				Provider: "openai",
				Model:    "text-embedding-3-small",
				APIKey:   "sk-embedding-secret",
				Config: map[string]interface{}{
					"token": "secret-token",
				},
			},
		},
	}

	sanitized := sanitizeResolvedIngredients(original)

	if sanitized.Embeddings[0].APIKey != redactedSecret {
		t.Errorf("embedding api_key = %q, want %q", sanitized.Embeddings[0].APIKey, redactedSecret)
	}
	if sanitized.Embeddings[0].Config["token"] != redactedSecret {
		t.Errorf("embedding config token = %v, want %q", sanitized.Embeddings[0].Config["token"], redactedSecret)
	}
}
