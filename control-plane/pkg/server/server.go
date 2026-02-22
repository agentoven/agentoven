// Package server provides the public entry point for initializing the
// AgentOven control plane server.
//
// This package exists in pkg/ (not internal/) so that the enterprise repo
// (agentoven-pro) can import it and compose the full server with Pro overrides.
//
// Usage (OSS):
//
//	srv, err := server.New(ctx)
//	http.ListenAndServe(":8080", srv.Handler)
//
// Usage (Pro):
//
//	srv, err := server.New(ctx)
//	proHandler := tierEnforcer.Middleware(srv.Handler)
//	http.ListenAndServe(":8080", proHandler)
package server

import (
	"context"
	"fmt"
	"os"
	"time"

	"net/http"

	"github.com/agentoven/agentoven/control-plane/internal/api"
	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	aoauth "github.com/agentoven/agentoven/control-plane/internal/auth"
	"github.com/agentoven/agentoven/control-plane/internal/catalog"
	"github.com/agentoven/agentoven/control-plane/internal/config"
	"github.com/agentoven/agentoven/control-plane/internal/embeddings"
	"github.com/agentoven/agentoven/control-plane/internal/mcpgw"
	"github.com/agentoven/agentoven/control-plane/internal/notify"
	ragpkg "github.com/agentoven/agentoven/control-plane/internal/rag"
	"github.com/agentoven/agentoven/control-plane/internal/retention"
	modelrouter "github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/sessions"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/internal/telemetry"
	"github.com/agentoven/agentoven/control-plane/internal/vectorstore"
	"github.com/agentoven/agentoven/control-plane/internal/workflow"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"

	"github.com/rs/zerolog/log"
)

// Config is the public configuration for the control plane server.
type Config struct {
	Port            int
	Version         string
	OTELEnabled     bool
	OTELEndpoint    string
	ServiceName     string
}

// Server holds the initialized AgentOven control plane.
type Server struct {
	// Handler is the HTTP handler with all routes and middleware.
	Handler http.Handler

	// Store is the data store (in-memory for OSS).
	// Exposed so Pro can use it in TierEnforcer and other middleware.
	Store store.Store

	// Router is the model router instance.
	// Exposed so Pro can call RegisterDriver() to add enterprise drivers.
	Router *modelrouter.ModelRouter

	// Notifier is the notification service.
	// Exposed so Pro can call RegisterDriver() to add Slack, Teams, etc.
	Notifier *notify.Service

	// Handlers is the HTTP handler collection.
	// Exposed so Pro can swap PromptValidator or other dependencies.
	Handlers *handlers.Handlers

	// RAGHandlers holds RAG/embedding/vectorstore HTTP handlers.
	RAGHandlers *handlers.RAGHandlers

	// EmbeddingRegistry holds registered embedding drivers.
	// Exposed so Pro can call Register() to add enterprise drivers.
	EmbeddingRegistry *embeddings.Registry

	// VectorStoreRegistry holds registered vector store drivers.
	// Exposed so Pro can call Register() to add enterprise drivers.
	VectorStoreRegistry *vectorstore.Registry

	// Config is the server configuration.
	Config *Config

	// Port is the port the server should listen on.
	Port int

	// RetentionJanitor runs periodic data retention cleanup.
	RetentionJanitor *retention.Janitor

	// PlanResolver resolves Kitchen → PlanLimits.
	// Exposed so Pro can replace with license-aware resolver.
	PlanResolver contracts.PlanResolver

	// TierEnforcer is HTTP middleware for quota enforcement.
	// OSS: no-op pass-through. Pro: checks plan limits.
	TierEnforcer contracts.TierEnforcer

	// AuthChain is the pluggable authentication provider chain.
	// OSS registers API key + service account providers.
	// Pro adds OIDC, SAML, LDAP, mTLS providers via RegisterProvider().
	AuthChain *aoauth.ProviderChain

	// Catalog is the live model capability database.
	// Exposed so Pro can trigger refreshes or register custom models.
	Catalog *catalog.Catalog

	// SessionStore manages multi-turn conversation sessions.
	// Exposed so Pro can replace with Redis/PostgreSQL-backed store.
	SessionStore contracts.SessionStore

	// retentionCancel cancels the retention janitor goroutine.
	retentionCancel context.CancelFunc

	// ShutdownFunc should be called on graceful shutdown to flush telemetry.
	ShutdownFunc func(context.Context) error
}

