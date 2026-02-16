// Package handlers implements the HTTP handlers for the AgentOven control plane.
// Phase 2: All handlers use the Store interface (PostgreSQL-backed) instead
// of in-memory maps. New handlers added for Model Router, MCP Gateway, and
// Workflow Engine.
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/api/middleware"
	"github.com/agentoven/agentoven/control-plane/internal/mcpgw"
	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/internal/workflow"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// Handlers holds all handler dependencies.
type Handlers struct {
	Store      store.Store
	Router     *router.ModelRouter
	MCPGateway *mcpgw.Gateway
	Workflow   *workflow.Engine
}

// New creates a new Handlers instance with all dependencies.
func New(s store.Store, mr *router.ModelRouter, gw *mcpgw.Gateway, wf *workflow.Engine) *Handlers {
	return &Handlers{
		Store:      s,
		Router:     mr,
		MCPGateway: gw,
		Workflow:   wf,
	}
}

// ══════════════════════════════════════════════════════════════
// ── Agent Handlers ───────────────────────────────────────────
// ══════════════════════════════════════════════════════════════

func (h *Handlers) ListAgents(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	agents, err := h.Store.ListAgents(r.Context(), kitchen)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if agents == nil {
		agents = []models.Agent{}
	}
	respondJSON(w, http.StatusOK, agents)
}

func (h *Handlers) RegisterAgent(w http.ResponseWriter, r *http.Request) {
	var req models.Agent
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	kitchen := middleware.GetKitchen(r.Context())
	req.ID = uuid.New().String()
	req.Status = models.AgentStatusDraft
	req.Kitchen = kitchen
	req.CreatedAt = time.Now().UTC()
	req.UpdatedAt = time.Now().UTC()
	req.A2AEndpoint = "/agents/" + req.Name + "/a2a"

	if err := h.Store.CreateAgent(r.Context(), &req); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	log.Info().Str("agent", req.Name).Str("id", req.ID).Str("kitchen", kitchen).Msg("Agent registered")
	respondJSON(w, http.StatusCreated, req)
}

