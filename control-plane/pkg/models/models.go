package models

import (
	"encoding/json"
	"time"
)

// ── Agent ────────────────────────────────────────────────────

type AgentStatus string

const (
	AgentStatusDraft   AgentStatus = "draft"
	AgentStatusBaking  AgentStatus = "baking"
	AgentStatusReady   AgentStatus = "ready"
	AgentStatusCooled  AgentStatus = "cooled"
	AgentStatusBurnt   AgentStatus = "burnt"
	AgentStatusRetired AgentStatus = "retired"
)

type Agent struct {
	ID          string      `json:"id" db:"id"`
	Name        string      `json:"name" db:"name"`
	Description string      `json:"description" db:"description"`
	Framework   string      `json:"framework" db:"framework"`
	Status      AgentStatus `json:"status" db:"status"`
	Kitchen     string      `json:"kitchen" db:"kitchen"`
	Version     string      `json:"version" db:"version"`

	// A2A Configuration
	A2AEndpoint string   `json:"a2a_endpoint,omitempty" db:"a2a_endpoint"`
	Skills      []string `json:"skills,omitempty"`

	// Model Configuration
	ModelProvider string `json:"model_provider,omitempty" db:"model_provider"`
	ModelName     string `json:"model_name,omitempty" db:"model_name"`

	// Ingredients (embedded for simplicity; normalize for large payloads)
	Ingredients []Ingredient `json:"ingredients,omitempty"`

	// Metadata
	Tags      map[string]string `json:"tags,omitempty"`
	CreatedAt time.Time         `json:"created_at" db:"created_at"`
	UpdatedAt time.Time         `json:"updated_at" db:"updated_at"`
	CreatedBy string            `json:"created_by,omitempty" db:"created_by"`
}

// ── Ingredient ───────────────────────────────────────────────

type IngredientKind string

const (
	IngredientModel  IngredientKind = "model"
	IngredientTool   IngredientKind = "tool"
	IngredientPrompt IngredientKind = "prompt"
	IngredientData   IngredientKind = "data"
)

type Ingredient struct {
	ID       string                 `json:"id"`
	Name     string                 `json:"name"`
	Kind     IngredientKind         `json:"kind"`
	Config   map[string]interface{} `json:"config,omitempty"`
	Required bool                   `json:"required"`
}

// ── Recipe (Workflow DAG) ────────────────────────────────────

type StepKind string

const (
	StepAgent     StepKind = "agent"
	StepHumanGate StepKind = "human_gate"
	StepEvaluator StepKind = "evaluator"
	StepCondition StepKind = "condition"
	StepFanOut    StepKind = "fan_out"
	StepFanIn     StepKind = "fan_in"
)

type Step struct {
	Name         string                 `json:"name"`
	Kind         StepKind               `json:"kind"`
	AgentRef     string                 `json:"agent_ref,omitempty"`
	DependsOn    []string               `json:"depends_on,omitempty"`
	Config       map[string]interface{} `json:"config,omitempty"`
	MaxRetries   int                    `json:"max_retries,omitempty"`
	TimeoutSecs  int                    `json:"timeout_secs,omitempty"`
}

