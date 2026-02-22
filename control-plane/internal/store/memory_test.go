package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// newTestStore creates a fresh in-memory store for tests with no persistence.
func newTestStore(t *testing.T) store.Store {
	t.Helper()
	// Use a temp dir so tests don't write to ~/.agentoven/
	dir := t.TempDir()
	os.Setenv("AGENTOVEN_DATA_DIR", dir)
	defer os.Unsetenv("AGENTOVEN_DATA_DIR")
	s := store.NewMemoryStore()
	t.Cleanup(func() { s.Close() })
	return s
}

// ─── Agent CRUD ──────────────────────────────────────────────

func TestCreateAndGetAgent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	agent := &models.Agent{
		Name:    "test-agent",
		Kitchen: "default",
		Status:  models.AgentStatusDraft,
		Mode:    models.AgentModeManaged,
	}

	if err := s.CreateAgent(ctx, agent); err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}

	got, err := s.GetAgent(ctx, "default", "test-agent")
	if err != nil {
		t.Fatalf("GetAgent() error = %v", err)
	}
	if got.Name != "test-agent" {
		t.Errorf("GetAgent().Name = %q, want %q", got.Name, "test-agent")
	}
	if got.Status != models.AgentStatusDraft {
		t.Errorf("GetAgent().Status = %q, want %q", got.Status, models.AgentStatusDraft)
	}
}

func TestCreateAgent_Upsert(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	agent := &models.Agent{Name: "dup", Kitchen: "default", Status: models.AgentStatusDraft}
	if err := s.CreateAgent(ctx, agent); err != nil {
		t.Fatalf("CreateAgent() first call error = %v", err)
	}
	// Second create should overwrite (upsert behavior in memory store)
	agent2 := &models.Agent{Name: "dup", Kitchen: "default", Status: models.AgentStatusReady}
	if err := s.CreateAgent(ctx, agent2); err != nil {
		t.Fatalf("CreateAgent() second call error = %v", err)
	}

	got, _ := s.GetAgent(ctx, "default", "dup")
	if got.Status != models.AgentStatusReady {
		t.Errorf("After upsert, Status = %q, want %q", got.Status, models.AgentStatusReady)
	}
}

func TestListAgents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for _, name := range []string{"a1", "a2", "a3"} {
		s.CreateAgent(ctx, &models.Agent{Name: name, Kitchen: "default"})
	}
	// Different kitchen
	s.CreateAgent(ctx, &models.Agent{Name: "other", Kitchen: "other-kitchen"})

	agents, err := s.ListAgents(ctx, "default")
	if err != nil {
		t.Fatalf("ListAgents() error = %v", err)
	}
	if len(agents) != 3 {
		t.Errorf("ListAgents() returned %d agents, want 3", len(agents))
	}
}

func TestUpdateAgent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.CreateAgent(ctx, &models.Agent{
		Name:    "upd",
		Kitchen: "default",
		Status:  models.AgentStatusDraft,
	})

	updated := &models.Agent{
		Name:    "upd",
		Kitchen: "default",
		Status:  models.AgentStatusReady,
	}
	if err := s.UpdateAgent(ctx, updated); err != nil {
		t.Fatalf("UpdateAgent() error = %v", err)
	}

	got, _ := s.GetAgent(ctx, "default", "upd")
	if got.Status != models.AgentStatusReady {
		t.Errorf("After update, Status = %q, want %q", got.Status, models.AgentStatusReady)
	}
}

func TestDeleteAgent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.CreateAgent(ctx, &models.Agent{Name: "del", Kitchen: "default"})
	if err := s.DeleteAgent(ctx, "default", "del"); err != nil {
		t.Fatalf("DeleteAgent() error = %v", err)
	}

	_, err := s.GetAgent(ctx, "default", "del")
	if err == nil {
		t.Error("GetAgent() after delete should return error, got nil")
	}
}

// ─── Agent Versioning ────────────────────────────────────────