func (h *Handlers) GetAgent(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	kitchen := middleware.GetKitchen(r.Context())

	agent, err := h.Store.GetAgent(r.Context(), kitchen, agentName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	respondJSON(w, http.StatusOK, agent)
}

func (h *Handlers) UpdateAgent(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	kitchen := middleware.GetKitchen(r.Context())

	agent, err := h.Store.GetAgent(r.Context(), kitchen, agentName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	var req models.Agent
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.Description != "" {
		agent.Description = req.Description
	}
	if req.Framework != "" {
		agent.Framework = req.Framework
	}
	if req.ModelProvider != "" {
		agent.ModelProvider = req.ModelProvider
	}
	if req.ModelName != "" {
		agent.ModelName = req.ModelName
	}
	if len(req.Ingredients) > 0 {
		agent.Ingredients = req.Ingredients
	}
	if len(req.Skills) > 0 {
		agent.Skills = req.Skills
	}
	if len(req.Tags) > 0 {
		if agent.Tags == nil {
			agent.Tags = map[string]string{}
		}
		for k, v := range req.Tags {
			agent.Tags[k] = v
		}
	}
	agent.UpdatedAt = time.Now().UTC()

	if err := h.Store.UpdateAgent(r.Context(), agent); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, agent)
}

func (h *Handlers) DeleteAgent(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	kitchen := middleware.GetKitchen(r.Context())

	if err := h.Store.DeleteAgent(r.Context(), kitchen, agentName); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	log.Info().Str("agent", agentName).Str("kitchen", kitchen).Msg("Agent retired")
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) BakeAgent(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	kitchen := middleware.GetKitchen(r.Context())

	agent, err := h.Store.GetAgent(r.Context(), kitchen, agentName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	var req struct {
		Version     string `json:"version"`
		Environment string `json:"environment"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	// ── Validate ingredients before baking ───────────────────────
	var errors []string

	// Must have a model provider configured (via top-level field or model ingredient)
	hasModel := agent.ModelProvider != ""
	for _, ing := range agent.Ingredients {
		if ing.Kind == models.IngredientModel {
			hasModel = true
			break
		}
	}
	if !hasModel {
		errors = append(errors, "Agent must have a model provider configured (set model_provider or add a model ingredient)")
	}

	// Validate referenced provider exists and has an API key
	if agent.ModelProvider != "" {
		provider, err := h.Store.GetProvider(r.Context(), agent.ModelProvider)
		if err != nil {
			errors = append(errors, fmt.Sprintf("Model provider '%s' not found — register it first", agent.ModelProvider))
		} else if provider.Kind != "ollama" {
			if provider.Config == nil || provider.Config["api_key"] == nil || provider.Config["api_key"] == "" {
				errors = append(errors, fmt.Sprintf("Model provider '%s' has no API key configured", agent.ModelProvider))
			}
		}
	}

	// Validate referenced tools exist and are enabled
	for _, ing := range agent.Ingredients {
		if ing.Kind == models.IngredientTool {
			tool, err := h.Store.GetTool(r.Context(), kitchen, ing.Name)
			if err != nil {
				errors = append(errors, fmt.Sprintf("Tool ingredient '%s' not found — register it first", ing.Name))
			} else if !tool.Enabled {
				errors = append(errors, fmt.Sprintf("Tool '%s' is disabled — enable it before baking", ing.Name))
			}
		}
	}

	if len(errors) > 0 {
		respondJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error":   "Agent cannot be baked — missing or invalid ingredients",
			"details": errors,
		})
		return
	}

	agent.Status = models.AgentStatusBaking
	agent.UpdatedAt = time.Now().UTC()
	if req.Version != "" {
		agent.Version = req.Version
	}
	agent.A2AEndpoint = "/agents/" + agentName + "/a2a"

	if err := h.Store.UpdateAgent(r.Context(), agent); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	log.Info().
		Str("agent", agentName).
		Str("version", agent.Version).
		Str("environment", req.Environment).
		Msg("Agent baking started")

	// Validate provider connectivity async, then mark ready or burnt
	go func() {
		time.Sleep(1 * time.Second)

		// Health-check the provider
		if agent.ModelProvider != "" {
			results := h.Router.HealthCheck(context.Background())
			if healthy, found := results[agent.ModelProvider]; found && !healthy {
				agent.Status = models.AgentStatusBurnt
				if agent.Tags == nil {
					agent.Tags = map[string]string{}
				}
				agent.Tags["error"] = fmt.Sprintf("Provider '%s' health check failed", agent.ModelProvider)
				agent.UpdatedAt = time.Now().UTC()
				h.Store.UpdateAgent(context.Background(), agent)
				log.Warn().Str("agent", agentName).Msg("Agent burnt — provider health check failed")
				return
			}
		}

		agent.Status = models.AgentStatusReady
		agent.UpdatedAt = time.Now().UTC()
		if agent.Tags == nil {
			agent.Tags = map[string]string{}
		}
		delete(agent.Tags, "error")
		if err := h.Store.UpdateAgent(context.Background(), agent); err != nil {
			log.Warn().Err(err).Str("agent", agentName).Msg("Failed to update agent to ready")
		} else {
			log.Info().Str("agent", agentName).Msg("Agent is ready")
		}
	}()

	respondJSON(w, http.StatusAccepted, map[string]string{
		"name":        agentName,
		"version":     agent.Version,
		"environment": req.Environment,
		"status":      string(models.AgentStatusBaking),
		"agent_card":  "/agents/" + agentName + "/a2a/.well-known/agent-card.json",
	})
}

// TestAgent sends a single test message through the agent's configured provider.
func (h *Handlers) TestAgent(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	kitchen := middleware.GetKitchen(r.Context())

	agent, err := h.Store.GetAgent(r.Context(), kitchen, agentName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	if agent.ModelProvider == "" {
		respondError(w, http.StatusBadRequest, "Agent has no model provider configured")
		return
	}

	var req struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Message == "" {
		respondError(w, http.StatusBadRequest, "Request must include a non-empty 'message' field")
		return
	}

	// Build messages — include system prompt from ingredients if present
	messages := []models.ChatMessage{}
	for _, ing := range agent.Ingredients {
		if ing.Kind == models.IngredientPrompt {
			if text, ok := ing.Config["text"].(string); ok && text != "" {
				messages = append(messages, models.ChatMessage{Role: "system", Content: text})
			}
		}
	}
	messages = append(messages, models.ChatMessage{Role: "user", Content: req.Message})

	// Route through the model router
	start := time.Now()
	routeReq := &models.RouteRequest{
		Messages: messages,
		Model:    agent.ModelName,
		Strategy: models.RoutingFallback,
		Kitchen:  kitchen,
		AgentRef: agentName,
	}

	resp, err := h.Router.Route(r.Context(), routeReq)
	duration := time.Since(start)
	if err != nil {
		respondError(w, http.StatusBadGateway, "Model provider error: "+err.Error())
		return
	}

	// Record trace
	trace := &models.Trace{
		ID:          uuid.New().String(),
		AgentName:   agentName,
		Kitchen:     kitchen,
		Status:      "completed",
		DurationMs:  duration.Milliseconds(),
		TotalTokens: resp.Usage.TotalTokens,
		CostUSD:     resp.Usage.EstimatedCost,
		Metadata: map[string]interface{}{
			"provider": resp.Provider,
			"model":    resp.Model,
			"type":     "test",
		},
		CreatedAt: time.Now().UTC(),
	}
	h.Store.CreateTrace(r.Context(), trace)

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"agent":     agentName,
		"response":  resp.Content,
		"provider":  resp.Provider,
		"model":     resp.Model,
		"usage":     resp.Usage,
		"latency_ms": duration.Milliseconds(),
		"trace_id":  trace.ID,
	})
}

func (h *Handlers) CoolAgent(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	kitchen := middleware.GetKitchen(r.Context())

	agent, err := h.Store.GetAgent(r.Context(), kitchen, agentName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	agent.Status = models.AgentStatusCooled
	agent.UpdatedAt = time.Now().UTC()
	h.Store.UpdateAgent(r.Context(), agent)

	log.Info().Str("agent", agentName).Msg("Agent cooled")
	respondJSON(w, http.StatusOK, map[string]string{
		"name":   agentName,
		"status": string(models.AgentStatusCooled),
	})
}

func (h *Handlers) ListAgentVersions(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, []string{})
}

func (h *Handlers) GetAgentVersion(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]string{})
}

// ══════════════════════════════════════════════════════════════
// ── Recipe Handlers ──────────────────────────────────────────
// ══════════════════════════════════════════════════════════════

func (h *Handlers) ListRecipes(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	recipes, err := h.Store.ListRecipes(r.Context(), kitchen)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if recipes == nil {
		recipes = []models.Recipe{}
	}
	respondJSON(w, http.StatusOK, recipes)
}

func (h *Handlers) CreateRecipe(w http.ResponseWriter, r *http.Request) {
	var req models.Recipe
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	kitchen := middleware.GetKitchen(r.Context())
	req.ID = uuid.New().String()
	req.Kitchen = kitchen
	req.CreatedAt = time.Now().UTC()
	req.UpdatedAt = time.Now().UTC()

	if err := h.Store.CreateRecipe(r.Context(), &req); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	log.Info().Str("recipe", req.Name).Str("id", req.ID).Msg("Recipe created")
	respondJSON(w, http.StatusCreated, req)
}

func (h *Handlers) GetRecipe(w http.ResponseWriter, r *http.Request) {
	recipeName := chi.URLParam(r, "recipeName")
	kitchen := middleware.GetKitchen(r.Context())

	recipe, err := h.Store.GetRecipe(r.Context(), kitchen, recipeName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	respondJSON(w, http.StatusOK, recipe)
}

func (h *Handlers) UpdateRecipe(w http.ResponseWriter, r *http.Request) {
	recipeName := chi.URLParam(r, "recipeName")
	kitchen := middleware.GetKitchen(r.Context())

	recipe, err := h.Store.GetRecipe(r.Context(), kitchen, recipeName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	var req models.Recipe
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.Description != "" {
		recipe.Description = req.Description
	}
	if len(req.Steps) > 0 {
		recipe.Steps = req.Steps
	}
	recipe.UpdatedAt = time.Now().UTC()

	if err := h.Store.UpdateRecipe(r.Context(), recipe); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, recipe)
}

func (h *Handlers) DeleteRecipe(w http.ResponseWriter, r *http.Request) {
	recipeName := chi.URLParam(r, "recipeName")
	kitchen := middleware.GetKitchen(r.Context())

	if err := h.Store.DeleteRecipe(r.Context(), kitchen, recipeName); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) BakeRecipe(w http.ResponseWriter, r *http.Request) {
	recipeName := chi.URLParam(r, "recipeName")
	kitchen := middleware.GetKitchen(r.Context())

	recipe, err := h.Store.GetRecipe(r.Context(), kitchen, recipeName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	// Parse optional input
	var input map[string]interface{}
	json.NewDecoder(r.Body).Decode(&input)

	runID, err := h.Workflow.ExecuteRecipe(r.Context(), recipe, kitchen, input)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	log.Info().
		Str("recipe", recipeName).
		Str("run_id", runID).
		Int("steps", len(recipe.Steps)).
		Msg("Recipe execution started")

	respondJSON(w, http.StatusAccepted, map[string]string{
		"recipe": recipeName,
		"status": "running",
		"run_id": runID,
		"poll":   "/api/v1/recipes/" + recipeName + "/runs/" + runID,
	})
}

func (h *Handlers) RecipeHistory(w http.ResponseWriter, r *http.Request) {
	recipeName := chi.URLParam(r, "recipeName")
	kitchen := middleware.GetKitchen(r.Context())

	recipe, err := h.Store.GetRecipe(r.Context(), kitchen, recipeName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	runs, err := h.Store.ListRecipeRuns(r.Context(), recipe.ID, 50)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if runs == nil {
		runs = []models.RecipeRun{}
	}
	respondJSON(w, http.StatusOK, runs)
}

// GetRecipeRun returns the status and results of a specific recipe run.
func (h *Handlers) GetRecipeRun(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runId")

	run, err := h.Store.GetRecipeRun(r.Context(), runID)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	resp := map[string]interface{}{
		"run": run,
	}
	if run.Status == models.RecipeRunPaused {
		resp["pending_gates"] = h.Workflow.GetPendingGates(run.ID)
	}

	respondJSON(w, http.StatusOK, resp)
}

// CancelRecipeRun cancels a running recipe execution.
func (h *Handlers) CancelRecipeRun(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runId")

	if ok := h.Workflow.CancelRun(runID); !ok {
		respondError(w, http.StatusNotFound, "Run not found or already completed: "+runID)
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"run_id": runID,
		"status": "canceled",
	})
}

// ApproveGate approves or rejects a human gate in a recipe run.
func (h *Handlers) ApproveGate(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runId")
	stepName := chi.URLParam(r, "stepName")

	var req struct {
		Approved bool `json:"approved"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if ok := h.Workflow.ApproveGate(runID, stepName, req.Approved); !ok {
		respondError(w, http.StatusNotFound, fmt.Sprintf("No pending gate '%s' for run '%s'", stepName, runID))
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"run_id":   runID,
		"step":     stepName,
		"approved": req.Approved,
	})
}

// ══════════════════════════════════════════════════════════════
// ── Model Router Handlers ────────────────────────────────────
// ══════════════════════════════════════════════════════════════

func (h *Handlers) ListProviders(w http.ResponseWriter, r *http.Request) {
	providers, err := h.Store.ListProviders(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if providers == nil {
		providers = []models.ModelProvider{}
	}
	// Mask API keys in response
	masked := make([]models.ModelProvider, len(providers))
	for i, p := range providers {
		cp := p
		masked[i] = *maskProviderKeys(&cp)
	}
	respondJSON(w, http.StatusOK, masked)
}

func (h *Handlers) CreateProvider(w http.ResponseWriter, r *http.Request) {
	var req models.ModelProvider
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	req.ID = uuid.New().String()
	req.CreatedAt = time.Now().UTC()

	if err := h.Store.CreateProvider(r.Context(), &req); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	log.Info().Str("provider", req.Name).Str("kind", req.Kind).Msg("Model provider registered")
	respondJSON(w, http.StatusCreated, req)
}

func (h *Handlers) GetProvider(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "providerName")
	provider, err := h.Store.GetProvider(r.Context(), name)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	respondJSON(w, http.StatusOK, maskProviderKeys(provider))
}

func (h *Handlers) DeleteProvider(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "providerName")
	if err := h.Store.DeleteProvider(r.Context(), name); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) UpdateProvider(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "providerName")

	provider, err := h.Store.GetProvider(r.Context(), name)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	var req models.ModelProvider
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.Kind != "" {
		provider.Kind = req.Kind
	}
	if req.Endpoint != "" {
		provider.Endpoint = req.Endpoint
	}
	if len(req.Models) > 0 {
		provider.Models = req.Models
	}
	if req.Config != nil {
		// Merge config — don't overwrite existing keys that aren't in the update
		if provider.Config == nil {
			provider.Config = map[string]interface{}{}
		}
		for k, v := range req.Config {
			provider.Config[k] = v
		}
	}
	provider.IsDefault = req.IsDefault

	if err := h.Store.UpdateProvider(r.Context(), provider); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	log.Info().Str("provider", name).Msg("Model provider updated")
	respondJSON(w, http.StatusOK, maskProviderKeys(provider))
}

