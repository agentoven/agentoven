package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/internal/api/middleware"
	"github.com/agentoven/agentoven/control-plane/internal/config"
	"github.com/agentoven/agentoven/control-plane/internal/integrations/langchain"
	"github.com/agentoven/agentoven/control-plane/internal/integrations/langfuse"
	"github.com/agentoven/agentoven/control-plane/internal/integrations/picoclaw"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

// NewRouter creates the HTTP router with all API routes.
// Phase 2: accepts *handlers.Handlers with store/router/gateway/engine deps.
// Phase 5 (RAG): accepts optional *handlers.RAGHandlers for RAG/embedding/vectorstore routes.
// Phase 7 (Auth): accepts AuthProviderChain for pluggable authentication.
func NewRouter(cfg *config.Config, h *handlers.Handlers, rh *handlers.RAGHandlers, authChain contracts.AuthProviderChain) http.Handler {
	r := chi.NewRouter()

	// Global middleware
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Recoverer)
	r.Use(chimw.Compress(5))
	r.Use(middleware.Logger)
	r.Use(middleware.TenantExtractor)
	r.Use(middleware.Telemetry)

	// Pluggable auth middleware (R7) — replaces old API key middleware.
	// The auth chain walks registered providers (API key, service account, OIDC, SAML, etc.)
	// and stores the resulting Identity in context for RBAC and handlers.
	if authChain != nil {
		authMW := middleware.NewAuthMiddleware(authChain)
		r.Use(authMW.Handler)
	}

	// CORS — configurable via AGENTOVEN_CORS_ORIGINS env var.
	// ISS-022 fix: when using wildcard origins, AllowCredentials must be false
	// to comply with the Fetch specification and prevent credential-leak attacks.
	corsOrigins := parseCORSOrigins()
	isWildcard := len(corsOrigins) == 1 && corsOrigins[0] == "*"
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   corsOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Kitchen-Id", "X-Request-Id", "X-API-Key", "X-Service-Token"},
		ExposedHeaders:   []string{"X-Request-Id", "X-Trace-Id"},
		AllowCredentials: !isWildcard, // safe: only allow credentials with explicit origins
		MaxAge:           300,
	}))

	// Health & info
	r.Get("/health", healthHandler)
	r.Get("/version", versionHandler(cfg))

	// API v1
	r.Route("/api/v1", func(r chi.Router) {
		// Agents (the Menu)
		r.Route("/agents", func(r chi.Router) {
			r.Get("/", h.ListAgents)
			r.Post("/", h.RegisterAgent)
			r.Route("/{agentName}", func(r chi.Router) {
				r.Get("/", h.GetAgent)
				r.Put("/", h.UpdateAgent)
				r.Delete("/", h.DeleteAgent)
				r.Post("/bake", h.BakeAgent)
				r.Post("/recook", h.RecookAgent)
				r.Post("/cool", h.CoolAgent)
				r.Post("/rewarm", h.RewarmAgent)
				r.Post("/test", h.TestAgent)
				r.Get("/config", h.GetAgentConfig)
				r.Post("/invoke", h.InvokeAgent)

				// Agent card (A2A-compatible metadata)
				r.Get("/card", h.GetAgentCard)

				// Agent versions
				r.Route("/versions", func(r chi.Router) {
					r.Get("/", h.ListAgentVersions)
					r.Get("/{version}", h.GetAgentVersion)
				})

				// Sessions — multi-turn conversations (R8)
				r.Route("/sessions", func(r chi.Router) {
					r.Get("/", h.ListSessions)
					r.Post("/", h.CreateSession)
					r.Route("/{sessionID}", func(r chi.Router) {
						r.Get("/", h.GetSession)
						r.Delete("/", h.DeleteSession)
						r.Post("/messages", h.SendSessionMessage)
					})
				})
			})
		})

		// Recipes (workflows)
		r.Route("/recipes", func(r chi.Router) {
			r.Get("/", h.ListRecipes)
			r.Post("/", h.CreateRecipe)
			r.Route("/{recipeName}", func(r chi.Router) {
				r.Get("/", h.GetRecipe)
				r.Put("/", h.UpdateRecipe)
				r.Delete("/", h.DeleteRecipe)
				r.Post("/bake", h.BakeRecipe)
				r.Get("/history", h.RecipeHistory)

				// Recipe runs
				r.Route("/runs", func(r chi.Router) {
					r.Get("/", h.RecipeHistory) // alias
					r.Route("/{runId}", func(r chi.Router) {
						r.Get("/", h.GetRecipeRun)
						r.Post("/cancel", h.CancelRecipeRun)
						r.Post("/gates/{stepName}/approve", h.ApproveGate)
					})
				})
			})
		})

		// Model Router
		r.Route("/models", func(r chi.Router) {
			r.Post("/route", h.RouteModel)
			r.Post("/route/stream", h.RouteModelStream)
			r.Get("/cost", h.GetCostSummary)

			// Model Catalog — live model capability database (R8)
			r.Route("/catalog", func(r chi.Router) {
				r.Get("/", h.ListCatalog)
				r.Post("/refresh", h.RefreshCatalog)
				r.Get("/{modelID}", h.GetCatalogModel)
			})

			// Discovery — which drivers support model listing (R8)
			r.Get("/discovery/drivers", h.ListDiscoveryDrivers)

			// Provider CRUD
			r.Route("/providers", func(r chi.Router) {
				r.Get("/", h.ListProviders)
				r.Post("/", h.CreateProvider)
				r.Route("/{providerName}", func(r chi.Router) {
					r.Get("/", h.GetProvider)
					r.Put("/", h.UpdateProvider)
					r.Delete("/", h.DeleteProvider)
					r.Post("/test", h.TestProvider)
					r.Post("/discover", h.DiscoverModels) // R8: trigger model discovery
				})
			})
		})

		// MCP Gateway — tool management
		r.Route("/tools", func(r chi.Router) {
			r.Get("/", h.ListMCPTools)
			r.Post("/", h.RegisterMCPTool)
			r.Route("/{toolName}", func(r chi.Router) {
				r.Get("/", h.GetMCPTool)
				r.Put("/", h.UpdateMCPTool)
				r.Delete("/", h.DeleteMCPTool)
			})
		})

		// Prompt Store — versioned prompt management
		r.Route("/prompts", func(r chi.Router) {
			r.Get("/", h.ListPrompts)
			r.Post("/", h.CreatePrompt)
			r.Route("/{promptName}", func(r chi.Router) {
				r.Get("/", h.GetPrompt)
				r.Put("/", h.UpdatePrompt)
				r.Delete("/", h.DeletePrompt)
				r.Post("/validate", h.ValidatePrompt)
				r.Get("/versions", h.ListPromptVersions)
				r.Get("/versions/{version}", h.GetPromptVersion)
			})
		})

		// Kitchen Settings — per-kitchen configuration (API keys, validation config)
		r.Route("/settings", func(r chi.Router) {
			r.Get("/", h.GetKitchenSettings)
			r.Put("/", h.UpdateKitchenSettings)
		})

		// Approvals — durable human gate approvals
		r.Route("/approvals", func(r chi.Router) {
			r.Get("/", h.ListApprovals)
			r.Get("/{gateKey}", h.GetApproval)
			r.Post("/{runId}/{stepName}", h.ApproveGateWithMetadata)
		})

		// Notification Channels — per-kitchen notification configuration
		r.Route("/channels", func(r chi.Router) {
			r.Get("/", h.ListChannels)
			r.Post("/", h.CreateChannel)
			r.Route("/{channelName}", func(r chi.Router) {
				r.Get("/", h.GetChannel)
				r.Put("/", h.UpdateChannel)
				r.Delete("/", h.DeleteChannel)
			})
		})

		// Audit Events — read-only (events are created by middleware)
		r.Route("/audit", func(r chi.Router) {
			r.Get("/", h.ListAuditEvents)
			r.Get("/count", h.CountAuditEvents)
		})

		// Traces & Observability
		r.Route("/traces", func(r chi.Router) {
			r.Get("/", h.ListTraces)
			r.Get("/{traceId}", h.GetTrace)
		})

		// Kitchens — OSS is single-kitchen (read-only, "default" kitchen only)
		r.Route("/kitchens", func(r chi.Router) {
			r.Get("/", h.ListKitchens)
			r.Get("/{kitchenId}", h.GetKitchen)
		})

		// ── RAG & Intelligence (Release 5) ──────────────────

		// RAG Pipeline — query and ingest
		if rh != nil {
			r.Route("/rag", func(r chi.Router) {
				r.Post("/query", rh.RAGQuery)
				r.Post("/ingest", rh.RAGIngest)
			})

			// Embedding Drivers — list and invoke
			r.Route("/embeddings", func(r chi.Router) {
				r.Get("/", rh.ListEmbeddingDrivers)
				r.Get("/health", rh.EmbeddingHealth)
				r.Post("/{driver}/embed", rh.EmbedText)
			})

			// Vector Store Drivers — list and health
			r.Route("/vectorstores", func(r chi.Router) {
				r.Get("/", rh.ListVectorStoreDrivers)
				r.Get("/health", rh.VectorStoreHealth)
			})

			// Data Connectors — Pro feature placeholder
			r.Route("/connectors", func(r chi.Router) {
				r.Get("/", rh.ListConnectors)
			})
		}

		// Guardrails — list available guardrail kinds + configuration
		r.Route("/guardrails", func(r chi.Router) {
			r.Get("/kinds", h.ListGuardrailKinds)
		})
	})

	// MCP Gateway — JSON-RPC endpoint
	r.Route("/mcp", func(r chi.Router) {
		r.Post("/", h.MCPEndpoint)
		r.Get("/sse", h.MCPSSEEndpoint)
	})

	// A2A Gateway — agent-to-agent protocol endpoint
	r.Route("/a2a", func(r chi.Router) {
		r.Post("/", h.A2AEndpoint)
		r.Get("/.well-known/agent-card.json", h.ServeAgentCard)
	})

	// Per-agent A2A endpoints
	r.Route("/agents/{agentName}/a2a", func(r chi.Router) {
		r.Post("/", h.A2AAgentEndpoint)
		r.Get("/.well-known/agent-card.json", h.ServeAgentSpecificCard)
	})

	// ── Integration Endpoints ────────────────────────────────

	// LangChain adapter — expose AgentOven agents as LangChain tools
	lcAdapter := langchain.NewAdapter(h.Store)
	r.Route("/langchain", func(r chi.Router) {
		r.Get("/tools", lcAdapter.HandleListTools)
		r.Post("/invoke", lcAdapter.HandleInvoke)
	})

	// LangFuse bridge — bidirectional trace exchange with LangFuse
	lfBaseURL := os.Getenv("LANGFUSE_BASE_URL")
	lfPublicKey := os.Getenv("LANGFUSE_PUBLIC_KEY")
	lfSecretKey := os.Getenv("LANGFUSE_SECRET_KEY")
	lfBridge := langfuse.NewBridge(h.Store, lfBaseURL, lfPublicKey, lfSecretKey)
	r.Route("/langfuse", func(r chi.Router) {
		r.Post("/ingest", lfBridge.HandleIngest)   // import LangFuse traces
		r.Post("/export", lfBridge.HandleExport)    // export AgentOven traces
	})

	// PicoClaw adapter — IoT agent management, A2A relay, chat gateways, heartbeat
	pcAdapter := picoclaw.NewAdapter(h.Store)
	pcGateway := picoclaw.NewGatewayManager(h.Store, pcAdapter)
	r.Route("/picoclaw", func(r chi.Router) {
		// Instance management
		r.Get("/instances", pcAdapter.HandleListInstances)
		r.Post("/instances", pcAdapter.HandleRegister)

		// Task relay to PicoClaw instances
		r.Post("/relay", pcAdapter.HandleRelay)

		// Heartbeat / health check
		r.Get("/health", pcAdapter.HandleHealthCheck)

		// Chat gateway management
		r.Get("/gateways", pcGateway.HandleListDrivers)
		r.Post("/gateways", pcGateway.HandleCreateGateway)
		r.Delete("/gateways", pcGateway.HandleStopGateway)
	})

	// Dashboard UI — serve from dashboard/dist (SPA with fallback to index.html)
	dashboardDir := findDashboardDir()
	if dashboardDir != "" {
		fileServer := http.FileServer(http.Dir(dashboardDir))
		r.Get("/*", func(w http.ResponseWriter, req *http.Request) {
			// If the file exists, serve it; otherwise serve index.html (SPA routing)
			path := filepath.Join(dashboardDir, strings.TrimPrefix(req.URL.Path, "/"))
			if _, err := os.Stat(path); os.IsNotExist(err) {
				http.ServeFile(w, req, filepath.Join(dashboardDir, "index.html"))
				return
			}
			fileServer.ServeHTTP(w, req)
		})
	}

	return r
}

