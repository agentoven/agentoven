package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/api/middleware"
	"github.com/agentoven/agentoven/control-plane/pkg/a2aauth"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	pkgmw "github.com/agentoven/agentoven/control-plane/pkg/middleware"
)

// keyChain authenticates the bearer token "user-key" as a baker scoped to kitchen "alpha"
// and "free-key" as an unscoped admin; anything else is anonymous.
type keyChain struct{}

func (keyChain) RegisterProvider(contracts.AuthProvider) {}
func (keyChain) Authenticate(_ context.Context, r *http.Request) (*contracts.Identity, error) {
	switch strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") {
	case "user-key":
		return &contracts.Identity{Subject: "u1", Provider: "apikey", Role: "baker", Kitchen: "alpha"}, nil
	case "free-key":
		return &contracts.Identity{Subject: "u2", Provider: "apikey", Role: "admin"}, nil
	}
	return nil, nil
}

// serve runs a request through the real auth middleware and RequireIdentity (the guard the
// router puts on A2A execution routes) and reports the status and what the handler saw.
func serve(t *testing.T, requireAuth bool, method, path string, headers map[string]string) (int, *contracts.Identity, string) {
	t.Helper()
	if _, set := os.LookupEnv("AGENT_NAME"); set && !strings.HasPrefix(t.Name(), "TestAPodWithoutAToken") {
		t.Setenv("AGENT_NAME", "")
	}
	if requireAuth {
		t.Setenv("AGENTOVEN_REQUIRE_AUTH", "true")
	} else {
		t.Setenv("AGENTOVEN_REQUIRE_AUTH", "false")
	}
	var seen *contracts.Identity
	var kitchen string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, kitchen = pkgmw.GetIdentity(r.Context()), pkgmw.GetKitchen(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	var h http.Handler = middleware.RequireIdentity(inner)
	if !strings.HasSuffix(path, "agent-card.json") && !strings.HasSuffix(path, "agent.json") {
		h = middleware.RequireIdentity(inner)
	} else {
		h = inner // discovery routes carry no identity guard
	}
	h = middleware.TenantExtractor(middleware.NewAuthMiddleware(keyChain{}).Handler(h))

	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, seen, kitchen
}

var a2aExecutionPaths = []string{
	"/a2a",
	"/agents/bot/a2a",
	"/env/prod/agents/bot/a2a",
	"/api/v1/agents/bot/a2a",
}

// The bug: POST /a2a was on the public-path list, so it ran any ready agent with no identity.
func TestA2AExecutionRequiresAnIdentity(t *testing.T) {
	for _, requireAuth := range []bool{false, true} {
		for _, path := range a2aExecutionPaths {
			if code, id, _ := serve(t, requireAuth, http.MethodPost, path, nil); code != http.StatusUnauthorized || id != nil {
				t.Errorf("anonymous POST %s (require_auth=%v): %d, identity=%v; want 401", path, requireAuth, code, id)
			}
		}
	}
}

func TestA2ADiscoveryStaysPublicButOnlyForGET(t *testing.T) {
	for _, path := range []string{"/a2a/.well-known/agent-card.json", "/.well-known/agent.json"} {
		if code, _, _ := serve(t, true, http.MethodGet, path, nil); code != http.StatusOK {
			t.Errorf("GET %s should be public even when auth is required: %d", path, code)
		}
		if code, _, _ := serve(t, true, http.MethodPost, path, nil); code == http.StatusOK {
			t.Errorf("POST %s must not be public", path)
		}
	}
}

func TestA2AIdentifiesACallerWhoSendsAValidTokenAndBindsTheKitchen(t *testing.T) {
	for _, path := range a2aExecutionPaths {
		code, id, kitchen := serve(t, false, http.MethodPost, path, map[string]string{"Authorization": "Bearer user-key"})
		if code != http.StatusOK || id == nil || id.Subject != "u1" || kitchen != "alpha" {
			t.Errorf("POST %s with a key: %d, identity=%v, kitchen=%q; want 200, u1, alpha", path, code, id, kitchen)
		}
	}
	// A caller scoped to one kitchen cannot claim another: the cross-kitchen grant check
	// downstream starts from the caller's kitchen, so this is where it is made trustworthy.
	if code, _, _ := serve(t, false, http.MethodPost, "/a2a", map[string]string{"Authorization": "Bearer user-key", "X-Kitchen": "beta"}); code != http.StatusForbidden {
		t.Errorf("a key scoped to alpha claiming beta: %d, want 403", code)
	}
	if code, _, kitchen := serve(t, false, http.MethodPost, "/a2a", map[string]string{"Authorization": "Bearer free-key", "X-Kitchen": "beta"}); code != http.StatusOK || kitchen != "beta" {
		t.Errorf("an unscoped key may name a kitchen: %d %q", code, kitchen)
	}
}

func TestTheWorkflowEngineIsRecognisedOnA2AExecutionOnly(t *testing.T) {
	good := map[string]string{a2aauth.InternalHeader: a2aauth.InternalKey(), "X-Kitchen": "prod"}
	for _, requireAuth := range []bool{false, true} {
		for _, path := range a2aExecutionPaths {
			code, id, kitchen := serve(t, requireAuth, http.MethodPost, path, good)
			if code != http.StatusOK || id == nil || id.Provider != "internal" || kitchen != "prod" {
				t.Errorf("engine POST %s (require_auth=%v): %d, %v, %q", path, requireAuth, code, id, kitchen)
			}
		}
	}
	// Wrong or empty secret, and the right secret on a route that is not A2A execution.
	for name, h := range map[string]map[string]string{
		"wrong":  {a2aauth.InternalHeader: "nope"},
		"empty":  {a2aauth.InternalHeader: ""},
		"absent": nil,
	} {
		if code, _, _ := serve(t, false, http.MethodPost, "/a2a", h); code != http.StatusUnauthorized {
			t.Errorf("%s internal secret accepted: %d", name, code)
		}
	}
	for _, path := range []string{"/api/v1/agents/bot/invoke", "/api/v1/agents", "/mcp-not-a2a"} {
		if code, _, _ := serve(t, true, http.MethodPost, path, good); code == http.StatusOK {
			t.Errorf("the internal secret must not authenticate %s", path)
		}
	}
}

func TestAPodAcceptsOnlyTheControlPlaneItWasToldAbout(t *testing.T) {
	t.Setenv(a2aauth.PodTokenEnv, "pod-token-for-bot")
	for _, requireAuth := range []bool{false, true} {
		if code, id, _ := serve(t, requireAuth, http.MethodPost, "/a2a", map[string]string{a2aauth.PodTokenHeader: "pod-token-for-bot"}); code != http.StatusOK || id == nil || id.Provider != "internal" {
			t.Errorf("control plane with the pod's token (require_auth=%v): %d %v", requireAuth, code, id)
		}
	}
	if code, _, _ := serve(t, false, http.MethodPost, "/a2a", map[string]string{a2aauth.PodTokenHeader: "token-for-another-agent"}); code != http.StatusUnauthorized {
		t.Errorf("another agent's token: %d", code)
	}
	if code, _, _ := serve(t, false, http.MethodPost, "/a2a", nil); code != http.StatusUnauthorized {
		t.Errorf("no token: %d", code)
	}
	if code, _, _ := serve(t, true, http.MethodPost, "/api/v1/agents/bot/invoke", map[string]string{a2aauth.PodTokenHeader: "pod-token-for-bot"}); code == http.StatusOK {
		t.Error("a pod token must not authenticate anything but A2A execution")
	}
}

// A control plane has no pod token, so a caller who sends the header gets nothing for it.
func TestAControlPlaneIgnoresAPodTokenHeader(t *testing.T) {
	t.Setenv(a2aauth.PodTokenEnv, "")
	if code, _, _ := serve(t, false, http.MethodPost, "/a2a", map[string]string{a2aauth.PodTokenHeader: ""}); code != http.StatusUnauthorized {
		t.Errorf("empty header against empty config must not match: %d", code)
	}
	if code, _, _ := serve(t, false, http.MethodPost, "/a2a", map[string]string{a2aauth.PodTokenHeader: "anything"}); code != http.StatusUnauthorized {
		t.Errorf("a guessed header against no config: %d", code)
	}
}

// A pod launched without a token must keep working (it cannot tell the control plane from
// anyone), but only a pod: the control plane never has an AGENT_NAME.
func TestAPodWithoutATokenAcceptsA2AButAControlPlaneDoesNot(t *testing.T) {
	t.Setenv(a2aauth.PodTokenEnv, "")

	t.Setenv("AGENT_NAME", "")
	if code, _, _ := serve(t, false, http.MethodPost, "/a2a", nil); code != http.StatusUnauthorized {
		t.Errorf("a control plane must refuse an anonymous caller: %d", code)
	}

	t.Setenv("AGENT_NAME", "bot")
	if code, id, _ := serve(t, false, http.MethodPost, "/a2a", nil); code != http.StatusOK || id == nil || id.Provider != "internal" {
		t.Errorf("a legacy pod keeps accepting calls: %d %v", code, id)
	}
	// Not for routes that are not A2A execution.
	if code, _, _ := serve(t, true, http.MethodPost, "/api/v1/agents/bot/invoke", nil); code == http.StatusOK {
		t.Error("the legacy allowance covers A2A execution only")
	}

	// Once a pod has a token, the allowance is gone: only the control plane's token opens it.
	t.Setenv(a2aauth.PodTokenEnv, "tok")
	if code, _, _ := serve(t, false, http.MethodPost, "/a2a", nil); code != http.StatusUnauthorized {
		t.Errorf("a pod with a token must refuse an anonymous caller: %d", code)
	}
	if code, _, _ := serve(t, false, http.MethodPost, "/a2a", map[string]string{a2aauth.PodTokenHeader: "tok"}); code != http.StatusOK {
		t.Errorf("and accept the control plane: %d", code)
	}
}

func TestIsInternalCallerSaysWhoMayChooseProviderTLS(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/a2a", nil)
	if middleware.IsInternalCaller(req) {
		t.Error("an anonymous request is not the platform")
	}
	user := req.WithContext(pkgmw.SetIdentity(req.Context(), &contracts.Identity{Subject: "u", Provider: "apikey", Role: "admin"}))
	if middleware.IsInternalCaller(user) {
		t.Error("even an admin user is not the platform")
	}
	internal := req.WithContext(pkgmw.SetIdentity(req.Context(), &contracts.Identity{Subject: "internal:workflow", Provider: "internal"}))
	if !middleware.IsInternalCaller(internal) {
		t.Error("the engine is the platform")
	}
}
