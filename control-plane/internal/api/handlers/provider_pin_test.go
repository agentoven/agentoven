package handlers_test

import (
	"context"
	"net/http"
	"testing"
)

// A provider's credential is sent to its endpoint: repointing a provider that holds one needs the
// permission the edition gives (ProviderRepoint) or a credential entered in the same request.
func TestRepointingAProviderThatHoldsACredentialIsPinned(t *testing.T) {
	h, _, _ := vaultHandlers(t)
	callAgentAPI(h.CreateProvider, http.MethodPost, "/api/v1/providers", nil,
		`{"name":"oai","kind":"openai","endpoint":"https://api.example.com/v1","config":{"api_key":"sk-AAAAAAAAAAAA"}}`)
	allowed := false
	h.ProviderRepoint = func(*http.Request) bool { return allowed }
	endpoint := func() string {
		p, _ := h.Store.GetProvider(context.Background(), "default", "oai")
		return p.Endpoint
	}

	// The attack: a caller who may update a provider but not manage them points it at their own host.
	if code := putProvider(h, "oai", `{"endpoint":"http://attacker.invalid/v1"}`); code != http.StatusForbidden {
		t.Fatalf("repoint without the permission: %d, want 403", code)
	}
	if endpoint() != "https://api.example.com/v1" {
		t.Fatalf("endpoint changed to %q", endpoint())
	}
	// Same host, other path, scheme or port are different origins only when host/scheme/port differ.
	if code := putProvider(h, "oai", `{"endpoint":"https://api.example.com/v2"}`); code != http.StatusOK {
		t.Errorf("a path change on the same host: %d", code)
	}
	if code := putProvider(h, "oai", `{"endpoint":"https://api.example.com:8443/v2"}`); code != http.StatusForbidden {
		t.Errorf("another port is another host: %d", code)
	}
	if code := putProvider(h, "oai", `{"endpoint":"http://api.example.com/v2"}`); code != http.StatusForbidden {
		t.Errorf("another scheme is another origin: %d", code)
	}
	// Echoing the stored masked key back with the new endpoint is not entering a credential.
	if code := putProvider(h, "oai", `{"endpoint":"http://attacker.invalid/v1","config":{"api_key":"sk-A****"}}`); code != http.StatusForbidden {
		t.Errorf("masked key echoed: %d, want 403", code)
	}
	// A new pool that keeps one old entry by reference is not a replacement either.
	if code := putProvider(h, "oai", `{"endpoint":"http://attacker.invalid/v1","api_keys":[{"key":"sk-NEWNEWNEWNEW","label":"n","enabled":true},{"secret_ref":"vault://provider-oai-key-1","label":"o","enabled":true}]}`); code != http.StatusForbidden {
		t.Errorf("a pool that keeps an old credential: %d, want 403", code)
	}
	if endpoint() != "https://api.example.com/v2" {
		t.Fatalf("endpoint changed to %q by a refused update", endpoint())
	}

	// Entering the key again replaces the credential, so the host may change with it.
	if code := putProvider(h, "oai", `{"endpoint":"https://other.example.com/v1","config":{"api_key":"sk-BBBBBBBBBBBB"}}`); code != http.StatusOK {
		t.Errorf("repoint with a fresh key: %d", code)
	}
	// The permission lets a caller repoint without the key.
	allowed = true
	if code := putProvider(h, "oai", `{"endpoint":"https://third.example.com/v1"}`); code != http.StatusOK {
		t.Errorf("repoint with provider:manage: %d", code)
	}
}

// A provider with no credential has nothing to steal: its endpoint stays freely editable, which is how a
// local model server is pointed somewhere else.
func TestRepointingAProviderWithoutACredentialIsFree(t *testing.T) {
	h, _, _ := vaultHandlers(t)
	callAgentAPI(h.CreateProvider, http.MethodPost, "/api/v1/providers", nil, `{"name":"local","kind":"ollama","endpoint":"http://localhost:11434"}`)
	h.ProviderRepoint = func(*http.Request) bool { return false }
	if code := putProvider(h, "local", `{"endpoint":"http://other-host:11434"}`); code != http.StatusOK {
		t.Fatalf("got %d", code)
	}
}
