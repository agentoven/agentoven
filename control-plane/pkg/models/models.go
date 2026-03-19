package models

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ── Semantic Versioning Helpers ──────────────────────────────

// DefaultAgentVersion is the initial version assigned to newly created agents.
const DefaultAgentVersion = "0.1.0"

// ParseSemver splits a "major.minor.patch" string. Returns (0,1,0) on error.
func ParseSemver(v string) (major, minor, patch int) {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return 0, 1, 0
	}
	major, _ = strconv.Atoi(parts[0])
	minor, _ = strconv.Atoi(parts[1])
	patch, _ = strconv.Atoi(parts[2])
	return
}

// FormatSemver formats major.minor.patch into a version string.
func FormatSemver(major, minor, patch int) string {
	return fmt.Sprintf("%d.%d.%d", major, minor, patch)
}

// BumpPatch increments the patch component: 0.1.2 → 0.1.3
func BumpPatch(v string) string {
	major, minor, patch := ParseSemver(v)
	return FormatSemver(major, minor, patch+1)
}

// BumpMinor increments the minor component and resets patch: 0.1.3 → 0.2.0
func BumpMinor(v string) string {
	major, minor, _ := ParseSemver(v)
	return FormatSemver(major, minor+1, 0)
}

// BumpMajor increments the major component and resets minor+patch: 0.2.3 → 1.0.0
func BumpMajor(v string) string {
	major, _, _ := ParseSemver(v)
	return FormatSemver(major+1, 0, 0)
}

// IsSemver returns true if the string looks like "X.Y.Z".
func IsSemver(v string) bool {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if _, err := strconv.Atoi(p); err != nil {
			return false
		}
	}
	return true
}

// MigrateLegacyVersion converts old integer versions ("1","2","3") to semver.
// If already semver, returns as-is.
func MigrateLegacyVersion(v string) string {
	if IsSemver(v) {
		return v
	}
	// Legacy integer version: treat as 0.<n>.0
	if n, err := strconv.Atoi(v); err == nil {
		return FormatSemver(0, n, 0)
	}
	return DefaultAgentVersion
}

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

type AgentMode string

const (
	AgentModeManaged  AgentMode = "managed"
	AgentModeExternal AgentMode = "external"
)

// AgentBehavior controls whether an agent is single-turn (reactive) or
// runs an autonomous reasoning loop with memory (agentic).
type AgentBehavior string

const (
	// BehaviorReactive is the default — single request/response, no memory.
	BehaviorReactive AgentBehavior = "reactive"
	// BehaviorAgentic enables the autonomous loop with sliding context,
	// native tool calling, session memory, and agent-to-agent delegation.
	BehaviorAgentic AgentBehavior = "agentic"
)

// ReasoningStrategy controls how the agentic loop reasons about tasks.
type ReasoningStrategy string

const (
	// StrategyReAct uses the ReAct pattern: Thought → Action → Observation → repeat.
	StrategyReAct ReasoningStrategy = "react"
	// StrategyPlanAndExecute generates a full plan first, then executes each step.
	StrategyPlanAndExecute ReasoningStrategy = "plan-and-execute"
	// StrategyReflexion adds a self-critique step after each action.
	StrategyReflexion ReasoningStrategy = "reflexion"
)

// ExecutionMode determines how an agent process is spawned at bake time.
type ExecutionMode string

const (
	// ExecModeLocal spawns a Python process on the control plane host.
	ExecModeLocal ExecutionMode = "local"
	// ExecModeDocker runs the agent as a Docker container.
	ExecModeDocker ExecutionMode = "docker"
	// ExecModeK8s deploys the agent as a Kubernetes Deployment + Service.
	ExecModeK8s ExecutionMode = "k8s"
)

// ProcessStatus describes the lifecycle of a spawned agent process.
type ProcessStatus string

const (
	ProcessStarting ProcessStatus = "starting"
	ProcessRunning  ProcessStatus = "running"
	ProcessStopped  ProcessStatus = "stopped"
	ProcessFailed   ProcessStatus = "failed"
)

// ProcessInfo tracks the runtime state of a spawned agent process.
type ProcessInfo struct {
	AgentName   string        `json:"agent_name"`
	Kitchen     string        `json:"kitchen"`
	Mode        ExecutionMode `json:"mode"`
	Status      ProcessStatus `json:"status"`
	Port        int           `json:"port"`
	Endpoint    string        `json:"endpoint"`               // full URL (http://host:port)
	PID         int           `json:"pid,omitempty"`          // local mode
	ContainerID string        `json:"container_id,omitempty"` // docker mode
	PodName     string        `json:"pod_name,omitempty"`     // k8s mode
	StartedAt   time.Time     `json:"started_at"`
	Error       string        `json:"error,omitempty"`
}

type Agent struct {
	ID          string      `json:"id" db:"id"`
	Name        string      `json:"name" db:"name"`
	Description string      `json:"description" db:"description"`
	Framework   string      `json:"framework" db:"framework"`
	Mode        AgentMode   `json:"mode" db:"mode"`
	Status      AgentStatus `json:"status" db:"status"`
	Kitchen     string      `json:"kitchen" db:"kitchen"`
	Version     string      `json:"version" db:"version"`

	// VersionBump controls how the store should bump the version on update.
	// "patch" → 0.1.0→0.1.1, "minor" → 0.1.1→0.2.0, "major" → 0.2.0→1.0.0
	// Empty string → no version bump (status-only updates). Not persisted.
	VersionBump string `json:"-" db:"-"`

	// ExecutionMode determines how the agent process is spawned (local/docker/k8s).
	// Defaults to "local" if not specified.
	ExecutionMode ExecutionMode `json:"execution_mode,omitempty" db:"execution_mode"`

	// Managed mode configuration
	MaxTurns int `json:"max_turns,omitempty" db:"max_turns"`

	// Agentic behavior — controls whether the agent runs an autonomous
	// reasoning loop with memory, tool calling, and delegation.
	Behavior          AgentBehavior     `json:"behavior,omitempty" db:"behavior"`
	ContextBudget     int               `json:"context_budget,omitempty" db:"context_budget"`         // max tokens for sliding context window (default: 16000)
	SummaryModel      string            `json:"summary_model,omitempty" db:"summary_model"`           // cheap model for context compression (e.g. "gpt-4o-mini")
	ReasoningStrategy ReasoningStrategy `json:"reasoning_strategy,omitempty" db:"reasoning_strategy"` // "react", "plan-and-execute", "reflexion"

	// A2A Configuration — A2AEndpoint is the stable control-plane URL
	// (always /agents/{name}/a2a). BackendEndpoint is the actual backend
	// URL where the agent process or external service lives. The control
	// plane proxies A2A calls from A2AEndpoint → BackendEndpoint (ADR-0007).
	A2AEndpoint     string   `json:"a2a_endpoint,omitempty" db:"a2a_endpoint"`
	BackendEndpoint string   `json:"backend_endpoint,omitempty" db:"backend_endpoint"`
	Skills          []string `json:"skills,omitempty"`

	// Model Configuration
	ModelProvider string `json:"model_provider,omitempty" db:"model_provider"`
	ModelName     string `json:"model_name,omitempty" db:"model_name"`

	// Backup provider for automatic failover when primary provider fails
	BackupProvider string `json:"backup_provider,omitempty" db:"backup_provider"`
	BackupModel    string `json:"backup_model,omitempty" db:"backup_model"`

	// Guardrails — input/output validation rules applied during invoke
	Guardrails []Guardrail `json:"guardrails,omitempty"`

	// Ingredients (embedded for simplicity; normalize for large payloads)
	Ingredients []Ingredient `json:"ingredients,omitempty"`

	// ResolvedConfig caches the fully-resolved ingredient configuration
	// set at bake time. Used by InvokeAgent and A2A handlers to avoid
	// re-resolving on every request.
	ResolvedConfig *ResolvedIngredients `json:"resolved_config,omitempty"`

	// Process tracks the runtime state of the spawned agent process.
	// Populated by the ProcessManager at bake time.
	Process *ProcessInfo `json:"process,omitempty"`

	// Metadata
	Tags      map[string]string `json:"tags,omitempty"`
	CreatedAt time.Time         `json:"created_at" db:"created_at"`
	UpdatedAt time.Time         `json:"updated_at" db:"updated_at"`
	CreatedBy string            `json:"created_by,omitempty" db:"created_by"`
}

// ── Ingredient ───────────────────────────────────────────────

type IngredientKind string

const (
	IngredientModel         IngredientKind = "model"
	IngredientTool          IngredientKind = "tool"
	IngredientPrompt        IngredientKind = "prompt"
	IngredientData          IngredientKind = "data"
	IngredientObservability IngredientKind = "observability"
	IngredientEmbedding     IngredientKind = "embedding"
	IngredientVectorStore   IngredientKind = "vectorstore"
	IngredientRetriever     IngredientKind = "retriever"
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
	StepRAG       StepKind = "rag"
	StepRouter    StepKind = "router"     // R8: route to exactly one of N branches
	StepMap       StepKind = "map"        // R8: iterate over array, run agent per item
	StepSubRecipe StepKind = "sub_recipe" // R8: invoke another recipe as a sub-workflow
)

type Branch struct {
	Condition string `json:"condition"`
	NextStep  string `json:"next_step"`
}