func (h *Handlers) RouteModel(w http.ResponseWriter, r *http.Request) {
	var req models.RouteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.Kitchen == "" {
		req.Kitchen = middleware.GetKitchen(r.Context())
	}

	resp, err := h.Router.Route(r.Context(), &req)
	if err != nil {
		respondError(w, http.StatusBadGateway, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, resp)
}

func (h *Handlers) GetCostSummary(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	summary := h.Router.GetCostSummary(kitchen)
	respondJSON(w, http.StatusOK, summary)
}

func (h *Handlers) HealthCheckProviders(w http.ResponseWriter, r *http.Request) {
	results := h.Router.HealthCheck(r.Context())
	respondJSON(w, http.StatusOK, results)
}

// ══════════════════════════════════════════════════════════════
// ── MCP Gateway Handlers ─────────────────────────────────────
// ══════════════════════════════════════════════════════════════

func (h *Handlers) MCPEndpoint(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())

	var req models.MCPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(models.MCPResponse{
			Jsonrpc: "2.0",
			Error: &models.MCPError{
				Code:    -32700,
				Message: "Parse error",
				Data:    err.Error(),
			},
			ID: nil,
		})
		return
	}

	log.Info().Str("method", req.Method).Str("kitchen", kitchen).Msg("MCP request received")

	resp := h.MCPGateway.HandleJSONRPC(r.Context(), kitchen, &req)
	if resp == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *Handlers) MCPSSEEndpoint(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())

	flusher, ok := w.(http.Flusher)
	if !ok {
		respondError(w, http.StatusInternalServerError, "SSE not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := h.MCPGateway.Subscribe(kitchen)
	defer h.MCPGateway.Unsubscribe(kitchen, ch)

	fmt.Fprintf(w, "event: connected\ndata: {\"kitchen\":\"%s\"}\n\n", kitchen)
	flusher.Flush()

	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(msg)
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(data))
			flusher.Flush()

		case <-r.Context().Done():
			return
		}
	}
}

