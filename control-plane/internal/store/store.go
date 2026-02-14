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

	// Ping checks if the database is reachable.
	Ping(ctx context.Context) error

	// Close releases all resources held by the store.
	Close() error

	// Migrate runs database migrations.
	Migrate(ctx context.Context) error
}

// ── Agent Store ─────────────────────────────────────────────

type AgentStore interface {
	ListAgents(ctx context.Context, kitchen string) ([]models.Agent, error)
	GetAgent(ctx context.Context, kitchen, name string) (*models.Agent, error)
	CreateAgent(ctx context.Context, agent *models.Agent) error
	UpdateAgent(ctx context.Context, agent *models.Agent) error
	DeleteAgent(ctx context.Context, kitchen, name string) error
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

type TraceStore interface {
	ListTraces(ctx context.Context, kitchen string, limit int) ([]models.Trace, error)
	GetTrace(ctx context.Context, id string) (*models.Trace, error)
	CreateTrace(ctx context.Context, trace *models.Trace) error
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