// LoadConfig loads configuration from environment variables.
func LoadConfig() *Config {
	cfg := config.Load()
	return &Config{
		Port:         cfg.Port,
		Version:      cfg.Version,
		OTELEnabled:  cfg.Telemetry.Enabled,
		OTELEndpoint: cfg.Telemetry.OTLPEndpoint,
		ServiceName:  cfg.Telemetry.ServiceName,
	}
}

// New initializes all OSS control plane components and returns a ready Server.
// This is the primary entry point for both OSS and Pro main.go.
func New(ctx context.Context) (*Server, error) {
	return NewWithConfig(ctx, LoadConfig())
}

// NewWithConfig initializes the control plane with an explicit configuration.
func NewWithConfig(ctx context.Context, pubCfg *Config) (*Server, error) {
	// Build internal config from public config
	cfg := config.Load()
	if pubCfg.Port > 0 {
		cfg.Port = pubCfg.Port
	}

	// Initialize telemetry
	shutdown, err := telemetry.Init(cfg.Telemetry)
	if err != nil {
		return nil, fmt.Errorf("init telemetry: %w", err)
	}

	// OSS uses in-memory store (single kitchen, zero configuration)
	dataStore := store.NewMemoryStore()
	log.Info().Msg("✅ In-memory store initialized")

	return buildServer(ctx, cfg, pubCfg, dataStore, shutdown)
}

// NewWithStore initializes the control plane with an externally-provided store.
// This is the primary entry point for Pro, which passes its PostgresStore.
// The caller is responsible for running migrations and closing the store.
func NewWithStore(ctx context.Context, dataStore store.Store) (*Server, error) {
	return NewWithStoreAndConfig(ctx, dataStore, LoadConfig())
}

// NewWithStoreAndConfig initializes the control plane with an external store and explicit config.
func NewWithStoreAndConfig(ctx context.Context, dataStore store.Store, pubCfg *Config) (*Server, error) {
	cfg := config.Load()
	if pubCfg.Port > 0 {
		cfg.Port = pubCfg.Port
	}

	shutdown, err := telemetry.Init(cfg.Telemetry)
	if err != nil {
		return nil, fmt.Errorf("init telemetry: %w", err)
	}

	log.Info().Msg("✅ External store provided")

	return buildServer(ctx, cfg, pubCfg, dataStore, shutdown)
}

