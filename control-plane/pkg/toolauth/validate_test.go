package toolauth_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/toolauth"
)

func TestOnlyBearerApiKeyAndBasicAreAccepted(t *testing.T) {
	ok := []map[string]interface{}{
		nil, {},
		{"type": "bearer", "token": "t"}, {"type": "bearer", "credential_ref": "c"},
		{"type": "api-key", "header": "X-Api-Key", "key": "k"}, {"type": "api_key", "header": "X-Api-Key", "credential_ref": "c"},
		{"type": "basic", "username": "svc", "password": "p"}, {"type": "basic", "username": "svc", "credential_ref": "c"},
		{"token": "older registration with no type"},
	}
	for _, c := range ok {
		if err := toolauth.Validate(c); err != nil {
			t.Errorf("%v refused: %v", c, err)
		}
	}
	bad := map[string]map[string]interface{}{
		"oauth client credentials": {"type": "oauth2", "client_id": "i", "client_secret": "s", "token_url": "https://idp/token"},
		"oauth under a known type": {"type": "bearer", "token": "t", "client_secret": "s"},
		"custom headers":           {"type": "headers", "headers": map[string]interface{}{"Authorization": "Bearer x"}},
		"headers beside a bearer":  {"type": "bearer", "headers": map[string]interface{}{"X": "y"}},
		"a password on a bearer":   {"type": "bearer", "password": "p"},
		"a secret on no type":      {"secret": "s"},
		"a nested value":           {"type": "bearer", "token": map[string]interface{}{"v": "x"}},
		"a number":                 {"type": "bearer", "token": 12345},
		"a type that is not text":  {"type": 7},
		"header injection":         {"type": "api-key", "header": "X-A\r\nX-B: c", "key": "k"},
		"a header that frames":     {"type": "api-key", "header": "Content-Length", "key": "k"},
		"a value with a newline":   {"type": "bearer", "token": "a\nb"},
		"a huge value":             {"type": "bearer", "token": strings.Repeat("x", 5000)},
	}
	for name, c := range bad {
		err := toolauth.Validate(c)
		var inv *toolauth.InvalidError
		if !errors.As(err, &inv) {
			t.Errorf("%s: want an InvalidError, got %v", name, err)
		}
		if err != nil && strings.Contains(err.Error(), "s3cr3t") {
			t.Errorf("%s: the message echoes a value", name)
		}
	}
	// What the message says names the field, not its value.
	err := toolauth.Validate(map[string]interface{}{"type": "oauth2", "client_secret": "s3cr3t"})
	if err == nil || !strings.Contains(err.Error(), "oauth2") || strings.Contains(err.Error(), "s3cr3t") {
		t.Errorf("%v", err)
	}
}

func TestBasicAuthIsResolvedFromACredentialAndApplied(t *testing.T) {
	cfg := map[string]interface{}{"type": "basic", "username": "svc-agentoven", "credential_ref": "erp-pw"}
	got, err := toolauth.Resolve(context.Background(), creds{"k/erp-pw": "pw-version-1"}, "k", cfg)
	if err != nil || got["username"] != "svc-agentoven" || got["password"] != "pw-version-1" {
		t.Fatalf("%v %v", got, err)
	}
	r, _ := http.NewRequest("GET", "https://erp.example.test/x", nil)
	toolauth.Apply(r, got)
	if u, p, ok := r.BasicAuth(); !ok || u != "svc-agentoven" || p != "pw-version-1" {
		t.Errorf("basic auth was not set: %q %q %v", u, p, ok)
	}
	if vals := toolauth.Values(got); len(vals) != 1 || vals[0] != "pw-version-1" {
		t.Errorf("the password must be scrubbed from results: %v", vals)
	}
	red := toolauth.Redacted(map[string]interface{}{"type": "basic", "username": "svc", "password": "p", "credential_ref": "c", "client_secret_ref": "tool-x-client_secret"})
	if red["password"] != "****" || red["username"] != "svc" || red["client_secret_ref"] != "tool-x-client_secret" {
		t.Errorf("a name is shown, a password is not: %v", red)
	}
}

func TestLiteralSecretFindsTheOneFieldASupportedShapeKeepsASecretIn(t *testing.T) {
	cases := []struct {
		cfg               map[string]interface{}
		typ, field, value string
		ok                bool
	}{
		{map[string]interface{}{"type": "bearer", "token": "t"}, "bearer", "token", "t", true},
		{map[string]interface{}{"token": "t"}, "bearer", "token", "t", true},
		{map[string]interface{}{"type": "api-key", "key": "k", "header": "X"}, "api-key", "key", "k", true},
		{map[string]interface{}{"type": "basic", "username": "u", "password": "p"}, "basic", "password", "p", true},
		{map[string]interface{}{"type": "basic", "password": "p", "credential_ref": "c"}, "", "", "", false},
		{map[string]interface{}{"type": "bearer", "password": "p"}, "", "", "", false},
		{nil, "", "", "", false},
	}
	for _, c := range cases {
		typ, field, value, ok := toolauth.LiteralSecret(c.cfg)
		if typ != c.typ || field != c.field || value != c.value || ok != c.ok {
			t.Errorf("%v: got %q %q %q %v", c.cfg, typ, field, value, ok)
		}
	}
}

func TestStrayFieldsAreEveryValueThatIsNotAName(t *testing.T) {
	got := toolauth.StrayFields(map[string]interface{}{
		"type": "oauth2", "header": "X", "username": "u", "credential_ref": "c", "already_ref": "tool-x-already",
		"client_secret": "s", "client_id": "i", "empty": "", "nothing": nil, "headers": map[string]interface{}{"Authorization": "Bearer x"},
	})
	want := map[string]string{"client_secret": "s", "client_id": "i", "headers": `{"Authorization":"Bearer x"}`}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q", k, got[k], v)
		}
	}
}
