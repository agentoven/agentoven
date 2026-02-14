// Package contracts defines the service interfaces for the AgentOven control plane.
//
// These interfaces form the boundary between the OSS and enterprise repos.
// The OSS repo ships concrete implementations (ModelRouter, Gateway, Engine).
// The enterprise repo (agentoven-pro) can provide enhanced implementations
// that wrap or replace the defaults.
//
// The Handlers struct in api/handlers uses these interfaces, so swapping
// a community implementation for an enterprise one is a single line change
// in the wiring code (main.go).
package contracts

import (
	"context"
	"net/http"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// Store is a type alias for the internal Store interface.
// Exposed in pkg/ so the enterprise repo can reference it in its own
// middleware and services without importing internal/ directly.
type Store = store.Store

// ErrNotFound is a type alias for the internal ErrNotFound error.
type ErrNotFound = store.ErrNotFound

// ── Model Router Service ────────────────────────────────────

// ModelRouterService routes LLM requests to configured providers.
// OSS implementation: internal/router.ModelRouter
// Pro implementation: enhanced router with budget enforcement, custom strategies
type ModelRouterService interface {
	// Route sends a request through the router using the specified strategy.
	Route(ctx context.Context, req *models.RouteRequest) (*models.RouteResponse, error)

	// GetCostSummary returns cost tracking data for a kitchen.
	GetCostSummary(kitchen string) *models.CostSummary

	// HealthCheck pings all configured providers and returns their status.
	HealthCheck(ctx context.Context) map[string]string
}

// ── MCP Gateway Service ─────────────────────────────────────

// MCPGatewayService handles MCP (Model Context Protocol) requests.
// OSS implementation: internal/mcpgw.Gateway
// Pro implementation: enhanced gateway with cross-org federation, advanced auth
type MCPGatewayService interface {
	// HandleJSONRPC processes an MCP JSON-RPC 2.0 request.
	HandleJSONRPC(ctx context.Context, kitchen string, req *models.MCPRequest) *models.MCPResponse

	// Subscribe registers a channel for SSE events in a kitchen.
	Subscribe(kitchen string) chan models.MCPResponse

	// Unsubscribe removes an SSE channel for a kitchen.
	Unsubscribe(kitchen string, ch chan models.MCPResponse)
}

// ── Workflow Service ────────────────────────────────────────

// WorkflowService executes recipe workflows (DAGs).
// OSS implementation: internal/workflow.Engine
// Pro implementation: enhanced engine with distributed execution, advanced scheduling
type WorkflowService interface {
	// ExecuteRecipe starts an async recipe execution.
	// Returns the run ID immediately; execution happens in background.
	ExecuteRecipe(ctx context.Context, recipe *models.Recipe, kitchen string, input map[string]interface{}) (string, error)

	// CancelRun cancels a running recipe execution.
	CancelRun(runID string) error

	// ApproveGate approves a human gate step in a running recipe.
	ApproveGate(runID, stepName string) error
}

// ── Provider Driver ─────────────────────────────────────────

// ProviderDriver is the interface for model provider integrations.
// OSS ships: OpenAI, Azure OpenAI, Anthropic, Ollama drivers.
// Pro adds:  AWS Bedrock, Azure AI Foundry, Google Vertex, SageMaker drivers.
//
// Drivers are registered in the Model Router via RegisterDriver().
type ProviderDriver interface {
	// Kind returns the provider identifier (e.g., "openai", "bedrock").
	Kind() string

	// Call sends a chat completion request to the provider.
	Call(ctx context.Context, provider *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, error)

	// HealthCheck verifies the provider is reachable.
	HealthCheck(ctx context.Context, provider *models.ModelProvider) error
}

// ── Plan Resolver ───────────────────────────────────────────

// PlanResolver resolves a Kitchen to its PlanLimits.
// OSS implementation: CommunityPlanResolver (returns static community limits).
// Pro implementation: reads JWT license key to determine tier + limits.
type PlanResolver interface {
	// Resolve returns the plan limits for the given kitchen.
	Resolve(ctx context.Context, kitchen *models.Kitchen) (*models.PlanLimits, error)
}

// ── Tier Enforcer ───────────────────────────────────────────

// TierEnforcer is HTTP middleware that enforces plan limits.
// It checks quotas (max agents, max providers, etc.) before allowing requests.
type TierEnforcer interface {
	// Middleware returns an http.Handler middleware that enforces tier limits.
	Middleware(next http.Handler) http.Handler
}
