package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/middleware"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/go-chi/chi/v5"
)

// ── Helpers ──────────────────────────────────────────────────

func newTestHandlers(t *testing.T) (*handlers.Handlers, store.Store) {
	t.Helper()
	dir := t.TempDir()
	os.Setenv("AGENTOVEN_DATA_DIR", dir)
	defer os.Unsetenv("AGENTOVEN_DATA_DIR")
	s := store.NewMemoryStore()
	t.Cleanup(func() { s.Close() })
	h := &handlers.Handlers{Store: s}
	return h, s
}

func withKitchen(r *http.Request, kitchen string) *http.Request {
	ctx := middleware.SetKitchen(r.Context(), kitchen)
	return r.WithContext(ctx)
}

func withChiParam(r *http.Request, key, value string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rctx)
	return r.WithContext(ctx)
}

func seedAgent(t *testing.T, s store.Store, kitchen, name string) {
	t.Helper()
	err := s.CreateAgent(context.Background(), &models.Agent{
		Name:    name,
		Kitchen: kitchen,
		Status:  models.AgentStatusDraft,
		Mode:    models.AgentModeManaged,
	})
	if err != nil {
		t.Fatalf("seedAgent(%q, %q) error = %v", kitchen, name, err)
	}
}

// ── DeleteAgent ──────────────────────────────────────────────

func TestDeleteAgent_Success(t *testing.T) {
	h, s := newTestHandlers(t)
	seedAgent(t, s, "default", "my-agent")

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/agents/my-agent", nil)
	req = withKitchen(req, "default")
	req = withChiParam(req, "agentName", "my-agent")
	w := httptest.NewRecorder()

	h.DeleteAgent(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("DeleteAgent() status = %d, want %d", w.Code, http.StatusOK)
	}

	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("Failed to decode response body: %v", err)
	}
	if body["status"] != "deleted" {
		t.Errorf("body[status] = %q, want %q", body["status"], "deleted")
	}
	if body["agent"] != "my-agent" {
		t.Errorf("body[agent] = %q, want %q", body["agent"], "my-agent")
	}

	// Verify the agent is actually gone from the store.
	_, err := s.GetAgent(context.Background(), "default", "my-agent")
	if err == nil {
		t.Error("GetAgent() after delete should return error, got nil")
	}
}

func TestDeleteAgent_NotFound(t *testing.T) {
	h, _ := newTestHandlers(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/agents/nonexistent", nil)
	req = withKitchen(req, "default")
	req = withChiParam(req, "agentName", "nonexistent")
	w := httptest.NewRecorder()

	h.DeleteAgent(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("DeleteAgent(nonexistent) status = %d, want %d", w.Code, http.StatusNotFound)
	}

	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("Failed to decode error body: %v", err)
	}
	if body["error"] == "" {
		t.Error("Expected non-empty error message in 404 response")
	}
}

func TestDeleteAgent_WrongKitchen(t *testing.T) {
	h, s := newTestHandlers(t)
	seedAgent(t, s, "kitchen-a", "isolated-agent")

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/agents/isolated-agent", nil)
	req = withKitchen(req, "kitchen-b")
	req = withChiParam(req, "agentName", "isolated-agent")
	w := httptest.NewRecorder()

	h.DeleteAgent(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("DeleteAgent(wrong kitchen) status = %d, want %d", w.Code, http.StatusNotFound)
	}

	// Original agent should still exist in its own kitchen.
	_, err := s.GetAgent(context.Background(), "kitchen-a", "isolated-agent")
	if err != nil {
		t.Errorf("Agent in kitchen-a should still exist, got error: %v", err)
	}
}

// ── GetAgent ─────────────────────────────────────────────────