type Step struct {
	Name        string                 `json:"name"`
	Kind        StepKind               `json:"kind"`
	AgentRef    string                 `json:"agent_ref,omitempty"`
	DependsOn   []string               `json:"depends_on,omitempty"`
	Config      map[string]interface{} `json:"config,omitempty"`
	MaxRetries  int                    `json:"max_retries,omitempty"`
	TimeoutSecs int                    `json:"timeout_secs,omitempty"`

	// Branching — dynamic routing after step completion.
	// Each Branch.Condition is evaluated against the step's output JSON
	// using expr-lang/expr. First matching branch's NextStep is activated.
	// If no branch matches, DefaultNext is used. If neither, falls through
	// to dependency-based resolution (backward compatible).
	Branches    []Branch `json:"branches,omitempty"`
	DefaultNext string   `json:"default_next,omitempty"`

	// Looping — repeat the step while LoopCondition evaluates to true.
	// LoopCondition is an expr-lang/expr expression evaluated against
	// the step's output. MaxIterations is a hard cap (required when
	// LoopCondition is set). Retry (MaxRetries) is inner per-attempt;
	// loop is outer per-iteration.
	LoopCondition string `json:"loop_condition,omitempty"`
	MaxIterations int    `json:"max_iterations,omitempty"`

	// Map / Iteration — run the referenced agent once per item in an
	// upstream step's output array. SourcePath is a dot-separated path
	// to the array field (e.g. "items" or "results.documents").
	// MaxConcurrency limits parallel invocations (0 = unlimited).
	SourcePath     string `json:"source_path,omitempty"`
	MaxConcurrency int    `json:"max_concurrency,omitempty"`

	// Sub-recipe — invoke another recipe as a nested workflow.
	// RecipeRef is the name of the sub-recipe. InputMapping and
	// OutputMapping map parent fields ↔ sub-recipe fields.
	RecipeRef     string            `json:"recipe_ref,omitempty"`
	InputMapping  map[string]string `json:"input_mapping,omitempty"`
	OutputMapping map[string]string `json:"output_mapping,omitempty"`

	// Notification — MCP tool names with "notify" capability to fire on
	// gate_waiting, step_completed, or step_failed events.
	NotifyTools []string `json:"notify_tools,omitempty"`
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
	Plan                    Plan              `json:"plan"`
	MaxAgents               int               `json:"max_agents"`
	MaxRecipes              int               `json:"max_recipes"`
	MaxProviders            int               `json:"max_providers"`
	MaxMCPTools             int               `json:"max_mcp_tools"`
	MaxPrompts              int               `json:"max_prompts"`
	TraceRetentionDays      int               `json:"trace_retention_days"`
	AllowedStrategies       []RoutingStrategy `json:"allowed_strategies"`
	CloudProviders          bool              `json:"cloud_providers"`           // Bedrock, Foundry, Vertex
	SSO                     bool              `json:"sso"`                       // SSO/SAML
	Federation              bool              `json:"federation"`                // Cross-org
	CustomDrivers           bool              `json:"custom_drivers"`            // Custom ProviderDrivers
	PromptValidation        bool              `json:"prompt_validation"`         // Pro prompt validator
	LLMJudge                bool              `json:"llm_judge"`                 // LLM-as-judge prompt checking
	MaxOutputRetentionDays  int               `json:"max_output_retention_days"` // Agent I/O retention
	MaxAuditRetentionDays   int               `json:"max_audit_retention_days"`  // Audit event retention
	RequireThinkingAudit    bool              `json:"require_thinking_audit"`    // Force thinking mode
	MaxGateWaitMinutes      int               `json:"max_gate_wait_minutes"`     // SLA for human gates
	MaxNotificationChannels int               `json:"max_notification_channels"` // Notification channel quota

	// RAG & Embeddings
	MaxEmbeddingProviders int  `json:"max_embedding_providers"` // Embedding provider quota
	MaxVectorStores       int  `json:"max_vector_stores"`       // Vector store backend quota
	DataConnectors        bool `json:"data_connectors"`         // Pro: data lake connectors
	RAGTemplates          bool `json:"rag_templates"`           // Pro: framework templates
	AgentMonitor          bool `json:"agent_monitor"`           // Pro: agent validator/monitor

	// Test Suites (Pro)
	MaxTestSuites         int  `json:"max_test_suites"`          // 0 = disabled (community)
	MaxConcurrentTestRuns int  `json:"max_concurrent_test_runs"` // max parallel runs per kitchen
	MaxTestCasesPerSuite  int  `json:"max_test_cases_per_suite"` // max cases in a single suite
	ScheduledTestRuns     bool `json:"scheduled_test_runs"`      // cron-based scheduled runs
	ExternalTestBackends  bool `json:"external_test_backends"`   // Celery, Temporal, K8s backends

	// Environments & Promotion (Pro)
	MaxEnvironments   int  `json:"max_environments"`   // 0 = disabled (community), 3 = pro default
	PromotionWorkflow bool `json:"promotion_workflow"` // stage gating with approval gates

	// Service Accounts (Pro)
	MaxServiceAccounts     int  `json:"max_service_accounts"`     // 0 = disabled (community)
	ManagedServiceAccounts bool `json:"managed_service_accounts"` // kitchen-scoped SA CRUD

	// External Trackers (Pro)
	ExternalTrackers   bool `json:"external_trackers"`   // JIRA, GitLab, GitHub, Azure DevOps
	TraceabilityMatrix bool `json:"traceability_matrix"` // requirement→test→agent traceability
}

// CommunityLimits returns the default PlanLimits for the free community tier.
func CommunityLimits() *PlanLimits {
	return &PlanLimits{
		Plan:                    PlanCommunity,
		MaxAgents:               10,
		MaxRecipes:              5,
		MaxProviders:            2,
		MaxMCPTools:             5,
		MaxPrompts:              10,
		TraceRetentionDays:      7,
		AllowedStrategies:       []RoutingStrategy{RoutingFallback},
		CloudProviders:          false,
		SSO:                     false,
		Federation:              false,
		CustomDrivers:           false,
		PromptValidation:        false,
		LLMJudge:                false,
		MaxOutputRetentionDays:  0, // no output retention in community
		MaxAuditRetentionDays:   0, // no audit in community
		RequireThinkingAudit:    false,
		MaxGateWaitMinutes:      0, // no SLA enforcement
		MaxNotificationChannels: 2,

		// RAG & Embeddings — generous community limits
		MaxEmbeddingProviders: 2,
		MaxVectorStores:       2,
		DataConnectors:        false, // Pro only
		RAGTemplates:          false, // Pro only
		AgentMonitor:          false, // Pro only

		// Test Suites — disabled in community
		MaxTestSuites:         0,     // disabled
		MaxConcurrentTestRuns: 0,     // disabled
		MaxTestCasesPerSuite:  0,     // disabled
		ScheduledTestRuns:     false, // Pro only
		ExternalTestBackends:  false, // Enterprise only

		// Environments — disabled in community
		MaxEnvironments:   0,     // disabled
		PromotionWorkflow: false, // Pro only

		// Service Accounts — disabled in community
		MaxServiceAccounts:     0,     // disabled
		ManagedServiceAccounts: false, // Pro only

		// External Trackers — disabled in community
		ExternalTrackers:   false, // Pro only
		TraceabilityMatrix: false, // Pro only
	}
}

// ── Server Info ──────────────────────────────────────────────

// ServerInfo is the response for GET /api/v1/info. It tells CLI clients
// what edition is running, which features are available, and what the
// plan limits are. The CLI uses this to gate Pro-only commands at runtime.
type ServerInfo struct {
	Service  string          `json:"service"`
	Version  string          `json:"version"`
	Edition  string          `json:"edition"` // "community", "pro", "enterprise"
	Plan     Plan            `json:"plan"`
	Org      string          `json:"org,omitempty"`
	Features *ServerFeatures `json:"features"`
	Limits   *PlanLimits     `json:"limits"`
	Auth     *ServerAuth     `json:"auth"`
	License  *LicenseInfo    `json:"license,omitempty"`
}

// ServerFeatures describes which capabilities the server supports.
// OSS returns all false; Pro populates based on the license.
type ServerFeatures struct {
	Environments    bool `json:"environments"`
	TestSuites      bool `json:"test_suites"`
	ServiceAccounts bool `json:"service_accounts"`
	ScopedKeys      bool `json:"scoped_keys"`
	SSO             bool `json:"sso"`
	Federation      bool `json:"federation"`
	CloudProviders  bool `json:"cloud_providers"`
	Audit           bool `json:"audit"`
	RAG             bool `json:"rag"`
	Guardrails      bool `json:"guardrails"`
	Promotions      bool `json:"promotions"`
	PhoneHome       bool `json:"phone_home"`
}

// ServerAuth describes the authentication configuration.
type ServerAuth struct {
	Providers   []string `json:"providers"`
	SSOEnabled  bool     `json:"sso_enabled"`
	RequireAuth bool     `json:"require_auth"`
}

// LicenseInfo provides license status (Pro/Enterprise only).
type LicenseInfo struct {
	Valid     bool   `json:"valid"`
	ExpiresAt string `json:"expires_at,omitempty"`
	Org       string `json:"org,omitempty"`
	Plan      Plan   `json:"plan"`
	LicenseID string `json:"license_id,omitempty"`
}

// CommunityServerInfo returns the default ServerInfo for OSS/Community.
func CommunityServerInfo(version string) *ServerInfo {
	return &ServerInfo{
		Service: "agentoven-control-plane",
		Version: version,
		Edition: "community",
		Plan:    PlanCommunity,
		Features: &ServerFeatures{
			Environments:    false,
			TestSuites:      false,
			ServiceAccounts: false,
			ScopedKeys:      true, // OSS has scoped keys
			SSO:             false,
			Federation:      false,
			CloudProviders:  false,
			Audit:           false,
			RAG:             true, // OSS has RAG
			Guardrails:      true, // OSS has community guardrails
			Promotions:      false,
			PhoneHome:       false,
		},
		Limits: CommunityLimits(),
		Auth: &ServerAuth{
			Providers:   []string{"apikey"},
			SSOEnabled:  false,
			RequireAuth: false,
		},
	}
}

// ── Trace ────────────────────────────────────────────────────

type Trace struct {
	ID             string                 `json:"id" db:"id"`
	AgentName      string                 `json:"agent_name" db:"agent_name"`
	RecipeName     string                 `json:"recipe_name,omitempty" db:"recipe_name"`
	Kitchen        string                 `json:"kitchen" db:"kitchen"`
	Status         string                 `json:"status" db:"status"`
	DurationMs     int64                  `json:"duration_ms" db:"duration_ms"`
	TotalTokens    int64                  `json:"total_tokens" db:"total_tokens"`
	CostUSD        float64                `json:"cost_usd" db:"cost_usd"`
	Metadata       map[string]interface{} `json:"metadata,omitempty"`
	UserID         string                 `json:"user_id,omitempty" db:"user_id"`
	OutputText     string                 `json:"output_text,omitempty" db:"output_text"`
	ThinkingBlocks []ThinkingBlock        `json:"thinking_blocks,omitempty"`
	CreatedAt      time.Time              `json:"created_at" db:"created_at"`

	// ── Enriched fields (R10 — LangSmith-parity tracing) ─────
	InputText       string                 `json:"input_text,omitempty" db:"input_text"`
	ParentTraceID   string                 `json:"parent_trace_id,omitempty" db:"parent_trace_id"`
	SessionID       string                 `json:"session_id,omitempty" db:"session_id"`
	Tags            []string               `json:"tags,omitempty"`
	Usage           *TokenUsage            `json:"usage,omitempty"`
	RuntimeMetadata map[string]interface{} `json:"runtime_metadata,omitempty"`

	// Spans are loaded eagerly by GetTrace (with spans=true) or ListSpansByTrace.
	// Not persisted on the Trace row itself — stored in the spans table/map.
	Spans []Span `json:"spans,omitempty" db:"-"`
}

// ── Span ─────────────────────────────────────────────────────

// SpanKind categorizes what a span represents in the execution tree.
type SpanKind string

const (
	SpanKindAgent     SpanKind = "agent"     // top-level agent invocation
	SpanKindLLM       SpanKind = "llm"       // a single LLM call
	SpanKindTool      SpanKind = "tool"      // a tool/function call execution
	SpanKindRetriever SpanKind = "retriever" // RAG retrieval operation
	SpanKindChain     SpanKind = "chain"     // a chain/pipeline step
	SpanKindEmbedding SpanKind = "embedding" // an embedding operation
)

// Span represents a single operation within a trace — analogous to an OTEL span.
// Spans form a tree: the root span has no ParentSpanID, child spans reference their parent.
// This enables LangSmith-style waterfall visualization of agent execution.
type Span struct {
	ID           string                 `json:"id" db:"id"`
	TraceID      string                 `json:"trace_id" db:"trace_id"`
	ParentSpanID string                 `json:"parent_span_id,omitempty" db:"parent_span_id"`
	Name         string                 `json:"name" db:"name"`
	Kind         SpanKind               `json:"kind" db:"kind"`
	Status       string                 `json:"status" db:"status"` // "running", "completed", "failed"
	StartTime    time.Time              `json:"start_time" db:"start_time"`
	EndTime      time.Time              `json:"end_time" db:"end_time"`
	DurationMs   int64                  `json:"duration_ms" db:"duration_ms"`
	Input        json.RawMessage        `json:"input,omitempty" db:"input"`   // request payload (messages, tool args)
	Output       json.RawMessage        `json:"output,omitempty" db:"output"` // response payload (content, tool result)
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
	Usage        *TokenUsage            `json:"usage,omitempty"`                  // token breakdown for LLM spans
	Model        string                 `json:"model,omitempty" db:"model"`       // model name (LLM spans)
	Provider     string                 `json:"provider,omitempty" db:"provider"` // provider name (LLM spans)
	Error        string                 `json:"error,omitempty" db:"error"`       // error message if status=failed
	Events       []SpanEvent            `json:"events,omitempty"`                 // timed events within the span
}