type Recipe struct {
	ID          string    `json:"id" db:"id"`
	Name        string    `json:"name" db:"name"`
	Description string    `json:"description" db:"description"`
	Kitchen     string    `json:"kitchen" db:"kitchen"`
	Steps       []Step    `json:"steps"`
	Version     string    `json:"version" db:"version"`
	CreatedAt   time.Time `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time `json:"updated_at" db:"updated_at"`
	CreatedBy   string    `json:"created_by,omitempty" db:"created_by"`
}

// ── Kitchen (Workspace/Tenant) ───────────────────────────────

type Plan string

const (
	PlanCommunity  Plan = "community"
	PlanPro        Plan = "pro"
	PlanEnterprise Plan = "enterprise"
)

type Kitchen struct {
	ID          string            `json:"id" db:"id"`
	Name        string            `json:"name" db:"name"`
	Description string            `json:"description,omitempty" db:"description"`
	Owner       string            `json:"owner" db:"owner"`
	Plan        Plan              `json:"plan" db:"plan"`
	Tags        map[string]string `json:"tags,omitempty"`
	CreatedAt   time.Time         `json:"created_at" db:"created_at"`
}

// PlanLimits defines the quotas and feature gates for a given plan tier.
// The CommunityPlanResolver returns static limits; the Pro resolver reads
// the license key to determine tier-specific limits.
type PlanLimits struct {
	Plan               Plan   `json:"plan"`
	MaxAgents          int    `json:"max_agents"`
	MaxRecipes         int    `json:"max_recipes"`
	MaxProviders       int    `json:"max_providers"`
	MaxMCPTools        int    `json:"max_mcp_tools"`
	TraceRetentionDays int    `json:"trace_retention_days"`
	AllowedStrategies  []RoutingStrategy `json:"allowed_strategies"`
	CloudProviders     bool   `json:"cloud_providers"`      // Bedrock, Foundry, Vertex
	SSO                bool   `json:"sso"`                   // SSO/SAML
	Federation         bool   `json:"federation"`            // Cross-org
	CustomDrivers      bool   `json:"custom_drivers"`        // Custom ProviderDrivers
}

// CommunityLimits returns the default PlanLimits for the free community tier.
func CommunityLimits() *PlanLimits {
	return &PlanLimits{
		Plan:               PlanCommunity,
		MaxAgents:          10,
		MaxRecipes:         5,
		MaxProviders:       2,
		MaxMCPTools:        5,
		TraceRetentionDays: 7,
		AllowedStrategies:  []RoutingStrategy{RoutingFallback},
		CloudProviders:     false,
		SSO:                false,
		Federation:         false,
		CustomDrivers:      false,
	}
}

// ── Trace ────────────────────────────────────────────────────

type Trace struct {
	ID          string                 `json:"id" db:"id"`
	AgentName   string                 `json:"agent_name" db:"agent_name"`
	RecipeName  string                 `json:"recipe_name,omitempty" db:"recipe_name"`
	Kitchen     string                 `json:"kitchen" db:"kitchen"`
	Status      string                 `json:"status" db:"status"`
	DurationMs  int64                  `json:"duration_ms" db:"duration_ms"`
	TotalTokens int64                  `json:"total_tokens" db:"total_tokens"`
	CostUSD     float64                `json:"cost_usd" db:"cost_usd"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt   time.Time              `json:"created_at" db:"created_at"`
}

// ── Model Provider ───────────────────────────────────────────

type ModelProvider struct {
	ID        string                 `json:"id" db:"id"`
	Name      string                 `json:"name" db:"name"`
	Kind      string                 `json:"kind" db:"kind"`
	Endpoint  string                 `json:"endpoint,omitempty" db:"endpoint"`
	Models    []string               `json:"models"`
	Config    map[string]interface{} `json:"config,omitempty"`
	IsDefault bool                   `json:"is_default" db:"is_default"`
	CreatedAt time.Time              `json:"created_at" db:"created_at"`

	// Health check cache — populated by TestProvider
	LastTestedAt    *time.Time `json:"last_tested_at,omitempty" db:"last_tested_at"`
	LastTestHealthy *bool      `json:"last_test_healthy,omitempty" db:"last_test_healthy"`
	LastTestError   string     `json:"last_test_error,omitempty" db:"last_test_error"`
	LastTestLatency int64      `json:"last_test_latency_ms,omitempty" db:"last_test_latency_ms"`
}

// ProviderTestResult is returned by the TestProvider endpoint.
type ProviderTestResult struct {
	Provider  string `json:"provider"`
	Kind      string `json:"kind"`
	Healthy   bool   `json:"healthy"`
	LatencyMs int64  `json:"latency_ms"`
	Model     string `json:"model,omitempty"`
	Error     string `json:"error,omitempty"`
}

// ── Recipe Run ───────────────────────────────────────────────

type RecipeRunStatus string

const (
	RecipeRunSubmitted  RecipeRunStatus = "submitted"
	RecipeRunRunning    RecipeRunStatus = "running"
	RecipeRunCompleted  RecipeRunStatus = "completed"
	RecipeRunFailed     RecipeRunStatus = "failed"
	RecipeRunCanceled   RecipeRunStatus = "canceled"
	RecipeRunPaused     RecipeRunStatus = "paused"
)

