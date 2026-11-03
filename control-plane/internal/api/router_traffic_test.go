package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/api"
	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	aoauth "github.com/agentoven/agentoven/control-plane/internal/auth"
	"github.com/agentoven/agentoven/control-plane/internal/config"
	"github.com/agentoven/agentoven/control-plane/internal/mcpgw"
	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

type countingLimiter struct{ n int32 }

func (l *countingLimiter) Admit(*http.Request, string, string) error {
	atomic.AddInt32(&l.n, 1)
	return &models.RateLimitedError{Scope: "kitchen", Limit: "invocations_per_minute", RetryAfter: 1e9, Reason: "limit"}
}

// The invocations-per-minute limit used to cover four routes. Every route that starts agent work asks the
// edition's limiter before it does anything (review 5, finding 10): the A2A endpoints a recipe step uses, the
// test route, resume and the LangChain adapter as well as invoke, streams, session messages and tasks.
func TestEveryRouteThatRunsAnAgentAsksTheTrafficLimiter(t *testing.T) {
	t.Setenv("AGENTOVEN_API_KEYS", "test-key")
	t.Setenv("AGENTOVEN_REQUIRE_AUTH", "false")
	t.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	t.Cleanup(func() { s.Close() })
	h := handlers.New(s, router.NewModelRouter(s), mcpgw.NewGateway(s), nil, nil, nil, nil)
	lim := &countingLimiter{}
	h.Traffic = lim
	chain := aoauth.NewProviderChain()
	chain.RegisterProvider(aoauth.NewAPIKeyProvider())
	mux := api.NewRouter(&config.Config{}, h, nil, chain, nil, nil)

	routes := []string{
		"/api/v1/agents/bot/invoke", "/api/v1/agents/bot/invoke/stream", "/api/v1/agents/bot/test",
		"/api/v1/agents/bot/runs/t1/resume", "/api/v1/agents/bot/sessions/s1/messages", "/api/v1/tasks",
		"/api/v1/agents/bot/a2a", "/a2a", "/agents/bot/a2a", "/env/prod/agents/bot/a2a", "/langchain/invoke",
	}
	for _, path := range routes {
		before := atomic.LoadInt32(&lim.n)
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer test-key")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusTooManyRequests || atomic.LoadInt32(&lim.n) != before+1 {
			t.Errorf("POST %s: %d (limiter asked %d times); the limiter must refuse it with 429 before anything runs", path, rec.Code, atomic.LoadInt32(&lim.n)-before)
		}
	}
}