func TestGetAgent_Success(t *testing.T) {
	h, s := newTestHandlers(t)
	seedAgent(t, s, "default", "test-agent")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/test-agent", nil)
	req = withKitchen(req, "default")
	req = withChiParam(req, "agentName", "test-agent")
	w := httptest.NewRecorder()

	h.GetAgent(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GetAgent() status = %d, want %d", w.Code, http.StatusOK)
	}

	var agent models.Agent
	if err := json.NewDecoder(w.Body).Decode(&agent); err != nil {
		t.Fatalf("Failed to decode agent: %v", err)
	}
	if agent.Name != "test-agent" {
		t.Errorf("agent.Name = %q, want %q", agent.Name, "test-agent")
	}
}

func TestGetAgent_NotFound(t *testing.T) {
	h, _ := newTestHandlers(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/ghost", nil)
	req = withKitchen(req, "default")
	req = withChiParam(req, "agentName", "ghost")
	w := httptest.NewRecorder()

	h.GetAgent(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("GetAgent(ghost) status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

// ── ListAgents ───────────────────────────────────────────────

func TestListAgents_Empty(t *testing.T) {
	h, _ := newTestHandlers(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	req = withKitchen(req, "default")
	w := httptest.NewRecorder()

	h.ListAgents(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("ListAgents() status = %d, want %d", w.Code, http.StatusOK)
	}

	var agents []models.Agent
	if err := json.NewDecoder(w.Body).Decode(&agents); err != nil {
		t.Fatalf("Failed to decode agents list: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("ListAgents() returned %d agents, want 0", len(agents))
	}
}

func TestListAgents_KitchenIsolation(t *testing.T) {
	h, s := newTestHandlers(t)
	seedAgent(t, s, "kitchen-x", "agent-1")
	seedAgent(t, s, "kitchen-x", "agent-2")
	seedAgent(t, s, "kitchen-y", "agent-3")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	req = withKitchen(req, "kitchen-x")
	w := httptest.NewRecorder()

	h.ListAgents(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("ListAgents() status = %d, want %d", w.Code, http.StatusOK)
	}

	var agents []models.Agent
	if err := json.NewDecoder(w.Body).Decode(&agents); err != nil {
		t.Fatalf("Failed to decode agents: %v", err)
	}
	if len(agents) != 2 {
		t.Errorf("ListAgents(kitchen-x) returned %d agents, want 2", len(agents))
	}
}

// ── RegisterAgent ────────────────────────────────────────────

func TestRegisterAgent_Success(t *testing.T) {
	h, _ := newTestHandlers(t)

	body := `{"name":"new-agent","description":"A test agent"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withKitchen(req, "default")
	w := httptest.NewRecorder()

	h.RegisterAgent(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("RegisterAgent() status = %d, want %d", w.Code, http.StatusCreated)
	}

	var agent models.Agent
	if err := json.NewDecoder(w.Body).Decode(&agent); err != nil {
		t.Fatalf("Failed to decode registered agent: %v", err)
	}
	if agent.Name != "new-agent" {
		t.Errorf("agent.Name = %q, want %q", agent.Name, "new-agent")
	}
	if agent.Status != models.AgentStatusDraft {
		t.Errorf("agent.Status = %q, want %q", agent.Status, models.AgentStatusDraft)
	}
	if agent.ID == "" {
		t.Error("agent.ID should be populated with a UUID")
	}
	if agent.Kitchen != "default" {
		t.Errorf("agent.Kitchen = %q, want %q", agent.Kitchen, "default")
	}
}

func TestRegisterAgent_BadJSON(t *testing.T) {
	h, _ := newTestHandlers(t)

	body := `{not-valid-json}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withKitchen(req, "default")
	w := httptest.NewRecorder()

	h.RegisterAgent(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("RegisterAgent(bad JSON) status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// ── Response format ──────────────────────────────────────────

func TestResponseHeaders_JSON(t *testing.T) {
	h, _ := newTestHandlers(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	req = withKitchen(req, "default")
	w := httptest.NewRecorder()

	h.ListAgents(w, req)

	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}
}

// ── RegisterAgent Stable URL ─────────────────────────────────

func TestRegisterAgent_StableA2AEndpoint(t *testing.T) {
	h, _ := newTestHandlers(t)

	body := `{"name":"url-test","description":"Test stable URL"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withKitchen(req, "default")
	w := httptest.NewRecorder()

	h.RegisterAgent(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("RegisterAgent() status = %d, want %d", w.Code, http.StatusCreated)
	}

	var agent models.Agent
	if err := json.NewDecoder(w.Body).Decode(&agent); err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}
	want := "/agents/url-test/a2a"
	if agent.A2AEndpoint != want {
		t.Errorf("a2a_endpoint = %q, want %q", agent.A2AEndpoint, want)
	}
}

func TestRegisterAgent_ExternalMode_BackendEndpoint(t *testing.T) {
	h, _ := newTestHandlers(t)

	body := `{"name":"ext-agent","mode":"external","backend_endpoint":"https://ext.example.com/a2a","description":"External"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withKitchen(req, "default")
	w := httptest.NewRecorder()

	h.RegisterAgent(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("RegisterAgent(external) status = %d, want %d", w.Code, http.StatusCreated)
	}

	var agent models.Agent
	if err := json.NewDecoder(w.Body).Decode(&agent); err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}
	// a2a_endpoint must be stable control-plane URL, NOT the backend
	if agent.A2AEndpoint != "/agents/ext-agent/a2a" {
		t.Errorf("a2a_endpoint = %q, want stable path", agent.A2AEndpoint)
	}
	if agent.BackendEndpoint != "https://ext.example.com/a2a" {
		t.Errorf("backend_endpoint = %q, want %q", agent.BackendEndpoint, "https://ext.example.com/a2a")
	}
	if agent.Mode != models.AgentModeExternal {
		t.Errorf("mode = %q, want %q", agent.Mode, models.AgentModeExternal)
	}
}

// ── A2A Proxy — Agent Not Found ──────────────────────────────

func TestA2AAgentEndpoint_NotFound(t *testing.T) {
	h, _ := newTestHandlers(t)

	body := `{"jsonrpc":"2.0","method":"tasks/send","id":"1","params":{}}`
	req := httptest.NewRequest(http.MethodPost, "/agents/ghost/a2a", strings.NewReader(body))
	req = withKitchen(req, "default")
	req = withChiParam(req, "agentName", "ghost")
	w := httptest.NewRecorder()

	h.A2AAgentEndpoint(w, req)

	if w.Code != http.StatusOK { // JSON-RPC errors return 200 with error payload
		t.Logf("Status = %d (acceptable for JSON-RPC error)", w.Code)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode A2A response: %v", err)
	}
	errObj, ok := resp["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected JSON-RPC error object, got %+v", resp)
	}
	if code := errObj["code"].(float64); code != -32001 {
		t.Errorf("error.code = %v, want -32001", code)
	}
}

// ── A2A Proxy — Agent Not Ready ──────────────────────────────

func TestA2AAgentEndpoint_NotReady(t *testing.T) {
	h, s := newTestHandlers(t)
	seedAgent(t, s, "default", "draft-agent") // status=draft

	body := `{"jsonrpc":"2.0","method":"tasks/send","id":"2","params":{}}`
	req := httptest.NewRequest(http.MethodPost, "/agents/draft-agent/a2a", strings.NewReader(body))
	req = withKitchen(req, "default")
	req = withChiParam(req, "agentName", "draft-agent")
	w := httptest.NewRecorder()

	h.A2AAgentEndpoint(w, req)

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}
	errObj, ok := resp["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected JSON-RPC error, got %+v", resp)
	}
	if code := errObj["code"].(float64); code != -32002 {
		t.Errorf("error.code = %v, want -32002", code)
	}
}

// ── A2A Proxy — No Backend ───────────────────────────────────

func TestA2AAgentEndpoint_NoBackend(t *testing.T) {
	h, s := newTestHandlers(t)

	// Create a ready agent with NO process and no backend endpoint
	err := s.CreateAgent(context.Background(), &models.Agent{
		Name:    "no-backend",
		Kitchen: "default",
		Status:  models.AgentStatusReady,
		Mode:    models.AgentModeManaged,
	})
	if err != nil {
		t.Fatalf("seedAgent error: %v", err)
	}

	body := `{"jsonrpc":"2.0","method":"tasks/send","id":"3","params":{}}`
	req := httptest.NewRequest(http.MethodPost, "/agents/no-backend/a2a", strings.NewReader(body))
	req = withKitchen(req, "default")
	req = withChiParam(req, "agentName", "no-backend")
	w := httptest.NewRecorder()

	h.A2AAgentEndpoint(w, req)

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}
	errObj, ok := resp["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected JSON-RPC error, got %+v", resp)
	}
	if code := errObj["code"].(float64); code != -32003 {
		t.Errorf("error.code = %v, want -32003 (no backend)", code)
	}
}

// ── A2A Proxy — External Agent Relays ────────────────────────

func TestA2AAgentEndpoint_ExternalAgent_Proxy(t *testing.T) {
	// Spin up a mock backend that echoes back a JSON-RPC response
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","result":{"status":"completed","text":"hello from backend"},"id":"4"}`))
	}))
	defer backend.Close()

	h, s := newTestHandlers(t)
	err := s.CreateAgent(context.Background(), &models.Agent{
		Name:            "ext-proxy",
		Kitchen:         "default",
		Status:          models.AgentStatusReady,
		Mode:            models.AgentModeExternal,
		BackendEndpoint: backend.URL,
	})
	if err != nil {
		t.Fatalf("CreateAgent error: %v", err)
	}

	body := `{"jsonrpc":"2.0","method":"tasks/send","id":"4","params":{"message":{"parts":[{"text":"hi"}]}}}`
	req := httptest.NewRequest(http.MethodPost, "/agents/ext-proxy/a2a", strings.NewReader(body))
	req = withKitchen(req, "default")
	req = withChiParam(req, "agentName", "ext-proxy")
	w := httptest.NewRecorder()

	h.A2AAgentEndpoint(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("A2A proxy status = %d, want 200", w.Code)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode proxy response: %v", err)
	}
	result, ok := resp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected result in response, got %+v", resp)
	}
	if result["text"] != "hello from backend" {
		t.Errorf("result.text = %v, want 'hello from backend'", result["text"])
	}
}

