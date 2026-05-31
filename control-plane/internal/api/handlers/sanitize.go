package handlers

import (
	"strings"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

const redactedSecret = "<redacted>"

var secretConfigKeys = map[string]struct{}{
	"api_key":      {},
	"apikey":       {},
	"api_secret":   {},
	"apisecret":    {},
	"token":        {},
	"access_token": {},
	"secret":       {},
	"authorization": {},
	"password":     {},
}

// sanitizeResolvedIngredients returns a copy of resolved ingredients with secrets redacted.
// The original value is not mutated so cached runtime config stays intact.
func sanitizeResolvedIngredients(in *models.ResolvedIngredients) *models.ResolvedIngredients {
	if in == nil {
		return nil
	}

	out := *in

	if in.Model != nil {
		m := *in.Model
		m.APIKey = redactedSecret
		m.Config = redactSecretMap(m.Config)
		out.Model = &m
	}

	if len(in.Tools) > 0 {
		tools := make([]models.ResolvedTool, len(in.Tools))
		for i, tool := range in.Tools {
			tools[i] = tool
			tools[i].Schema = redactSecretMap(tool.Schema)
		}
		out.Tools = tools
	}

	if len(in.Data) > 0 {
		data := make([]models.ResolvedData, len(in.Data))
		for i, item := range in.Data {
			data[i] = item
			data[i].Config = redactSecretMap(item.Config)
		}
		out.Data = data
	}

	if len(in.Embeddings) > 0 {
		embeddings := make([]models.ResolvedEmbedding, len(in.Embeddings))
		for i, emb := range in.Embeddings {
			embeddings[i] = emb
			embeddings[i].APIKey = redactedSecret
			embeddings[i].Config = redactSecretMap(emb.Config)
		}
		out.Embeddings = embeddings
	}

	if len(in.VectorStores) > 0 {
		stores := make([]models.ResolvedVectorStore, len(in.VectorStores))
		for i, vs := range in.VectorStores {
			stores[i] = vs
			stores[i].Config = redactSecretMap(vs.Config)
		}
		out.VectorStores = stores
	}

	return &out
}

func redactSecretMap(cfg map[string]interface{}) map[string]interface{} {
	if cfg == nil {
		return nil
	}
	out := make(map[string]interface{}, len(cfg))
	for k, v := range cfg {
		out[k] = redactConfigValue(k, v)
	}
	return out
}

func redactConfigValue(key string, value interface{}) interface{} {
	if isSecretKey(key) {
		return redactedSecret
	}
	switch typed := value.(type) {
	case map[string]interface{}:
		return redactSecretMap(typed)
	case []interface{}:
		items := make([]interface{}, len(typed))
		for i, item := range typed {
			if m, ok := item.(map[string]interface{}); ok {
				items[i] = redactSecretMap(m)
			} else {
				items[i] = redactConfigValue("", item)
			}
		}
		return items
	default:
		return value
	}
}

func isSecretKey(key string) bool {
	_, ok := secretConfigKeys[strings.ToLower(key)]
	return ok
}