func (h *Handlers) ListMCPTools(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	tools, err := h.Store.ListTools(r.Context(), kitchen)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if tools == nil {
		tools = []models.MCPTool{}
	}
	respondJSON(w, http.StatusOK, tools)
}

func (h *Handlers) RegisterMCPTool(w http.ResponseWriter, r *http.Request) {
	var req models.MCPTool
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	kitchen := middleware.GetKitchen(r.Context())
	req.ID = uuid.New().String()
	req.Kitchen = kitchen
	req.Enabled = true
	req.CreatedAt = time.Now().UTC()
	req.UpdatedAt = time.Now().UTC()

	if req.Transport == "" {
		req.Transport = "http"
	}

	if err := h.Store.CreateTool(r.Context(), &req); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	log.Info().Str("tool", req.Name).Str("transport", req.Transport).Str("kitchen", kitchen).Msg("MCP tool registered")
	respondJSON(w, http.StatusCreated, req)
}

func (h *Handlers) GetMCPTool(w http.ResponseWriter, r *http.Request) {
	toolName := chi.URLParam(r, "toolName")
	kitchen := middleware.GetKitchen(r.Context())

	tool, err := h.Store.GetTool(r.Context(), kitchen, toolName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	respondJSON(w, http.StatusOK, tool)
}

func (h *Handlers) DeleteMCPTool(w http.ResponseWriter, r *http.Request) {
	toolName := chi.URLParam(r, "toolName")
	kitchen := middleware.GetKitchen(r.Context())

	if err := h.Store.DeleteTool(r.Context(), kitchen, toolName); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) UpdateMCPTool(w http.ResponseWriter, r *http.Request) {
	toolName := chi.URLParam(r, "toolName")
	kitchen := middleware.GetKitchen(r.Context())

	tool, err := h.Store.GetTool(r.Context(), kitchen, toolName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	var req models.MCPTool
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.Description != "" {
		tool.Description = req.Description
	}
	if req.Endpoint != "" {
		tool.Endpoint = req.Endpoint
	}
	if req.Transport != "" {
		tool.Transport = req.Transport
	}
	if req.Schema != nil {
		tool.Schema = req.Schema
	}
	if req.AuthConfig != nil {
		tool.AuthConfig = req.AuthConfig
	}
	// Always apply enabled (it's a boolean, false is meaningful)
	tool.Enabled = req.Enabled
	tool.UpdatedAt = time.Now().UTC()

	if err := h.Store.UpdateTool(r.Context(), tool); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	log.Info().Str("tool", toolName).Str("kitchen", kitchen).Msg("MCP tool updated")
	respondJSON(w, http.StatusOK, tool)
}

// ══════════════════════════════════════════════════════════════
// ── Trace Handlers ───────────────────────────────────────────
// ══════════════════════════════════════════════════════════════

func (h *Handlers) ListTraces(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	traces, err := h.Store.ListTraces(r.Context(), kitchen, 100)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if traces == nil {
		traces = []models.Trace{}
	}
	respondJSON(w, http.StatusOK, traces)
}

func (h *Handlers) GetTrace(w http.ResponseWriter, r *http.Request) {
	traceID := chi.URLParam(r, "traceId")
	trace, err := h.Store.GetTrace(r.Context(), traceID)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	respondJSON(w, http.StatusOK, trace)
}

// ══════════════════════════════════════════════════════════════
// ── Kitchen Handlers ─────────────────────────────────────────
// ══════════════════════════════════════════════════════════════

func (h *Handlers) ListKitchens(w http.ResponseWriter, r *http.Request) {
	kitchens, err := h.Store.ListKitchens(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if kitchens == nil {
		kitchens = []models.Kitchen{}
	}
	respondJSON(w, http.StatusOK, kitchens)
}

func (h *Handlers) CreateKitchen(w http.ResponseWriter, r *http.Request) {
	var req models.Kitchen
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	req.ID = uuid.New().String()
	req.CreatedAt = time.Now().UTC()

	if err := h.Store.CreateKitchen(r.Context(), &req); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	log.Info().Str("kitchen", req.Name).Str("id", req.ID).Msg("Kitchen created")
	respondJSON(w, http.StatusCreated, req)
}

func (h *Handlers) GetKitchen(w http.ResponseWriter, r *http.Request) {
	kitchenID := chi.URLParam(r, "kitchenId")
	kitchen, err := h.Store.GetKitchen(r.Context(), kitchenID)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	respondJSON(w, http.StatusOK, kitchen)
}

// ══════════════════════════════════════════════════════════════
// ── A2A Gateway Handlers ─────────────────────────────────────
// ══════════════════════════════════════════════════════════════

func (h *Handlers) A2AEndpoint(w http.ResponseWriter, r *http.Request) {
	var rpcReq struct {
		Jsonrpc string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params,omitempty"`
		ID      interface{}     `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&rpcReq); err != nil {
		w.Header().Set("Content-Type", "application/a2a+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"error": map[string]interface{}{
				"code":    -32700,
				"message": "Parse error",
				"data":    err.Error(),
			},
			"id": nil,
		})
		return
	}

	log.Info().Str("method", rpcReq.Method).Msg("A2A JSON-RPC request received")

	switch rpcReq.Method {
	case "tasks/send":
		h.handleA2ATaskSend(w, r, rpcReq.Params, rpcReq.ID)
	case "tasks/get":
		h.handleA2ATaskGet(w, rpcReq.Params, rpcReq.ID)
	default:
		w.Header().Set("Content-Type", "application/a2a+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"error": map[string]interface{}{
				"code":    -32601,
				"message": "Method not found",
				"data":    "Method '" + rpcReq.Method + "' is not yet implemented in the gateway",
			},
			"id": rpcReq.ID,
		})
	}
}

func (h *Handlers) handleA2ATaskSend(w http.ResponseWriter, r *http.Request, params json.RawMessage, rpcID interface{}) {
	var taskReq struct {
		ID      string `json:"id"`
		Message struct {
			Role  string `json:"role"`
			Parts []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"message"`
	}
	if err := json.Unmarshal(params, &taskReq); err != nil {
		w.Header().Set("Content-Type", "application/a2a+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"error":   map[string]interface{}{"code": -32602, "message": "Invalid params"},
			"id":      rpcID,
		})
		return
	}

	kitchen := middleware.GetKitchen(r.Context())
	trace := &models.Trace{
		ID:        uuid.New().String(),
		AgentName: "a2a-gateway",
		Kitchen:   kitchen,
		Status:    "submitted",
		CreatedAt: time.Now().UTC(),
	}
	h.Store.CreateTrace(r.Context(), trace)

	w.Header().Set("Content-Type", "application/a2a+json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"jsonrpc": "2.0",
		"result": map[string]interface{}{
			"id":     taskReq.ID,
			"status": map[string]string{"state": "submitted"},
		},
		"id": rpcID,
	})
}

func (h *Handlers) handleA2ATaskGet(w http.ResponseWriter, params json.RawMessage, rpcID interface{}) {
	var req struct {
		ID string `json:"id"`
	}
	json.Unmarshal(params, &req)

	w.Header().Set("Content-Type", "application/a2a+json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"jsonrpc": "2.0",
		"result": map[string]interface{}{
			"id":     req.ID,
			"status": map[string]string{"state": "completed"},
		},
		"id": rpcID,
	})
}