// ── A2A Proxy — Managed Agent With Process ───────────────────

func TestA2AAgentEndpoint_ManagedAgent_Proxy(t *testing.T) {
	// Mock backend simulating a running agent process
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the proxy sent the X-AgentOven-Agent header
		if r.Header.Get("X-AgentOven-Agent") != "managed-proxy" {
			t.Errorf("backend got X-AgentOven-Agent = %q, want 'managed-proxy'", r.Header.Get("X-AgentOven-Agent"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","result":{"status":"completed"},"id":"5"}`))
	}))
	defer backend.Close()

	h, s := newTestHandlers(t)
	err := s.CreateAgent(context.Background(), &models.Agent{
		Name:    "managed-proxy",
		Kitchen: "default",
		Status:  models.AgentStatusReady,
		Mode:    models.AgentModeManaged,
		Process: &models.ProcessInfo{
			AgentName: "managed-proxy",
			Kitchen:   "default",
			Status:    models.ProcessRunning,
			Port:      9100,
			Endpoint:  backend.URL, // point at mock
		},
	})
	if err != nil {
		t.Fatalf("CreateAgent error: %v", err)
	}

	body := `{"jsonrpc":"2.0","method":"tasks/send","id":"5","params":{}}`
	req := httptest.NewRequest(http.MethodPost, "/agents/managed-proxy/a2a", strings.NewReader(body))
	req = withKitchen(req, "default")
	req = withChiParam(req, "agentName", "managed-proxy")
	w := httptest.NewRecorder()

	h.A2AAgentEndpoint(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Managed proxy status = %d, want 200", w.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["result"] == nil {
		t.Errorf("Expected result from managed agent proxy, got %+v", resp)
	}
}

