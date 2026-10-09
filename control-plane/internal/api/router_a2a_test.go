package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/api"
	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	aoauth "github.com/agentoven/agentoven/control-plane/internal/auth"
	"github.com/agentoven/agentoven/control-plane/internal/config"
	"github.com/agentoven/agentoven/control-plane/internal/mcpgw"
	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/a2aauth"
)

// newTestRouter is the real route table, with auth configured as a deployment would (an API key).
func newTestRouter(t *testing.T, requireAuth string) http.Handler {
	t.Helper()
	t.Setenv("AGENTOVEN_API_KEYS", "test-key")
	t.Setenv("AGENTOVEN_REQUIRE_AUTH", requireAuth)
	t.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	t.Cleanup(func() { s.Close() })
	h := handlers.New(s, router.NewModelRouter(s), mcpgw.NewGateway(s), nil, nil, nil, nil)

	chain := aoauth.NewProviderChain()
	chain.RegisterProvider(aoauth.NewAPIKeyProvider())
	return api.NewRouter(&config.Config{}, h, nil, chain, nil, nil)
}

func do(h http.Handler, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tasks/send","params":{}}`))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Every way of running an agent over A2A refuses an anonymous caller, whatever the global
// AGENTOVEN_REQUIRE_AUTH says: /a2a used to be on the public-path list and ran any agent.
func TestEveryA2AExecutionRouteRefusesAnAnonymousCaller(t *testing.T) {
	for _, requireAuth := range []string{"false", "true"} {
		h := newTestRouter(t, requireAuth)
		for _, path := range []string{"/a2a", "/agents/bot/a2a", "/env/prod/agents/bot/a2a", "/api/v1/agents/bot/a2a"} {
			if rec := do(h, http.MethodPost, path, nil); rec.Code != http.StatusUnauthorized {
				t.Errorf("anonymous POST %s (require_auth=%s): %d %s; want 401", path, requireAuth, rec.Code, rec.Body.String())
			}
			// A made-up header is not a credential.
			if rec := do(h, http.MethodPost, path, map[string]string{a2aauth.InternalHeader: "guess", a2aauth.PodTokenHeader: "guess"}); rec.Code != http.StatusUnauthorized {
				t.Errorf("guessed secrets on %s: %d", path, rec.Code)
			}
		}
	}
}

func TestA2AExecutionAcceptsAKeyAndTheEngine(t *testing.T) {
	h := newTestRouter(t, "false")
	for _, path := range []string{"/a2a", "/agents/bot/a2a", "/env/prod/agents/bot/a2a"} {
		if rec := do(h, http.MethodPost, path, map[string]string{"Authorization": "Bearer test-key"}); rec.Code == http.StatusUnauthorized {
			t.Errorf("a valid key was refused on %s: %d", path, rec.Code)
		}
		if rec := do(h, http.MethodPost, path, map[string]string{a2aauth.InternalHeader: a2aauth.InternalKey()}); rec.Code == http.StatusUnauthorized {
			t.Errorf("the engine was refused on %s: %d", path, rec.Code)
		}
	}
}

func TestOnlyTheAgentCardIsPublic(t *testing.T) {
	h := newTestRouter(t, "true")
	if rec := do(h, http.MethodGet, "/a2a/.well-known/agent-card.json", nil); rec.Code != http.StatusOK {
		t.Errorf("the gateway's agent card must stay public for discovery: %d", rec.Code)
	}
	if rec := do(h, http.MethodGet, "/health", nil); rec.Code != http.StatusOK {
		t.Errorf("health: %d", rec.Code)
	}
	if rec := do(h, http.MethodPost, "/a2a/.well-known/agent-card.json", nil); rec.Code == http.StatusOK {
		t.Error("a POST to the card path is not discovery")
	}
}
