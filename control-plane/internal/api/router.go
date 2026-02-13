package api

import (
	"encoding/json"
	"net/http"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/internal/api/middleware"
	"github.com/agentoven/agentoven/control-plane/internal/config"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

// NewRouter creates the HTTP router with all API routes.
func NewRouter(cfg *config.Config) http.Handler {
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
			r.Get("/", handlers.ListAgents)
			r.Post("/", handlers.RegisterAgent)
			r.Route("/{agentName}", func(r chi.Router) {
				r.Get("/", handlers.GetAgent)
				r.Put("/", handlers.UpdateAgent)
				r.Delete("/", handlers.DeleteAgent)
				r.Post("/bake", handlers.BakeAgent)
				r.Post("/cool", handlers.CoolAgent)

				// Agent versions
				r.Route("/versions", func(r chi.Router) {
					r.Get("/", handlers.ListAgentVersions)
					r.Get("/{version}", handlers.GetAgentVersion)
				})
			})
		})

		// Recipes (workflows)
		r.Route("/recipes", func(r chi.Router) {
			r.Get("/", handlers.ListRecipes)
			r.Post("/", handlers.CreateRecipe)
			r.Route("/{recipeName}", func(r chi.Router) {
				r.Get("/", handlers.GetRecipe)
				r.Put("/", handlers.UpdateRecipe)
				r.Delete("/", handlers.DeleteRecipe)
				r.Post("/bake", handlers.BakeRecipe)
				r.Get("/history", handlers.RecipeHistory)
			})
		})

		// Model Router
		r.Route("/models", func(r chi.Router) {
			r.Get("/providers", handlers.ListProviders)
			r.Post("/route", handlers.RouteModel)
			r.Get("/cost", handlers.GetCostSummary)
		})

		// Traces & Observability
		r.Route("/traces", func(r chi.Router) {
			r.Get("/", handlers.ListTraces)
			r.Get("/{traceId}", handlers.GetTrace)
		})

		// Kitchens (workspaces)
		r.Route("/kitchens", func(r chi.Router) {
			r.Get("/", handlers.ListKitchens)
			r.Post("/", handlers.CreateKitchen)
			r.Get("/{kitchenId}", handlers.GetKitchen)
		})
	})

	// A2A Gateway — agent-to-agent protocol endpoint
	r.Route("/a2a", func(r chi.Router) {
		r.Post("/", handlers.A2AEndpoint)
		r.Get("/.well-known/agent-card.json", handlers.ServeAgentCard)
	})

	// Per-agent A2A endpoints
	r.Route("/agents/{agentName}/a2a", func(r chi.Router) {
		r.Post("/", handlers.A2AAgentEndpoint)
		r.Get("/.well-known/agent-card.json", handlers.ServeAgentSpecificCard)
	})

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
