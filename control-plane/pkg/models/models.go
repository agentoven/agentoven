package models

import "time"

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

type Kitchen struct {
	ID          string            `json:"id" db:"id"`
	Name        string            `json:"name" db:"name"`
	Description string            `json:"description,omitempty" db:"description"`
	Owner       string            `json:"owner" db:"owner"`
	Tags        map[string]string `json:"tags,omitempty"`
	CreatedAt   time.Time         `json:"created_at" db:"created_at"`
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
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Kind      string   `json:"kind"` // azure-openai, anthropic, ollama, bedrock, vertex, groq, together
	Endpoint  string   `json:"endpoint,omitempty"`
	Models    []string `json:"models"`
	IsDefault bool     `json:"is_default"`
}