// SpanEvent is a timestamped event within a span (e.g. "first token", "tool selected").
type SpanEvent struct {
	Name       string                 `json:"name"`
	Timestamp  time.Time              `json:"timestamp"`
	Attributes map[string]interface{} `json:"attributes,omitempty"`
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

	// API Key Rotation — multiple keys with automatic rotation
	APIKeys          []APIKeyEntry `json:"api_keys,omitempty"`
	RotationStrategy string        `json:"rotation_strategy,omitempty" db:"rotation_strategy"` // "round-robin", "random", "weighted"

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
	RecipeRunSubmitted RecipeRunStatus = "submitted"
	RecipeRunRunning   RecipeRunStatus = "running"
	RecipeRunCompleted RecipeRunStatus = "completed"
	RecipeRunFailed    RecipeRunStatus = "failed"
	RecipeRunCanceled  RecipeRunStatus = "canceled"
	RecipeRunPaused    RecipeRunStatus = "paused"
)

type RecipeRun struct {
	ID           string                 `json:"id" db:"id"`
	RecipeID     string                 `json:"recipe_id" db:"recipe_id"`
	Kitchen      string                 `json:"kitchen" db:"kitchen"`
	Status       RecipeRunStatus        `json:"status" db:"status"`
	Input        map[string]interface{} `json:"input,omitempty"`
	Output       map[string]interface{} `json:"output,omitempty"`
	StepResults  []StepResult           `json:"step_results,omitempty"`
	ParentRunID  string                 `json:"parent_run_id,omitempty" db:"parent_run_id"` // R8: set when this run is a sub-recipe invocation
	StartedAt    time.Time              `json:"started_at" db:"started_at"`
	CompletedAt  *time.Time             `json:"completed_at,omitempty" db:"completed_at"`
	DurationMs   int64                  `json:"duration_ms,omitempty" db:"duration_ms"`
	TotalTokens  int64                  `json:"total_tokens,omitempty" db:"total_tokens"`
	TotalCostUSD float64                `json:"total_cost_usd,omitempty" db:"total_cost_usd"`
	Error        string                 `json:"error,omitempty" db:"error"`
}

type StepResult struct {
	StepName      string                 `json:"step_name"`
	StepKind      string                 `json:"step_kind"`
	Status        string                 `json:"status"` // completed, failed, skipped, canceled
	Output        map[string]interface{} `json:"output,omitempty"`
	AgentRef      string                 `json:"agent_ref,omitempty"`
	StartedAt     time.Time              `json:"started_at"`
	DurationMs    int64                  `json:"duration_ms"`
	Error         string                 `json:"error,omitempty"`
	Tokens        int64                  `json:"tokens,omitempty"`
	CostUSD       float64                `json:"cost_usd,omitempty"`
	GateStatus    string                 `json:"gate_status,omitempty"` // waiting, approved, rejected
	NotifyResults []NotifyResult         `json:"notify_results,omitempty"`
	BranchTaken   string                 `json:"branch_taken,omitempty"` // which branch condition matched

	// R8: Map/Iteration results — one sub-result per item processed.
	SubResults []StepResult `json:"sub_results,omitempty"`
	ItemCount  int          `json:"item_count,omitempty"`

	// R8: Loop results — how many iterations ran + output history.
	LoopIterations int                      `json:"loop_iterations,omitempty"`
	LoopHistory    []map[string]interface{} `json:"loop_history,omitempty"`

	// R8: Sub-recipe run ID for traceability.
	SubRunID string `json:"sub_run_id,omitempty"`
}

type NotifyResult struct {
	Tool      string    `json:"tool"`
	Success   bool      `json:"success"`
	Error     string    `json:"error,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// ── MCP Tool ─────────────────────────────────────────────────

type MCPTool struct {
	ID           string                 `json:"id" db:"id"`
	Name         string                 `json:"name" db:"name"`
	Description  string                 `json:"description" db:"description"`
	Kitchen      string                 `json:"kitchen" db:"kitchen"`
	Endpoint     string                 `json:"endpoint" db:"endpoint"`
	Transport    string                 `json:"transport" db:"transport"` // http, sse, stdio
	Schema       map[string]interface{} `json:"schema,omitempty"`
	AuthConfig   map[string]interface{} `json:"auth_config,omitempty"`
	Capabilities []string               `json:"capabilities"` // ["tool"], ["notify"], ["tool","notify"]
	Enabled      bool                   `json:"enabled" db:"enabled"`
	CreatedAt    time.Time              `json:"created_at" db:"created_at"`
	UpdatedAt    time.Time              `json:"updated_at" db:"updated_at"`
}

// ── MCP Upstream (ADR-0014) ──────────────────────────────────
// MCPUpstream represents a Microsoft MCP Gateway (or compatible) upstream
// server that proxies tool calls to containerized MCP servers. This is a
// model-only definition for future use — no endpoints or handlers yet.
// See ADR/0014-microsoft-mcp-gateway-upstream.md for architecture details.

type MCPUpstream struct {
	ID        string            `json:"id" db:"id"`
	Name      string            `json:"name" db:"name"`
	Kitchen   string            `json:"kitchen" db:"kitchen"`
	Endpoint  string            `json:"endpoint" db:"endpoint"`           // e.g., "https://mcp-gw.internal:8080"
	Transport string            `json:"transport" db:"transport"`         // "http" or "sse"
	AuthType  string            `json:"auth_type" db:"auth_type"`         // "none", "bearer", "entra-id", "api-key"
	AuthRef   string            `json:"auth_ref,omitempty" db:"auth_ref"` // reference to secret/provider
	Labels    map[string]string `json:"labels,omitempty"`                 // routing labels
	Enabled   bool              `json:"enabled" db:"enabled"`
	CreatedAt time.Time         `json:"created_at" db:"created_at"`
	UpdatedAt time.Time         `json:"updated_at" db:"updated_at"`
}

// ── Model Routing ────────────────────────────────────────────

type RoutingStrategy string

const (
	RoutingFallback         RoutingStrategy = "fallback"
	RoutingCostOptimized    RoutingStrategy = "cost-optimized"
	RoutingLatencyOptimized RoutingStrategy = "latency-optimized"
	RoutingRoundRobin       RoutingStrategy = "round-robin"
)

type RouteRequest struct {
	Messages []ChatMessage   `json:"messages"`
	Model    string          `json:"model,omitempty"`
	Strategy RoutingStrategy `json:"strategy,omitempty"`
	Kitchen  string          `json:"kitchen,omitempty"`
	AgentRef string          `json:"agent_ref,omitempty"`

	// ── Model Parameters (R8) ───────────────────────────────
	// These fields let callers control model behavior. Drivers map them
	// to provider-specific request fields using the catalog data.
	Temperature    *float64        `json:"temperature,omitempty"`
	MaxTokens      *int            `json:"max_tokens,omitempty"`
	TopP           *float64        `json:"top_p,omitempty"`
	StopSequences  []string        `json:"stop,omitempty"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
	Stream         bool            `json:"stream,omitempty"`

	// ── Tool Calling (R8) ────────────────────────────────────
	Tools      []ToolDefinition `json:"tools,omitempty"`
	ToolChoice interface{}      `json:"tool_choice,omitempty"` // "auto", "none", "required", or {type,function}

	// ── Session/Conversation (R8) ────────────────────────────
	SessionID string `json:"session_id,omitempty"` // conversation context

	// ── Thinking / Extended Reasoning (R10) ──────────────────
	ThinkingEnabled bool `json:"thinking_enabled,omitempty"` // enable extended thinking (o-series, Claude)
	ThinkingBudget  int  `json:"thinking_budget,omitempty"`  // max thinking tokens (0 = model default)

	// ── Prompt Caching (R10) ─────────────────────────────────
	// When true, the router marks system prompts and stable context prefix
	// with cache-control hints. Anthropic uses explicit "ephemeral" breakpoints;
	// OpenAI uses automatic prefix caching. Reduces costs on repeated prompts.
	EnableCaching bool `json:"enable_caching,omitempty"`
}

// ResponseFormat specifies structured output from the LLM.
type ResponseFormat struct {
	Type       string                 `json:"type"`                  // "text", "json_object", "json_schema"
	JSONSchema map[string]interface{} `json:"json_schema,omitempty"` // for type=json_schema
}

// ToolDefinition describes a tool the LLM can call.
type ToolDefinition struct {
	Type     string       `json:"type"` // "function"
	Function ToolFunction `json:"function"`
}

// ToolFunction describes a callable function for tool-use.
type ToolFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"` // JSON Schema
}

// ToolCallResult is a structured tool call returned by the LLM.
type ToolCallResult struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // "function"
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON string
	} `json:"function"`
}

// ContentPart represents one piece of a multi-part message (text, image, tool_use, tool_result).
type ContentPart struct {
	Type       string                 `json:"type"` // "text", "image_url", "tool_use", "tool_result"
	Text       string                 `json:"text,omitempty"`
	ImageURL   *ImageURL              `json:"image_url,omitempty"`    // for type=image_url
	ToolUseID  string                 `json:"tool_use_id,omitempty"`  // for type=tool_result
	Content    string                 `json:"content,omitempty"`      // for type=tool_result (text content)
	ToolCallID string                 `json:"tool_call_id,omitempty"` // for tool results
	Extra      map[string]interface{} `json:"extra,omitempty"`        // provider-specific extensions
}

// ImageURL describes an image for vision-capable models.
type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"` // "auto", "low", "high"
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`

	// ── Rich Content (R8) ────────────────────────────────────
	// When ContentParts is set, Content may be empty. Drivers use ContentParts
	// for multi-modal messages (text + images) and tool call/results.
	ContentParts []ContentPart    `json:"content_parts,omitempty"`
	ToolCalls    []ToolCallResult `json:"tool_calls,omitempty"`   // assistant messages with tool calls
	ToolCallID   string           `json:"tool_call_id,omitempty"` // tool result messages
	Name         string           `json:"name,omitempty"`         // function name for tool messages

	// ── Prompt Caching (R10) ─────────────────────────────────
	// CacheControl signals the provider to cache this message's content.
	// Supported: Anthropic ("ephemeral"), OpenAI (auto-prefix caching).
	// Drivers that don't support caching ignore this field.
	CacheControl string `json:"cache_control,omitempty"` // "ephemeral" for Anthropic cache breakpoint
}

type RouteResponse struct {
	ID             string          `json:"id"`
	Provider       string          `json:"provider"`
	Model          string          `json:"model"`
	Strategy       RoutingStrategy `json:"strategy"`
	Content        string          `json:"content"`
	ThinkingBlocks []ThinkingBlock `json:"thinking_blocks,omitempty"`
	Usage          TokenUsage      `json:"usage"`
	LatencyMs      int64           `json:"latency_ms"`
	Cached         bool            `json:"cached"`

	// ── Enriched Response (R8) ───────────────────────────────
	FinishReason string           `json:"finish_reason,omitempty"` // "stop", "tool_calls", "length", "content_filter"
	ToolCalls    []ToolCallResult `json:"tool_calls,omitempty"`    // structured tool calls from LLM
	ContentParts []ContentPart    `json:"content_parts,omitempty"` // multi-part response
}

type TokenUsage struct {
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	ThinkingTokens  int64   `json:"thinking_tokens,omitempty"`
	TotalTokens     int64   `json:"total_tokens"`
	EstimatedCost   float64 `json:"estimated_cost_usd"`
	CacheHits       int     `json:"cache_hits,omitempty"`        // prompt-cache hits from provider
	CachedTokens    int64   `json:"cached_tokens,omitempty"`     // tokens served from cache
	CacheCreation   int64   `json:"cache_creation,omitempty"`    // tokens written to cache this turn
	CacheSavingsUSD float64 `json:"cache_savings_usd,omitempty"` // estimated $ saved via caching
}

type CostSummary struct {
	TotalCostUSD float64            `json:"total_cost_usd"`
	TotalTokens  int64              `json:"total_tokens"`
	Period       string             `json:"period"`
	ByAgent      map[string]float64 `json:"by_agent"`
	ByModel      map[string]float64 `json:"by_model"`
	ByProvider   map[string]float64 `json:"by_provider"`
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

// ── Prompt Store ─────────────────────────────────────────────

type Prompt struct {
	ID        string    `json:"id" db:"id"`
	Name      string    `json:"name" db:"name"`
	Version   int       `json:"version" db:"version"`
	Template  string    `json:"template" db:"template"`
	Variables []string  `json:"variables"` // extracted from template {{var}} placeholders
	Kitchen   string    `json:"kitchen" db:"kitchen"`
	Tags      []string  `json:"tags,omitempty"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

