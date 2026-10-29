package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
)

type fixedProvider struct{ id *contracts.Identity }

func (p fixedProvider) Name() string  { return "fixed" }
func (p fixedProvider) Enabled() bool { return true }
func (p fixedProvider) Authenticate(context.Context, *http.Request) (*contracts.Identity, error) {
	c := *p.id
	return &c, nil
}

func TestEnricherRunsOnTheIdentityAProviderReturned(t *testing.T) {
	chain := NewProviderChain()
	chain.RegisterProvider(fixedProvider{&contracts.Identity{Subject: "u", Role: "viewer"}})
	chain.SetEnricher(func(_ context.Context, r *http.Request, id *contracts.Identity) (*contracts.Identity, error) {
		out := *id
		out.Role = "chef"
		out.Claims = map[string]string{"kitchen_seen": r.Header.Get("X-Kitchen")}
		return &out, nil
	})
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Kitchen", "alpha")
	id, err := chain.Authenticate(context.Background(), r)
	if err != nil || id == nil || id.Role != "chef" || id.Claims["kitchen_seen"] != "alpha" {
		t.Fatalf("enriched identity = %+v, %v", id, err)
	}
}

func TestEnricherCanRefuseWithATypedError(t *testing.T) {
	chain := NewProviderChain()
	chain.RegisterProvider(fixedProvider{&contracts.Identity{Subject: "u"}})
	chain.SetEnricher(func(context.Context, *http.Request, *contracts.Identity) (*contracts.Identity, error) {
		return nil, &contracts.AuthError{Status: 403, Code: "kitchen_access_denied", Message: "no"}
	})
	id, err := chain.Authenticate(context.Background(), httptest.NewRequest("GET", "/", nil))
	var ae *contracts.AuthError
	if id != nil || err == nil {
		t.Fatalf("want a refusal, got %+v %v", id, err)
	}
	if e, ok := err.(*contracts.AuthError); !ok || e.Status != 403 {
		t.Fatalf("error = %#v (%v)", err, ae)
	}
}

func TestNoEnricherLeavesTheIdentityAlone(t *testing.T) {
	chain := NewProviderChain()
	chain.RegisterProvider(fixedProvider{&contracts.Identity{Subject: "u", Role: "baker"}})
	id, err := chain.Authenticate(context.Background(), httptest.NewRequest("GET", "/", nil))
	if err != nil || id.Role != "baker" {
		t.Fatalf("%+v %v", id, err)
	}
}
