package toolauth_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/agentoven/agentoven/control-plane/pkg/toolauth"
)

type creds map[string]string

func (c creds) GetKitchenCredential(_ context.Context, kitchen, name string) (*models.KitchenCredential, error) {
	if v, ok := c[kitchen+"/"+name]; ok {
		return &models.KitchenCredential{Kitchen: kitchen, Name: name, Value: v}, nil
	}
	return nil, errors.New("not found")
}

func TestAReferenceIsResolvedAtCallTimeSoARotationTakesEffectAtOnce(t *testing.T) {
	store := creds{"k/firecrawl": "token-v1"}
	cfg := toolauth.Ref("bearer", "", "firecrawl")

	got, err := toolauth.Resolve(context.Background(), store, "k", cfg)
	if err != nil || got["token"] != "token-v1" || got["type"] != "bearer" {
		t.Fatalf("%v %v", got, err)
	}
	store["k/firecrawl"] = "token-v2" // rotated, nothing re-registered
	if got, _ := toolauth.Resolve(context.Background(), store, "k", cfg); got["token"] != "token-v2" {
		t.Errorf("a rotation must be seen by the next call: %v", got)
	}
	if _, stored := cfg["token"]; stored {
		t.Error("resolving must not write the value back into the stored config")
	}

	api, err := toolauth.Resolve(context.Background(), creds{"k/acme": "key-1"}, "k", toolauth.Ref("api-key", "X-Api-Key", "acme"))
	if err != nil || api["key"] != "key-1" || api["header"] != "X-Api-Key" {
		t.Errorf("api-key: %v %v", api, err)
	}
}

func TestACredentialThatCannotBeReadFailsTheCallInsteadOfSkippingAuth(t *testing.T) {
	if _, err := toolauth.Resolve(context.Background(), creds{}, "k", toolauth.Ref("bearer", "", "gone")); err == nil || !strings.Contains(err.Error(), "gone") {
		t.Errorf("a missing credential must be an error naming it: %v", err)
	}
	if _, err := toolauth.Resolve(context.Background(), nil, "k", toolauth.Ref("bearer", "", "x")); err == nil {
		t.Error("no store means no authentication, which must not be silently allowed")
	}
	if _, err := toolauth.Resolve(context.Background(), creds{"k/x": "v"}, "k", toolauth.Ref("kerberos", "", "x")); err == nil {
		t.Error("an unsupported type is an error")
	}
	// A kitchen cannot read another's credential.
	if _, err := toolauth.Resolve(context.Background(), creds{"other/x": "v"}, "k", toolauth.Ref("bearer", "", "x")); err == nil {
		t.Error("resolution is per kitchen")
	}
}

func TestLiteralAndMissingConfigsPassThroughUnchanged(t *testing.T) {
	lit := map[string]interface{}{"type": "bearer", "token": "literal"}
	if got, err := toolauth.Resolve(context.Background(), creds{}, "k", lit); err != nil || got["token"] != "literal" {
		t.Errorf("older tools with a literal token keep working: %v %v", got, err)
	}
	if got, err := toolauth.Resolve(context.Background(), creds{}, "k", nil); err != nil || got != nil {
		t.Errorf("no auth is no auth: %v %v", got, err)
	}
}

func TestApplySetsTheHeadersAndIgnoresIncompleteConfigs(t *testing.T) {
	req := func() *http.Request { r, _ := http.NewRequest("POST", "http://x", nil); return r }
	r := req()
	toolauth.Apply(r, map[string]interface{}{"type": "bearer", "token": "t"})
	if r.Header.Get("Authorization") != "Bearer t" {
		t.Errorf("%v", r.Header)
	}
	r = req()
	toolauth.Apply(r, map[string]interface{}{"type": "api-key", "header": "X-K", "key": "k"})
	if r.Header.Get("X-K") != "k" {
		t.Errorf("%v", r.Header)
	}
	r = req()
	toolauth.Apply(r, map[string]interface{}{"type": "bearer", "token": ""})
	toolauth.Apply(r, map[string]interface{}{"type": "api-key", "key": "k"}) // no header name
	toolauth.Apply(r, nil)
	if len(r.Header) != 0 {
		t.Errorf("incomplete configs must set nothing: %v", r.Header)
	}
}

func TestRedactionKeepsTheCredentialNameAndHidesEverythingElse(t *testing.T) {
	got := toolauth.Redacted(map[string]interface{}{"type": "bearer", "token": "sk-live-1", "credential_ref": "firecrawl", "header": "X", "key": "k", "password": "p", "anything": "else"})
	if got["credential_ref"] != "firecrawl" || got["type"] != "bearer" || got["header"] != "X" {
		t.Errorf("the credential's name says what the tool uses and is kept: %v", got)
	}
	for _, k := range []string{"token", "key", "password", "anything"} {
		if got[k] != "****" {
			t.Errorf("%s was not hidden: %v", k, got[k])
		}
	}
	if toolauth.Redacted(nil) != nil {
		t.Error("nil stays nil")
	}

	tools := []models.MCPTool{{Name: "a", AuthConfig: map[string]interface{}{"type": "bearer", "token": "secret"}}}
	out := toolauth.RedactTools(tools)
	if out[0].AuthConfig["token"] != "****" || tools[0].AuthConfig["token"] != "secret" {
		t.Error("redaction must return a copy and never alter the stored tool")
	}
}