// ── Prompt Validation ────────────────────────────────────────

type ValidationSeverity string

const (
	ValidationError   ValidationSeverity = "error"
	ValidationWarning ValidationSeverity = "warning"
	ValidationInfo    ValidationSeverity = "info"
)

type ValidationIssue struct {
	Severity   ValidationSeverity `json:"severity"`
	Category   string             `json:"category"` // injection, structure, compliance, security
	Message    string             `json:"message"`
	Line       int                `json:"line,omitempty"`
	Position   int                `json:"position,omitempty"`
	Suggestion string             `json:"suggestion,omitempty"` // suggested fix
}

type ValidationReport struct {
	PromptName  string            `json:"prompt_name"`
	Version     int               `json:"version"`
	Score       int               `json:"score"` // 0-100 security/quality score
	Issues      []ValidationIssue `json:"issues"`
	TokenCount  int               `json:"token_count,omitempty"`
	ModelCompat map[string]bool   `json:"model_compat,omitempty"` // model -> compatible
	LLMAnalysis string            `json:"llm_analysis,omitempty"` // Pro: LLM-as-judge summary
	ValidatedAt time.Time         `json:"validated_at"`
	ValidatedBy string            `json:"validated_by"` // "community" or "pro"
}

// ── Kitchen Settings ─────────────────────────────────────────