func (h *Handlers) ServeAgentCard(w http.ResponseWriter, r *http.Request) {
	card := map[string]interface{}{
		"name":        "AgentOven Gateway",
		"description": "AgentOven control plane — multi-agent orchestration gateway",
		"version":     "0.2.0",
		"provider": map[string]string{
			"organization": "AgentOven",
			"url":          "https://agentoven.dev",
		},
		"supportedInterfaces": []map[string]string{
			{
				"url":             r.Host + "/a2a",
				"protocolBinding": "jsonrpc-http",
				"protocolVersion": "1.0",
			},
		},
		"capabilities": map[string]bool{
			"streaming":         true,
			"pushNotifications": true,
		},
	}
	w.Header().Set("Content-Type", "application/a2a+json")
	json.NewEncoder(w).Encode(card)
}

func (h *Handlers) A2AAgentEndpoint(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	kitchen := middleware.GetKitchen(r.Context())

	agent, err := h.Store.GetAgent(r.Context(), kitchen, agentName)
	if err != nil {
		w.Header().Set("Content-Type", "application/a2a+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"error": map[string]interface{}{
				"code":    -32001,
				"message": "Agent not found",
				"data":    "Agent '" + agentName + "' is not registered",
			},
			"id": nil,
		})
		return
	}

	if agent.Status != models.AgentStatusReady {
		w.Header().Set("Content-Type", "application/a2a+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"error": map[string]interface{}{
				"code":    -32002,
				"message": "Agent not ready",
				"data":    "Agent '" + agentName + "' status is " + string(agent.Status),
			},
			"id": nil,
		})
		return
	}

	log.Info().Str("agent", agentName).Msg("Routing A2A request to agent")
	respondJSON(w, http.StatusOK, map[string]string{
		"agent":  agentName,
		"status": "routing",
	})
}

