package models

import "strings"

// RedactedValue stands in for a secret in anything returned to an API caller. A caller can tell a key
// is set (the value is not empty) without being able to read it.
const RedactedValue = "****"

// secretKey says whether a config key names a secret: api_key, token, password, and the like.
func secretKey(k string) bool {
	k = strings.ToLower(k)
	for _, part := range []string{"key", "secret", "token", "password", "passwd", "credential", "authorization", "bearer"} {
		if strings.Contains(k, part) {
			return true
		}
	}
	return false
}

// redactMap returns a copy of m with the value of every secret-looking key replaced, at any depth.
// A key that is set but empty stays empty, so "not configured" is still visible.
func redactMap(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return nil
	}
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		switch val := v.(type) {
		case map[string]interface{}:
			out[k] = redactMap(val)
		case string:
			if secretKey(k) && val != "" {
				out[k] = RedactedValue
			} else {
				out[k] = val
			}
		default:
			if secretKey(k) && v != nil {
				out[k] = RedactedValue
			} else {
				out[k] = v
			}
		}
	}
	return out
}

func redactString(s string) string {
	if s == "" {
		return ""
	}
	return RedactedValue
}

// Redacted returns a copy of the resolved ingredients that is safe to return from the API: the provider
// and embedding keys, and any secret in a model, embedding, vector store or data config, are masked.
// The original, which the executor and gateway run on, is not changed.
func (r *ResolvedIngredients) Redacted() *ResolvedIngredients {
	if r == nil {
		return nil
	}
	out := *r
	if r.Model != nil {
		m := *r.Model
		m.APIKey = redactString(m.APIKey)
		m.Config = redactMap(m.Config)
		out.Model = &m
	}
	if r.Embeddings != nil {
		out.Embeddings = make([]ResolvedEmbedding, len(r.Embeddings))
		for i, e := range r.Embeddings {
			e.APIKey = redactString(e.APIKey)
			e.Config = redactMap(e.Config)
			out.Embeddings[i] = e
		}
	}
	if r.VectorStores != nil {
		out.VectorStores = make([]ResolvedVectorStore, len(r.VectorStores))
		for i, v := range r.VectorStores {
			v.Config = redactMap(v.Config)
			out.VectorStores[i] = v
		}
	}
	if r.Data != nil {
		out.Data = make([]ResolvedData, len(r.Data))
		for i, d := range r.Data {
			d.Config = redactMap(d.Config)
			out.Data[i] = d
		}
	}
	return &out
}

// Redacted returns a copy of the agent that is safe to return from the API. See ResolvedIngredients.Redacted.
func (a Agent) Redacted() Agent {
	a.ResolvedConfig = a.ResolvedConfig.Redacted()
	if a.Ingredients != nil {
		ing := make([]Ingredient, len(a.Ingredients))
		for i, in := range a.Ingredients {
			in.Config = redactMap(in.Config)
			ing[i] = in
		}
		a.Ingredients = ing
	}
	return a
}

// RedactAgents does Agent.Redacted for a list.
func RedactAgents(agents []Agent) []Agent {
	if agents == nil {
		return nil
	}
	out := make([]Agent, len(agents))
	for i, a := range agents {
		out[i] = a.Redacted()
	}
	return out
}

// restoreMap puts back, in incoming, the stored value of every key that still carries the mask. A client
// that reads an agent and writes it back has only ever seen the mask; it must not overwrite the secret with it.
func restoreMap(incoming, stored map[string]interface{}) map[string]interface{} {
	if incoming == nil {
		return nil
	}
	out := make(map[string]interface{}, len(incoming))
	for k, v := range incoming {
		old := stored[k]
		switch val := v.(type) {
		case map[string]interface{}:
			oldMap, _ := old.(map[string]interface{})
			out[k] = restoreMap(val, oldMap)
		case string:
			if val == RedactedValue && old != nil {
				out[k] = old
			} else {
				out[k] = val
			}
		default:
			out[k] = v
		}
	}
	return out
}

// RestoreRedactedIngredients returns incoming with any masked config value replaced by the one already stored
// for the same ingredient (matched by kind and name). A value the caller really changed is kept as sent.
func RestoreRedactedIngredients(incoming, stored []Ingredient) []Ingredient {
	if len(incoming) == 0 {
		return incoming
	}
	out := make([]Ingredient, len(incoming))
	for i, in := range incoming {
		for _, old := range stored {
			if old.Kind == in.Kind && old.Name == in.Name {
				in.Config = restoreMap(in.Config, old.Config)
				break
			}
		}
		out[i] = in
	}
	return out
}
