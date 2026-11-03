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

// serveGlobal runs a request through the auth middleware alone, the way a route with no identity guard of its
// own meets it: with the global flag off it lets anyone in, with it on it wants a credential.
func serveGlobal(t *testing.T, requireAuth bool, method, path string, headers map[string]string) (int, *contracts.Identity, string) {
	t.Helper()
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
	h := middleware.TenantExtractor(middleware.NewAuthMiddleware(keyChain{}).Handler(inner))
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, seen, kitchen
}

// The MCP gateway runs a kitchen's tools with the credentials stored for them. It used to be a public path,
// so even with AGENTOVEN_REQUIRE_AUTH=true anyone could call it (review 5, finding 1). Now it follows the
// global flag like every other route: protected when the deployment requires authentication, unchanged
// (anonymous use works) when it does not, which is the community edition's default. The enterprise edition
// demands an identity on top (agentoven-pro routeguard).
func TestTheMCPGatewayFollowsTheGlobalAuthFlag(t *testing.T) {
	for _, tc := range []struct{ method, path string }{{"POST", "/mcp"}, {"POST", "/mcp/"}, {"GET", "/mcp/sse"}} {
		if code, _, _ := serveGlobal(t, true, tc.method, tc.path, map[string]string{"X-Kitchen": "victim"}); code != http.StatusUnauthorized {
			t.Errorf("anonymous %s %s with authentication required: %d, want 401", tc.method, tc.path, code)
		}
		if code, id, _ := serveGlobal(t, false, tc.method, tc.path, nil); code != http.StatusOK || id != nil {
			t.Errorf("anonymous %s %s with authentication off must keep working: %d %v", tc.method, tc.path, code, id)
		}
		// A credential is looked at, and the kitchen it is scoped to wins over the header.
		code, id, kitchen := serveGlobal(t, true, tc.method, tc.path, map[string]string{"Authorization": "Bearer user-key"})
		if code != http.StatusOK || id == nil || kitchen != "alpha" {
			t.Errorf("%s %s with a key scoped to alpha: %d, identity %v, kitchen %q", tc.method, tc.path, code, id, kitchen)
		}
		if code, _, _ := serveGlobal(t, true, tc.method, tc.path, map[string]string{"Authorization": "Bearer user-key", "X-Kitchen": "victim"}); code != http.StatusForbidden {
			t.Errorf("%s %s: a key scoped to alpha naming kitchen victim got %d; want 403", tc.method, tc.path, code)
		}
	}
}
