package models

import (
	"encoding/json"
	"sort"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The response types are walked by reflection: every secret-bearing place in them is filled with a canary
// (a field or map key named like a secret, a header, a URL with a user, a password and a token), the type's
// redactor runs, and the canary must be nowhere in what comes out. A field added tomorrow to a type that
// goes to API callers is covered without anyone remembering to extend a redactor: either the redactor
// handles it or this test fails.
const canary = "CANARY-SECRET-VALUE"

// secretMap is what a free-form config (map[string]interface{}) is filled with: secrets under their own
// names, under free-form headers, in a list, in a nested map and in the credentials of a URL.
func secretMap() map[string]interface{} {
	return map[string]interface{}{
		"api_key":  canary,
		"password": canary,
		"token":    canary,
		"headers":  map[string]interface{}{"X-Whatever": canary, "Authorization": "Bearer " + canary},
		"url":      "https://user:" + canary + "@host.example/p?x=1&token=" + canary + "&y=2",
		"endpoint": "https://" + canary + "@host.example/",
		"nested":   map[string]interface{}{"client_secret": canary, "list": []interface{}{map[string]interface{}{"api_key": canary}}},
		"keys":     []interface{}{map[string]interface{}{"secret": canary}},
		"plain":    "visible",
	}
}

// notSecretBearing are fields that are free-form but hold no credential: JSON Schemas, labels, a tracker
// project key, the names of the secret references a bot may use, a KMS key id.
var notSecretBearing = map[string]bool{
	"schema": true, "output_schema": true, "input_schema": true, "tags": true, "metadata": true, "labels": true,
	"project_key": true, "credentials": true, "encryption_key_id": true, "custom_fields": true, "runtime_metadata": true,
}

// fill gives every settable place under v a value: a canary where the name says secret, "plain" elsewhere.
func fill(v reflect.Value, name string, depth int) {
	if depth > 8 {
		return
	}
	switch v.Kind() {
	case reflect.String:
		if IsSecretKey(name) {
			v.SetString(canary)
		} else {
			v.SetString("plain")
		}
	case reflect.Ptr:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem(), name, depth+1)
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fill(s.Index(0), name, depth+1)
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		switch v.Type().Elem().Kind() {
		case reflect.Interface:
			for k, val := range secretMap() {
				m.SetMapIndex(reflect.ValueOf(k), reflect.ValueOf(val))
			}
		case reflect.String:
			m.SetMapIndex(reflect.ValueOf("Authorization"), reflect.ValueOf("Bearer "+canary))
			m.SetMapIndex(reflect.ValueOf("api_key"), reflect.ValueOf(canary))
			m.SetMapIndex(reflect.ValueOf("prod"), reflect.ValueOf("https://u:"+canary+"@host.example/p?token="+canary))
			m.SetMapIndex(reflect.ValueOf("plain"), reflect.ValueOf("visible"))
		}
		if v.Type().Key().Kind() == reflect.String && m.Len() > 0 {
			v.Set(m)
		}
	case reflect.Interface:
		v.Set(reflect.ValueOf(secretMap()))
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(time.Time{}) {
			return
		}
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if !f.IsExported() || f.Tag.Get("json") == "-" {
				continue // what is not serialised is not returned
			}
			fname := strings.Split(f.Tag.Get("json"), ",")[0]
			if fname == "" {
				fname = f.Name
			}
			if notSecretBearing[fname] {
				v.Field(i).Set(reflect.Zero(f.Type))
				continue
			}
			fill(v.Field(i), fname, depth+1)
		}
	}
}

// leakPaths lists where in the JSON of v the canary still is, as paths a reader can act on.
func leakPaths(t *testing.T, v interface{}) []string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var doc interface{}
	_ = json.Unmarshal(raw, &doc)
	var out []string
	var walk func(path string, n interface{})
	walk = func(path string, n interface{}) {
		switch x := n.(type) {
		case map[string]interface{}:
			for k, c := range x {
				if strings.Contains(k, canary) {
					out = append(out, path+"."+k+" (a key)")
				}
				walk(path+"."+k, c)
			}
		case []interface{}:
			for i, c := range x {
				walk(path+"[]", c)
				_ = i
			}
		case string:
			if strings.Contains(x, canary) {
				out = append(out, path)
			}
		}
	}
	walk("", doc)
	sort.Strings(out)
	return out
}

