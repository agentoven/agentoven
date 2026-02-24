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
	"fmt"
	"net/http"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/router"
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

	// CancelRun cancels a running recipe execution. Returns true if the run was found and canceled.
	CancelRun(runID string) bool

	// ApproveGate approves a human gate step in a running recipe (simple boolean).
	ApproveGate(runID, stepName string, approved bool) bool

	// ApproveGateWithMetadata approves or rejects a gate with full approver identity,
	// channel, and comments. Used by Slack/Teams callback handlers and the compliance UI.
	ApproveGateWithMetadata(runID, stepName string, approved bool, approverID, approverEmail, channel, comments string) bool
}

// ── Notification Service ────────────────────────────────────

// NotificationEvent is the payload sent to notification tools and channels.
// Defined in contracts so Pro channel drivers can reference it without
// importing internal/notify.
type NotificationEvent struct {
	Type       string                 `json:"type"`
	RunID      string                 `json:"run_id"`
	RecipeName string                 `json:"recipe_name,omitempty"`
	StepName   string                 `json:"step_name,omitempty"`
	Kitchen    string                 `json:"kitchen"`
	Payload    map[string]interface{} `json:"payload,omitempty"`
	Timestamp  time.Time              `json:"timestamp"`
}

// ChannelDriver sends a notification event through a specific channel kind.
// OSS ships WebhookChannelDriver. Pro registers Slack, Teams, etc.
// Defined in contracts so both repos can reference the same interface.
type ChannelDriver interface {
	// Kind returns the ChannelKind this driver handles.
	Kind() models.ChannelKind

	// Send delivers an event through the given notification channel.
	Send(ctx context.Context, channel *models.NotificationChannel, event NotificationEvent) error
}

// NotificationService dispatches notifications to registered channels.
// OSS implementation: notify.Service (MCP-tool based + webhook driver).
// Pro implementation: adds Slack, Teams, Discord, Email, Zapier drivers.
type NotificationService interface {
	// Dispatch sends a notification event through the specified tools/channels.
	Dispatch(ctx context.Context, kitchen string, tools []string, event interface{}) []map[string]interface{}

	// RegisterDriver adds or replaces a channel driver for the given kind.
	RegisterDriver(driver ChannelDriver)
}

// ── Provider Driver ─────────────────────────────────────────

// ProviderDriver is a type alias for the router's ProviderDriver interface.
// OSS ships: OpenAI, Azure OpenAI, Anthropic, Ollama drivers.
// Pro adds:  AWS Bedrock, Azure AI Foundry, Google Vertex, SageMaker drivers.
//
// Drivers are registered in the Model Router via RegisterDriver().
type ProviderDriver = router.ProviderDriver

// EmbeddingCapableDriver is a type alias for the router's EmbeddingCapableDriver.
// Provider drivers that implement this optional interface automatically
// expose embedding capabilities — no separate embedding config needed.
type EmbeddingCapableDriver = router.EmbeddingCapableDriver

// EmbeddingModelInfo describes an embedding model available from a provider.
type EmbeddingModelInfo = router.EmbeddingModelInfo

// ModelDiscoveryDriver is a type alias for the router's ModelDiscoveryDriver.
// Provider drivers that implement this optional interface can auto-discover
// available models from the provider's API.
type ModelDiscoveryDriver = router.ModelDiscoveryDriver

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

// ── Prompt Validator Service ────────────────────────────────

// PromptValidatorService validates prompt templates for security, structure,
// and compliance. OSS ships a basic validator; Pro ships the full enterprise
// validator with injection detection, model checks, deny-lists, and LLM-judge.
type PromptValidatorService interface {
	// Validate runs all configured checks on a prompt template.
	// Returns a ValidationReport with score, issues, and model compatibility.
	Validate(ctx context.Context, prompt *models.Prompt, settings *models.KitchenSettings) (*models.ValidationReport, error)

	// SanitizeVariables checks and sanitizes template variable values
	// before substitution to prevent indirect prompt injection.
	SanitizeVariables(ctx context.Context, variables map[string]string, settings *models.KitchenSettings) (map[string]string, []models.ValidationIssue, error)

	// Edition returns the validator edition ("community" or "pro").
	Edition() string
}

// ── Archive Driver ───────────────────────────────────────────