// KitchenSettings stores per-kitchen configuration including API keys
// for validation services, LLM-based checks, and compliance rules.
type KitchenSettings struct {
	KitchenID string `json:"kitchen_id" db:"kitchen_id"`

	// Validation LLM — the model used for LLM-as-judge prompt checks (Pro)
	ValidationProvider string `json:"validation_provider,omitempty"` // e.g. "openai", "anthropic"
	ValidationModel    string `json:"validation_model,omitempty"`    // e.g. "gpt-4o-mini"
	ValidationAPIKey   string `json:"validation_api_key,omitempty"`  // API key for validation LLM
	ValidationEndpoint string `json:"validation_endpoint,omitempty"` // custom endpoint (Azure OpenAI, etc.)

	// Compliance deny-list — custom blocked phrases/patterns
	DenyPatterns []string `json:"deny_patterns,omitempty"`

	// Max template size in characters (0 = unlimited)
	MaxTemplateSize int `json:"max_template_size,omitempty"`

	// Auto-validate prompts on create/update
	AutoValidate bool `json:"auto_validate"`

	// Require approval for prompts with validation errors (Pro)
	RequireApproval bool `json:"require_approval,omitempty"`

	// Require thinking/reasoning audit on all LLM calls (Pro)
	// When enabled, Anthropic extended thinking and OpenAI reasoning are forced,
	// and responses without thinking blocks are flagged for compliance review.
	RequireThinkingAudit bool `json:"require_thinking_audit,omitempty"`

	// Retention overrides — per-kitchen override for trace and audit retention.
	// 0 = use the plan default. Values are in days.
	MaxOutputRetentionDays int `json:"max_output_retention_days,omitempty"`
	MaxAuditRetentionDays  int `json:"max_audit_retention_days,omitempty"`

	// Archive policy — controls what happens to expired data.
	// nil = use default (purge-only for community, archive-and-purge for pro).
	ArchivePolicy *ArchivePolicy `json:"archive_policy,omitempty"`

	// ── Test Runner Configuration (Pro) ─────────────────────
	// Controls how test suite runs are dispatched and executed.
	// Community edition: test suites are disabled (no-op backend).

	// TestRunnerBackend selects the execution backend for this kitchen.
	// Supported: "local" (bounded goroutine pool, default), "celery", "temporal", "k8s".
	TestRunnerBackend string `json:"test_runner_backend,omitempty"`

	// TestRunnerConfig holds backend-specific settings.
	// For "celery": {"broker_url": "redis://...", "queue": "tests"}
	// For "temporal": {"namespace": "default", "task_queue": "agent-tests"}
	// For "k8s": {"namespace": "agentoven", "image": "agentoven/runner:latest"}
	TestRunnerConfig map[string]string `json:"test_runner_config,omitempty"`

	// MaxConcurrentRuns overrides the plan-level limit per kitchen.
	// 0 = use plan default.
	MaxConcurrentRuns int `json:"max_concurrent_runs,omitempty"`

	// ── External Tracker Configuration (Pro) ────────────────
	// Connects test suites to an external issue tracker (JIRA, GitLab, etc.)
	// for automatic result sync and traceability matrix generation.
	ExternalTracker *ExternalTrackerConfig `json:"external_tracker,omitempty"`

	// Custom metadata
	Metadata map[string]string `json:"metadata,omitempty"`

	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

// ── Agent Config (Resolved Ingredients) ──────────────────────

// ResolvedIngredients is the output of ingredient resolution at bake/invoke time.
type ResolvedIngredients struct {
	Model        *ResolvedModel        `json:"model,omitempty"`
	Tools        []ResolvedTool        `json:"tools,omitempty"`
	Prompt       *ResolvedPrompt       `json:"prompt,omitempty"`
	Data         []ResolvedData        `json:"data,omitempty"`
	Embeddings   []ResolvedEmbedding   `json:"embeddings,omitempty"`
	VectorStores []ResolvedVectorStore `json:"vector_stores,omitempty"`
	Retrievers   []ResolvedRetriever   `json:"retrievers,omitempty"`
}

type ResolvedModel struct {
	Provider string                 `json:"provider"`
	Kind     string                 `json:"kind"`
	Model    string                 `json:"model"`
	Endpoint string                 `json:"endpoint"`
	APIKey   string                 `json:"api_key"`
	Config   map[string]interface{} `json:"config,omitempty"`
}

type ResolvedTool struct {
	Name       string                 `json:"name"`
	Endpoint   string                 `json:"endpoint"`
	Transport  string                 `json:"transport"`
	Schema     map[string]interface{} `json:"schema,omitempty"`
	Version    string                 `json:"version,omitempty"`     // pinned at bake time
	SchemaHash string                 `json:"schema_hash,omitempty"` // SHA-256 of schema at bake time
	BakedAt    time.Time              `json:"baked_at,omitempty"`    // when this tool was resolved
}

type ResolvedPrompt struct {
	Name     string `json:"name"`
	Version  int    `json:"version"`
	Template string `json:"template"`
	Rendered string `json:"rendered"`
}

type ResolvedData struct {
	Name   string                 `json:"name"`
	URI    string                 `json:"uri"`
	Config map[string]interface{} `json:"config,omitempty"`
}

// ResolvedEmbedding is the output of resolving an "embedding" ingredient.
type ResolvedEmbedding struct {
	Provider       string                 `json:"provider"`        // provider kind: "openai", "ollama", "azure-openai"
	ProviderName   string                 `json:"provider_name"`   // user-supplied provider name (ISS-009)
	Model          string                 `json:"model"`           // "text-embedding-3-small"
	Dimensions     int                    `json:"dimensions"`      // 1536, 3072, etc.
	BatchSize      int                    `json:"batch_size"`      // max texts per embed call
	DistanceMetric string                 `json:"distance_metric"` // "cosine", "euclidean", "dot"
	Endpoint       string                 `json:"endpoint,omitempty"`
	APIKey         string                 `json:"api_key,omitempty"`
	Config         map[string]interface{} `json:"config,omitempty"`
}

// VectorStoreBackend identifies a vector store implementation.
type VectorStoreBackend string

const (
	VectorStoreEmbedded   VectorStoreBackend = "embedded"   // in-memory brute-force (OSS default)
	VectorStorePGVector   VectorStoreBackend = "pgvector"   // user-provided PostgreSQL + pgvector
	VectorStorePinecone   VectorStoreBackend = "pinecone"   // Pro
	VectorStoreQdrant     VectorStoreBackend = "qdrant"     // Pro
	VectorStoreCosmosDB   VectorStoreBackend = "cosmosdb"   // Pro
	VectorStoreChroma     VectorStoreBackend = "chroma"     // OSS
	VectorStoreSnowflake  VectorStoreBackend = "snowflake"  // Pro (Cortex Search)
	VectorStoreDatabricks VectorStoreBackend = "databricks" // Pro (Vector Search)
)

// ResolvedVectorStore is the output of resolving a "vectorstore" ingredient.
type ResolvedVectorStore struct {
	Backend    VectorStoreBackend     `json:"backend"`
	Index      string                 `json:"index"`               // index/collection name
	Namespace  string                 `json:"namespace,omitempty"` // optional sub-partition
	Dimensions int                    `json:"dimensions"`
	Config     map[string]interface{} `json:"config,omitempty"` // backend-specific config
}

// ResolvedRetriever is the output of resolving a "retriever" ingredient.
type ResolvedRetriever struct {
	EmbeddingRef   string  `json:"embedding_ref"`             // ingredient ID of the embedding
	VectorStoreRef string  `json:"vectorstore_ref"`           // ingredient ID of the vector store
	TopK           int     `json:"top_k"`                     // number of results
	ScoreThreshold float64 `json:"score_threshold,omitempty"` // min similarity score
	RerankStrategy string  `json:"rerank_strategy,omitempty"` // "none", "cross-encoder", "llm"
	HybridSearch   bool    `json:"hybrid_search,omitempty"`   // combine dense + sparse
}

// ── RAG ───────────────────────────────────────────────────────

// RAGStrategy identifies a RAG pipeline pattern.
type RAGStrategy string

const (
	RAGNaive          RAGStrategy = "naive"           // embed query → search → stuff → LLM
	RAGSentenceWindow RAGStrategy = "sentence-window" // retrieve chunks + surrounding context
	RAGParentDocument RAGStrategy = "parent-document" // retrieve children, return parents
	RAGHyDE           RAGStrategy = "hyde"            // LLM generates hypothetical answer → embed → search
	RAGAgentic        RAGStrategy = "agentic"         // agent decides when/what to retrieve
)

// RAGQueryRequest is the input to a RAG query endpoint.
type RAGQueryRequest struct {
	Kitchen   string            `json:"kitchen"`
	Question  string            `json:"question"`
	Strategy  RAGStrategy       `json:"strategy,omitempty"`  // default: naive
	TopK      int               `json:"top_k,omitempty"`     // default: 5
	MinScore  float64           `json:"min_score,omitempty"` // default: 0.0
	Namespace string            `json:"namespace,omitempty"` // optional partition filter
	Filter    map[string]string `json:"filter,omitempty"`    // metadata filters
}

// RAGQueryResult is the output of a RAG query.
type RAGQueryResult struct {
	Answer          string         `json:"answer"`
	Sources         []SearchResult `json:"sources"`
	Strategy        RAGStrategy    `json:"strategy"`
	ChunksRetrieved int            `json:"chunks_retrieved"`
	TokensUsed      int64          `json:"tokens_used"`
	LatencyMs       int64          `json:"latency_ms"`
}

// RAGIngestRequest is the input to the document ingestion endpoint.
type RAGIngestRequest struct {
	Kitchen           string        `json:"kitchen"`
	Documents         []RawDocument `json:"documents"`
	ChunkSize         int           `json:"chunk_size,omitempty"`         // default: 512 tokens
	ChunkOverlap      int           `json:"chunk_overlap,omitempty"`      // default: 50 tokens
	EmbeddingProvider string        `json:"embedding_provider,omitempty"` // which registered driver
	Namespace         string        `json:"namespace,omitempty"`
}

// RawDocument is a single document to ingest into a RAG pipeline.
type RawDocument struct {
	ID       string            `json:"id,omitempty"`
	Content  string            `json:"content"`
	Metadata map[string]string `json:"metadata,omitempty"`
	MIMEType string            `json:"mime_type,omitempty"` // text/plain, application/pdf, etc.
}

// RAGIngestResult is the output of document ingestion.
type RAGIngestResult struct {
	DocumentsProcessed int   `json:"documents_processed"`
	ChunksCreated      int   `json:"chunks_created"`
	VectorsStored      int   `json:"vectors_stored"`
	LatencyMs          int64 `json:"latency_ms"`
}

// VectorDoc is a document stored in the vector index.
type VectorDoc struct {
	ID        string            `json:"id"`
	Kitchen   string            `json:"kitchen"`
	Content   string            `json:"content"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Vector    []float64         `json:"vector"`
	Namespace string            `json:"namespace,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

// SearchResult is a single vector search result.
type SearchResult struct {
	Doc   VectorDoc `json:"doc"`
	Score float64   `json:"score"`
}

// ── Data Connectors (Pro) ────────────────────────────────────

// DataConnectorKind identifies a data source connector type.
type DataConnectorKind string

const (
	ConnectorSnowflake  DataConnectorKind = "snowflake"
	ConnectorDatabricks DataConnectorKind = "databricks"
	ConnectorS3         DataConnectorKind = "s3"
	ConnectorADLS       DataConnectorKind = "adls"
	ConnectorGCS        DataConnectorKind = "gcs"
)

// DataConnectorConfig holds configuration for a data lake connector.
type DataConnectorConfig struct {
	ID               string                 `json:"id"`
	Name             string                 `json:"name"`
	Kind             DataConnectorKind      `json:"kind"`
	Kitchen          string                 `json:"kitchen"`
	ConnectionConfig map[string]interface{} `json:"connection_config"`          // host, warehouse, database, etc.
	Credentials      map[string]string      `json:"credentials,omitempty"`      // encrypted at rest in PG
	Query            string                 `json:"query,omitempty"`            // SQL query or table name
	RefreshInterval  string                 `json:"refresh_interval,omitempty"` // cron expression
	Active           bool                   `json:"active"`
	LastSyncAt       *time.Time             `json:"last_sync_at,omitempty"`
	LastSyncError    string                 `json:"last_sync_error,omitempty"`
	CreatedAt        time.Time              `json:"created_at"`
	UpdatedAt        time.Time              `json:"updated_at"`
}

// AgentConfig is the full resolved configuration returned by GET /agents/{name}/config.
type AgentConfig struct {
	Agent       Agent               `json:"agent"`
	Ingredients ResolvedIngredients `json:"ingredients"`
}

// ── Streaming ────────────────────────────────────────────────

// StreamChunk is a single token/event from a streaming model response.
type StreamChunk struct {
	Content         string         `json:"content,omitempty"`          // text content token
	ThinkingContent string         `json:"thinking_content,omitempty"` // thinking/reasoning token
	ToolCall        *ToolCallChunk `json:"tool_call,omitempty"`        // partial tool call
	Done            bool           `json:"done"`                       // stream complete
	Error           string         `json:"error,omitempty"`            // stream error
	Usage           *TokenUsage    `json:"usage,omitempty"`            // final usage (on done)
}

// ToolCallChunk is a partial tool call from a streaming response.
type ToolCallChunk struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"` // partial JSON string
}

// ── Thinking Blocks (Audit) ──────────────────────────────────

// ThinkingBlock captures an LLM's reasoning/chain-of-thought output.
// Stored in traces for compliance audit (FDA 21 CFR Part 11, FINRA model explainability).
type ThinkingBlock struct {
	Content    string    `json:"content"`
	TokenCount int64     `json:"token_count"`
	Model      string    `json:"model"`
	Provider   string    `json:"provider"`
	Timestamp  time.Time `json:"timestamp"`
}

// ── Audit Events ─────────────────────────────────────────────

// AuditEvent represents an auditable action for compliance tracking.
type AuditEvent struct {
	ID                 string                 `json:"id" db:"id"`
	Timestamp          time.Time              `json:"timestamp" db:"timestamp"`
	UserID             string                 `json:"user_id" db:"user_id"`
	UserEmail          string                 `json:"user_email" db:"user_email"`
	Action             string                 `json:"action" db:"action"`
	Resource           string                 `json:"resource" db:"resource"`
	ResourceID         string                 `json:"resource_id,omitempty" db:"resource_id"`
	Kitchen            string                 `json:"kitchen" db:"kitchen"`
	Details            map[string]interface{} `json:"details,omitempty"`
	IP                 string                 `json:"ip" db:"ip"`
	UserAgent          string                 `json:"user_agent" db:"user_agent"`
	ResponseStatus     int                    `json:"response_status" db:"response_status"`
	RegulationTags     []string               `json:"regulation_tags,omitempty"`     // HIPAA, SOC2, GxP, GDPR
	DataClassification string                 `json:"data_classification,omitempty"` // public, internal, confidential, restricted
}

// AuditFilter provides query options for listing audit events.
type AuditFilter struct {
	Kitchen  string
	UserID   string
	Action   string
	Resource string
	Since    *time.Time
	Until    *time.Time
	Limit    int
	Offset   int
}

// ── Approval Records ─────────────────────────────────────────

// ApprovalRecord captures the full metadata of a human gate decision.
// Durable — survives server restarts. Required for compliance audit trails.
type ApprovalRecord struct {
	ID              string                 `json:"id" db:"id"`
	GateKey         string                 `json:"gate_key" db:"gate_key"` // runID:stepName
	RunID           string                 `json:"run_id" db:"run_id"`
	StepName        string                 `json:"step_name" db:"step_name"`
	Kitchen         string                 `json:"kitchen" db:"kitchen"`
	Status          string                 `json:"status" db:"status"` // waiting, approved, rejected, expired
	ApproverID      string                 `json:"approver_id,omitempty" db:"approver_id"`
	ApproverEmail   string                 `json:"approver_email,omitempty" db:"approver_email"`
	ApproverChannel string                 `json:"approver_channel,omitempty" db:"approver_channel"` // api, slack, teams, email
	Comments        string                 `json:"comments,omitempty" db:"comments"`
	Metadata        map[string]interface{} `json:"metadata,omitempty"`
	RequestedAt     time.Time              `json:"requested_at" db:"requested_at"`
	ResolvedAt      *time.Time             `json:"resolved_at,omitempty" db:"resolved_at"`
	MaxWaitMinutes  int                    `json:"max_wait_minutes,omitempty" db:"max_wait_minutes"`
}

// ── Notification Channels ────────────────────────────────────

// ChannelKind identifies a notification channel type.
type ChannelKind string

const (
	ChannelWebhook ChannelKind = "webhook"
	ChannelSlack   ChannelKind = "slack"
	ChannelTeams   ChannelKind = "teams"
	ChannelDiscord ChannelKind = "discord"
	ChannelEmail   ChannelKind = "email"
	ChannelZapier  ChannelKind = "zapier"
)

// NotificationChannel represents a configured notification endpoint.
type NotificationChannel struct {
	ID        string                 `json:"id" db:"id"`
	Name      string                 `json:"name" db:"name"`
	Kind      ChannelKind            `json:"kind" db:"kind"`
	Kitchen   string                 `json:"kitchen" db:"kitchen"`
	URL       string                 `json:"url,omitempty" db:"url"`       // webhook/slack/teams/discord URL
	Secret    string                 `json:"secret,omitempty" db:"secret"` // HMAC signing secret (webhook)
	Config    map[string]interface{} `json:"config,omitempty"`             // kind-specific config
	Events    []string               `json:"events"`                       // events to subscribe to
	Active    bool                   `json:"active" db:"active"`
	CreatedAt time.Time              `json:"created_at" db:"created_at"`
	UpdatedAt time.Time              `json:"updated_at" db:"updated_at"`
}

// ── Archive ──────────────────────────────────────────────────

// ArchiveMode controls what happens to expired data during retention cleanup.
type ArchiveMode string

const (
	// ArchiveModeNone purges expired data without archiving (default for community).
	ArchiveModeNone ArchiveMode = "none"

	// ArchiveModeArchiveAndPurge archives expired data then deletes from hot store.
	ArchiveModeArchiveAndPurge ArchiveMode = "archive-and-purge"

	// ArchiveModeArchiveOnly archives expired data but keeps it in hot store.
	// Useful during migration/validation — "prove the archive works before I trust it."
	ArchiveModeArchiveOnly ArchiveMode = "archive-only"

	// ArchiveModePurgeOnly deletes expired data without archiving (explicit opt-in).
	ArchiveModePurgeOnly ArchiveMode = "purge-only"
)

// ArchivePolicy configures how a kitchen's expired data is handled.
// Stored as part of KitchenSettings.
type ArchivePolicy struct {
	// Mode controls archive behavior (none, archive-and-purge, archive-only, purge-only).
	Mode ArchiveMode `json:"mode"`

	// Backend is the archive driver kind to use ("local", "s3", "azure-blob", "gcs").
	// Must match a registered ArchiveDriver.Kind().
	Backend string `json:"backend,omitempty"`

	// Config holds backend-specific configuration.
	// S3:         {"bucket": "...", "prefix": "...", "region": "..."}
	// Azure Blob: {"container": "...", "prefix": "...", "account": "..."}
	// GCS:        {"bucket": "...", "prefix": "..."}
	// Local:      {"path": "/var/agentoven/archive"}
	Config map[string]string `json:"config,omitempty"`

	// EncryptionKeyID is an optional KMS key reference for encrypting archives.
	// S3: KMS key ARN; Azure: Key Vault key URL; GCS: CMEK resource name.
	EncryptionKeyID string `json:"encryption_key_id,omitempty"`

	// CompressArchives enables gzip compression on archived data.
	CompressArchives bool `json:"compress_archives,omitempty"`
}

// ArchiveRecord tracks a completed archive operation.
// These records form the compliance audit trail for data lifecycle management.
type ArchiveRecord struct {
	ID          string    `json:"id" db:"id"`
	Kitchen     string    `json:"kitchen" db:"kitchen"`
	DataKind    string    `json:"data_kind" db:"data_kind"` // "traces" or "audit_events"
	RecordCount int       `json:"record_count" db:"record_count"`
	Backend     string    `json:"backend" db:"backend"`       // driver kind
	URI         string    `json:"uri" db:"uri"`               // storage path/URL
	SizeBytes   int64     `json:"size_bytes" db:"size_bytes"` // archive size
	Compressed  bool      `json:"compressed" db:"compressed"`
	Encrypted   bool      `json:"encrypted" db:"encrypted"`
	OldestItem  time.Time `json:"oldest_item" db:"oldest_item"`
	NewestItem  time.Time `json:"newest_item" db:"newest_item"`
	CreatedAt   time.Time `json:"created_at" db:"created_at"`
}

// ── Extended PlanLimits ──────────────────────────────────────

// NOTE: The following fields should be added to PlanLimits via the extended
// limits mechanism. They are defined as a separate struct to avoid breaking
// existing code. Pro merges these into the base PlanLimits.
type ExtendedPlanLimits struct {
	MaxOutputRetentionDays  int  `json:"max_output_retention_days"`
	MaxAuditRetentionDays   int  `json:"max_audit_retention_days"`
	RequireThinkingAudit    bool `json:"require_thinking_audit"` // force thinking mode, flag missing
	MaxGateWaitMinutes      int  `json:"max_gate_wait_minutes"`  // SLA for human gate resolution
	MaxNotificationChannels int  `json:"max_notification_channels"`
}

// ── Trace Aggregation ────────────────────────────────────────

// TraceAggregation holds computed metrics over a set of traces.
type TraceAggregation struct {
	Key             string  `json:"key"` // agent name, provider name, model name
	InvocationCount int64   `json:"invocation_count"`
	ErrorCount      int64   `json:"error_count"`
	ErrorRate       float64 `json:"error_rate"`
	TotalTokens     int64   `json:"total_tokens"`
	TotalCostUSD    float64 `json:"total_cost_usd"`
	AvgLatencyMs    float64 `json:"avg_latency_ms"`
	P50LatencyMs    float64 `json:"p50_latency_ms"`
	P95LatencyMs    float64 `json:"p95_latency_ms"`
	P99LatencyMs    float64 `json:"p99_latency_ms"`
}

// DailyCost holds cost data for a single day.
type DailyCost struct {
	Date       string             `json:"date"` // YYYY-MM-DD
	TotalCost  float64            `json:"total_cost"`
	ByAgent    map[string]float64 `json:"by_agent,omitempty"`
	ByModel    map[string]float64 `json:"by_model,omitempty"`
	ByProvider map[string]float64 `json:"by_provider,omitempty"`
}

// ── PicoClaw / IoT Agent Integration ─────────────────────────

// PicoClawInstance represents a registered PicoClaw device (IoT edge agent).
type PicoClawInstance struct {
	ID          string                 `json:"id" db:"id"`
	Name        string                 `json:"name" db:"name"`
	Description string                 `json:"description,omitempty" db:"description"`
	Kitchen     string                 `json:"kitchen" db:"kitchen"`
	Endpoint    string                 `json:"endpoint" db:"endpoint"`                 // http://device-ip:port
	AgentName   string                 `json:"agent_name,omitempty" db:"agent_name"`   // linked AgentOven agent
	DeviceType  string                 `json:"device_type,omitempty" db:"device_type"` // "risc-v", "arm", "x86"
	Platform    string                 `json:"platform,omitempty" db:"platform"`       // "linux", "android"
	Version     string                 `json:"version,omitempty" db:"version"`         // PicoClaw version
	Status      PicoClawStatus         `json:"status" db:"status"`
	Skills      []string               `json:"skills,omitempty"`
	Gateways    []string               `json:"gateways,omitempty"` // active chat gateways: ["telegram", "discord"]
	Heartbeat   HeartbeatConfig        `json:"heartbeat,omitempty"`
	LastSeen    *time.Time             `json:"last_seen,omitempty" db:"last_seen"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt   time.Time              `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time              `json:"updated_at" db:"updated_at"`
}

// PicoClawStatus tracks the health state of a PicoClaw instance.
type PicoClawStatus string

const (
	PicoClawStatusOnline   PicoClawStatus = "online"
	PicoClawStatusOffline  PicoClawStatus = "offline"
	PicoClawStatusDegraded PicoClawStatus = "degraded" // heartbeat delayed but not expired
	PicoClawStatusUnknown  PicoClawStatus = "unknown"
)

// HeartbeatConfig configures health monitoring for a PicoClaw instance.
type HeartbeatConfig struct {
	Enabled      bool `json:"enabled"`
	IntervalSecs int  `json:"interval_secs,omitempty"` // check frequency (default 60)
	TimeoutSecs  int  `json:"timeout_secs,omitempty"`  // consider offline after this (default 180)
}

// HeartbeatResult is the response from a PicoClaw /status health check.
type HeartbeatResult struct {
	InstanceID string         `json:"instance_id"`
	Status     PicoClawStatus `json:"status"`
	Uptime     int64          `json:"uptime_secs,omitempty"`
	MemoryMB   float64        `json:"memory_mb,omitempty"`
	Skills     []string       `json:"skills,omitempty"`
	Model      string         `json:"model,omitempty"` // active LLM model
	Error      string         `json:"error,omitempty"`
	CheckedAt  time.Time      `json:"checked_at"`
}

// ── Chat Gateways ────────────────────────────────────────────

// ChatGatewayKind identifies a chat platform.
type ChatGatewayKind string

const (
	GatewayTelegram ChatGatewayKind = "telegram"
	GatewayDiscord  ChatGatewayKind = "discord"
	GatewaySlackBot ChatGatewayKind = "slack-bot"
	GatewayDingTalk ChatGatewayKind = "dingtalk"
	GatewayLINE     ChatGatewayKind = "line"
	GatewayWeCom    ChatGatewayKind = "wecom"
)

// ChatGateway represents a chat platform gateway that bridges AgentOven
// agents to messaging platforms (inspired by PicoClaw's gateway mode).
type ChatGateway struct {
	ID        string                 `json:"id" db:"id"`
	Name      string                 `json:"name" db:"name"`
	Kind      ChatGatewayKind        `json:"kind" db:"kind"`
	Kitchen   string                 `json:"kitchen" db:"kitchen"`
	AgentName string                 `json:"agent_name" db:"agent_name"` // agent to route messages to
	Active    bool                   `json:"active" db:"active"`
	Config    map[string]interface{} `json:"config,omitempty"` // platform-specific: bot_token, webhook_url, etc.
	CreatedAt time.Time              `json:"created_at" db:"created_at"`
	UpdatedAt time.Time              `json:"updated_at" db:"updated_at"`
}

// GatewayMessage represents an inbound or outbound message from a chat platform.
type GatewayMessage struct {
	GatewayID string    `json:"gateway_id"`
	Platform  string    `json:"platform"`   // "telegram", "discord", etc.
	ChannelID string    `json:"channel_id"` // chat/room/channel identifier
	UserID    string    `json:"user_id"`
	UserName  string    `json:"user_name,omitempty"`
	Text      string    `json:"text"`
	Direction string    `json:"direction"` // "inbound" or "outbound"
	Timestamp time.Time `json:"timestamp"`
}

// ══════════════════════════════════════════════════════════════
// ── A2A Agent Card (R8) ──────────────────────────────────────
// ══════════════════════════════════════════════════════════════

// AgentCard is the A2A-protocol agent card that describes an agent's capabilities,
// endpoints, and supported interaction patterns to external callers.
type AgentCard struct {
	Name           string            `json:"name"`
	Description    string            `json:"description,omitempty"`
	URL            string            `json:"url"` // A2A endpoint URL
	Version        string            `json:"version,omitempty"`
	Provider       AgentCardProvider `json:"provider,omitempty"`
	Capabilities   AgentCapabilities `json:"capabilities"`
	Skills         []AgentSkill      `json:"skills,omitempty"`
	InputModes     []string          `json:"defaultInputModes,omitempty"`  // "text", "image", "audio", "video"
	OutputModes    []string          `json:"defaultOutputModes,omitempty"` // "text", "image", "audio"
	Authentication *AgentAuth        `json:"authentication,omitempty"`
}

// AgentCardProvider identifies who built/operates the agent.
type AgentCardProvider struct {
	Organization string `json:"organization,omitempty"`
	URL          string `json:"url,omitempty"`
}

// AgentCapabilities describes what the agent supports.
type AgentCapabilities struct {
	Streaming              bool `json:"streaming,omitempty"`
	PushNotifications      bool `json:"pushNotifications,omitempty"`
	StateTransitionHistory bool `json:"stateTransitionHistory,omitempty"`
	// R8 additions for session/turn management
	Sessions         bool `json:"sessions,omitempty"`         // supports multi-turn sessions
	HumanInput       bool `json:"humanInput,omitempty"`       // can request human input mid-execution
	ToolCalling      bool `json:"toolCalling,omitempty"`      // supports tool-use
	Vision           bool `json:"vision,omitempty"`           // supports image inputs
	StructuredOutput bool `json:"structuredOutput,omitempty"` // supports JSON schema output
}

// AgentSkill describes one capability the agent offers.
type AgentSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Examples    []string `json:"examples,omitempty"` // example queries
}

