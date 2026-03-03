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
	ScopedKeyStore
	TestSuiteStore
	EnvironmentStore
	AgentDeploymentStore
	ServiceAccountStore

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

	// Agent filtering — tag and environment queries (R9)
	ListAgentsByTag(ctx context.Context, kitchen, tagKey, tagValue string) ([]models.Agent, error)
	ListAgentsByEnvironment(ctx context.Context, kitchen, envSlug string) ([]models.Agent, error)
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
	DeleteKitchen(ctx context.Context, id string) error
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

// ── Scoped API Key Store ────────────────────────────────────

// ScopedKeyStore manages scoped API keys for the Agent Viewer.
// Scoped keys grant access to specific agents with usage quotas and traceability.
type ScopedKeyStore interface {
	// CreateScopedKey persists a new scoped API key.
	CreateScopedKey(ctx context.Context, key *models.ScopedAPIKey) error

	// GetScopedKey returns a scoped key by ID within a kitchen.
	GetScopedKey(ctx context.Context, kitchen, id string) (*models.ScopedAPIKey, error)

	// GetScopedKeyByHash looks up a scoped key by its bcrypt hash.
	// Used by the ScopedKeyProvider to authenticate requests.
	GetScopedKeyByHash(ctx context.Context, keyHash string) (*models.ScopedAPIKey, error)

	// ListScopedKeys returns all scoped keys in a kitchen.
	ListScopedKeys(ctx context.Context, kitchen string) ([]models.ScopedAPIKey, error)

	// IncrementScopedKeyUsage atomically increments the call count for a key.
	IncrementScopedKeyUsage(ctx context.Context, id string) error

	// RevokeScopedKey marks a key as revoked.
	RevokeScopedKey(ctx context.Context, kitchen, id string) error

	// DeleteScopedKey permanently removes a scoped key.
	DeleteScopedKey(ctx context.Context, kitchen, id string) error
}

// TestSuiteStore manages test suites, test cases, and test run results.
type TestSuiteStore interface {
	// CreateTestSuite creates a new test suite with its cases.
	CreateTestSuite(ctx context.Context, suite *models.TestSuite) error

	// GetTestSuite returns a test suite by kitchen and ID.
	GetTestSuite(ctx context.Context, kitchen, id string) (*models.TestSuite, error)

	// ListTestSuites returns all test suites in a kitchen.
	ListTestSuites(ctx context.Context, kitchen string) ([]models.TestSuite, error)

	// UpdateTestSuite updates an existing test suite.
	UpdateTestSuite(ctx context.Context, suite *models.TestSuite) error

	// DeleteTestSuite removes a test suite.
	DeleteTestSuite(ctx context.Context, kitchen, id string) error

	// CreateTestRun records a new test run result.
	CreateTestRun(ctx context.Context, run *models.TestRun) error

	// GetTestRun returns a specific test run.
	GetTestRun(ctx context.Context, id string) (*models.TestRun, error)

	// ListTestRuns returns test runs for a suite, most recent first.
	ListTestRuns(ctx context.Context, suiteID string, limit int) ([]models.TestRun, error)
}

// ── Errors ──────────────────────────────────────────────────

// ── Environment Store (R9) ──────────────────────────────────

// EnvironmentStore manages deployment environments (dev, qa, prod, custom)
// and their configuration. Pro feature gated by PlanLimits.MaxEnvironments.
type EnvironmentStore interface {
	// CreateEnvironment persists a new environment.
	CreateEnvironment(ctx context.Context, env *models.Environment) error

	// GetEnvironment returns an environment by kitchen and slug.
	GetEnvironment(ctx context.Context, kitchen, slug string) (*models.Environment, error)

	// ListEnvironments returns all environments in a kitchen, ordered by Order.
	ListEnvironments(ctx context.Context, kitchen string) ([]models.Environment, error)

	// UpdateEnvironment updates an existing environment.
	UpdateEnvironment(ctx context.Context, env *models.Environment) error

	// DeleteEnvironment removes a non-default environment.
	DeleteEnvironment(ctx context.Context, kitchen, slug string) error

	// SeedDefaultEnvironments creates the default dev/qa/prod environments if none exist.
	SeedDefaultEnvironments(ctx context.Context, kitchen string) error
}

// ── Agent Deployment Store (R9) ─────────────────────────────

// AgentDeploymentStore tracks which agent versions are deployed in which environments.
// A deployment is immutable once active — new promotions create new deployment records.
type AgentDeploymentStore interface {
	// CreateDeployment records a new agent deployment.
	CreateDeployment(ctx context.Context, deployment *models.AgentDeployment) error

	// GetDeployment returns a deployment by ID.
	GetDeployment(ctx context.Context, id string) (*models.AgentDeployment, error)

	// GetActiveDeployment returns the currently active deployment for an agent in an environment.
	GetActiveDeployment(ctx context.Context, kitchen, agentName, envSlug string) (*models.AgentDeployment, error)

	// ListDeployments returns all deployments for an agent across environments.
	ListDeployments(ctx context.Context, kitchen, agentName string, limit int) ([]models.AgentDeployment, error)

	// ListDeploymentsByEnvironment returns all deployments in an environment.
	ListDeploymentsByEnvironment(ctx context.Context, kitchen, envSlug string, limit int) ([]models.AgentDeployment, error)

	// UpdateDeploymentStatus changes a deployment's status (e.g. active → rolled_back).
	UpdateDeploymentStatus(ctx context.Context, id string, status models.DeploymentStatus) error
}

// ── Service Account Store (R9) ──────────────────────────────

// ServiceAccountStore manages kitchen-scoped machine identities.
// Service accounts produce Identity objects for RBAC, unlike ScopedAPIKeys
// which are agent-scoped access tokens.
type ServiceAccountStore interface {
	// CreateServiceAccount persists a new service account.
	CreateServiceAccount(ctx context.Context, sa *models.ServiceAccount) error

	// GetServiceAccount returns a service account by kitchen and ID.
	GetServiceAccount(ctx context.Context, kitchen, id string) (*models.ServiceAccount, error)

	// GetServiceAccountByTokenHash looks up a service account by token hash.
	// Used by the auth provider chain to authenticate requests.
	GetServiceAccountByTokenHash(ctx context.Context, tokenHash string) (*models.ServiceAccount, error)

	// ListServiceAccounts returns all service accounts in a kitchen.
	ListServiceAccounts(ctx context.Context, kitchen string) ([]models.ServiceAccount, error)

	// UpdateServiceAccountLastUsed updates the last-used timestamp atomically.
	UpdateServiceAccountLastUsed(ctx context.Context, id string) error

	// RevokeServiceAccount marks a service account as revoked.
	RevokeServiceAccount(ctx context.Context, kitchen, id, revokedBy string) error

	// DeleteServiceAccount permanently removes a service account.
	DeleteServiceAccount(ctx context.Context, kitchen, id string) error
}

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
