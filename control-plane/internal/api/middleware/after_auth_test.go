package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/api/middleware"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	pkgmw "github.com/agentoven/agentoven/control-plane/pkg/middleware"
)

// install runs fn as an outer middleware that adds after-authentication functions to the request.
func install(next http.Handler, fns ...pkgmw.AfterAuthFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		for _, fn := range fns {
			ctx = pkgmw.WithAfterAuth(ctx, fn)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// The after-authentication seam is how an edition acts on a request once it is believed: Pro settles the
// kitchen's canonical name and writes the intent record of a decision there. It must see the verified
// identity and the settled kitchen, run in order, be able to change the request, and be able to refuse it.
func TestAfterAuthHooksSeeTheVerifiedRequestRunInOrderAndCanRefuse(t *testing.T) {
	t.Setenv("AGENTOVEN_REQUIRE_AUTH", "false")
	var order []string
	var sawIdentity *contracts.Identity
	var sawKitchen string
	first := func(r *http.Request, id *contracts.Identity) (*http.Request, error) {
		order = append(order, "first")
		sawIdentity, sawKitchen = id, pkgmw.GetKitchen(r.Context())
		return r.WithContext(pkgmw.SetKitchen(r.Context(), "canonical")), nil
	}
	second := func(r *http.Request, _ *contracts.Identity) (*http.Request, error) {
		order = append(order, "second:"+pkgmw.GetKitchen(r.Context()))
		return nil, nil
	}
	var served string
	h := install(middleware.TenantExtractor(middleware.NewAuthMiddleware(keyChain{}).Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = pkgmw.GetKitchen(r.Context())
	}))), first, second)

	req := httptest.NewRequest("POST", "/api/v1/agents", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer free-key")
	req.Header.Set("X-Kitchen", "someid")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if strings.Join(order, ",") != "first,second:canonical" || served != "canonical" {
		t.Fatalf("order %v, served kitchen %q", order, served)
	}
	if sawIdentity == nil || sawIdentity.Subject != "u2" || sawKitchen != "someid" {
		t.Fatalf("the hook must see the identity and the kitchen the router settled: %+v %q", sawIdentity, sawKitchen)
	}

	// An anonymous request on an installation that does not require authentication reaches the hooks with no identity.
	order, sawIdentity = nil, &contracts.Identity{}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/agents", strings.NewReader("{}")))
	if sawIdentity != nil || len(order) != 2 {
		t.Fatalf("anonymous: identity %+v, hooks run %v", sawIdentity, order)
	}

	// A refusal answers the request with its own status and the handler never runs.
	ran := false
	refuse := func(*http.Request, *contracts.Identity) (*http.Request, error) {
		return nil, &contracts.AuthError{Status: http.StatusServiceUnavailable, Code: "audit_unavailable", Message: "no trail"}
	}
	h = install(middleware.NewAuthMiddleware(keyChain{}).Handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { ran = true })), refuse)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/agents", strings.NewReader("{}")))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "audit_unavailable") || ran {
		t.Fatalf("refusal: %d %s ran=%v", rec.Code, rec.Body, ran)
	}

	// A request that fails authentication never reaches them: nothing about it is believed.
	called := false
	t.Setenv("AGENTOVEN_REQUIRE_AUTH", "true")
	h = install(middleware.NewAuthMiddleware(keyChain{}).Handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})),
		func(*http.Request, *contracts.Identity) (*http.Request, error) { called = true; return nil, nil })
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/agents", strings.NewReader("{}")))
	if rec.Code != http.StatusUnauthorized || called {
		t.Fatalf("unauthenticated: %d, hooks called=%v", rec.Code, called)
	}
}