// AgentAuth describes how to authenticate with the agent.
type AgentAuth struct {
	Schemes []string `json:"schemes,omitempty"` // "apiKey", "bearer", "oauth2"
}

// ══════════════════════════════════════════════════════════════
// ── Sessions & Conversations (R8) ────────────────────────────
// ══════════════════════════════════════════════════════════════

// Session represents a multi-turn conversation between a user and an agent.
type Session struct {
	ID          string                 `json:"id" db:"id"`
	AgentName   string                 `json:"agent_name" db:"agent_name"`
	Kitchen     string                 `json:"kitchen" db:"kitchen"`
	UserID      string                 `json:"user_id,omitempty" db:"user_id"`
	Status      SessionStatus          `json:"status" db:"status"`
	Messages    []ChatMessage          `json:"messages,omitempty"` // conversation history
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
	MaxTurns    int                    `json:"max_turns,omitempty" db:"max_turns"`
	TurnCount   int                    `json:"turn_count" db:"turn_count"`
	TotalTokens int64                  `json:"total_tokens" db:"total_tokens"`
	TotalCost   float64                `json:"total_cost_usd" db:"total_cost_usd"`
	CreatedAt   time.Time              `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time              `json:"updated_at" db:"updated_at"`
	ExpiresAt   *time.Time             `json:"expires_at,omitempty" db:"expires_at"`
}

// SessionStatus tracks the lifecycle of a session.
type SessionStatus string

const (
	SessionActive    SessionStatus = "active"
	SessionPaused    SessionStatus = "paused" // waiting for human input
	SessionCompleted SessionStatus = "completed"
	SessionExpired   SessionStatus = "expired"
)

// SessionMessage is the request body for sending a message to a session.
type SessionMessage struct {
	Content      string                 `json:"content"`
	ContentParts []ContentPart          `json:"content_parts,omitempty"` // multi-modal
	PromptVars   map[string]string      `json:"prompt_vars,omitempty"`   // template variables
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
}

// SessionResponse is the response from a session message.
type SessionResponse struct {
	SessionID     string               `json:"session_id"`
	TurnNumber    int                  `json:"turn_number"`
	Content       string               `json:"content"`
	ToolCalls     []ToolCallResult     `json:"tool_calls,omitempty"`
	FinishReason  string               `json:"finish_reason,omitempty"` // "stop", "tool_calls", "human_input", "max_turns"
	Usage         TokenUsage           `json:"usage"`
	ContextBudget *ContextBudgetReport `json:"context_budget,omitempty"`
	LatencyMs     int64                `json:"latency_ms"`
	Status        SessionStatus        `json:"status"`
}

// ContextBudgetReport tracks how much of the model's context window is consumed.
// Returned in every SessionResponse so callers can visualise usage (progress bar, meter)
// and take action before the window fills up.
type ContextBudgetReport struct {
	// ModelLimit is the model's maximum context window (tokens), from ModelCapability.
	ModelLimit int `json:"model_limit"`
	// Budget is the effective budget for the conversation (may be less than ModelLimit).
	// Derived from Agent.ContextBudget when set, otherwise equals ModelLimit.
	Budget int `json:"budget"`
	// Used is the estimated token count of the current conversation context
	// (system prompt + history + last response).
	Used int `json:"used"`
	// Remaining is Budget − Used (clamped to 0).
	Remaining int `json:"remaining"`
	// UtilizationPct is Used/Budget × 100, rounded to one decimal place.
	UtilizationPct float64 `json:"utilization_pct"`
	// Summarised indicates whether older messages were summarised to stay within budget.
	Summarised bool `json:"summarised"`
	// CacheHits is the number of prompt-cache hits from the provider (if supported).
	CacheHits int `json:"cache_hits,omitempty"`
	// TokensSaved is the estimated tokens saved through prompt caching this turn.
	TokensSaved int `json:"tokens_saved,omitempty"`
}

// HumanInputRequest represents a request for human input during agent execution.
type HumanInputRequest struct {
	SessionID   string    `json:"session_id"`
	AgentName   string    `json:"agent_name"`
	Kitchen     string    `json:"kitchen"`
	Prompt      string    `json:"prompt"`                 // what the agent is asking the human
	InputType   string    `json:"input_type"`             // "text", "approval", "choice"
	Choices     []string  `json:"choices,omitempty"`      // for input_type=choice
	Timeout     int       `json:"timeout_secs,omitempty"` // max wait time
	RequestedAt time.Time `json:"requested_at"`
}

// ══════════════════════════════════════════════════════════════
// ── Model Catalog (R8) ───────────────────────────────────────
// ══════════════════════════════════════════════════════════════

// ModelCapability describes the known capabilities and pricing for a model.
// Populated from the model catalog (LiteLLM enrichment + provider discovery).
type ModelCapability struct {
	ModelID           string   `json:"model_id"`                    // canonical ID: "openai/gpt-5-mini"
	ProviderKind      string   `json:"provider_kind"`               // "openai", "anthropic", "ollama"
	ModelName         string   `json:"model_name"`                  // provider-specific: "gpt-5-mini"
	DisplayName       string   `json:"display_name,omitempty"`      // human-friendly name
	ContextWindow     int      `json:"context_window,omitempty"`    // max input tokens
	MaxOutputTokens   int      `json:"max_output_tokens,omitempty"` // max output tokens
	InputCostPer1K    float64  `json:"input_cost_per_1k,omitempty"`
	OutputCostPer1K   float64  `json:"output_cost_per_1k,omitempty"`
	SupportsTools     bool     `json:"supports_tools,omitempty"`
	SupportsVision    bool     `json:"supports_vision,omitempty"`
	SupportsStreaming bool     `json:"supports_streaming,omitempty"`
	SupportsThinking  bool     `json:"supports_thinking,omitempty"` // extended thinking / reasoning
	SupportsJSON      bool     `json:"supports_json,omitempty"`     // structured JSON output
	TokenParamName    string   `json:"token_param_name,omitempty"`  // "max_tokens" or "max_completion_tokens"
	APIVersion        string   `json:"api_version,omitempty"`       // recommended API version
	Modalities        []string `json:"modalities,omitempty"`        // ["text", "image", "audio"]
	DeprecatedAt      string   `json:"deprecated_at,omitempty"`     // ISO date
	Source            string   `json:"source,omitempty"`            // "catalog", "discovery", "manual"
}

// DiscoveredModel is a model found by querying a provider's list-models API.
type DiscoveredModel struct {
	ID        string            `json:"id"`       // model ID from provider
	Provider  string            `json:"provider"` // provider name
	Kind      string            `json:"kind"`     // provider kind
	OwnedBy   string            `json:"owned_by,omitempty"`
	CreatedAt int64             `json:"created_at,omitempty"` // unix timestamp
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// ══════════════════════════════════════════════════════════════
// ── Environment (Pro, R8) ────────────────────────────────────
// ══════════════════════════════════════════════════════════════

// Environment represents a deployment stage (dev, qa, prod, or custom).
// Pro feature: agents are promoted between environments with approval gates.
// Each environment has its own URL, provider overrides, and promotion policy.
type Environment struct {
	ID               string                 `json:"id" db:"id"`
	Name             string                 `json:"name" db:"name"` // display name: "Development", "QA", "Production"
	Slug             string                 `json:"slug" db:"slug"` // machine name: "dev", "qa", "prod"
	Kitchen          string                 `json:"kitchen" db:"kitchen"`
	Description      string                 `json:"description,omitempty" db:"description"`
	URL              string                 `json:"url,omitempty" db:"url"` // environment-specific control plane URL
	Order            int                    `json:"order" db:"order"`       // promotion order: 0=dev, 1=qa, 2=prod
	IsDefault        bool                   `json:"is_default" db:"is_default"`
	IsProduction     bool                   `json:"is_production" db:"is_production"`
	ApprovalRequired bool                   `json:"approval_required" db:"approval_required"`         // require human approval for promotion into this env
	PromotionPolicy  string                 `json:"promotion_policy,omitempty" db:"promotion_policy"` // "manual", "auto", "gated" (default: "manual")
	Config           map[string]interface{} `json:"config,omitempty"`                                 // env-specific overrides
	Providers        map[string]string      `json:"providers,omitempty"`                              // provider name → env-specific provider name
	Tags             map[string]string      `json:"tags,omitempty"`
	CreatedAt        time.Time              `json:"created_at" db:"created_at"`
	UpdatedAt        time.Time              `json:"updated_at" db:"updated_at"`
}

// DefaultEnvironments returns the three standard deployment stages.
func DefaultEnvironments(kitchen string) []Environment {
	now := time.Now().UTC()
	return []Environment{
		{Name: "Development", Slug: "dev", Kitchen: kitchen, Order: 0, IsDefault: true, PromotionPolicy: "auto", CreatedAt: now, UpdatedAt: now},
		{Name: "QA", Slug: "qa", Kitchen: kitchen, Order: 1, ApprovalRequired: false, PromotionPolicy: "manual", CreatedAt: now, UpdatedAt: now},
		{Name: "Production", Slug: "prod", Kitchen: kitchen, Order: 2, IsProduction: true, ApprovalRequired: true, PromotionPolicy: "gated", CreatedAt: now, UpdatedAt: now},
	}
}

// PromotionRequest describes promoting an agent from one environment to another.
type PromotionRequest struct {
	AgentName  string `json:"agent_name"`
	FromEnv    string `json:"from_env"`              // "dev"
	ToEnv      string `json:"to_env"`                // "qa"
	VersionPin string `json:"version,omitempty"`     // specific version to promote (default: latest)
	DryRun     bool   `json:"dry_run,omitempty"`     // preview without applying
	Comment    string `json:"comment,omitempty"`     // promotion reason / change notes
	TrackerRef string `json:"tracker_ref,omitempty"` // linked issue (e.g. "PROJ-1234")
}

// PromotionResult describes the outcome of a promotion.
type PromotionResult struct {
	AgentName    string    `json:"agent_name"`
	FromEnv      string    `json:"from_env"`
	ToEnv        string    `json:"to_env"`
	Version      string    `json:"version"`
	Status       string    `json:"status"`         // "promoted", "pending_approval", "dry_run", "failed"
	Diff         string    `json:"diff,omitempty"` // human-readable diff of changes
	Error        string    `json:"error,omitempty"`
	ApprovalID   string    `json:"approval_id,omitempty"`   // if gated, the approval record ID
	DeploymentID string    `json:"deployment_id,omitempty"` // the created AgentDeployment ID
	PromotedBy   string    `json:"promoted_by,omitempty"`
	PromotedAt   time.Time `json:"promoted_at"`
}

// ── Agent Deployment (Pro, R9) ──────────────────────────────

// DeploymentStatus tracks the lifecycle of an agent deployment in an environment.
type DeploymentStatus string

const (
	DeploymentPending    DeploymentStatus = "pending"     // awaiting approval
	DeploymentActive     DeploymentStatus = "active"      // live in this environment
	DeploymentRolledBack DeploymentStatus = "rolled_back" // replaced by rollback
	DeploymentSuperseded DeploymentStatus = "superseded"  // replaced by newer promotion
)

// AgentDeployment records an agent version deployed to a specific environment.
// This is the join entity: agent × version × environment.
// Immutable once active — a new promotion creates a new deployment.
type AgentDeployment struct {
	ID           string                 `json:"id" db:"id"`
	AgentName    string                 `json:"agent_name" db:"agent_name"`
	Kitchen      string                 `json:"kitchen" db:"kitchen"`
	Environment  string                 `json:"environment" db:"environment"` // env slug
	Version      string                 `json:"version" db:"version"`         // semver snapshot
	Status       DeploymentStatus       `json:"status" db:"status"`
	PromotedFrom string                 `json:"promoted_from,omitempty" db:"promoted_from"` // source env slug
	DeployedBy   string                 `json:"deployed_by" db:"deployed_by"`
	DeployedAt   time.Time              `json:"deployed_at" db:"deployed_at"`
	RolledBackAt *time.Time             `json:"rolled_back_at,omitempty" db:"rolled_back_at"`
	RolledBackBy string                 `json:"rolled_back_by,omitempty" db:"rolled_back_by"`
	Comment      string                 `json:"comment,omitempty" db:"comment"`
	TrackerRef   string                 `json:"tracker_ref,omitempty" db:"tracker_ref"` // linked issue
	Config       map[string]interface{} `json:"config,omitempty"`                       // env-specific config snapshot
	Metadata     map[string]string      `json:"metadata,omitempty"`
}

// ── Service Account (Pro, R9) ────────────────────────────────

// ServiceAccount is a kitchen-scoped machine identity for agent-to-agent
// calls, CI/CD pipelines, and external integrations. Unlike ScopedAPIKeys
// (which grant access to specific agents), service accounts are identity-level
// and produce an Identity for RBAC.
//
// Token format: "ao_sa_<random>" — 32 bytes, base62 encoded.
// Stored as bcrypt hash, never in plaintext after creation.
type ServiceAccount struct {
	ID          string     `json:"id" db:"id"`
	Name        string     `json:"name" db:"name"` // unique within kitchen
	Kitchen     string     `json:"kitchen" db:"kitchen"`
	Description string     `json:"description,omitempty" db:"description"`
	Role        string     `json:"role" db:"role"`                 // AgentOven role (baker, chef, etc.)
	Scopes      []string   `json:"scopes,omitempty"`               // optional fine-grained scopes
	TokenHash   string     `json:"-" db:"token_hash"`              // bcrypt, never in JSON
	TokenPrefix string     `json:"token_prefix" db:"token_prefix"` // first 8 chars ("ao_sa_Ab")
	ExpiresAt   *time.Time `json:"expires_at,omitempty" db:"expires_at"`
	CreatedBy   string     `json:"created_by" db:"created_by"`
	CreatedAt   time.Time  `json:"created_at" db:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty" db:"last_used_at"`
	Revoked     bool       `json:"revoked" db:"revoked"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty" db:"revoked_at"`
	RevokedBy   string     `json:"revoked_by,omitempty" db:"revoked_by"`
}