func (h *Handlers) ServeAgentSpecificCard(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	kitchen := middleware.GetKitchen(r.Context())

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host

	agent, err := h.Store.GetAgent(r.Context(), kitchen, agentName)
	if err != nil {
		card := map[string]interface{}{
			"name":        agentName,
			"description": "Agent managed by AgentOven (not found)",
			"supportedInterfaces": []map[string]string{
				{
					"url":             scheme + "://" + host + "/agents/" + agentName + "/a2a",
					"protocolBinding": "jsonrpc-http",
				},
			},
		}
		w.Header().Set("Content-Type", "application/a2a+json")
		json.NewEncoder(w).Encode(card)
		return
	}

	skills := make([]map[string]interface{}, 0)
	for _, s := range agent.Skills {
		skills = append(skills, map[string]interface{}{
			"id":          s,
			"name":        s,
			"description": "Skill: " + s,
		})
	}

	card := map[string]interface{}{
		"name":        agent.Name,
		"description": agent.Description,
		"version":     agent.Version,
		"provider": map[string]string{
			"organization": "AgentOven",
			"url":          "https://agentoven.dev",
		},
		"supportedInterfaces": []map[string]string{
			{
				"url":             scheme + "://" + host + "/agents/" + agentName + "/a2a",
				"protocolBinding": "jsonrpc-http",
				"protocolVersion": "1.0",
			},
		},
		"capabilities": map[string]bool{
			"streaming":         true,
			"pushNotifications": true,
		},
		"skills": skills,
	}
	w.Header().Set("Content-Type", "application/a2a+json")
	json.NewEncoder(w).Encode(card)
}

// ── Helpers ──────────────────────────────────────────────────

func respondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func respondError(w http.ResponseWriter, status int, message string) {
	respondJSON(w, status, map[string]string{"error": message})
}

// maskProviderKeys redacts sensitive fields (api_key, api_secret) in provider
// config before returning to API consumers.
func maskProviderKeys(p *models.ModelProvider) *models.ModelProvider {
	if p.Config == nil {
		return p
	}
	cp := *p
	cp.Config = make(map[string]interface{}, len(p.Config))
	for k, v := range p.Config {
		cp.Config[k] = v
	}
	for _, key := range []string{"api_key", "api_secret"} {
		if val, ok := cp.Config[key].(string); ok && len(val) > 4 {
			cp.Config[key] = val[:4] + "****"
		} else if ok {
			cp.Config[key] = "****"
		}
	}
	return &cp
}
