package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/internal/guardrails"
	"github.com/agentoven/agentoven/control-plane/internal/resolver"
	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/sessions"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// noTools is a gateway for agents that call none.
type noTools struct{}

func (noTools) HandleJSONRPC(_ context.Context, _ string, req *models.MCPRequest) *models.MCPResponse {
	return &models.MCPResponse{Jsonrpc: "2.0", ID: req.ID, Error: &models.MCPError{Code: -32601, Message: "no tools"}}
}

// goAgentHandlers builds handlers for an agent the Go executor runs (no separate
// process), whose model replies with the given text.
func goAgentHandlers(t *testing.T, modelReply string, rules []models.Guardrail) *handlers.Handlers {
	t.Helper()
	dir := t.TempDir()
	os.Setenv("AGENTOVEN_DATA_DIR", dir)
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	t.Cleanup(func() { s.Close() })

	mr := router.NewModelRouter(s)
	mr.RegisterDriver(&scriptedSkillDriver{content: modelReply})
	if err := s.CreateProvider(context.Background(), &models.ModelProvider{
		Name: "p", Kitchen: "default", Kind: "scripted", Models: []string{"m"}, IsDefault: true,
	}); err != nil {
		t.Fatal(err)
	}

	agent := &models.Agent{
		Name: "go-agent", Kitchen: "default", Mode: models.AgentModeManaged,
		Status:         models.AgentStatusReady,
		ResolvedConfig: &models.ResolvedIngredients{Model: &models.ResolvedModel{Model: "m"}},
		Guardrails:     rules,
		MaxTurns:       3,
	}
	if err := s.CreateAgent(context.Background(), agent); err != nil {
		t.Fatal(err)
	}

	sess := sessions.NewMemorySessionStore()
	return &handlers.Handlers{
		Store:           s,
		Router:          mr,
		Executor:        executor.NewExecutor(s, mr, noTools{}, sess),
		Resolver:        resolver.NewResolver(s),
		PromptValidator: &contracts.CommunityPromptValidator{},
		Guardrails:      &guardrails.CommunityGuardrailService{},
		Sessions:        sess,
	}
}

func TestGoExecutorStreamHoldsBackALeakyReply(t *testing.T) {
	h := goAgentHandlers(t, "their SSN is "+leakedSSN, nativeGuardrails())
	rec := call(h.StreamInvokeAgent, "/invoke/stream", msg("what is the id?"), map[string]string{"agentName": "go-agent"})

	mustHold(t, rec.Code == http.StatusForbidden, "expected 403 for a leaky streamed reply, got "+rec.Body.String())
	mustHold(t, !strings.Contains(rec.Body.String(), leakedSSN), "the SSN reached the caller before the check: "+rec.Body.String())
}

func TestGoExecutorStreamReleasesACleanReplyWithLowercaseKeys(t *testing.T) {
	h := goAgentHandlers(t, "a perfectly clean answer", nativeGuardrails())
	rec := call(h.StreamInvokeAgent, "/invoke/stream", msg("hello"), map[string]string{"agentName": "go-agent"})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("expected an SSE response, got %q", ct)
	}

	var sawToken, sawDone bool
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var frame map[string]interface{}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
			t.Fatalf("a frame was not JSON: %q", line)
		}
		// The Python agent runner streams {"type":"token","content":...}; the Go executor
		// must use the same keys so one client reads both runtimes.
		if _, capitalised := frame["Type"]; capitalised {
			t.Fatalf("frame uses a capitalised key, which the Python runtime's clients cannot read: %q", line)
		}
		switch frame["type"] {
		case "token":
			sawToken = frame["content"] == "a perfectly clean answer"
		case "done":
			sawDone = true
		}
	}
	if !sawToken || !sawDone {
		t.Fatalf("expected a lowercase token frame and a done frame, got: %s", rec.Body.String())
	}
}

func TestGoExecutorStreamIsNotHeldBackWithoutOutputRules(t *testing.T) {
	// No output rule means nothing to check, so the reply is not delayed or buffered.
	h := goAgentHandlers(t, "their SSN is "+leakedSSN, nativeGuardrails()[:1])
	rec := call(h.StreamInvokeAgent, "/invoke/stream", msg("hello"), map[string]string{"agentName": "go-agent"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), leakedSSN) {
		t.Fatalf("an agent without output rules should stream unchanged, got %d: %s", rec.Code, rec.Body.String())
	}
}
