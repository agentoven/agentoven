// Package store provides the storage interface and implementations for the AgentOven control plane.
// Phase 1 used in-memory maps; Phase 2 introduces PostgreSQL-backed persistence.
package store

import (
	"context"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// Store is the primary storage interface for the control plane.
// All handler code depends on this interface, making it easy to swap
// between in-memory (tests) and PostgreSQL (production) implementations.
type Store interface {
	AgentStore
	RecipeStore
	KitchenStore
	TraceStore
	ModelProviderStore
	RecipeRunStore
	MCPToolStore
	PromptStore
	KitchenSettingsStore
	AuditStore
	ApprovalStore
	NotificationChannelStore
	VectorDocStore
	DataConnectorStore
	SessionStore

	// Ping checks if the database is reachable.
	Ping(ctx context.Context) error

	// Close releases all resources held by the store.
	Close() error

	// Migrate runs database migrations.
	Migrate(ctx context.Context) error
}

// ── Session Store ───────────────────────────────────────────

// SessionStore manages multi-turn conversation sessions for agentic agents.
type SessionStore interface {
	GetSession(ctx context.Context, id string) (*models.Session, error)
	CreateSession(ctx context.Context, session *models.Session) error
	UpdateSession(ctx context.Context, session *models.Session) error
	DeleteSession(ctx context.Context, id string) error
	ListSessionsByAgent(ctx context.Context, kitchen, agentName string, limit int) ([]models.Session, error)
}

// ── Agent Store ─────────────────────────────────────────────

type AgentStore interface {
	ListAgents(ctx context.Context, kitchen string) ([]models.Agent, error)
	GetAgent(ctx context.Context, kitchen, name string) (*models.Agent, error)
	CreateAgent(ctx context.Context, agent *models.Agent) error
	UpdateAgent(ctx context.Context, agent *models.Agent) error
	DeleteAgent(ctx context.Context, kitchen, name string) error

	// Agent versioning — tracks historical versions of agents
	ListAgentVersions(ctx context.Context, kitchen, name string) ([]models.Agent, error)
	GetAgentVersion(ctx context.Context, kitchen, name, version string) (*models.Agent, error)
}

// ── Recipe Store ────────────────────────────────────────────

type RecipeStore interface {
	ListRecipes(ctx context.Context, kitchen string) ([]models.Recipe, error)
	GetRecipe(ctx context.Context, kitchen, name string) (*models.Recipe, error)
	CreateRecipe(ctx context.Context, recipe *models.Recipe) error
	UpdateRecipe(ctx context.Context, recipe *models.Recipe) error
	DeleteRecipe(ctx context.Context, kitchen, name string) error
}

// ── Kitchen Store ───────────────────────────────────────────

type KitchenStore interface {
	ListKitchens(ctx context.Context) ([]models.Kitchen, error)
	GetKitchen(ctx context.Context, id string) (*models.Kitchen, error)
	CreateKitchen(ctx context.Context, kitchen *models.Kitchen) error
}

// ── Trace Store ─────────────────────────────────────────────

// TraceFilter defines optional filters for listing traces.
type TraceFilter struct {
	AgentName  string // exact match on agent_name
	RecipeName string // exact match on recipe_name
	Status     string // exact match on status
	Limit      int    // max results (default 100)
}

type TraceStore interface {
	ListTraces(ctx context.Context, kitchen string, limit int) ([]models.Trace, error)
	ListTracesFiltered(ctx context.Context, kitchen string, filter TraceFilter) ([]models.Trace, error)
	GetTrace(ctx context.Context, id string) (*models.Trace, error)
	CreateTrace(ctx context.Context, trace *models.Trace) error
	DeleteTrace(ctx context.Context, id string) error
}

// ── Model Provider Store ────────────────────────────────────

type ModelProviderStore interface {
	ListProviders(ctx context.Context) ([]models.ModelProvider, error)
	GetProvider(ctx context.Context, name string) (*models.ModelProvider, error)
	CreateProvider(ctx context.Context, provider *models.ModelProvider) error
	UpdateProvider(ctx context.Context, provider *models.ModelProvider) error
	DeleteProvider(ctx context.Context, name string) error
}

// ── Recipe Run Store ────────────────────────────────────────

type RecipeRunStore interface {
	ListRecipeRuns(ctx context.Context, recipeID string, limit int) ([]models.RecipeRun, error)
	GetRecipeRun(ctx context.Context, id string) (*models.RecipeRun, error)
	CreateRecipeRun(ctx context.Context, run *models.RecipeRun) error
	UpdateRecipeRun(ctx context.Context, run *models.RecipeRun) error
}

// ── MCP Tool Store ──────────────────────────────────────────

type MCPToolStore interface {
	ListTools(ctx context.Context, kitchen string) ([]models.MCPTool, error)
	GetTool(ctx context.Context, kitchen, name string) (*models.MCPTool, error)
	CreateTool(ctx context.Context, tool *models.MCPTool) error
	UpdateTool(ctx context.Context, tool *models.MCPTool) error
	DeleteTool(ctx context.Context, kitchen, name string) error
}

// ── Prompt Store ────────────────────────────────────────────

type PromptStore interface {
	ListPrompts(ctx context.Context, kitchen string) ([]models.Prompt, error)
	GetPrompt(ctx context.Context, kitchen, name string) (*models.Prompt, error)
	GetPromptVersion(ctx context.Context, kitchen, name string, version int) (*models.Prompt, error)
	ListPromptVersions(ctx context.Context, kitchen, name string) ([]models.Prompt, error)
	CreatePrompt(ctx context.Context, prompt *models.Prompt) error
	UpdatePrompt(ctx context.Context, prompt *models.Prompt) error // auto-bumps version, keeps history
	DeletePrompt(ctx context.Context, kitchen, name string) error
}

// ── Kitchen Settings Store ──────────────────────────────────

type KitchenSettingsStore interface {
	GetKitchenSettings(ctx context.Context, kitchenID string) (*models.KitchenSettings, error)
	UpsertKitchenSettings(ctx context.Context, settings *models.KitchenSettings) error
}

// ── Audit Store ─────────────────────────────────────────────

type AuditStore interface {
	// CreateAuditEvent persists an audit event.
	CreateAuditEvent(ctx context.Context, event *models.AuditEvent) error

	// ListAuditEvents returns filtered audit events.
	ListAuditEvents(ctx context.Context, filter models.AuditFilter) ([]models.AuditEvent, error)

	// CountAuditEvents returns the count of events matching the filter.
	CountAuditEvents(ctx context.Context, filter models.AuditFilter) (int64, error)

	// DeleteAuditEvent removes an audit event by ID.
	DeleteAuditEvent(ctx context.Context, id string) error
}

// ── Approval Store ──────────────────────────────────────────

type ApprovalStore interface {
	// CreateApproval persists a new approval record (status=waiting).
	CreateApproval(ctx context.Context, record *models.ApprovalRecord) error

	// GetApproval returns an approval by gate key.
	GetApproval(ctx context.Context, gateKey string) (*models.ApprovalRecord, error)

	// UpdateApproval updates an approval record (approve/reject).
	UpdateApproval(ctx context.Context, record *models.ApprovalRecord) error

	// ListApprovals returns approvals filtered by kitchen and status.
	ListApprovals(ctx context.Context, kitchen, status string, limit int) ([]models.ApprovalRecord, error)
}

// ── Notification Channel Store ──────────────────────────────

type NotificationChannelStore interface {
	ListChannels(ctx context.Context, kitchen string) ([]models.NotificationChannel, error)
	GetChannel(ctx context.Context, kitchen, name string) (*models.NotificationChannel, error)
	CreateChannel(ctx context.Context, channel *models.NotificationChannel) error
	UpdateChannel(ctx context.Context, channel *models.NotificationChannel) error
	DeleteChannel(ctx context.Context, kitchen, name string) error
}

// ── Vector Doc Store ────────────────────────────────────────

// VectorDocStore provides CRUD for vector documents used by RAG pipelines.
// The in-memory store uses the embedded vector index; PostgreSQL uses pgvector.
type VectorDocStore interface {
	// UpsertVectorDocs inserts or updates documents in the vector index.
	UpsertVectorDocs(ctx context.Context, kitchen string, docs []models.VectorDoc) error

	// SearchVectorDocs performs similarity search returning top-k results.
	SearchVectorDocs(ctx context.Context, kitchen string, vector []float64, topK int, namespace string) ([]models.SearchResult, error)

	// DeleteVectorDocs removes documents by ID.
	DeleteVectorDocs(ctx context.Context, kitchen string, ids []string) error

	// CountVectorDocs returns the document count for a kitchen/namespace.
	CountVectorDocs(ctx context.Context, kitchen, namespace string) (int64, error)

	// ListVectorNamespaces returns distinct namespaces for a kitchen.
	ListVectorNamespaces(ctx context.Context, kitchen string) ([]string, error)
}

// ── Data Connector Store ────────────────────────────────────

// DataConnectorStore persists data connector configurations.
type DataConnectorStore interface {
	ListConnectors(ctx context.Context, kitchen string) ([]models.DataConnectorConfig, error)
	GetConnector(ctx context.Context, kitchen, id string) (*models.DataConnectorConfig, error)
	CreateConnector(ctx context.Context, connector *models.DataConnectorConfig) error
	UpdateConnector(ctx context.Context, connector *models.DataConnectorConfig) error
	DeleteConnector(ctx context.Context, kitchen, id string) error
}

// ── Errors ──────────────────────────────────────────────────

// ErrNotFound is returned when a requested entity does not exist.
type ErrNotFound struct {
	Entity string
	Key    string
}

func (e *ErrNotFound) Error() string {
	return e.Entity + " not found: " + e.Key
}

// ── Filter helpers ──────────────────────────────────────────

// ListFilter provides common pagination/filter options.
type ListFilter struct {
	Limit  int
	Offset int
	Since  *time.Time
}