// buildServer is the shared constructor that wires all services.
func buildServer(ctx context.Context, cfg *config.Config, pubCfg *Config, dataStore store.Store, shutdown func(context.Context) error) (*Server, error) {

	// Seed default kitchen
	seedDefaultKitchen(ctx, dataStore)

	// Initialize services
	mr := modelrouter.NewModelRouter(dataStore)
	gw := mcpgw.NewGateway(dataStore)
	ns := notify.NewService(dataStore)
	wf := workflow.NewEngine(dataStore, ns)

	log.Info().Msg("✅ Model Router initialized")
	log.Info().Msg("✅ MCP Gateway initialized")
	log.Info().Msg("✅ Notification Service initialized")
	log.Info().Msg("✅ Workflow Engine initialized")

	// ── Model Catalog (Release 8) ──────────────────────────
	cat := catalog.NewCatalog("")
	cat.Start(ctx)
	log.Info().Msg("✅ Model catalog initialized")

	// ── Session Store (Release 8) ───────────────────────────
	sessStore := sessions.NewMemorySessionStore()
	log.Info().Msg("✅ Session store initialized (in-memory)")

	// Build handlers + API router
	h := handlers.New(dataStore, mr, gw, wf, cat, sessStore)

	// ── Pluggable Auth (Release 7) ─────────────────────────
	// Build the auth provider chain. Pro adds enterprise providers
	// (OIDC, SAML, LDAP, mTLS) by calling AuthChain.RegisterProvider()
	// on the returned Server struct.
	authChain := aoauth.NewProviderChain()

	// Register OSS auth providers (API key + service account)
	apiKeyProvider := aoauth.NewAPIKeyProvider()
	if apiKeyProvider.Enabled() {
		authChain.RegisterProvider(apiKeyProvider)
	}
	svcAcctProvider := aoauth.NewServiceAccountProvider()
	if svcAcctProvider.Enabled() {
		authChain.RegisterProvider(svcAcctProvider)
	}

	// ── RAG & Intelligence (Release 5) ──────────────────────

	// Initialize embedding registry.
	// Provider-first model: embeddings are auto-discovered from configured
	// model providers. If a provider's driver implements EmbeddingCapableDriver,
	// its embedding models are automatically available — no separate config needed.
	embReg := embeddings.NewRegistry()

	// Auto-discover embeddings from providers in the default kitchen.
	// When a provider (e.g. OpenAI) is configured with an API key,
	// its embedding capabilities are automatically registered.
	providers, _ := dataStore.ListProviders(ctx)
	for i := range providers {
		p := &providers[i]
		ecd, embModels := mr.DiscoverEmbeddingsForProvider(p)
		if ecd == nil {
			continue
		}
		// Register the first (default) embedding model from each provider.
		// All models remain discoverable via the API for explicit selection.
		if len(embModels) > 0 {
			adapter := embeddings.NewProviderEmbeddingAdapter(ecd, p, embModels[0])
			regName := fmt.Sprintf("%s:%s", p.Kind, p.Name)
			embReg.Register(regName, adapter)
			log.Info().
				Str("provider", p.Name).
				Str("kind", p.Kind).
				Str("model", embModels[0].Model).
				Int("dims", embModels[0].Dimensions).
				Msg("✅ Embedding auto-discovered from provider")
		}
	}

	// Fallback: env-var-based embedding registration for users who haven't
	// configured providers via the API yet. These will be superseded by
	// provider-discovered drivers if both exist.
	if len(embReg.List()) == 0 {
		if apiKey := os.Getenv("OPENAI_API_KEY"); apiKey != "" {
			model := os.Getenv("AGENTOVEN_EMBEDDING_MODEL")
			if model == "" {
				model = "text-embedding-3-small"
			}
			embReg.Register("openai", embeddings.NewOpenAIDriver(apiKey, model))
			log.Info().Msg("ℹ️  Embedding registered via OPENAI_API_KEY env var (configure a provider for auto-discovery)")
		}

		ollamaURL := os.Getenv("OLLAMA_URL")
		if ollamaURL == "" {
			ollamaURL = os.Getenv("OLLAMA_HOST")
		}
		if ollamaURL != "" {
			model := os.Getenv("AGENTOVEN_OLLAMA_EMBED_MODEL")
			if model == "" {
				model = "nomic-embed-text"
			}
			embReg.Register("ollama", embeddings.NewOllamaDriver(ollamaURL, model))
			log.Info().Msg("ℹ️  Embedding registered via OLLAMA_URL env var (configure a provider for auto-discovery)")
		}
	}

	// Initialize vector store registry with embedded (in-memory) driver
	vsReg := vectorstore.NewRegistry()
	embeddedVS := vectorstore.NewEmbeddedStore()
	vsReg.Register("embedded", embeddedVS)
	log.Info().Msg("✅ Embedded vector store registered (in-memory, 50K max)")

	// Auto-register pgvector if connection URL is set
	// Users bring their own PG + vector extension
	if pgURL := os.Getenv("AGENTOVEN_PGVECTOR_URL"); pgURL != "" {
		dims := 1536 // default for OpenAI text-embedding-3-small
		pgvs, err := vectorstore.NewPgvectorStore(ctx, pgURL, dims)
		if err != nil {
			log.Warn().Err(err).Msg("⚠️  pgvector store init failed, using embedded only")
		} else {
			vsReg.Register("pgvector", pgvs)
			log.Info().Msg("✅ pgvector store registered")
		}
	}

	// Build RAG pipeline (uses first available embedding driver + default vector store)
	var ragPipeline *ragpkg.Pipeline
	var ragIngester *ragpkg.Ingester
	embDriverNames := embReg.List()
	if len(embDriverNames) > 0 {
		defaultEmb, _ := embReg.Get(embDriverNames[0])
		defaultVS, _ := vsReg.Get("embedded")
		ragPipeline = ragpkg.NewPipeline(defaultEmb, defaultVS, mr)
		ragIngester = ragpkg.NewIngester(defaultEmb, defaultVS, ragpkg.DefaultChunkerConfig())
		log.Info().Str("embedding", embDriverNames[0]).Msg("✅ RAG pipeline initialized")
	} else {
		log.Info().Msg("ℹ️  No embedding drivers configured — RAG pipeline disabled (set OPENAI_API_KEY or OLLAMA_URL)")
	}

	rh := &handlers.RAGHandlers{
		Embeddings:  embReg,
		VectorStore: vsReg,
		Pipeline:    ragPipeline,
		Ingester:    ragIngester,
	}

	router := api.NewRouter(cfg, h, rh, authChain)

	// Start retention janitor (runs every 6 hours)
	janitor := retention.NewJanitor(dataStore, 6*time.Hour)

	// Register OSS default archive driver (local JSONL files)
	localArchiver := retention.NewLocalFileArchiver("", true) // default path, gzip on
	janitor.RegisterArchiver(localArchiver)
	log.Info().Str("driver", localArchiver.Kind()).Msg("✅ Local file archiver registered")

	retCtx, retCancel := context.WithCancel(context.Background())
	go janitor.Start(retCtx)

	// Community plan resolver + tier enforcer (no-op in OSS).
	// Pro overrides these on the returned Server struct before starting.
	planResolver := &contracts.CommunityPlanResolver{}
	tierEnforcer := &contracts.CommunityTierEnforcer{}

	return &Server{
		Handler:             router,
		Store:               dataStore,
		Router:              mr,
		Notifier:            ns,
		Handlers:            h,
		RAGHandlers:         rh,
		EmbeddingRegistry:   embReg,
		VectorStoreRegistry: vsReg,
		Config:              pubCfg,
		Port:                cfg.Port,
		RetentionJanitor:    janitor,
		PlanResolver:        planResolver,
		TierEnforcer:        tierEnforcer,
		AuthChain:           authChain,
		Catalog:             cat,
		SessionStore:        sessStore,
		retentionCancel:     retCancel,
		ShutdownFunc:        shutdown,
	}, nil
}

func seedDefaultKitchen(ctx context.Context, s store.Store) {
	_, err := s.GetKitchen(ctx, "default")
	if err != nil {
		k := &models.Kitchen{
			ID:          "default",
			Name:        "Default Kitchen",
			Description: "The default workspace",
			Owner:       "system",
			Plan:        models.PlanCommunity,
			CreatedAt:   time.Now().UTC(),
		}
		if err := s.CreateKitchen(ctx, k); err != nil {
			log.Warn().Err(err).Msg("Failed to seed default kitchen")
		} else {
			log.Info().Msg("✅ Default kitchen seeded")
		}
	}
}

// Shutdown stops all background goroutines (retention janitor, etc.)
// and flushes telemetry. Should be called on graceful shutdown.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.retentionCancel != nil {
		s.retentionCancel()
	}
	if s.Catalog != nil {
		s.Catalog.Stop()
	}
	if s.ShutdownFunc != nil {
		return s.ShutdownFunc(ctx)
	}
	return nil
}
