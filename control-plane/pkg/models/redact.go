package models

import (
	"net/url"
	"strings"
)

// RedactedValue stands in for a secret in anything returned to an API caller. A caller can tell a key
// is set (the value is not empty) without being able to read it.
const RedactedValue = "****"

// IsSecretKey says whether a config key names a secret: api_key, a token, a password and the like.
// It is deliberately narrow. Settings such as max_tokens or top_k are not secrets and must stay readable.
func IsSecretKey(k string) bool {
	k = strings.ToLower(k)
	// A reference (secret_ref, api_key_ref) names where a secret lives; it is not the secret, and a client
	// that reads a rule must see which credential it points at.
	if strings.HasSuffix(k, "_ref") || strings.HasSuffix(k, "-ref") {
		return false
	}
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

// secretQueryParams are query parameters that carry a credential whatever they are named exactly: a URL
// with ?token=... in an ingredient or a rule is a secret even though no field is called "token".
var secretQueryParams = []string{"token", "key", "sig", "signature", "code", "auth", "apikey", "api_key", "api-key", "access_token", "password", "secret"}

func secretParam(name string) bool {
	n := strings.ToLower(name)
	for _, p := range secretQueryParams {
		if n == p {
			return true
		}
	}
	return IsSecretKey(n)
}

// RedactURL masks the credentials a URL carries: the user and password of its userinfo (a token is often
// the user name) and the value of any secret-looking query parameter. A string that is not a URL, or a URL
// with nothing secret in it, is returned as it is. The rest of the URL stays readable so a person can still
// tell which endpoint it is.
func RedactURL(s string) string {
	if !strings.Contains(s, "://") {
		return s
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return s
	}
	changed := false
	if u.User != nil {
		u.User = url.User(RedactedValue)
		changed = true
	}
	if u.RawQuery != "" {
		pairs := strings.Split(u.RawQuery, "&")
		for i, pair := range pairs {
			name, value, ok := strings.Cut(pair, "=")
			if unescaped, err := url.QueryUnescape(name); err == nil {
				name = unescaped
			}
			if ok && value != "" && secretParam(name) {
				pairs[i] = pair[:strings.Index(pair, "=")] + "=" + RedactedValue
				changed = true
			}
		}
		u.RawQuery = strings.Join(pairs, "&")
	}
	if !changed {
		return s
	}
	return u.String()
}

// redactValue masks one value found in a free-form config: text that is a URL loses its credentials, and
// a value under a secret key (or under "headers", whose names are free) is masked whole, at any depth in
// a list or a map. A key that is set but empty stays empty, so "not configured" is still visible.
func redactValue(v interface{}, secret bool, mask func(string) string) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(val))
		for k, inner := range val {
			out[k] = redactValue(inner, secret || IsSecretKey(k) || strings.EqualFold(k, "headers"), mask)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(val))
		for k, inner := range val {
			if inner != "" && (secret || IsSecretKey(k) || strings.EqualFold(k, "headers")) {
				out[k] = mask(inner)
			} else {
				out[k] = RedactURL(inner)
			}
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(val))
		for i, inner := range val {
			out[i] = redactValue(inner, secret, mask)
		}
		return out
	case []string:
		out := make([]string, len(val))
		for i, inner := range val {
			out[i] = redactValue(inner, secret, mask).(string)
		}
		return out
	case string:
		switch {
		case val == "", IsReference(val):
			return val // a reference names where a secret lives; it is what a reader should see
		case secret:
			return mask(val)
		default:
			return RedactURL(val)
		}
	case nil, bool:
		return v
	default:
		if secret {
			return mask("x") // a number or other scalar under a secret key is masked too
		}
		return v
	}
}

// redactMap returns a copy of m with the value of every secret-looking key replaced, at any depth, in lists
// as well as maps, and the credentials in any URL it holds masked.
func redactMap(m map[string]interface{}, mask func(string) string) map[string]interface{} {
	if m == nil {
		return nil
	}
	return redactValue(m, false, mask).(map[string]interface{})
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
	if a.Draft != nil && a.Draft.Ingredients != nil {
		d := *a.Draft // the draft carries ingredients too, with the same secrets in their config
		d.Ingredients = make([]Ingredient, len(a.Draft.Ingredients))
		for i, in := range a.Draft.Ingredients {
			in.Config = RedactConfig(in.Config)
			d.Ingredients[i] = in
		}
		a.Draft = &d
	}
	a.EnvEndpoints = redactStrings(a.EnvEndpoints) // an endpoint can carry a token in its URL
	a.Guardrails = RedactGuardrails(a.Guardrails)
	if a.Draft != nil && a.Draft.Guardrails != nil {
		d := *a.Draft
		d.Guardrails = RedactGuardrails(a.Draft.Guardrails)
		a.Draft = &d
	}
	return a
}

// Redacted returns a copy of the rule that is safe to return from the API: a secret in its config (a
// llamaguard rule's api_key) and the credentials in any URL it holds are masked.
func (g Guardrail) Redacted() Guardrail {
	g.Config = RedactConfig(g.Config)
	return g
}

// RedactGuardrails does Guardrail.Redacted for a list; nil stays nil so "not set" stays visible.
func RedactGuardrails(rules []Guardrail) []Guardrail {
	if rules == nil {
		return nil
	}
	out := make([]Guardrail, len(rules))
	for i, r := range rules {
		out[i] = r.Redacted()
	}
	return out
}