// IsExpired returns true if the service account token has expired.
func (sa *ServiceAccount) IsExpired() bool {
	if sa.ExpiresAt == nil {
		return false
	}
	return time.Now().After(*sa.ExpiresAt)
}

// IsUsable returns true if the service account can authenticate.
func (sa *ServiceAccount) IsUsable() bool {
	return !sa.Revoked && !sa.IsExpired()
}

// ── External Tracker Integration (Pro, R9) ──────────────────

// TrackerKind identifies the external issue tracker type.
type TrackerKind string

const (
	TrackerJIRA        TrackerKind = "jira"
	TrackerGitLab      TrackerKind = "gitlab"
	TrackerGitHub      TrackerKind = "github"
	TrackerAzureDevOps TrackerKind = "azure-devops"
)

// ExternalTrackerConfig stores the connection settings for an external
// issue tracker. Configured at the kitchen level in KitchenSettings.
type ExternalTrackerConfig struct {
	Kind           TrackerKind       `json:"kind"`                    // jira, gitlab, github, azure-devops
	BaseURL        string            `json:"base_url"`                // e.g. "https://myorg.atlassian.net"
	ProjectKey     string            `json:"project_key"`             // e.g. "AGENT" for JIRA, repo path for GitLab
	APITokenRef    string            `json:"api_token_ref,omitempty"` // reference to stored secret (never plaintext)
	AutoSync       bool              `json:"auto_sync"`               // auto-sync test run results to tracker
	SyncOnComplete bool              `json:"sync_on_complete"`        // sync when test run completes
	CreateOnFail   bool              `json:"create_on_fail"`          // auto-create issue on test failure
	Labels         []string          `json:"labels,omitempty"`        // default labels for created issues
	CustomFields   map[string]string `json:"custom_fields,omitempty"` // tracker-specific custom field mappings
}