// ArchiveDriver writes expired data to a durable archive backend.
// OSS ships LocalFileArchiver (JSONL to disk).
// Pro registers S3Archiver, AzureBlobArchiver, GCSArchiver.
type ArchiveDriver interface {
	Kind() string
	ArchiveTraces(ctx context.Context, kitchen string, traces []models.Trace) (uri string, err error)
	ArchiveAuditEvents(ctx context.Context, kitchen string, events []models.AuditEvent) (uri string, err error)
	HealthCheck(ctx context.Context) error
}

// ── Embedding Driver ─────────────────────────────────────────

// EmbeddingDriver generates vector embeddings from text.
// OSS ships: OpenAI (text-embedding-3-small/large), Ollama (nomic-embed-text).
// Pro adds: Azure OpenAI Embeddings, Bedrock Titan, Vertex textembedding-gecko.
type EmbeddingDriver interface {
	// Kind returns a short identifier (e.g. "openai", "ollama", "azure-openai").
	Kind() string

	// Embed generates vector embeddings for a batch of texts.
	Embed(ctx context.Context, texts []string) ([][]float64, error)

	// Dimensions returns the vector dimensionality for this model.
	Dimensions() int

	// MaxBatchSize returns the maximum texts per Embed call.
	MaxBatchSize() int

	// HealthCheck verifies the embedding service is reachable.
	HealthCheck(ctx context.Context) error
}

// ── Vector Store Driver ──────────────────────────────────────

// VectorStoreDriver provides vector storage and similarity search.
// OSS ships: embedded (in-memory brute-force), pgvector (user-provided PG).
// Pro adds: Pinecone, Qdrant, Cosmos DB, Chroma, Snowflake Cortex, Databricks.
type VectorStoreDriver interface {
	// Kind returns a short identifier (e.g. "embedded", "pgvector", "pinecone").
	Kind() string

	// Upsert inserts or updates documents in the vector index.
	Upsert(ctx context.Context, kitchen string, docs []models.VectorDoc) error

	// Search performs similarity search returning top-k results.
	Search(ctx context.Context, kitchen string, vector []float64, topK int, filter map[string]string) ([]models.SearchResult, error)

	// Delete removes documents by ID from the vector index.
	Delete(ctx context.Context, kitchen string, ids []string) error

	// Count returns the number of documents in the index for a kitchen.
	Count(ctx context.Context, kitchen string) (int, error)

	// HealthCheck verifies the vector store is reachable.
	HealthCheck(ctx context.Context) error
}

// ── Data Connector Driver (Pro) ──────────────────────────────

// DataConnectorDriver connects to external data sources (data lakes, warehouses).
// Pro only: Snowflake, Databricks, S3/ADLS/GCS object stores.
type DataConnectorDriver interface {
	// Kind returns the connector type (e.g. "snowflake", "databricks", "s3").
	Kind() string

	// Connect establishes a connection to the data source.
	Connect(ctx context.Context, config map[string]interface{}) error

	// ReadDocuments reads raw documents from the data source.
	ReadDocuments(ctx context.Context, query string, limit int) ([]models.RawDocument, error)

	// HealthCheck verifies the data source is reachable.
	HealthCheck(ctx context.Context) error
}

// CommunityPromptValidator is the no-op/basic validator shipped with OSS.
// It performs minimal checks (name/template not empty, basic size limits)
// and always returns a passing score. Enterprise features (injection detection,
// model compatibility, LLM-judge) return placeholder results.
type CommunityPromptValidator struct{}

func (v *CommunityPromptValidator) Validate(_ context.Context, prompt *models.Prompt, settings *models.KitchenSettings) (*models.ValidationReport, error) {
	report := &models.ValidationReport{
		PromptName:  prompt.Name,
		Version:     prompt.Version,
		Score:       100,
		Issues:      []models.ValidationIssue{},
		ValidatedAt: time.Now().UTC(),
		ValidatedBy: "community",
	}

	// Basic size check
	maxSize := 50000 // 50KB default
	if settings != nil && settings.MaxTemplateSize > 0 {
		maxSize = settings.MaxTemplateSize
	}
	if len(prompt.Template) > maxSize {
		report.Score = 60
		report.Issues = append(report.Issues, models.ValidationIssue{
			Severity: models.ValidationWarning,
			Category: "structure",
			Message:  fmt.Sprintf("Template exceeds recommended size (%d chars, limit %d)", len(prompt.Template), maxSize),
		})
	}

	// Basic empty check
	if len(prompt.Template) == 0 {
		report.Score = 0
		report.Issues = append(report.Issues, models.ValidationIssue{
			Severity: models.ValidationError,
			Category: "structure",
			Message:  "Template is empty",
		})
	}

	return report, nil
}

