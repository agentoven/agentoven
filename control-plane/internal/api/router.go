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

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

// NewRouter creates the HTTP router with all API routes.
// Phase 2: accepts *handlers.Handlers with store/router/gateway/engine deps.
func NewRouter(cfg *config.Config, h *handlers.Handlers) http.Handler {
	r := chi.NewRouter()

	// Global middleware
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Recoverer)
	r.Use(chimw.Compress(5))
	r.Use(middleware.Logger)
	r.Use(middleware.TenantExtractor)
	r.Use(middleware.Telemetry)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Kitchen-Id", "X-Request-Id"},
		ExposedHeaders:   []string{"X-Request-Id", "X-Trace-Id"},
		AllowCredentials: true,
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
				r.Post("/cool", h.CoolAgent)
				r.Post("/rewarm", h.RewarmAgent)
				r.Post("/test", h.TestAgent)

				// Agent versions
				r.Route("/versions", func(r chi.Router) {
					r.Get("/", h.ListAgentVersions)
					r.Get("/{version}", h.GetAgentVersion)
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
			r.Get("/cost", h.GetCostSummary)

			// Provider CRUD
			r.Route("/providers", func(r chi.Router) {
				r.Get("/", h.ListProviders)
				r.Post("/", h.CreateProvider)
				r.Route("/{providerName}", func(r chi.Router) {
					r.Get("/", h.GetProvider)
					r.Put("/", h.UpdateProvider)
					r.Delete("/", h.DeleteProvider)
					r.Post("/test", h.TestProvider)
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