func TestAgentVersioning(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.CreateAgent(ctx, &models.Agent{
		Name:    "versioned",
		Kitchen: "default",
		Status:  models.AgentStatusDraft,
	})

	// Version 1 should exist
	versions, err := s.ListAgentVersions(ctx, "default", "versioned")
	if err != nil {
		t.Fatalf("ListAgentVersions() error = %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("ListAgentVersions() returned %d, want 1", len(versions))
	}
	if versions[0].Version != "1" {
		t.Errorf("Initial version = %q, want %q", versions[0].Version, "1")
	}

	// Update to create version 2
	s.UpdateAgent(ctx, &models.Agent{
		Name:    "versioned",
		Kitchen: "default",
		Status:  models.AgentStatusReady,
	})

	versions, _ = s.ListAgentVersions(ctx, "default", "versioned")
	if len(versions) != 2 {
		t.Fatalf("ListAgentVersions() after update returned %d, want 2", len(versions))
	}

	// Get specific version
	v1, err := s.GetAgentVersion(ctx, "default", "versioned", "1")
	if err != nil {
		t.Fatalf("GetAgentVersion(1) error = %v", err)
	}
	if v1.Status != models.AgentStatusDraft {
		t.Errorf("Version 1 status = %q, want %q", v1.Status, models.AgentStatusDraft)
	}

	v2, err := s.GetAgentVersion(ctx, "default", "versioned", "2")
	if err != nil {
		t.Fatalf("GetAgentVersion(2) error = %v", err)
	}
	if v2.Status != models.AgentStatusReady {
		t.Errorf("Version 2 status = %q, want %q", v2.Status, models.AgentStatusReady)
	}
}

// ─── Kitchen CRUD ────────────────────────────────────────────

func TestCreateAndGetKitchen(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	k := &models.Kitchen{
		ID:          "test-kitchen",
		Name:        "Test Kitchen",
		Description: "A test kitchen",
	}
	if err := s.CreateKitchen(ctx, k); err != nil {
		t.Fatalf("CreateKitchen() error = %v", err)
	}

	got, err := s.GetKitchen(ctx, "test-kitchen")
	if err != nil {
		t.Fatalf("GetKitchen() error = %v", err)
	}
	if got.Description != "A test kitchen" {
		t.Errorf("GetKitchen().Description = %q, want %q", got.Description, "A test kitchen")
	}
	if got.Name != "Test Kitchen" {
		t.Errorf("GetKitchen().Name = %q, want %q", got.Name, "Test Kitchen")
	}
}

// ─── Trace CRUD + TTL ───────────────────────────────────────

func TestCreateAndGetTrace(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	trace := &models.Trace{
		ID:        "trace-1",
		AgentName: "agent-1",
		Kitchen:   "default",
		Status:    "completed",
		CostUSD:   0.001,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.CreateTrace(ctx, trace); err != nil {
		t.Fatalf("CreateTrace() error = %v", err)
	}

	got, err := s.GetTrace(ctx, "trace-1")
	if err != nil {
		t.Fatalf("GetTrace() error = %v", err)
	}
	if got.AgentName != "agent-1" {
		t.Errorf("GetTrace().AgentName = %q, want %q", got.AgentName, "agent-1")
	}
}

func TestListTraces(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		s.CreateTrace(ctx, &models.Trace{
			ID:        "trace-" + time.Now().Format("150405.000000") + "-" + string(rune('a'+i)),
			AgentName: "agent",
			Kitchen:   "default",
			CreatedAt: time.Now().UTC(),
		})
	}

	traces, err := s.ListTraces(ctx, "default", 10)
	if err != nil {
		t.Fatalf("ListTraces() error = %v", err)
	}
	if len(traces) != 5 {
		t.Errorf("ListTraces() returned %d, want 5", len(traces))
	}
}

// ─── Provider CRUD ──────────────────────────────────────────

func TestProviderCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	p := &models.ModelProvider{
		Name:     "openai-1",
		Kind:     "openai",
		Endpoint: "https://api.openai.com/v1",
		Models:   []string{"gpt-4o"},
	}
	if err := s.CreateProvider(ctx, p); err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}

	providers, err := s.ListProviders(ctx)
	if err != nil {
		t.Fatalf("ListProviders() error = %v", err)
	}
	if len(providers) != 1 {
		t.Errorf("ListProviders() returned %d, want 1", len(providers))
	}

	got, err := s.GetProvider(ctx, "openai-1")
	if err != nil {
		t.Fatalf("GetProvider() error = %v", err)
	}
	if got.Kind != "openai" {
		t.Errorf("GetProvider().Kind = %q, want %q", got.Kind, "openai")
	}

	if err := s.DeleteProvider(ctx, "openai-1"); err != nil {
		t.Fatalf("DeleteProvider() error = %v", err)
	}
	providers, _ = s.ListProviders(ctx)
	if len(providers) != 0 {
		t.Errorf("ListProviders() after delete returned %d, want 0", len(providers))
	}
}

// ─── Tool CRUD ──────────────────────────────────────────────

func TestToolCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	tool := &models.MCPTool{
		Name:      "calculator",
		Kitchen:   "default",
		Endpoint:  "http://localhost:8080",
		Transport: "http",
	}
	if err := s.CreateTool(ctx, tool); err != nil {
		t.Fatalf("CreateTool() error = %v", err)
	}

	tools, err := s.ListTools(ctx, "default")
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(tools) != 1 {
		t.Errorf("ListTools() returned %d, want 1", len(tools))
	}

	if err := s.DeleteTool(ctx, "default", "calculator"); err != nil {
		t.Fatalf("DeleteTool() error = %v", err)
	}
	tools, _ = s.ListTools(ctx, "default")
	if len(tools) != 0 {
		t.Errorf("After delete, ListTools() returned %d, want 0", len(tools))
	}
}

// ─── Recipe CRUD ────────────────────────────────────────────

func TestRecipeCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	recipe := &models.Recipe{
		Name:    "test-recipe",
		Kitchen: "default",
	}
	if err := s.CreateRecipe(ctx, recipe); err != nil {
		t.Fatalf("CreateRecipe() error = %v", err)
	}

	recipes, err := s.ListRecipes(ctx, "default")
	if err != nil {
		t.Fatalf("ListRecipes() error = %v", err)
	}
	if len(recipes) != 1 {
		t.Errorf("ListRecipes() returned %d, want 1", len(recipes))
	}

	if err := s.DeleteRecipe(ctx, "default", "test-recipe"); err != nil {
		t.Fatalf("DeleteRecipe() error = %v", err)
	}
	recipes, _ = s.ListRecipes(ctx, "default")
	if len(recipes) != 0 {
		t.Errorf("After delete, ListRecipes() returned %d, want 0", len(recipes))
	}
}

// ─── Close / Snapshot ───────────────────────────────────────

func TestCloseFlush(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("AGENTOVEN_DATA_DIR", dir)
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")

	ctx := context.Background()
	s.CreateAgent(ctx, &models.Agent{Name: "persist-me", Kitchen: "default"})

	// Close should flush to disk
	s.Close()

	// Reopen and verify data survived
	os.Setenv("AGENTOVEN_DATA_DIR", dir)
	s2 := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	defer s2.Close()

	got, err := s2.GetAgent(ctx, "default", "persist-me")
	if err != nil {
		t.Fatalf("After reopen, GetAgent() error = %v", err)
	}
	if got.Name != "persist-me" {
		t.Errorf("After reopen, agent name = %q, want %q", got.Name, "persist-me")
	}
}