type RecipeRun struct {
	ID          string                 `json:"id" db:"id"`
	RecipeID    string                 `json:"recipe_id" db:"recipe_id"`
	Kitchen     string                 `json:"kitchen" db:"kitchen"`
	Status      RecipeRunStatus        `json:"status" db:"status"`
	Input       map[string]interface{} `json:"input,omitempty"`
	Output      map[string]interface{} `json:"output,omitempty"`
	StepResults []StepResult           `json:"step_results,omitempty"`
	StartedAt   time.Time              `json:"started_at" db:"started_at"`
	CompletedAt *time.Time             `json:"completed_at,omitempty" db:"completed_at"`
	DurationMs  int64                  `json:"duration_ms,omitempty" db:"duration_ms"`
	Error       string                 `json:"error,omitempty" db:"error"`
}

type StepResult struct {
	StepName   string                 `json:"step_name"`
	StepKind   string                 `json:"step_kind"`
	Status     string                 `json:"status"`
	Output     map[string]interface{} `json:"output,omitempty"`
	AgentRef   string                 `json:"agent_ref,omitempty"`
	StartedAt  time.Time              `json:"started_at"`
	DurationMs int64                  `json:"duration_ms"`
	Error      string                 `json:"error,omitempty"`
	Tokens     int64                  `json:"tokens,omitempty"`
	CostUSD    float64                `json:"cost_usd,omitempty"`
}

// ── MCP Tool ─────────────────────────────────────────────────

type MCPTool struct {
	ID          string                 `json:"id" db:"id"`
	Name        string                 `json:"name" db:"name"`
	Description string                 `json:"description" db:"description"`
	Kitchen     string                 `json:"kitchen" db:"kitchen"`
	Endpoint    string                 `json:"endpoint" db:"endpoint"`
	Transport   string                 `json:"transport" db:"transport"` // http, sse, stdio
	Schema      map[string]interface{} `json:"schema,omitempty"`
	AuthConfig  map[string]interface{} `json:"auth_config,omitempty"`
	Enabled     bool                   `json:"enabled" db:"enabled"`
	CreatedAt   time.Time              `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time              `json:"updated_at" db:"updated_at"`
}

// ── Model Routing ────────────────────────────────────────────

type RoutingStrategy string

const (
	RoutingFallback        RoutingStrategy = "fallback"
	RoutingCostOptimized   RoutingStrategy = "cost-optimized"
	RoutingLatencyOptimized RoutingStrategy = "latency-optimized"
	RoutingRoundRobin      RoutingStrategy = "round-robin"
)

type RouteRequest struct {
	Messages []ChatMessage   `json:"messages"`
	Model    string          `json:"model,omitempty"`
	Strategy RoutingStrategy `json:"strategy,omitempty"`
	Kitchen  string          `json:"kitchen,omitempty"`
	AgentRef string          `json:"agent_ref,omitempty"`
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type RouteResponse struct {
	ID             string           `json:"id"`
	Provider       string           `json:"provider"`
	Model          string           `json:"model"`
	Strategy       RoutingStrategy  `json:"strategy"`
	Content        string           `json:"content"`
	Usage          TokenUsage       `json:"usage"`
	LatencyMs      int64            `json:"latency_ms"`
	Cached         bool             `json:"cached"`
}

type TokenUsage struct {
	InputTokens    int64   `json:"input_tokens"`
	OutputTokens   int64   `json:"output_tokens"`
	TotalTokens    int64   `json:"total_tokens"`
	EstimatedCost  float64 `json:"estimated_cost_usd"`
}

type CostSummary struct {
	TotalCostUSD  float64            `json:"total_cost_usd"`
	TotalTokens   int64              `json:"total_tokens"`
	Period        string             `json:"period"`
	ByAgent       map[string]float64 `json:"by_agent"`
	ByModel       map[string]float64 `json:"by_model"`
	ByProvider    map[string]float64 `json:"by_provider"`
}

// ── MCP Protocol Types ───────────────────────────────────────

type MCPRequest struct {
	Jsonrpc string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      interface{}     `json:"id"`
}

type MCPResponse struct {
	Jsonrpc string      `json:"jsonrpc"`
	Result  interface{} `json:"result,omitempty"`
	Error   *MCPError   `json:"error,omitempty"`
	ID      interface{} `json:"id"`
}

type MCPError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type MCPToolInfo struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema map[string]interface{} `json:"inputSchema,omitempty"`
}

type MCPToolCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

type MCPToolResult struct {
	Content []MCPContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

type MCPContent struct {
	Type string `json:"type"` // text, image, resource
	Text string `json:"text,omitempty"`
}
