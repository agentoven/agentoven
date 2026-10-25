package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/internal/guardrails"
	"github.com/agentoven/agentoven/control-plane/internal/resolver"
	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/sessions"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/go-chi/chi/v5"
)

// These tests answer one question: does a framework-native agent (a user's
// LangChain / LangGraph process) get the same guardrail protection as a Go
// executor agent, on every route that can reach it? The process is faked at
// the protocol level (/invoke, /invoke/stream, A2A) and the guardrail engine
// is the real community one, so a pass means the real handler → real engine →
// process chain held, not that two mocks agreed with each other.
//
// Every route that can reach a hosted agent must enforce the same guardrails, so a
// bypass fails the test rather than being reported and skipped.

const (
	blockedWord = "forbidden-topic"
	leakedSSN   = "123-45-6789"
)

// fakeProcess stands in for a baked LangChain/LangGraph process.
type fakeProcess struct {
	srv     *httptest.Server
	mu      sync.Mutex
	seen    []string
	threads []string // thread_id of each call that carried one
	reply   string
}

func newFakeProcess(t *testing.T, reply string) *fakeProcess {
	t.Helper()
	p := &fakeProcess{reply: reply}
	mux := http.NewServeMux()

	record := func(msg string) {
		p.mu.Lock()
		p.seen = append(p.seen, msg)
		p.mu.Unlock()
	}

	mux.HandleFunc("/invoke", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Message  string `json:"message"`
			ThreadID string `json:"thread_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		record(body.Message)
		if body.ThreadID != "" {
			p.mu.Lock()
			p.threads = append(p.threads, body.ThreadID)
			p.mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"response": p.reply,
			"usage":    map[string]int{"input_tokens": 3, "output_tokens": 4, "total_tokens": 7},
		})
	})

	mux.HandleFunc("/invoke/stream", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Message  string `json:"message"`
			ThreadID string `json:"thread_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		record(body.Message)
		if body.ThreadID != "" {
			p.mu.Lock()
			p.threads = append(p.threads, body.ThreadID)
			p.mu.Unlock()
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"type\":\"token\",\"content\":%q}\n\n", p.reply)
		fmt.Fprint(w, "data: {\"type\":\"done\"}\n\n")
	})

	// A2A JSON-RPC lives at the process root, which is where the gateway
	// proxies it (proxyA2ARequest posts to the endpoint as-is).
	a2a := func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var rpc struct {
			ID     interface{} `json:"id"`
			Method string      `json:"method"`
			Params struct {
				Message struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"message"`
			} `json:"params"`
		}
		_ = json.Unmarshal(raw, &rpc)
		var text string
		for _, part := range rpc.Params.Message.Parts {
			text += part.Text
		}
		record(text)
		if strings.Contains(strings.ToLower(rpc.Method), "stream") {
			w.Header().Set("Content-Type", "text/event-stream")
			frame, _ := json.Marshal(map[string]interface{}{
				"jsonrpc": "2.0", "id": rpc.ID,
				"result": map[string]interface{}{
					"artifact": map[string]interface{}{"parts": []map[string]string{{"type": "text", "text": p.reply}}},
				},
			})
			fmt.Fprintf(w, "data: %s\n\n", frame)
			return
		}
		w.Header().Set("Content-Type", "application/a2a+json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      rpc.ID,
			"result": map[string]interface{}{
				"status":    map[string]string{"state": "completed"},
				"artifacts": []map[string]interface{}{{"parts": []map[string]string{{"type": "text", "text": p.reply}}}},
			},
		})
	}
	mux.HandleFunc("/a2a", a2a)
	mux.HandleFunc("/", a2a)

	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

func (p *fakeProcess) threadIDs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.threads...)
}

func (p *fakeProcess) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.seen)
}

func nativeGuardrails() []models.Guardrail {
	return []models.Guardrail{
		{ID: "no-forbidden", Kind: models.GuardrailContentFilter, Stage: models.GuardrailStageInput, Enabled: true,
			Config: map[string]interface{}{"blocked_words": []interface{}{blockedWord}}},
		{ID: "no-ssn-leak", Kind: models.GuardrailPIIDetection, Stage: models.GuardrailStageOutput, Enabled: true,
			Config: map[string]interface{}{"patterns": []interface{}{"ssn"}}},
	}
}

func nativeAgent(rt models.AgentRuntime, endpoint string) *models.Agent {
	return &models.Agent{
		Name: "native-agent", Kitchen: "default", Mode: models.AgentModeManaged,
		Runtime: rt, Status: models.AgentStatusReady,
		Process:        &models.ProcessInfo{Status: models.ProcessRunning, Endpoint: endpoint},
		ResolvedConfig: &models.ResolvedIngredients{},
		Guardrails:     nativeGuardrails(),
	}
}

func newNativeHandlers(t *testing.T, agent *models.Agent) (*handlers.Handlers, store.Store) {
	t.Helper()
	dir := t.TempDir()
	os.Setenv("AGENTOVEN_DATA_DIR", dir)
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	t.Cleanup(func() { s.Close() })

	if err := s.CreateAgent(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	h := &handlers.Handlers{
		Store:           s,
		Guardrails:      &guardrails.CommunityGuardrailService{},
		Resolver:        resolver.NewResolver(s),
		PromptValidator: &contracts.CommunityPromptValidator{},
	}
	return h, s
}

// mustHold fails the test when a guardrail was bypassed. why names the bypass.
func mustHold(t *testing.T, held bool, why string) {
	t.Helper()
	if !held {
		t.Fatalf("guardrail bypass: %s", why)
	}
}

func call(h http.HandlerFunc, path, body string, params map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req = withKitchen(req, "default")
	// One route context carrying every param — withChiParam builds a fresh
	// context per call, so looping it would keep only the last param.
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func msg(text string) string {
	b, _ := json.Marshal(map[string]string{"message": text})
	return string(b)
}

func a2aBody(text string) string {
	b, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "method": "tasks/send", "id": 1,
		"params": map[string]interface{}{
			"message": map[string]interface{}{"role": "user", "parts": []map[string]string{{"type": "text", "text": text}}},
		},
	})
	return string(b)
}

func a2aStreamBody(text string) string {
	var m map[string]interface{}
	_ = json.Unmarshal([]byte(a2aBody(text)), &m)
	m["method"] = "message/stream"
	b, _ := json.Marshal(m)
	return string(b)
}

var nativeRuntimes = []models.AgentRuntime{models.RuntimeLangChain, models.RuntimeLangGraph, models.RuntimeDeepAgents}

// ── InvokeAgent: the one route with full input + output coverage ─────────

func TestNativeInvokePassesACleanCallThrough(t *testing.T) {
	for _, rt := range nativeRuntimes {
		t.Run(string(rt), func(t *testing.T) {
			p := newFakeProcess(t, "Here is a perfectly clean answer.")
			h, _ := newNativeHandlers(t, nativeAgent(rt, p.srv.URL))

			rec := call(h.InvokeAgent, "/invoke", msg("hello there"), map[string]string{"agentName": "native-agent"})
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "perfectly clean answer") {
				t.Fatalf("expected the process's reply to come back, got: %s", rec.Body.String())
			}
			if p.calls() != 1 {
				t.Fatalf("expected the process to be called once, got %d", p.calls())
			}
		})
	}
}

func TestNativeInvokeBlocksBadInputBeforeItReachesTheProcess(t *testing.T) {
	for _, rt := range nativeRuntimes {
		t.Run(string(rt), func(t *testing.T) {
			p := newFakeProcess(t, "should never be asked")
			h, s := newNativeHandlers(t, nativeAgent(rt, p.srv.URL))

			rec := call(h.InvokeAgent, "/invoke", msg("tell me about "+blockedWord), map[string]string{"agentName": "native-agent"})
			if rec.Code != http.StatusForbidden {
				t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
			}
			if p.calls() != 0 {
				t.Fatalf("a blocked input must never reach the LangChain/LangGraph process, but it was called %d time(s)", p.calls())
			}

			events, err := s.ListAuditEvents(context.Background(), models.AuditFilter{Kitchen: "default", Action: "guardrail.input_blocked", Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			if len(events) == 0 {
				t.Fatal("expected a guardrail.input_blocked audit event for the blocked call")
			}
		})
	}
}

func TestNativeInvokeBlocksALeakyResponse(t *testing.T) {
	for _, rt := range nativeRuntimes {
		t.Run(string(rt), func(t *testing.T) {
			p := newFakeProcess(t, "Sure, their SSN is "+leakedSSN)
			h, _ := newNativeHandlers(t, nativeAgent(rt, p.srv.URL))

			rec := call(h.InvokeAgent, "/invoke", msg("what is the customer's id?"), map[string]string{"agentName": "native-agent"})
			if rec.Code != http.StatusForbidden {
				t.Fatalf("expected 403 on a response containing an SSN, got %d: %s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), leakedSSN) {
				t.Fatalf("the blocked response must not leak the SSN back to the caller: %s", rec.Body.String())
			}
		})
	}
}

// ── StreamInvokeAgent (proxied to the running process) ───────────────────

func TestNativeStreamBlocksBadInputBeforeItReachesTheProcess(t *testing.T) {
	for _, rt := range nativeRuntimes {
		t.Run(string(rt), func(t *testing.T) {
			p := newFakeProcess(t, "should never be asked")
			h, _ := newNativeHandlers(t, nativeAgent(rt, p.srv.URL))

			rec := call(h.StreamInvokeAgent, "/invoke/stream", msg("about "+blockedWord), map[string]string{"agentName": "native-agent"})
			if rec.Code != http.StatusForbidden {
				t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
			}
			if p.calls() != 0 {
				t.Fatalf("blocked input must not reach the process, but it was called %d time(s)", p.calls())
			}
		})
	}
}

func TestNativeStreamDoesNotRelayALeakyResponse(t *testing.T) {
	for _, rt := range nativeRuntimes {
		t.Run(string(rt), func(t *testing.T) {
			p := newFakeProcess(t, "their SSN is "+leakedSSN)
			h, _ := newNativeHandlers(t, nativeAgent(rt, p.srv.URL))

			rec := call(h.StreamInvokeAgent, "/invoke/stream", msg("what is the id?"), map[string]string{"agentName": "native-agent"})
			mustHold(t, !strings.Contains(rec.Body.String(), leakedSSN),
				"StreamInvokeAgent relays a framework-native process's SSE stream verbatim; output guardrails only run on the Go-executor fallback path, so a PII-bearing stream reaches the caller")
		})
	}
}

// ── TestAgent (the dashboard's "try it" button) ──────────────────────────

func TestNativeTestAgentBlocksBadInput(t *testing.T) {
	for _, rt := range nativeRuntimes {
		t.Run(string(rt), func(t *testing.T) {
			p := newFakeProcess(t, "should never be asked")
			h, _ := newNativeHandlers(t, nativeAgent(rt, p.srv.URL))

			rec := call(h.TestAgent, "/test", msg("about "+blockedWord), map[string]string{"agentName": "native-agent"})
			if rec.Code != http.StatusForbidden {
				t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
			}
			if p.calls() != 0 {
				t.Fatalf("blocked input must not reach the process, but it was called %d time(s)", p.calls())
			}
		})
	}
}

func TestNativeTestAgentBlocksALeakyResponse(t *testing.T) {
	for _, rt := range nativeRuntimes {
		t.Run(string(rt), func(t *testing.T) {
			p := newFakeProcess(t, "their SSN is "+leakedSSN)
			h, _ := newNativeHandlers(t, nativeAgent(rt, p.srv.URL))

			rec := call(h.TestAgent, "/test", msg("what is the id?"), map[string]string{"agentName": "native-agent"})
			mustHold(t, rec.Code == http.StatusForbidden && !strings.Contains(rec.Body.String(), leakedSSN),
				"TestAgent's framework-native branch returns the process's reply without running output guardrails (only its Go-executor branches do)")
		})
	}
}

// ── A2A gateway: /agents/{name}/a2a ──────────────────────────────────────
// This is the stable URL Pro's recipe engine and the LangChain adapter
// (internal/integrations/langchain) both call.

func TestNativeA2AGatewayProxiesACleanCall(t *testing.T) {
	for _, rt := range nativeRuntimes {
		t.Run(string(rt), func(t *testing.T) {
			p := newFakeProcess(t, "clean a2a answer")
			h, _ := newNativeHandlers(t, nativeAgent(rt, p.srv.URL))

			rec := call(h.A2AAgentEndpoint, "/agents/native-agent/a2a", a2aBody("hello"), map[string]string{"agentName": "native-agent"})
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "clean a2a answer") {
				t.Fatalf("expected the process's A2A reply, got %d: %s", rec.Code, rec.Body.String())
			}
			if p.calls() != 1 {
				t.Fatalf("expected one process call, got %d", p.calls())
			}
		})
	}
}

func TestNativeA2AGatewayBlocksBadInput(t *testing.T) {
	for _, rt := range nativeRuntimes {
		t.Run(string(rt), func(t *testing.T) {
			p := newFakeProcess(t, "answered anyway")
			h, _ := newNativeHandlers(t, nativeAgent(rt, p.srv.URL))

			call(h.A2AAgentEndpoint, "/agents/native-agent/a2a", a2aBody("about "+blockedWord), map[string]string{"agentName": "native-agent"})
			mustHold(t, p.calls() == 0,
				"the per-agent A2A gateway only evaluates input guardrails on its X-AO-Environment branch; the default branch proxies straight to the process, so a blocked input still reaches LangChain/LangGraph")
		})
	}
}

func TestNativeA2AGatewayBlocksALeakyResponse(t *testing.T) {
	for _, rt := range nativeRuntimes {
		t.Run(string(rt), func(t *testing.T) {
			p := newFakeProcess(t, "their SSN is "+leakedSSN)
			h, _ := newNativeHandlers(t, nativeAgent(rt, p.srv.URL))

			rec := call(h.A2AAgentEndpoint, "/agents/native-agent/a2a", a2aBody("what is the id?"), map[string]string{"agentName": "native-agent"})
			mustHold(t, !strings.Contains(rec.Body.String(), leakedSSN),
				"no A2A gateway branch evaluates output guardrails — proxyA2ARequest copies the process's body back verbatim")
		})
	}
}

// ── Streams ──────────────────────────────────────────────────────────────

func TestNativeStreamReleasesACleanReplyIntact(t *testing.T) {
	for _, rt := range nativeRuntimes {
		t.Run(string(rt), func(t *testing.T) {
			p := newFakeProcess(t, "a perfectly clean streamed answer")
			h, _ := newNativeHandlers(t, nativeAgent(rt, p.srv.URL))

			rec := call(h.StreamInvokeAgent, "/invoke/stream", msg("hello"), map[string]string{"agentName": "native-agent"})
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "perfectly clean streamed answer") {
				t.Fatalf("a clean reply must be released after the check, got %d: %s", rec.Code, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
				t.Fatalf("the released reply must still be an SSE stream, got Content-Type %q", ct)
			}
		})
	}
}

func TestNativeStreamIsNotHeldBackWhenThereAreNoOutputRules(t *testing.T) {
	p := newFakeProcess(t, "their SSN is "+leakedSSN)
	agent := nativeAgent(models.RuntimeLangGraph, p.srv.URL)
	agent.Guardrails = agent.Guardrails[:1] // the input rule only
	h, _ := newNativeHandlers(t, agent)

	rec := call(h.StreamInvokeAgent, "/invoke/stream", msg("hello"), map[string]string{"agentName": "native-agent"})
	// With no output rule there is nothing to check, so the stream is relayed unchanged.
	// This pins the contract: only agents that asked for output checks pay the latency.
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), leakedSSN) {
		t.Fatalf("an agent without output rules should stream through untouched, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestNativeA2AStreamingIsHeldBackAndChecked(t *testing.T) {
	for _, rt := range nativeRuntimes {
		t.Run(string(rt)+"/leaky", func(t *testing.T) {
			p := newFakeProcess(t, "their SSN is "+leakedSSN)
			h, _ := newNativeHandlers(t, nativeAgent(rt, p.srv.URL))

			rec := call(h.A2AAgentEndpoint, "/agents/native-agent/a2a", a2aStreamBody("what is the id?"), map[string]string{"agentName": "native-agent"})
			mustHold(t, !strings.Contains(rec.Body.String(), leakedSSN),
				"a streamed A2A reply with an SSN reached the caller: "+rec.Body.String())
			mustHold(t, strings.Contains(rec.Body.String(), "Output blocked by guardrails"),
				"expected a JSON-RPC output-blocked error, got: "+rec.Body.String())
		})
		t.Run(string(rt)+"/clean", func(t *testing.T) {
			p := newFakeProcess(t, "clean streamed a2a answer")
			h, _ := newNativeHandlers(t, nativeAgent(rt, p.srv.URL))

			rec := call(h.A2AAgentEndpoint, "/agents/native-agent/a2a", a2aStreamBody("hello"), map[string]string{"agentName": "native-agent"})
			if !strings.Contains(rec.Body.String(), "clean streamed a2a answer") {
				t.Fatalf("a clean streamed reply must be released, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// ── Sessions ─────────────────────────────────────────────────────────────

// nativeSession builds handlers with a live session for a hosted agent. The router answers
// with a distinct text, so a reply that came from the model instead of the process shows.
func nativeSession(t *testing.T, rt models.AgentRuntime, processReply string) (*handlers.Handlers, *fakeProcess) {
	t.Helper()
	p := newFakeProcess(t, processReply)
	h, s := newNativeHandlers(t, nativeAgent(rt, p.srv.URL))

	mr := router.NewModelRouter(s)
	mr.RegisterDriver(&scriptedSkillDriver{content: "answer straight from the model"})
	if err := s.CreateProvider(context.Background(), &models.ModelProvider{
		Name: "p", Kitchen: "default", Kind: "scripted", Models: []string{"m"}, IsDefault: true,
	}); err != nil {
		t.Fatal(err)
	}
	h.Router = mr
	h.Sessions = sessions.NewMemorySessionStore()
	if err := h.Sessions.CreateSession(context.Background(), &models.Session{
		ID: "s1", AgentName: "native-agent", Kitchen: "default", Status: models.SessionActive,
	}); err != nil {
		t.Fatal(err)
	}
	return h, p
}

func sendSession(h *handlers.Handlers, text string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"content": text})
	return call(h.SendSessionMessage, "/sessions/s1/messages", string(body),
		map[string]string{"agentName": "native-agent", "sessionID": "s1"})
}

func TestNativeAgentSessionMessagesReachTheFrameworkProcess(t *testing.T) {
	for _, rt := range nativeRuntimes {
		t.Run(string(rt), func(t *testing.T) {
			h, p := nativeSession(t, rt, "answer from the framework process")

			rec := sendSession(h, "hello")
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			mustHold(t, p.calls() == 1, "a hosted agent's session message never reached its process")
			mustHold(t, strings.Contains(rec.Body.String(), "answer from the framework process"),
				"the reply must come from the agent's process, not the raw model: "+rec.Body.String())
			mustHold(t, !strings.Contains(rec.Body.String(), "straight from the model"),
				"the raw model answered a hosted agent's session")
			// The framework's checkpointer keys its memory on the thread, so every turn of
			// one session must carry the same one.
			if got := p.threadIDs(); len(got) != 1 || got[0] != "s1" {
				t.Fatalf("expected the session ID as thread_id, got %v", got)
			}

			sess, err := h.Sessions.GetSession(context.Background(), "s1")
			if err != nil {
				t.Fatal(err)
			}
			if sess.TurnCount != 1 || len(sess.Messages) != 2 || sess.Messages[1].Content != "answer from the framework process" {
				t.Fatalf("the control plane should keep the transcript for audit, got %+v", sess)
			}
		})
	}
}

func TestNativeAgentSessionBlocksBadInputBeforeTheProcess(t *testing.T) {
	for _, rt := range nativeRuntimes {
		t.Run(string(rt), func(t *testing.T) {
			h, p := nativeSession(t, rt, "never asked")
			rec := sendSession(h, "about "+blockedWord)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
			}
			mustHold(t, p.calls() == 0, "blocked session input reached the process")
		})
	}
}

func TestNativeAgentSessionBlocksALeakyReplyAndKeepsItOutOfTheTranscript(t *testing.T) {
	for _, rt := range nativeRuntimes {
		t.Run(string(rt), func(t *testing.T) {
			h, _ := nativeSession(t, rt, "their SSN is "+leakedSSN)
			rec := sendSession(h, "what is the id?")
			if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), leakedSSN) {
				t.Fatalf("expected a 403 with no SSN, got %d: %s", rec.Code, rec.Body.String())
			}
			sess, _ := h.Sessions.GetSession(context.Background(), "s1")
			for _, m := range sess.Messages {
				mustHold(t, !strings.Contains(m.Content, leakedSSN), "the blocked reply was stored in the session transcript")
			}
		})
	}
}