func (v *CommunityPromptValidator) SanitizeVariables(_ context.Context, variables map[string]string, _ *models.KitchenSettings) (map[string]string, []models.ValidationIssue, error) {
	// Community: pass-through, no sanitization
	return variables, nil, nil
}

func (v *CommunityPromptValidator) Edition() string {
	return "community"
}

// ── Community Plan Resolver ─────────────────────────────────

// CommunityPlanResolver always returns the static community tier limits.
// Pro overrides this with a license-aware resolver.
type CommunityPlanResolver struct{}

func (r *CommunityPlanResolver) Resolve(_ context.Context, _ *models.Kitchen) (*models.PlanLimits, error) {
	return models.CommunityLimits(), nil
}

// ── Community Tier Enforcer ─────────────────────────────────

// CommunityTierEnforcer is a no-op middleware for the OSS edition.
// Pro replaces this with quota-checking middleware.
type CommunityTierEnforcer struct{}

func (e *CommunityTierEnforcer) Middleware(next http.Handler) http.Handler {
	return next // pass-through in OSS
}

// ── Chat Gateway Driver ──────────────────────────────────────

// ChatGatewayDriver handles bidirectional message routing between AgentOven
// agents and chat platforms (Telegram, Discord, etc.). Inspired by PicoClaw's
// gateway mode. OSS ships a webhook-based driver; Pro ships platform-native
// drivers with rich message formatting.
type ChatGatewayDriver interface {
	// Kind returns the platform identifier (e.g. "telegram", "discord").
	Kind() models.ChatGatewayKind

	// Start begins listening for inbound messages on the platform.
	// Messages are delivered to the provided callback.
	Start(ctx context.Context, gateway *models.ChatGateway, onMessage func(models.GatewayMessage)) error

	// Send delivers an outbound message to the chat platform.
	Send(ctx context.Context, gateway *models.ChatGateway, msg models.GatewayMessage) error

	// Stop gracefully shuts down the gateway listener.
	Stop(ctx context.Context, gateway *models.ChatGateway) error

	// HealthCheck verifies the platform credentials are valid.
	HealthCheck(ctx context.Context, gateway *models.ChatGateway) error
}

// ── Model Discovery Driver (R8) ─────────────────────────────

// ModelDiscoveryDriver is defined in the router package and re-exported
// above as a type alias. See router.ModelDiscoveryDriver for docs.

// ── Session Store (R8) ──────────────────────────────────────

// SessionStore manages multi-turn conversation sessions.
// OSS ships in-memory implementation. Pro adds Redis/PostgreSQL.
type SessionStore interface {
	// CreateSession starts a new session for an agent.
	CreateSession(ctx context.Context, session *models.Session) error

	// GetSession retrieves a session by ID.
	GetSession(ctx context.Context, sessionID string) (*models.Session, error)

	// UpdateSession updates session state (messages, status, turn count).
	UpdateSession(ctx context.Context, session *models.Session) error

	// ListSessions lists sessions for an agent in a kitchen.
	ListSessions(ctx context.Context, kitchen, agentName string) ([]models.Session, error)

	// DeleteSession removes a session.
	DeleteSession(ctx context.Context, sessionID string) error
}

// ── Guardrail Service (R9) ──────────────────────────────────

// GuardrailService evaluates guardrails on agent input and output.
// OSS ships a community implementation. Pro can override with
// LLM-judge, external policy engines, or custom webhook validators.
type GuardrailService interface {
	// EvaluateInput runs input-stage guardrails against the user message.
	EvaluateInput(ctx context.Context, guardrails []models.Guardrail, message string) (*models.GuardrailEvaluation, error)

	// EvaluateOutput runs output-stage guardrails against the model response.
	EvaluateOutput(ctx context.Context, guardrails []models.Guardrail, response string) (*models.GuardrailEvaluation, error)
}