func TestResponseTypesLeakNoSecret(t *testing.T) {
	cases := map[string]struct {
		build  func() interface{}
		redact func(interface{}) interface{}
	}{
		"Agent": {
			func() interface{} { var v Agent; fill(reflect.ValueOf(&v).Elem(), "", 0); return v },
			func(v interface{}) interface{} { return v.(Agent).Redacted() },
		},
		"AgentConfig": {
			func() interface{} { var v AgentConfig; fill(reflect.ValueOf(&v).Elem(), "", 0); return v },
			func(v interface{}) interface{} { return v.(AgentConfig).Redacted() },
		},
		"ResolvedIngredients": {
			func() interface{} { var v ResolvedIngredients; fill(reflect.ValueOf(&v).Elem(), "", 0); return &v },
			func(v interface{}) interface{} { return v.(*ResolvedIngredients).Redacted() },
		},
		"Guardrail": {
			func() interface{} { var v Guardrail; fill(reflect.ValueOf(&v).Elem(), "", 0); return v },
			func(v interface{}) interface{} { return v.(Guardrail).Redacted() },
		},
		"ModelProvider": {
			func() interface{} { var v ModelProvider; fill(reflect.ValueOf(&v).Elem(), "", 0); return v },
			func(v interface{}) interface{} { return v.(ModelProvider).Redacted() },
		},
		"KitchenSettings": {
			func() interface{} { var v KitchenSettings; fill(reflect.ValueOf(&v).Elem(), "", 0); return v },
			func(v interface{}) interface{} { return v.(KitchenSettings).Redacted() },
		},
	}
	for name, c := range cases {
		in := c.build()
		if len(leakPaths(t, in)) == 0 {
			t.Fatalf("%s: the test filled no secret, so it proves nothing", name)
		}
		if out := leakPaths(t, c.redact(in)); len(out) > 0 {
			t.Errorf("%s: a secret is returned to the API caller at: %s", name, strings.Join(dedupe(out), ", "))
		}
	}
}

// RedactURL keeps the rest of a URL readable and masks only what authenticates.
func TestRedactURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://host.example/path":                         "https://host.example/path",
		"https://u:p@host.example/path":                     "https://%2A%2A%2A%2A@host.example/path",
		"https://host.example/p?x=1&token=abc&y=2":          "https://host.example/p?x=1&token=****&y=2",
		"https://host.example/p?sig=abc&api_key=zzz":        "https://host.example/p?sig=****&api_key=****",
		"https://host.example/p?page=2":                     "https://host.example/p?page=2",
		"not a url":                                         "not a url",
		"https?://regex.example/":                           "https?://regex.example/",
		"https://host.example/p?token=":                     "https://host.example/p?token=",
	} {
		if got := RedactURL(in); got != want {
			t.Errorf("RedactURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// A client that reads a rule and writes it back keeps the stored secret, in a list and in a URL as well.
func TestRestoreRedactedGuardrailKeepsStoredSecrets(t *testing.T) {
	stored := []Guardrail{{ID: "r1", Kind: GuardrailLlamaGuard, Config: map[string]interface{}{
		"api_key": "K-REAL", "endpoint": "https://u:pw@guard.example/x?token=T-REAL&m=1", "keys": []interface{}{"A-REAL"},
	}}}
	back := RestoreRedactedGuardrails(RedactGuardrails(stored), stored)
	b, _ := json.Marshal(back)
	for _, want := range []string{"K-REAL", "pw", "T-REAL", "A-REAL"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the stored %q was lost on write-back: %s", want, b)
		}
	}
	// A secret the client really changed is kept as sent.
	changed := []Guardrail{{ID: "r1", Kind: GuardrailLlamaGuard, Config: map[string]interface{}{"api_key": "K-NEW"}}}
	if got := RestoreRedactedGuardrails(changed, stored)[0].Config["api_key"]; got != "K-NEW" {
		t.Errorf("a changed key was replaced by %v", got)
	}
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