// parseCORSOrigins reads allowed CORS origins from the environment.
// Default: wildcard (open access, no credentials).
// Production: set AGENTOVEN_CORS_ORIGINS to a comma-separated list.
//
// Examples:
//
//	AGENTOVEN_CORS_ORIGINS=https://agentoven.dev,https://docs.agentoven.dev,http://localhost:5173
//	AGENTOVEN_CORS_ORIGINS=*  (default — open access, credentials disabled)
func parseCORSOrigins() []string {
	originsEnv := os.Getenv("AGENTOVEN_CORS_ORIGINS")
	if originsEnv == "" {
		// Default: wildcard (safe with AllowCredentials=false)
		return []string{"*"}
	}

	var origins []string
	for _, o := range strings.Split(originsEnv, ",") {
		o = strings.TrimSpace(o)
		if o != "" {
			origins = append(origins, o)
		}
	}
	if len(origins) == 0 {
		return []string{"*"}
	}
	return origins
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]string{
		"status": "healthy",
		"service": "agentoven-control-plane",
	})
}

func versionHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"version": cfg.Version,
			"service": "agentoven-control-plane",
		})
	}
}

// findDashboardDir looks for the built dashboard UI in several locations.
func findDashboardDir() string {
	candidates := []string{}

	// Highest priority: explicit env var
	if envDir := os.Getenv("AGENTOVEN_DASHBOARD_DIR"); envDir != "" {
		candidates = append(candidates, envDir)
	}

	// Check relative to the binary itself (Homebrew / packaged installs).
	// This runs before CWD-relative paths so packaged installs always win.
	if exe, err := os.Executable(); err == nil {
		// Use the raw (possibly symlinked) path first, then resolved path
		rawDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(rawDir, "..", "share", "agentoven", "dashboard"),
		)

		// Follow symlinks (brew symlinks /opt/homebrew/bin → Cellar/)
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			resolvedDir := filepath.Dir(resolved)
			if resolvedDir != rawDir {
				candidates = append(candidates,
					filepath.Join(resolvedDir, "..", "share", "agentoven", "dashboard"),
				)
			}
		}
	}

	// CWD-relative paths (dev mode)
	candidates = append(candidates,
		"dashboard/dist",               // running from control-plane/ dir
		"../dashboard/dist",            // running from control-plane/cmd/server/
		"control-plane/dashboard/dist", // running from repo root
	)

	for _, dir := range candidates {
		abs, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		// Verify the dir exists AND contains index.html
		if info, err := os.Stat(abs); err == nil && info.IsDir() {
			if _, err := os.Stat(filepath.Join(abs, "index.html")); err == nil {
				return abs
			}
		}
	}
	return ""
}