// RestoreRedactedGuardrails returns incoming with any masked config value replaced by the one already stored
// for the same rule (matched by id, else by kind and stage), so a client that reads an agent and writes its
// rules back cannot replace a rule's api_key with the mask.
func RestoreRedactedGuardrails(incoming, stored []Guardrail) []Guardrail {
	if len(incoming) == 0 || len(stored) == 0 {
		return incoming
	}
	out := make([]Guardrail, len(incoming))
	for i, in := range incoming {
		for _, old := range stored {
			if (in.ID != "" && old.ID == in.ID) || (in.ID == "" && old.Kind == in.Kind && old.Stage == in.Stage) {
				in.Config = restoreMap(in.Config, old.Config)
				break
			}
		}
		out[i] = in
	}
	return out
}

// Redacted returns a copy of the agent's configuration that is safe to return from the API.
func (c AgentConfig) Redacted() AgentConfig {
	c.Agent = c.Agent.Redacted()
	c.Ingredients = *c.Ingredients.Redacted()
	return c
}

// Redacted returns a copy of the kitchen's settings that is safe to return from the API: the key of the
// validation model is shown as a short prefix, like a provider's.
func (k KitchenSettings) Redacted() KitchenSettings {
	if k.ValidationAPIKey != "" && !IsReference(k.ValidationAPIKey) {
		k.ValidationAPIKey = maskPrefix(k.ValidationAPIKey)
	}
	k.ValidationEndpoint = RedactURL(k.ValidationEndpoint)
	if k.ArchivePolicy != nil {
		ap := *k.ArchivePolicy // backend settings can hold access keys
		ap.Config = redactStrings(ap.Config)
		k.ArchivePolicy = &ap
	}
	k.TestRunnerConfig = redactStrings(k.TestRunnerConfig)
	return k
}

// redactStrings is redactMap for a map of text: values under secret keys and credentials in URLs are masked.
func redactStrings(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	return redactValue(m, false, fullMask).(map[string]string)
}

// RestoreRedacted returns incoming with every masked setting replaced by the one stored, so a client that
// reads the settings and writes them back cannot overwrite a key with its mask.
func (k KitchenSettings) RestoreRedacted(stored *KitchenSettings) KitchenSettings {
	if stored == nil {
		return k
	}
	if IsMasked(k.ValidationAPIKey) {
		k.ValidationAPIKey = stored.ValidationAPIKey
	}
	if k.ValidationEndpoint != stored.ValidationEndpoint && k.ValidationEndpoint == RedactURL(stored.ValidationEndpoint) {
		k.ValidationEndpoint = stored.ValidationEndpoint
	}
	if k.ArchivePolicy != nil && stored.ArchivePolicy != nil {
		ap := *k.ArchivePolicy
		ap.Config = restoreStrings(ap.Config, stored.ArchivePolicy.Config)
		k.ArchivePolicy = &ap
	}
	k.TestRunnerConfig = restoreStrings(k.TestRunnerConfig, stored.TestRunnerConfig)
	return k
}

func restoreStrings(incoming, stored map[string]string) map[string]string {
	if incoming == nil {
		return nil
	}
	out := make(map[string]string, len(incoming))
	for k, v := range incoming {
		old, ok := stored[k]
		if ok && old != "" && ((IsSecretKey(k) && IsMasked(v)) || (v != old && v == RedactURL(old))) {
			v = old
		}
		out[k] = v
	}
	return out
}

// IsReference says whether a value is a reference to a secret (vault://, env://, k8s://) rather than the
// secret itself. A reference is shown as it is.
func IsReference(v string) bool {
	return strings.HasPrefix(v, "vault://") || strings.HasPrefix(v, "env://") || strings.HasPrefix(v, "k8s://")
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

// restoreValue puts back, in incoming, the stored value of every secret that still carries the mask. A client
// that reads an object and writes it back has only ever seen the mask; it must not overwrite the secret with
// it. Text is restored when it is the mask itself (under a secret key) or exactly what RedactURL shows of the
// stored URL. Lists are matched by position, maps by key.
func restoreValue(incoming, stored interface{}, secret bool) interface{} {
	switch val := incoming.(type) {
	case map[string]interface{}:
		old, _ := stored.(map[string]interface{})
		out := make(map[string]interface{}, len(val))
		for k, inner := range val {
			out[k] = restoreValue(inner, old[k], secret || IsSecretKey(k) || strings.EqualFold(k, "headers"))
		}
		return out
	case []interface{}:
		old, _ := stored.([]interface{})
		out := make([]interface{}, len(val))
		for i, inner := range val {
			var o interface{}
			if i < len(old) {
				o = old[i]
			}
			out[i] = restoreValue(inner, o, secret)
		}
		return out
	case string:
		oldText, ok := stored.(string)
		if !ok || oldText == "" {
			return incoming
		}
		if (secret && IsMasked(val)) || (val != oldText && val == RedactURL(oldText)) {
			return oldText
		}
		return incoming
	default:
		return incoming
	}
}

func restoreMap(incoming, stored map[string]interface{}) map[string]interface{} {
	if incoming == nil {
		return nil
	}
	return restoreValue(incoming, stored, false).(map[string]interface{})
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