// ── GetAgentCard — Stable URL ────────────────────────────────

func TestGetAgentCard_StableURL(t *testing.T) {
	h, s := newTestHandlers(t)
	// Create an agent with a process — the card URL must NOT leak the process endpoint
	err := s.CreateAgent(context.Background(), &models.Agent{
		Name:        "card-test",
		Kitchen:     "default",
		Status:      models.AgentStatusReady,
		Mode:        models.AgentModeManaged,
		A2AEndpoint: "/agents/card-test/a2a",
		Process: &models.ProcessInfo{
			AgentName: "card-test",
			Kitchen:   "default",
			Status:    models.ProcessRunning,
			Port:      9100,
			Endpoint:  "http://localhost:9100",
		},
	})
	if err != nil {
		t.Fatalf("CreateAgent error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/card-test/card", nil)
	req = withKitchen(req, "default")
	req = withChiParam(req, "agentName", "card-test")
	w := httptest.NewRecorder()

	h.GetAgentCard(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GetAgentCard status = %d, want 200", w.Code)
	}

	var card models.AgentCard
	if err := json.NewDecoder(w.Body).Decode(&card); err != nil {
		t.Fatalf("Failed to decode card: %v", err)
	}

	// The card URL must be the stable control-plane URL, NOT the subprocess
	want := "/agents/card-test/a2a"
	if card.URL != want {
		t.Errorf("card.URL = %q, want %q (must never leak process endpoint)", card.URL, want)
	}
}

// ── ResolveBackendEndpoint logic ─────────────────────────────

func TestResolveBackendEndpoint_ManagedRunning(t *testing.T) {
	h, _ := newTestHandlers(t)
	agent := &models.Agent{
		Mode: models.AgentModeManaged,
		Process: &models.ProcessInfo{
			Status:   models.ProcessRunning,
			Endpoint: "http://localhost:9100",
		},
	}
	got := h.ResolveBackendEndpoint(agent)
	if got != "http://localhost:9100" {
		t.Errorf("resolveBackend(managed+running) = %q, want process endpoint", got)
	}
}

func TestResolveBackendEndpoint_ManagedNoProcess(t *testing.T) {
	h, _ := newTestHandlers(t)
	agent := &models.Agent{
		Mode:    models.AgentModeManaged,
		Process: nil,
	}
	got := h.ResolveBackendEndpoint(agent)
	if got != "" {
		t.Errorf("resolveBackend(managed, no process) = %q, want empty", got)
	}
}

func TestResolveBackendEndpoint_External(t *testing.T) {
	h, _ := newTestHandlers(t)
	agent := &models.Agent{
		Mode:            models.AgentModeExternal,
		BackendEndpoint: "https://external.example.com/a2a",
	}
	got := h.ResolveBackendEndpoint(agent)
	if got != "https://external.example.com/a2a" {
		t.Errorf("resolveBackend(external) = %q, want backend_endpoint", got)
	}
}

func TestResolveBackendEndpoint_EmptyMode(t *testing.T) {
	h, _ := newTestHandlers(t)
	// Empty mode defaults to managed behavior
	agent := &models.Agent{
		Mode: "",
		Process: &models.ProcessInfo{
			Status:   models.ProcessRunning,
			Endpoint: "http://localhost:9200",
		},
	}
	got := h.ResolveBackendEndpoint(agent)
	if got != "http://localhost:9200" {
		t.Errorf("resolveBackend(empty mode, running) = %q, want process endpoint", got)
	}
}
