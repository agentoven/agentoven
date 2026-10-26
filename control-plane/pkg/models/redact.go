package models

import "strings"

// RedactedValue stands in for a secret in anything returned to an API caller. A caller can tell a key
// is set (the value is not empty) without being able to read it.
const RedactedValue = "****"

// IsSecretKey says whether a config key names a secret: api_key, a token, a password and the like.
// It is deliberately narrow. Settings such as max_tokens or top_k are not secrets and must stay readable.
func IsSecretKey(k string) bool {
	k = strings.ToLower(k)
	for _, part := range []string{"password", "passwd", "secret", "credential", "authorization", "bearer", "apikey", "api_key", "api-key", "accesskey", "privatekey"} {
		if strings.Contains(k, part) {
			return true
		}
	}
	if k == "key" || k == "token" {
		return true
	}
	for _, suffix := range []string{"_key", "-key", "_token", "-token"} {
		if strings.HasSuffix(k, suffix) {
			return true
		}
	}
	return false
}

// IsMasked says whether a value is a mask this package (or the provider endpoints) produced, so a client
// that read a masked object and writes it back is not taken to be setting the secret to the mask.
func IsMasked(v string) bool {
	return strings.HasSuffix(v, RedactedValue)
}

// maskPrefix is how provider keys are shown: enough to tell two keys apart, never enough to use one.
func maskPrefix(v string) string {
	if len(v) > 4 {
		return v[:4] + RedactedValue
	}
	return RedactedValue
}

// redactMap returns a copy of m with the value of every secret-looking key replaced, at any depth. Only
// text is masked. A key that is set but empty stays empty, so "not configured" is still visible.
func redactMap(m map[string]interface{}, mask func(string) string) map[string]interface{} {
	if m == nil {
		return nil
	}
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		switch val := v.(type) {
		case map[string]interface{}:
			out[k] = redactMap(val, mask)
		case string:
			if IsSecretKey(k) && val != "" {
				out[k] = mask(val)
			} else {
				out[k] = val
			}
		default:
			out[k] = v
		}
	}
	return out
}

func fullMask(string) string { return RedactedValue }

// RedactConfig returns a copy of a free-form config with its secrets masked.
func RedactConfig(m map[string]interface{}) map[string]interface{} {
	return redactMap(m, fullMask)
}

// Redacted returns a copy of the resolved ingredients that is safe to return from the API: a secret in a
// model, embedding, vector store or data config is masked. (The provider's key is not in here at all; see
// ResolvedModel.) The original, which the executor runs on, is not changed.
func (r *ResolvedIngredients) Redacted() *ResolvedIngredients {
	if r == nil {
		return nil
	}
	out := *r
	if r.Model != nil {
		m := *r.Model
		m.Config = RedactConfig(m.Config)
		out.Model = &m
	}
	if r.Embeddings != nil {
		out.Embeddings = make([]ResolvedEmbedding, len(r.Embeddings))
		for i, e := range r.Embeddings {
			e.Config = RedactConfig(e.Config)
			out.Embeddings[i] = e
		}
	}
	if r.VectorStores != nil {
		out.VectorStores = make([]ResolvedVectorStore, len(r.VectorStores))
		for i, v := range r.VectorStores {
			v.Config = RedactConfig(v.Config)
			out.VectorStores[i] = v
		}
	}
	if r.Data != nil {
		out.Data = make([]ResolvedData, len(r.Data))
		for i, d := range r.Data {
			d.Config = RedactConfig(d.Config)
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
			in.Config = RedactConfig(in.Config)
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

// Redacted returns a copy of the provider that is safe to return from the API: the keys in its config
// (api_key, api_secret and any other secret-looking setting) and every key in its rotation pool are
// masked down to a short prefix. A secret reference (secret_ref) names where a key lives and is not
// itself a secret, so it is left as it is.
func (p ModelProvider) Redacted() ModelProvider {
	p.Config = redactMap(p.Config, maskPrefix)
	if p.APIKeys != nil {
		keys := make([]APIKeyEntry, len(p.APIKeys))
		for i, k := range p.APIKeys {
			if k.Key != "" {
				k.Key = maskPrefix(k.Key)
			}
			keys[i] = k
		}
		p.APIKeys = keys
	}
	return p
}

// restoreMap puts back, in incoming, the stored value of every secret that still carries the mask. A client
// that reads an object and writes it back has only ever seen the mask; it must not overwrite the secret with it.
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
			if IsSecretKey(k) && IsMasked(val) && old != nil {
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

// RestoreRedactedConfig does the same for a provider's config: a secret the caller sent back still masked
// keeps its stored value, so editing a provider's endpoint cannot overwrite its key with "sk-p****".
func RestoreRedactedConfig(incoming, stored map[string]interface{}) map[string]interface{} {
	return restoreMap(incoming, stored)
}
