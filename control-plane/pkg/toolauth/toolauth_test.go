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

type policyCreds struct {
	creds
	deny  error
	got   toolauth.Use
	reads int
}

func (p *policyCreds) GetKitchenCredential(ctx context.Context, k, n string) (*models.KitchenCredential, error) {
	p.reads++
	return p.creds.GetKitchenCredential(ctx, k, n)
}
func (p *policyCreds) CheckUse(_ context.Context, _, _ string, u toolauth.Use) error {
	p.got = u
	return p.deny
}

func TestResolveForAsksThePolicyBeforeReadingTheValue(t *testing.T) {
	p := &policyCreds{creds: creds{"kit/k": "tok"}}
	cfg := map[string]interface{}{"type": "bearer", "credential_ref": "k"}
	got, err := toolauth.ResolveFor(context.Background(), p, "kit", toolauth.Use{Skill: "s", Endpoint: "https://api.example/x"}, cfg)
	if err != nil || got["token"] != "tok" || p.got.Skill != "s" || p.got.Endpoint != "https://api.example/x" {
		t.Fatalf("%v %v %+v", got, err, p.got)
	}
	p.deny, p.reads = errors.New("host not allowed"), 0
	if _, err := toolauth.ResolveFor(context.Background(), p, "kit", toolauth.Use{}, cfg); err == nil || p.reads != 0 {
		t.Errorf("a refused use must not load the value: err %v reads %d", err, p.reads)
	}
	// A store with no policy allows the use, and a literal token needs no policy.
	if _, err := toolauth.ResolveFor(context.Background(), creds{"kit/k": "tok"}, "kit", toolauth.Use{}, cfg); err != nil {
		t.Error(err)
	}
	if got, err := toolauth.ResolveFor(context.Background(), p, "kit", toolauth.Use{}, map[string]interface{}{"type": "bearer", "token": "lit"}); err != nil || got["token"] != "lit" {
		t.Errorf("literal: %v %v", got, err)
	}
}

func TestAServerThatEchoesTheCredentialDoesNotHandItToTheModel(t *testing.T) {
	resolved := map[string]interface{}{"type": "bearer", "token": "tok-SECRET-123456"}
	res := &models.MCPToolResult{Content: []models.MCPContent{
		{Type: "text", Text: "echo: Authorization: Bearer tok-SECRET-123456 and again tok-SECRET-123456"},
		{Type: "text", Text: "nothing to see"},
	}}
	out, err := toolauth.ScrubResult(res, errors.New("401 for token tok-SECRET-123456"), resolved)
	if strings.Contains(out.Content[0].Text, "tok-SECRET") || strings.Contains(err.Error(), "tok-SECRET") {
		t.Fatalf("the credential survived: %q / %v", out.Content[0].Text, err)
	}
	if out.Content[0].Text != "echo: Authorization: Bearer [redacted] and again [redacted]" || out.Content[1].Text != "nothing to see" {
		t.Errorf("%q", out.Content[0].Text)
	}
	// An api-key credential too; a short value is left alone (it would mangle ordinary text); no credential, no change.
	out, _ = toolauth.ScrubResult(&models.MCPToolResult{Content: []models.MCPContent{{Text: "k=abcdefghij"}}}, nil, map[string]interface{}{"type": "api-key", "key": "abcdefghij"})
	if out.Content[0].Text != "k=[redacted]" {
		t.Errorf("api key: %q", out.Content[0].Text)
	}
	short, _ := toolauth.ScrubResult(&models.MCPToolResult{Content: []models.MCPContent{{Text: "ab is fine"}}}, nil, map[string]interface{}{"token": "ab"})
	if short.Content[0].Text != "ab is fine" {
		t.Errorf("short: %q", short.Content[0].Text)
	}
	same, e := toolauth.ScrubResult(nil, nil, nil)
	if same != nil || e != nil {
		t.Error("nil in, nil out")
	}
}