// TrackerSyncStatus tracks whether a test run has been synced to the external tracker.
type TrackerSyncStatus string

const (
	TrackerSyncNone    TrackerSyncStatus = ""        // not configured or no tracker ref
	TrackerSyncPending TrackerSyncStatus = "pending" // queued for sync
	TrackerSyncSynced  TrackerSyncStatus = "synced"  // successfully synced
	TrackerSyncFailed  TrackerSyncStatus = "failed"  // sync failed
)

// ── Traceability Matrix (Pro, R9) ────────────────────────────

// TraceabilityEntry links a requirement/ticket to test suites, test runs,
// agents, and environments — enabling full compliance traceability.
type TraceabilityEntry struct {
	TrackerRef   string            `json:"tracker_ref"`             // e.g. "PROJ-1234"
	TrackerKind  TrackerKind       `json:"tracker_kind"`            // jira, gitlab, etc.
	TrackerURL   string            `json:"tracker_url,omitempty"`   // direct link to the issue
	Title        string            `json:"title,omitempty"`         // issue title/summary
	Status       string            `json:"status,omitempty"`        // issue status (open, in-progress, done)
	SuiteIDs     []string          `json:"suite_ids"`               // linked test suite IDs
	LatestRunID  string            `json:"latest_run_id,omitempty"` // most recent test run
	LatestResult string            `json:"latest_result,omitempty"` // passed, failed, error
	AgentName    string            `json:"agent_name,omitempty"`
	Environment  string            `json:"environment,omitempty"` // which env was tested
	Version      string            `json:"version,omitempty"`     // agent version tested
	Tags         map[string]string `json:"tags,omitempty"`
	LastSyncedAt *time.Time        `json:"last_synced_at,omitempty"`
}

// TraceabilityMatrix is a collection of traceability entries for a kitchen,
// providing a compliance-ready view of requirement→test→agent→environment coverage.
type TraceabilityMatrix struct {
	Kitchen     string               `json:"kitchen"`
	Entries     []TraceabilityEntry  `json:"entries"`
	GeneratedAt time.Time            `json:"generated_at"`
	Coverage    TraceabilityCoverage `json:"coverage"`
}

// TraceabilityCoverage summarises how well requirements are covered by tests.
type TraceabilityCoverage struct {
	TotalRequirements int     `json:"total_requirements"`
	CoveredByTests    int     `json:"covered_by_tests"`
	PassingTests      int     `json:"passing_tests"`
	FailingTests      int     `json:"failing_tests"`
	CoveragePercent   float64 `json:"coverage_percent"` // 0.0 - 100.0
}

// ── Guardrails ──────────────────────────────────────────────

// GuardrailKind identifies the type of guardrail check.
type GuardrailKind string

const (
	GuardrailContentFilter    GuardrailKind = "content_filter"
	GuardrailPIIDetection     GuardrailKind = "pii_detection"
	GuardrailTopicRestriction GuardrailKind = "topic_restriction"
	GuardrailMaxLength        GuardrailKind = "max_length"
	GuardrailRegexFilter      GuardrailKind = "regex_filter"
	GuardrailPromptInjection  GuardrailKind = "prompt_injection"
	GuardrailCustom           GuardrailKind = "custom"
)

// GuardrailStage controls when a guardrail is evaluated.
type GuardrailStage string

const (
	GuardrailStageInput  GuardrailStage = "input"
	GuardrailStageOutput GuardrailStage = "output"
	GuardrailStageBoth   GuardrailStage = "both"
)

// Guardrail defines a single validation rule applied to agent I/O.
type Guardrail struct {
	ID        string                 `json:"id"`
	Name      string                 `json:"name,omitempty"`
	Kind      GuardrailKind          `json:"kind"`
	Stage     GuardrailStage         `json:"stage"`
	Config    map[string]interface{} `json:"config,omitempty"` // kind-specific configuration
	Enabled   bool                   `json:"enabled"`
	CreatedAt time.Time              `json:"created_at,omitempty"`
}

// GuardrailResult is the outcome of a single guardrail evaluation.
type GuardrailResult struct {
	Passed  bool          `json:"passed"`
	Kind    GuardrailKind `json:"kind"`
	Stage   string        `json:"stage"`             // "input" or "output"
	Message string        `json:"message,omitempty"` // explanation when blocked
}

// GuardrailEvaluation is the aggregate result of all guardrails for a request.
type GuardrailEvaluation struct {
	Passed  bool              `json:"passed"`
	Results []GuardrailResult `json:"results"`
}

// ── API Key Rotation ────────────────────────────────────────

// APIKeyEntry represents a single API key in a rotation pool.
type APIKeyEntry struct {
	Key     string `json:"key"`
	Label   string `json:"label,omitempty"`  // human-readable label (e.g. "prod-key-1")
	Weight  int    `json:"weight,omitempty"` // for weighted rotation (higher = more traffic)
	Enabled bool   `json:"enabled"`
}

// ══════════════════════════════════════════════════════════════
// ── Scoped API Keys (R8) ────────────────────────────────────
// ══════════════════════════════════════════════════════════════

// ScopedAPIKey grants access to specific agents with usage tracking.
// Used by the Agent Viewer to give end users access to individual agents
// without full control plane access.
//
// Key format: "ao_sk_<random>" — 32 bytes, base62 encoded.
// Stored as bcrypt hash, never in plaintext after creation.
type ScopedAPIKey struct {
	ID         string     `json:"id"`
	KeyHash    string     `json:"-"`          // bcrypt hash, never in JSON responses
	KeyPrefix  string     `json:"key_prefix"` // first 8 chars for identification (e.g. "ao_sk_Ab")
	Kitchen    string     `json:"kitchen"`
	AgentNames []string   `json:"agent_names"` // which agents this key can invoke
	Label      string     `json:"label"`       // human-readable name ("marketing-team", "demo-key")
	MaxCalls   int        `json:"max_calls"`   // 0 = unlimited
	CallCount  int        `json:"call_count"`  // current usage
	ExpiresAt  *time.Time `json:"expires_at"`  // nil = never expires
	CreatedBy  string     `json:"created_by"`  // Identity.Subject who created this key
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	Revoked    bool       `json:"revoked"`
}

// IsExpired returns true if the key has passed its expiration time.
func (k *ScopedAPIKey) IsExpired() bool {
	if k.ExpiresAt == nil {
		return false
	}
	return time.Now().After(*k.ExpiresAt)
}

// IsQuotaExceeded returns true if the key has reached its call limit.
func (k *ScopedAPIKey) IsQuotaExceeded() bool {
	if k.MaxCalls == 0 {
		return false
	}
	return k.CallCount >= k.MaxCalls
}

// CanAccessAgent returns true if the key grants access to the named agent.
func (k *ScopedAPIKey) CanAccessAgent(agentName string) bool {
	for _, name := range k.AgentNames {
		if name == agentName || name == "*" {
			return true
		}
	}
	return false
}

// ── Test Suites ──────────────────────────────────────────────
// Agent test suites allow batch evaluation of agent behavior against
// expected outputs — the primary gap vs LangSmith.

// TestSuite is a named collection of test cases for one or more agents.
type TestSuite struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Kitchen     string     `json:"kitchen"`
	Description string     `json:"description,omitempty"`
	AgentName   string     `json:"agent_name"` // target agent
	Cases       []TestCase `json:"cases"`
	Schedule    string     `json:"schedule,omitempty"`    // cron expression, e.g. "0 */6 * * *" (every 6h)
	NextRunAt   *time.Time `json:"next_run_at,omitempty"` // computed from Schedule
	Enabled     bool       `json:"enabled"`               // whether scheduled runs are active
	TrackerRef  string     `json:"tracker_ref,omitempty"` // linked external issue (e.g. "PROJ-1234")
	CreatedBy   string     `json:"created_by,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// TestCase is a single input → expected-output pair within a test suite.
type TestCase struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Input          string            `json:"input"`                     // user message
	ExpectedOutput string            `json:"expected_output,omitempty"` // for exact/similarity matching
	Tags           []string          `json:"tags,omitempty"`            // e.g. ["edge-case", "safety"]
	Variables      map[string]string `json:"variables,omitempty"`       // prompt template variables
	MaxLatencyMs   int64             `json:"max_latency_ms,omitempty"`  // SLA threshold
}

// TestRunStatus tracks the lifecycle of a test suite execution.
type TestRunStatus string

const (
	TestRunPending   TestRunStatus = "pending"
	TestRunRunning   TestRunStatus = "running"
	TestRunCompleted TestRunStatus = "completed"
	TestRunFailed    TestRunStatus = "failed"
)

// TestRun records one execution of a test suite.
type TestRun struct {
	ID                string            `json:"id"`
	SuiteID           string            `json:"suite_id"`
	SuiteName         string            `json:"suite_name"`
	Kitchen           string            `json:"kitchen"`
	AgentName         string            `json:"agent_name"`
	Status            TestRunStatus     `json:"status"`
	Results           []TestResult      `json:"results"`
	Summary           TestRunSummary    `json:"summary"`
	Trigger           string            `json:"trigger"`                       // "manual", "scheduled", "ci"
	TrackerRef        string            `json:"tracker_ref,omitempty"`         // inherited from suite or set per-run
	TrackerSyncStatus TrackerSyncStatus `json:"tracker_sync_status,omitempty"` // sync state with external tracker
	Environment       string            `json:"environment,omitempty"`         // which env the agent was tested in
	AgentVersion      string            `json:"agent_version,omitempty"`       // version of agent at test time
	StartedAt         time.Time         `json:"started_at"`
	CompletedAt       *time.Time        `json:"completed_at,omitempty"`
	DurationMs        int64             `json:"duration_ms"`
	CreatedBy         string            `json:"created_by,omitempty"`
}

// TestResult records the outcome of a single test case execution.
type TestResult struct {
	CaseID         string  `json:"case_id"`
	CaseName       string  `json:"case_name"`
	Input          string  `json:"input"`
	ExpectedOutput string  `json:"expected_output,omitempty"`
	ActualOutput   string  `json:"actual_output"`
	Passed         bool    `json:"passed"`
	LatencyMs      int64   `json:"latency_ms"`
	TokensUsed     int64   `json:"tokens_used"`
	CostUSD        float64 `json:"cost_usd"`
	Error          string  `json:"error,omitempty"`
	TraceID        string  `json:"trace_id,omitempty"` // link to the trace record
}

// TestRunSummary aggregates metrics across all test case results.
type TestRunSummary struct {
	TotalCases   int     `json:"total_cases"`
	Passed       int     `json:"passed"`
	Failed       int     `json:"failed"`
	Errors       int     `json:"errors"`
	PassRate     float64 `json:"pass_rate"` // 0.0 - 1.0
	AvgLatencyMs int64   `json:"avg_latency_ms"`
	TotalTokens  int64   `json:"total_tokens"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	P50LatencyMs int64   `json:"p50_latency_ms"`
	P95LatencyMs int64   `json:"p95_latency_ms"`
}
