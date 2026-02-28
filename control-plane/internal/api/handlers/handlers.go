// Package handlers implements the HTTP handlers for the AgentOven control plane.
// Phase 2: All handlers use the Store interface (PostgreSQL-backed) instead
// of in-memory maps. New handlers added for Model Router, MCP Gateway, and
// Workflow Engine.
package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/api/middleware"
	"github.com/agentoven/agentoven/control-plane/internal/catalog"
	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/internal/mcpgw"
	"github.com/agentoven/agentoven/control-plane/internal/process"
	"github.com/agentoven/agentoven/control-plane/internal/resolver"
	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/internal/workflow"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// Handlers holds all handler dependencies.
type Handlers struct {
	Store           store.Store
	Router          *router.ModelRouter
	MCPGateway      *mcpgw.Gateway
	Workflow        *workflow.Engine
	Resolver        *resolver.Resolver
	Executor        *executor.Executor
	ProcessManager  *process.Manager
	PromptValidator contracts.PromptValidatorService
	Catalog         *catalog.Catalog
	Sessions        contracts.SessionStore
	Guardrails      contracts.GuardrailService
}

// New creates a new Handlers instance with all dependencies.
func New(s store.Store, mr *router.ModelRouter, gw *mcpgw.Gateway, wf *workflow.Engine, cat *catalog.Catalog, sess contracts.SessionStore, pm *process.Manager) *Handlers {
	res := resolver.NewResolver(s)
	exec := executor.NewExecutor(s, mr, gw, sess)
	return &Handlers{
		Store:           s,
		Router:          mr,
		MCPGateway:      gw,
		Workflow:        wf,
		Resolver:        res,
		Executor:        exec,
		ProcessManager:  pm,
		PromptValidator: &contracts.CommunityPromptValidator{},
		Catalog:         cat,
		Sessions:        sess,
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

	// Default mode to managed if not specified
	if req.Mode == "" {
		req.Mode = models.AgentModeManaged
	}
	if req.MaxTurns <= 0 {
		req.MaxTurns = 10
	}

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
	if req.Mode != "" {
		agent.Mode = req.Mode
	}
	if req.MaxTurns > 0 {
		agent.MaxTurns = req.MaxTurns
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
	// R9 fields: backup provider and guardrails
	if req.BackupProvider != "" {
		agent.BackupProvider = req.BackupProvider
	}
	if req.BackupModel != "" {
		agent.BackupModel = req.BackupModel
	}
	if len(req.Guardrails) > 0 {
		agent.Guardrails = req.Guardrails
	}
	agent.UpdatedAt = time.Now().UTC()
	agent.VersionBump = "patch" // config edit → bump patch version

	if err := h.Store.UpdateAgent(r.Context(), agent); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, agent)
}

func (h *Handlers) DeleteAgent(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	kitchen := middleware.GetKitchen(r.Context())

	// Stop the agent process before deletion
	if h.ProcessManager != nil {
		if err := h.ProcessManager.Stop(r.Context(), kitchen, agentName); err != nil {
			log.Warn().Err(err).Str("agent", agentName).Msg("Failed to stop agent process during delete")
		}
	}

	if err := h.Store.DeleteAgent(r.Context(), kitchen, agentName); err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	log.Info().Str("agent", agentName).Str("kitchen", kitchen).Msg("Agent retired")
	respondJSON(w, http.StatusOK, map[string]string{"status": "deleted", "agent": agentName})
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
	// ISS-023 fix: check decode error instead of silently discarding it.
	// Allow io.EOF since the request body is optional for BakeAgent.
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		respondError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	// ── Validate & resolve ALL ingredients via the Resolver ──────
	// This replaces the old manual checks with full ingredient resolution:
	// model, tools, prompts, data, embeddings, vectorstores, retrievers.

	// If agent has a top-level ModelProvider but no model ingredient, synthesize one
	if agent.ModelProvider != "" {
		hasModelIngredient := false
		for _, ing := range agent.Ingredients {
			if ing.Kind == models.IngredientModel {
				hasModelIngredient = true
				break
			}
		}
		if !hasModelIngredient {
			agent.Ingredients = append(agent.Ingredients, models.Ingredient{
				ID:       "auto-model",
				Name:     agent.ModelProvider,
				Kind:     models.IngredientModel,
				Required: true,
				Config: map[string]interface{}{
					"provider": agent.ModelProvider,
					"model":    agent.ModelName,
				},
			})
		}
	}

	resolved, err := h.Resolver.Resolve(r.Context(), agent)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error":   "Agent cannot be baked — ingredient resolution failed",
			"details": err.Error(),
			"partial": resolved,
		})
		return
	}

	// Cache resolved config on the agent
	agent.ResolvedConfig = resolved
	agent.Status = models.AgentStatusBaking
	agent.UpdatedAt = time.Now().UTC()
	if req.Version != "" {
		// Caller explicitly set a version — use it directly
		agent.Version = req.Version
		agent.VersionBump = "" // no auto-bump, caller controls
	} else {
		// Auto-bump minor on bake (0.1.0 → 0.2.0)
		agent.VersionBump = "minor"
	}
	agent.A2AEndpoint = "/agents/" + agentName + "/a2a"

	if err := h.Store.UpdateAgent(r.Context(), agent); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Re-fetch agent from store to get the version the store computed
	agent, _ = h.Store.GetAgent(r.Context(), kitchen, agentName)

	log.Info().
		Str("agent", agentName).
		Str("version", agent.Version).
		Str("environment", req.Environment).
		Msg("Agent baking started")

	// Validate provider connectivity async, then mark ready or burnt
	go func() {
		time.Sleep(1 * time.Second)

		// Test the provider with a real credential-validating call
		if agent.ModelProvider != "" {
			provider, err := h.Store.GetProvider(context.Background(), agent.ModelProvider)
			if err == nil {
				result := h.Router.TestProvider(context.Background(), provider)
				if !result.Healthy {
					agent.Status = models.AgentStatusBurnt
					if agent.Tags == nil {
						agent.Tags = map[string]string{}
					}
					agent.Tags["error"] = fmt.Sprintf("Provider '%s' test failed: %s", agent.ModelProvider, result.Error)
					agent.UpdatedAt = time.Now().UTC()
					h.Store.UpdateAgent(context.Background(), agent)
					log.Warn().Str("agent", agentName).Msg("Agent burnt — provider test failed")
					return
				}
			}
		}

		// ── Start the agent process ──────────────────────────────
		// After provider validation passes, spawn the agent as a running
		// process (local Python / Docker / K8s) so it can serve A2A tasks.
		if h.ProcessManager != nil && agent.Mode != models.AgentModeExternal {
			procInfo, err := h.ProcessManager.Start(context.Background(), agent)
			if err != nil {
				agent.Status = models.AgentStatusBurnt
				if agent.Tags == nil {
					agent.Tags = map[string]string{}
				}
				agent.Tags["error"] = fmt.Sprintf("Agent process failed to start: %s", err.Error())
				agent.UpdatedAt = time.Now().UTC()
				h.Store.UpdateAgent(context.Background(), agent)
				log.Warn().Err(err).Str("agent", agentName).Msg("Agent burnt — process start failed")
				return
			}

			// Store process info but keep a2a_endpoint stable.
			// The control plane proxies /agents/{name}/a2a → process.endpoint
			// so clients always use the stable URL (ADR-0007).
			agent.Process = procInfo
			log.Info().
				Str("agent", agentName).
				Str("backend", procInfo.Endpoint).
				Int("port", procInfo.Port).
				Str("mode", string(procInfo.Mode)).
				Msg("Agent process spawned — control plane will proxy A2A calls")
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
		Message         string `json:"message"`
		ThinkingEnabled bool   `json:"thinking_enabled,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Message == "" {
		respondError(w, http.StatusBadRequest, "Request must include a non-empty 'message' field")
		return
	}

	// ── Input Guardrails ────────────────────────────────────
	if h.Guardrails != nil && len(agent.Guardrails) > 0 {
		eval, gErr := h.Guardrails.EvaluateInput(r.Context(), agent.Guardrails, req.Message)
		if gErr != nil {
			log.Warn().Err(gErr).Str("agent", agentName).Msg("Input guardrail evaluation error (test)")
		} else if !eval.Passed {
			respondJSON(w, http.StatusForbidden, map[string]interface{}{
				"error":      "Input blocked by guardrails",
				"guardrails": eval.Results,
			})
			return
		}
	}

	// When thinking is enabled, extend request timeout
	if req.ThinkingEnabled {
		var cancel context.CancelFunc
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		r = r.WithContext(ctx)
		_ = ctx
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
		Messages:        messages,
		Model:           agent.ModelName,
		Strategy:        models.RoutingFallback,
		Kitchen:         kitchen,
		AgentRef:        agentName,
		ThinkingEnabled: req.ThinkingEnabled,
	}

	resp, err := h.Router.Route(r.Context(), routeReq)
	duration := time.Since(start)
	if err != nil {
		respondError(w, http.StatusBadGateway, "Model provider error: "+err.Error())
		return
	}

	// ── Output Guardrails ───────────────────────────────────
	if h.Guardrails != nil && len(agent.Guardrails) > 0 {
		eval, gErr := h.Guardrails.EvaluateOutput(r.Context(), agent.Guardrails, resp.Content)
		if gErr != nil {
			log.Warn().Err(gErr).Str("agent", agentName).Msg("Output guardrail evaluation error (test)")
		} else if !eval.Passed {
			respondJSON(w, http.StatusForbidden, map[string]interface{}{
				"error":      "Output blocked by guardrails",
				"guardrails": eval.Results,
			})
			return
		}
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
		"agent":           agentName,
		"response":        resp.Content,
		"provider":        resp.Provider,
		"model":           resp.Model,
		"usage":           resp.Usage,
		"latency_ms":      duration.Milliseconds(),
		"trace_id":        trace.ID,
		"thinking_blocks": resp.ThinkingBlocks,
	})
}

// RecookAgent edits a baked agent's configuration and re-bakes it.
// POST /api/v1/agents/{agentName}/recook
//
// Accepts the same fields as UpdateAgent (description, framework, model_provider,
// model_name, ingredients, skills, tags, max_turns) plus optionally "version".
// After applying edits, runs the full bake pipeline (resolve + provider test).
// Bumps the minor version automatically unless version is explicitly provided.
func (h *Handlers) RecookAgent(w http.ResponseWriter, r *http.Request) {
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

	// Only allow re-cook on agents that have been baked at least once
	if agent.Status == models.AgentStatusDraft {
		respondError(w, http.StatusBadRequest,
			"Agent is still a draft — use bake, not re-cook")
		return
	}

	var req struct {
		Description    string              `json:"description"`
		Framework      string              `json:"framework"`
		ModelProvider  string              `json:"model_provider"`
		ModelName      string              `json:"model_name"`
		BackupProvider string              `json:"backup_provider"`
		BackupModel    string              `json:"backup_model"`
		Mode           models.AgentMode    `json:"mode"`
		MaxTurns       int                 `json:"max_turns"`
		Ingredients    []models.Ingredient `json:"ingredients"`
		Guardrails     []models.Guardrail  `json:"guardrails"`
		Skills         []string            `json:"skills"`
		Tags           map[string]string   `json:"tags"`
		Version        string              `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		respondError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	// ── Apply edits ──────────────────────────────────────────
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
	if req.BackupProvider != "" {
		agent.BackupProvider = req.BackupProvider
	}
	if req.BackupModel != "" {
		agent.BackupModel = req.BackupModel
	}
	if req.Mode != "" {
		agent.Mode = req.Mode
	}
	if req.MaxTurns > 0 {
		agent.MaxTurns = req.MaxTurns
	}
	if len(req.Ingredients) > 0 {
		agent.Ingredients = req.Ingredients
	}
	if len(req.Guardrails) > 0 {
		agent.Guardrails = req.Guardrails
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

	// ── Resolve ingredients (same as BakeAgent) ──────────────
	if agent.ModelProvider != "" {
		hasModelIngredient := false
		for _, ing := range agent.Ingredients {
			if ing.Kind == models.IngredientModel {
				hasModelIngredient = true
				break
			}
		}
		if !hasModelIngredient {
			agent.Ingredients = append(agent.Ingredients, models.Ingredient{
				ID:       "auto-model",
				Name:     agent.ModelProvider,
				Kind:     models.IngredientModel,
				Required: true,
				Config: map[string]interface{}{
					"provider": agent.ModelProvider,
					"model":    agent.ModelName,
				},
			})
		}
	}

	resolved, err := h.Resolver.Resolve(r.Context(), agent)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error":   "Agent cannot be re-cooked — ingredient resolution failed",
			"details": err.Error(),
			"partial": resolved,
		})
		return
	}

	// ── Update agent with new config + baking status ─────────
	agent.ResolvedConfig = resolved
	agent.Status = models.AgentStatusBaking
	agent.UpdatedAt = time.Now().UTC()
	if req.Version != "" {
		agent.Version = req.Version
		agent.VersionBump = ""
	} else {
		agent.VersionBump = "minor"
	}

	if err := h.Store.UpdateAgent(r.Context(), agent); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Re-fetch to get the store-computed version
	agent, _ = h.Store.GetAgent(r.Context(), kitchen, agentName)

	log.Info().
		Str("agent", agentName).
		Str("version", agent.Version).
		Msg("Agent re-cook started")

	// ── Async provider test (same as BakeAgent) ──────────────
	go func() {
		time.Sleep(1 * time.Second)

		if agent.ModelProvider != "" {
			provider, err := h.Store.GetProvider(context.Background(), agent.ModelProvider)
			if err == nil {
				result := h.Router.TestProvider(context.Background(), provider)
				if !result.Healthy {
					agent.Status = models.AgentStatusBurnt
					if agent.Tags == nil {
						agent.Tags = map[string]string{}
					}
					agent.Tags["error"] = fmt.Sprintf("Provider '%s' test failed: %s", agent.ModelProvider, result.Error)
					agent.UpdatedAt = time.Now().UTC()
					h.Store.UpdateAgent(context.Background(), agent)
					log.Warn().Str("agent", agentName).Msg("Agent burnt on re-cook — provider test failed")
					return
				}
			}
		}

		agent.Status = models.AgentStatusReady
		agent.UpdatedAt = time.Now().UTC()
		if agent.Tags != nil {
			delete(agent.Tags, "error")
		}
		if err := h.Store.UpdateAgent(context.Background(), agent); err != nil {
			log.Warn().Err(err).Str("agent", agentName).Msg("Failed to update agent to ready after re-cook")
		} else {
			log.Info().Str("agent", agentName).Msg("Agent re-cooked and ready")
		}
	}()

	respondJSON(w, http.StatusAccepted, map[string]interface{}{
		"name":       agentName,
		"version":    agent.Version,
		"status":     string(models.AgentStatusBaking),
		"re_cooked":  true,
		"agent_card": "/agents/" + agentName + "/a2a/.well-known/agent-card.json",
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

	// Stop the agent process if running
	if h.ProcessManager != nil {
		if err := h.ProcessManager.Stop(r.Context(), kitchen, agentName); err != nil {
			log.Warn().Err(err).Str("agent", agentName).Msg("Failed to stop agent process during cool")
		}
	}

	agent.Status = models.AgentStatusCooled
	agent.Process = nil // clear process info
	agent.UpdatedAt = time.Now().UTC()
	h.Store.UpdateAgent(r.Context(), agent)

	log.Info().Str("agent", agentName).Msg("Agent cooled")
	respondJSON(w, http.StatusOK, map[string]string{
		"name":   agentName,
		"status": string(models.AgentStatusCooled),
	})
}

// RewarmAgent transitions a cooled agent back to ready.
// POST /api/v1/agents/{agentName}/rewarm
func (h *Handlers) RewarmAgent(w http.ResponseWriter, r *http.Request) {
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

	if agent.Status != models.AgentStatusCooled {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("agent is '%s', only cooled agents can be rewarmed", agent.Status))
		return
	}

	agent.Status = models.AgentStatusReady
	agent.UpdatedAt = time.Now().UTC()
	if agent.Tags != nil {
		delete(agent.Tags, "error")
	}

	if err := h.Store.UpdateAgent(r.Context(), agent); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	log.Info().Str("agent", agentName).Msg("Agent rewarmed")
	respondJSON(w, http.StatusOK, map[string]string{
		"name":   agentName,
		"status": string(models.AgentStatusReady),
	})
}

func (h *Handlers) ListAgentVersions(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	agentName := chi.URLParam(r, "agentName")

	versions, err := h.Store.ListAgentVersions(r.Context(), kitchen, agentName)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, versions)
}

func (h *Handlers) GetAgentVersion(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	agentName := chi.URLParam(r, "agentName")
	version := chi.URLParam(r, "version")

	agent, err := h.Store.GetAgentVersion(r.Context(), kitchen, agentName, version)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, agent)
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

// RouteModelStream routes a model request with Server-Sent Events streaming.
// POST /api/v1/models/route/stream
func (h *Handlers) RouteModelStream(w http.ResponseWriter, r *http.Request) {
	var req models.RouteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.Kitchen == "" {
		req.Kitchen = middleware.GetKitchen(r.Context())
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		respondError(w, http.StatusInternalServerError, "Streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	err := h.Router.RouteStream(r.Context(), &req, func(chunk *models.StreamChunk) error {
		data, _ := json.Marshal(chunk)
		_, writeErr := fmt.Fprintf(w, "data: %s\n\n", data)
		if writeErr != nil {
			return writeErr
		}
		flusher.Flush()
		return nil
	})

	if err != nil {
		errChunk, _ := json.Marshal(models.StreamChunk{Error: err.Error(), Done: true})
		fmt.Fprintf(w, "data: %s\n\n", errChunk)
		flusher.Flush()
	}
}

func (h *Handlers) GetCostSummary(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	summary := h.Router.GetCostSummary(kitchen)
	respondJSON(w, http.StatusOK, summary)
}

// TestProvider performs a real credential-validating test against a single provider.
// POST /api/v1/models/providers/{providerName}/test
func (h *Handlers) TestProvider(w http.ResponseWriter, r *http.Request) {
	providerName := chi.URLParam(r, "providerName")
	if providerName == "" {
		respondError(w, http.StatusBadRequest, "provider name is required")
		return
	}

	provider, err := h.Store.GetProvider(r.Context(), providerName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, fmt.Sprintf("provider %q not found", providerName))
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	log.Info().Str("provider", providerName).Str("kind", provider.Kind).Msg("Testing provider")

	result := h.Router.TestProvider(r.Context(), provider)

	// Cache the test result on the provider
	now := time.Now().UTC()
	provider.LastTestedAt = &now
	provider.LastTestHealthy = &result.Healthy
	provider.LastTestError = result.Error
	provider.LastTestLatency = result.LatencyMs

	if err := h.Store.UpdateProvider(r.Context(), provider); err != nil {
		log.Warn().Err(err).Str("provider", providerName).Msg("Failed to cache test result on provider")
	}

	respondJSON(w, http.StatusOK, result)
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
	if len(req.Capabilities) == 0 {
		req.Capabilities = []string{"tool"}
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
	if len(req.Capabilities) > 0 {
		tool.Capabilities = req.Capabilities
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

	// Parse optional filter query params
	filter := store.TraceFilter{
		AgentName:  r.URL.Query().Get("agent"),
		RecipeName: r.URL.Query().Get("recipe"),
		Status:     r.URL.Query().Get("status"),
		Limit:      100,
	}

	traces, err := h.Store.ListTracesFiltered(r.Context(), kitchen, filter)
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
		// Optional: specify which agent to invoke
		Metadata struct {
			AgentName string `json:"agent_name"`
		} `json:"metadata"`
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

	// Extract text from message parts
	var userMessage string
	for _, part := range taskReq.Message.Parts {
		if part.Type == "text" || part.Type == "" {
			userMessage += part.Text
		}
	}

	taskID := taskReq.ID
	if taskID == "" {
		taskID = uuid.New().String()
	}

	// If an agent is specified, try to invoke it
	agentName := taskReq.Metadata.AgentName
	if agentName != "" && h.Executor != nil {
		agent, err := h.Store.GetAgent(r.Context(), kitchen, agentName)
		if err != nil {
			w.Header().Set("Content-Type", "application/a2a+json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"error": map[string]interface{}{
					"code":    -32001,
					"message": "Agent not found",
					"data":    agentName,
				},
				"id": rpcID,
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
					"data":    string(agent.Status),
				},
				"id": rpcID,
			})
			return
		}

		// Resolve agent ingredients before async execution
		resolved, err := h.Resolver.Resolve(r.Context(), agent)
		if err != nil {
			w.Header().Set("Content-Type", "application/a2a+json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"error": map[string]interface{}{
					"code":    -32003,
					"message": "Ingredient resolution failed",
					"data":    err.Error(),
				},
				"id": rpcID,
			})
			return
		}

		// Execute agent asynchronously
		go func() {
			execCtx := context.Background()
			response, execTrace, err := h.Executor.Execute(execCtx, agent, userMessage, resolved, nil, false)

			// Record trace with result
			status := "completed"
			costUSD := 0.0
			totalTokens := int64(0)
			_ = response // final text response stored in trace metadata below
			if err != nil {
				status = "failed"
				log.Warn().Err(err).Str("agent", agentName).Str("task_id", taskID).Msg("A2A agent execution failed")
			} else if execTrace != nil {
				costUSD = execTrace.Usage.EstimatedCost
				totalTokens = execTrace.Usage.TotalTokens
			}

			trace := &models.Trace{
				ID:          taskID,
				AgentName:   agentName,
				Kitchen:     kitchen,
				Status:      status,
				TotalTokens: totalTokens,
				CostUSD:     costUSD,
				Metadata: map[string]interface{}{
					"source":   "a2a",
					"task_id":  taskID,
					"response": response,
				},
				CreatedAt: time.Now().UTC(),
			}
			h.Store.CreateTrace(execCtx, trace)
		}()

		// Return immediately with submitted status (async execution)
		w.Header().Set("Content-Type", "application/a2a+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"result": map[string]interface{}{
				"id":     taskID,
				"status": map[string]string{"state": "working"},
				"metadata": map[string]string{
					"agent_name": agentName,
					"kitchen":    kitchen,
				},
			},
			"id": rpcID,
		})
		return
	}

	// No specific agent — record trace and return submitted
	trace := &models.Trace{
		ID:        taskID,
		AgentName: "a2a-gateway",
		Kitchen:   kitchen,
		Status:    "submitted",
		Metadata: map[string]interface{}{
			"source":  "a2a",
			"message": userMessage,
		},
		CreatedAt: time.Now().UTC(),
	}
	h.Store.CreateTrace(r.Context(), trace)

	w.Header().Set("Content-Type", "application/a2a+json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"jsonrpc": "2.0",
		"result": map[string]interface{}{
			"id":     taskID,
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

	// ── Resolve the backend endpoint ─────────────────────────
	// ADR-0007: The control plane is the A2A gateway. Clients always call
	// /agents/{name}/a2a (stable URL). The control plane proxies to:
	//   - Managed agents: ProcessInfo.Endpoint (local/docker/k8s subprocess)
	//   - External agents: BackendEndpoint (user-provided URL)
	// This ensures auth, RBAC, observability, and rate-limiting for every call.
	backendURL := h.ResolveBackendEndpoint(agent)
	if backendURL == "" {
		w.Header().Set("Content-Type", "application/a2a+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"error": map[string]interface{}{
				"code":    -32003,
				"message": "No backend available",
				"data":    "Agent '" + agentName + "' has no running process or backend endpoint configured",
			},
			"id": nil,
		})
		return
	}

	log.Info().
		Str("agent", agentName).
		Str("backend", backendURL).
		Str("mode", string(agent.Mode)).
		Msg("Proxying A2A request to agent backend")

	// ── Proxy the request ───────────────────────────────────
	h.proxyA2ARequest(w, r, backendURL, agentName)
}

// ResolveBackendEndpoint determines where to proxy A2A calls for an agent.
// For managed agents, it returns the process endpoint (subprocess/docker/k8s).
// For external agents, it returns the user-provided backend URL.
func (h *Handlers) ResolveBackendEndpoint(agent *models.Agent) string {
	// Managed agents: use the spawned process endpoint
	if agent.Mode == models.AgentModeManaged || agent.Mode == "" {
		if agent.Process != nil && agent.Process.Status == models.ProcessRunning {
			return agent.Process.Endpoint
		}
		return ""
	}

	// External agents: use the configured backend endpoint
	if agent.Mode == models.AgentModeExternal {
		return agent.BackendEndpoint
	}

	return ""
}

// proxyA2ARequest relays an HTTP request to a backend agent endpoint and
// streams the response back to the caller. This is the core of the
// control-plane-as-gateway pattern (ADR-0007).
func (h *Handlers) proxyA2ARequest(w http.ResponseWriter, r *http.Request, backendURL, agentName string) {
	// Read the original request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.Header().Set("Content-Type", "application/a2a+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"error":   map[string]interface{}{"code": -32700, "message": "Failed to read request body"},
			"id":      nil,
		})
		return
	}

	// Ensure the backend URL has no trailing slash for the root POST
	targetURL := strings.TrimRight(backendURL, "/") + "/"

	// Create the proxied request
	proxyReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		w.Header().Set("Content-Type", "application/a2a+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"error":   map[string]interface{}{"code": -32603, "message": "Failed to create proxy request: " + err.Error()},
			"id":      nil,
		})
		return
	}
	proxyReq.Header.Set("Content-Type", "application/json")
	proxyReq.Header.Set("X-AgentOven-Agent", agentName)

	// Forward the request to the backend
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(proxyReq)
	if err != nil {
		log.Warn().Err(err).Str("agent", agentName).Str("backend", backendURL).Msg("Backend request failed")
		w.Header().Set("Content-Type", "application/a2a+json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"error":   map[string]interface{}{"code": -32603, "message": "Backend unreachable: " + err.Error()},
			"id":      nil,
		})
		return
	}
	defer resp.Body.Close()

	// Copy backend response headers and body back to the caller
	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
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

// ══════════════════════════════════════════════════════════════
// ── Prompt Handlers ──────────────────────────────────────────
// ══════════════════════════════════════════════════════════════

func (h *Handlers) ListPrompts(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	prompts, err := h.Store.ListPrompts(r.Context(), kitchen)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if prompts == nil {
		prompts = []models.Prompt{}
	}
	respondJSON(w, http.StatusOK, prompts)
}

func (h *Handlers) CreatePrompt(w http.ResponseWriter, r *http.Request) {
	var req models.Prompt
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.Name == "" || req.Template == "" {
		respondError(w, http.StatusBadRequest, "name and template are required")
		return
	}

	kitchen := middleware.GetKitchen(r.Context())
	req.ID = uuid.New().String()
	req.Kitchen = kitchen
	req.CreatedAt = time.Now().UTC()
	req.UpdatedAt = time.Now().UTC()

	// Auto-extract variables from template
	req.Variables = resolver.ExtractVariables(req.Template)

	// Run prompt validation if auto-validate is enabled
	settings, _ := h.Store.GetKitchenSettings(r.Context(), kitchen)
	var report *models.ValidationReport
	if settings != nil && settings.AutoValidate {
		var err error
		report, err = h.PromptValidator.Validate(r.Context(), &req, settings)
		if err != nil {
			log.Warn().Err(err).Str("prompt", req.Name).Msg("Validation failed")
		}
		// Block creation if there are validation errors
		if report != nil {
			for _, issue := range report.Issues {
				if issue.Severity == models.ValidationError {
					respondJSON(w, http.StatusBadRequest, map[string]interface{}{
						"error":  "Prompt validation failed",
						"report": report,
					})
					return
				}
			}
		}
	}

	if err := h.Store.CreatePrompt(r.Context(), &req); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	log.Info().Str("prompt", req.Name).Int("version", req.Version).Str("kitchen", kitchen).Msg("Prompt created")

	response := map[string]interface{}{"prompt": req}
	if report != nil {
		response["validation"] = report
	}
	respondJSON(w, http.StatusCreated, response)
}

func (h *Handlers) GetPrompt(w http.ResponseWriter, r *http.Request) {
	promptName := chi.URLParam(r, "promptName")
	kitchen := middleware.GetKitchen(r.Context())

	prompt, err := h.Store.GetPrompt(r.Context(), kitchen, promptName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	respondJSON(w, http.StatusOK, prompt)
}

func (h *Handlers) UpdatePrompt(w http.ResponseWriter, r *http.Request) {
	promptName := chi.URLParam(r, "promptName")
	kitchen := middleware.GetKitchen(r.Context())

	existing, err := h.Store.GetPrompt(r.Context(), kitchen, promptName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	var req models.Prompt
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.Template != "" {
		existing.Template = req.Template
		existing.Variables = resolver.ExtractVariables(req.Template)
	}
	if len(req.Tags) > 0 {
		existing.Tags = req.Tags
	}

	// Run prompt validation if auto-validate is enabled
	settings, _ := h.Store.GetKitchenSettings(r.Context(), kitchen)
	var report *models.ValidationReport
	if settings != nil && settings.AutoValidate {
		var err error
		report, err = h.PromptValidator.Validate(r.Context(), existing, settings)
		if err != nil {
			log.Warn().Err(err).Str("prompt", promptName).Msg("Validation failed")
		}
		if report != nil {
			for _, issue := range report.Issues {
				if issue.Severity == models.ValidationError {
					respondJSON(w, http.StatusBadRequest, map[string]interface{}{
						"error":  "Prompt validation failed",
						"report": report,
					})
					return
				}
			}
		}
	}

	if err := h.Store.UpdatePrompt(r.Context(), existing); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Re-fetch to get the new version
	updated, _ := h.Store.GetPrompt(r.Context(), kitchen, promptName)
	if updated != nil {
		existing = updated
	}

	log.Info().Str("prompt", promptName).Int("version", existing.Version).Str("kitchen", kitchen).Msg("Prompt updated")

	response := map[string]interface{}{"prompt": existing}
	if report != nil {
		response["validation"] = report
	}
	respondJSON(w, http.StatusOK, response)
}

func (h *Handlers) DeletePrompt(w http.ResponseWriter, r *http.Request) {
	promptName := chi.URLParam(r, "promptName")
	kitchen := middleware.GetKitchen(r.Context())

	if err := h.Store.DeletePrompt(r.Context(), kitchen, promptName); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	log.Info().Str("prompt", promptName).Str("kitchen", kitchen).Msg("Prompt deleted")
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) ListPromptVersions(w http.ResponseWriter, r *http.Request) {
	promptName := chi.URLParam(r, "promptName")
	kitchen := middleware.GetKitchen(r.Context())

	versions, err := h.Store.ListPromptVersions(r.Context(), kitchen, promptName)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if versions == nil {
		versions = []models.Prompt{}
	}
	respondJSON(w, http.StatusOK, versions)
}

func (h *Handlers) GetPromptVersion(w http.ResponseWriter, r *http.Request) {
	promptName := chi.URLParam(r, "promptName")
	kitchen := middleware.GetKitchen(r.Context())
	versionStr := chi.URLParam(r, "version")

	version, err := strconv.Atoi(versionStr)
	if err != nil {
		respondError(w, http.StatusBadRequest, "version must be an integer")
		return
	}

	prompt, err := h.Store.GetPromptVersion(r.Context(), kitchen, promptName, version)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	respondJSON(w, http.StatusOK, prompt)
}

// ══════════════════════════════════════════════════════════════
// ── Agent Config Handler ─────────────────────────────────────
// ══════════════════════════════════════════════════════════════

// GetAgentConfig returns the fully resolved ingredient configuration for an agent.
// This is the endpoint external agents call to fetch their runtime config.
// GET /api/v1/agents/{agentName}/config
func (h *Handlers) GetAgentConfig(w http.ResponseWriter, r *http.Request) {
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

	resolved, err := h.Resolver.Resolve(r.Context(), agent)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error":   "Ingredient resolution failed",
			"details": err.Error(),
			"partial": resolved,
		})
		return
	}

	respondJSON(w, http.StatusOK, models.AgentConfig{
		Agent:       *agent,
		Ingredients: *resolved,
	})
}

// GetAgentProcess returns the process status for a running agent.
// GET /api/v1/agents/{agentName}/process
func (h *Handlers) GetAgentProcess(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	kitchen := middleware.GetKitchen(r.Context())

	if h.ProcessManager == nil {
		respondError(w, http.StatusServiceUnavailable, "Process manager not available")
		return
	}

	info, err := h.ProcessManager.Status(r.Context(), kitchen, agentName)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if info == nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"agent":   agentName,
			"status":  "no_process",
			"message": "No process tracked for this agent. Agent may be in external mode or not yet baked.",
		})
		return
	}

	respondJSON(w, http.StatusOK, info)
}

// ListProcesses returns all running agent processes.
// GET /api/v1/processes
func (h *Handlers) ListProcesses(w http.ResponseWriter, r *http.Request) {
	if h.ProcessManager == nil {
		respondJSON(w, http.StatusOK, []interface{}{})
		return
	}

	running := h.ProcessManager.ListRunning()
	if running == nil {
		running = []*models.ProcessInfo{}
	}
	respondJSON(w, http.StatusOK, running)
}

// StreamAgentLogs streams live agent process logs via Server-Sent Events.
// GET /api/v1/agents/{agentName}/logs
func (h *Handlers) StreamAgentLogs(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	kitchen := middleware.GetKitchen(r.Context())

	if h.ProcessManager == nil {
		respondError(w, http.StatusServiceUnavailable, "process manager not available")
		return
	}

	logBuf := h.ProcessManager.GetLogBuffer(kitchen, agentName)
	if logBuf == nil {
		respondError(w, http.StatusNotFound, fmt.Sprintf("no running process for agent '%s'", agentName))
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		respondError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Send recent log history first
	recent := logBuf.Recent(200)
	for _, entry := range recent {
		data, _ := json.Marshal(entry)
		fmt.Fprintf(w, "data: %s\n\n", data)
	}
	flusher.Flush()

	// Subscribe to live updates
	ch := logBuf.Subscribe()
	defer logBuf.Unsubscribe(ch)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case entry, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(entry)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// GetAgentLogs returns the recent log history for an agent (non-streaming).
// GET /api/v1/agents/{agentName}/logs/recent
func (h *Handlers) GetAgentLogs(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	kitchen := middleware.GetKitchen(r.Context())

	if h.ProcessManager == nil {
		respondError(w, http.StatusServiceUnavailable, "process manager not available")
		return
	}

	logBuf := h.ProcessManager.GetLogBuffer(kitchen, agentName)
	if logBuf == nil {
		respondError(w, http.StatusNotFound, fmt.Sprintf("no running process for agent '%s'", agentName))
		return
	}

	entries := logBuf.Recent(500)
	if entries == nil {
		entries = []process.LogEntry{}
	}
	respondJSON(w, http.StatusOK, entries)
}

// ══════════════════════════════════════════════════════════════
// ── Managed Agent Invoke Handler ─────────────────────────────
// ══════════════════════════════════════════════════════════════

// InvokeAgent executes a managed-mode agent's agentic loop.
// POST /api/v1/agents/{agentName}/invoke
func (h *Handlers) InvokeAgent(w http.ResponseWriter, r *http.Request) {
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

	if agent.Mode != models.AgentModeManaged {
		respondError(w, http.StatusBadRequest,
			fmt.Sprintf("Agent '%s' is in '%s' mode — use the A2A endpoint for external agents", agentName, agent.Mode))
		return
	}

	// Parse request body early so guardrails can evaluate before status check
	var req struct {
		Message         string            `json:"message"`
		Variables       map[string]string `json:"variables,omitempty"`        // prompt template variables
		ThinkingEnabled bool              `json:"thinking_enabled,omitempty"` // enable extended thinking
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Message == "" {
		respondError(w, http.StatusBadRequest, "Request must include a non-empty 'message' field")
		return
	}

	// ── Input Guardrails ────────────────────────────────────
	// Evaluate BEFORE status check — bad input should be rejected
	// regardless of agent state (security-first principle).
	if h.Guardrails != nil && len(agent.Guardrails) > 0 {
		eval, gErr := h.Guardrails.EvaluateInput(r.Context(), agent.Guardrails, req.Message)
		if gErr != nil {
			log.Warn().Err(gErr).Str("agent", agentName).Msg("Input guardrail evaluation error")
		} else if !eval.Passed {
			respondJSON(w, http.StatusForbidden, map[string]interface{}{
				"error":      "Input blocked by guardrails",
				"guardrails": eval.Results,
			})
			return
		}
	}

	if agent.Status != models.AgentStatusReady {
		respondError(w, http.StatusBadRequest,
			fmt.Sprintf("Agent '%s' is not ready (status: %s) — bake it first", agentName, agent.Status))
		return
	}

	// When thinking is enabled, extend the request timeout to avoid premature cancellation.
	// Thinking models (o-series, Claude extended thinking) can take 30-120s per turn.
	if req.ThinkingEnabled {
		var cancel context.CancelFunc
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		r = r.WithContext(ctx)
		_ = ctx // use new ctx
	}

	// Use cached resolved config from bake time, or re-resolve live
	var resolved *models.ResolvedIngredients
	if agent.ResolvedConfig != nil {
		resolved = agent.ResolvedConfig
	} else {
		resolved, err = h.Resolver.Resolve(r.Context(), agent)
		if err != nil {
			respondError(w, http.StatusBadRequest, "Ingredient resolution failed: "+err.Error())
			return
		}
	}

	// Sanitize variable values before prompt rendering
	sanitizedVars := req.Variables
	if len(req.Variables) > 0 {
		settings, _ := h.Store.GetKitchenSettings(r.Context(), kitchen)
		var issues []models.ValidationIssue
		sanitizedVars, issues, err = h.PromptValidator.SanitizeVariables(r.Context(), req.Variables, settings)
		if err != nil {
			respondError(w, http.StatusBadRequest, "Variable sanitization failed: "+err.Error())
			return
		}
		for _, issue := range issues {
			if issue.Severity == models.ValidationError {
				respondJSON(w, http.StatusBadRequest, map[string]interface{}{
					"error":  "Variable injection detected",
					"issues": issues,
				})
				return
			}
		}
	}

	// Execute the agentic loop
	response, trace, err := h.Executor.Execute(r.Context(), agent, req.Message, resolved, sanitizedVars, req.ThinkingEnabled)
	if err != nil {
		respondError(w, http.StatusBadGateway, "Execution failed: "+err.Error())
		return
	}

	// ── Output Guardrails ───────────────────────────────────
	if h.Guardrails != nil && len(agent.Guardrails) > 0 {
		eval, gErr := h.Guardrails.EvaluateOutput(r.Context(), agent.Guardrails, response)
		if gErr != nil {
			log.Warn().Err(gErr).Str("agent", agentName).Msg("Output guardrail evaluation error")
		} else if !eval.Passed {
			respondJSON(w, http.StatusForbidden, map[string]interface{}{
				"error":      "Output blocked by guardrails",
				"guardrails": eval.Results,
			})
			return
		}
	}

	// Record trace in store
	traceRecord := &models.Trace{
		ID:          trace.TraceID,
		AgentName:   agentName,
		Kitchen:     kitchen,
		Status:      "completed",
		DurationMs:  trace.TotalMs,
		TotalTokens: trace.Usage.TotalTokens,
		CostUSD:     trace.Usage.EstimatedCost,
		Metadata: map[string]interface{}{
			"mode":  "managed",
			"turns": len(trace.Turns),
			"type":  "invoke",
		},
		CreatedAt: time.Now().UTC(),
	}
	h.Store.CreateTrace(r.Context(), traceRecord)

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"agent":           agentName,
		"response":        response,
		"trace_id":        trace.TraceID,
		"turns":           len(trace.Turns),
		"usage":           trace.Usage,
		"latency_ms":      trace.TotalMs,
		"execution_trace": trace,
	})
}

// ══════════════════════════════════════════════════════════════
// ── Guardrails Handlers ──────────────────────────────────────
// ══════════════════════════════════════════════════════════════

// ListGuardrailKinds returns the available guardrail types and their configuration schemas.
// GET /api/v1/guardrails/kinds
func (h *Handlers) ListGuardrailKinds(w http.ResponseWriter, r *http.Request) {
	kinds := []map[string]interface{}{
		{
			"kind":        "content_filter",
			"label":       "Content Filter",
			"description": "Block messages containing specific words or phrases",
			"config_schema": map[string]interface{}{
				"blocked_words":  "string[]  — list of blocked words/phrases",
				"case_sensitive": "boolean   — whether matching is case-sensitive (default: false)",
			},
		},
		{
			"kind":        "pii_detection",
			"label":       "PII Detection",
			"description": "Detect and block personally identifiable information",
			"config_schema": map[string]interface{}{
				"patterns": "string[]  — PII types to check: email, phone, ssn, credit_card (default: all)",
			},
		},
		{
			"kind":        "topic_restriction",
			"label":       "Topic Restriction",
			"description": "Restrict conversation to allowed topics or block specific topics",
			"config_schema": map[string]interface{}{
				"allowed_topics": "string[]  — if set, messages must contain at least one allowed topic",
				"blocked_topics": "string[]  — messages containing these topics are blocked",
			},
		},
		{
			"kind":        "max_length",
			"label":       "Max Length",
			"description": "Enforce character or word length limits",
			"config_schema": map[string]interface{}{
				"max_characters": "integer  — maximum character count",
				"max_words":      "integer  — maximum word count",
			},
		},
		{
			"kind":        "regex_filter",
			"label":       "Regex Filter",
			"description": "Match messages against a custom regex pattern",
			"config_schema": map[string]interface{}{
				"pattern":        "string   — regex pattern to match against",
				"block_on_match": "boolean  — block when pattern matches (default: true)",
			},
		},
		{
			"kind":        "prompt_injection",
			"label":       "Prompt Injection",
			"description": "Detect common prompt injection and jailbreak attempts",
			"config_schema": map[string]interface{}{
				"sensitivity": "string  — detection sensitivity: high, medium, low (default: medium)",
			},
		},
		{
			"kind":        "custom",
			"label":       "Custom",
			"description": "Custom guardrail (Pro: webhook/LLM-judge, OSS: no-op pass-through)",
			"config_schema": map[string]interface{}{
				"webhook_url": "string  — (Pro only) URL to call for custom evaluation",
			},
		},
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"kinds":  kinds,
		"stages": []string{"input", "output", "both"},
	})
}

// ══════════════════════════════════════════════════════════════
// ── Kitchen Settings Handlers ────────────────────────────────
// ══════════════════════════════════════════════════════════════

// GetKitchenSettings returns the settings for the current kitchen.
// GET /api/v1/settings
func (h *Handlers) GetKitchenSettings(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	settings, err := h.Store.GetKitchenSettings(r.Context(), kitchen)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Mask the validation API key before returning
	if settings.ValidationAPIKey != "" {
		if len(settings.ValidationAPIKey) > 4 {
			settings.ValidationAPIKey = settings.ValidationAPIKey[:4] + "****"
		} else {
			settings.ValidationAPIKey = "****"
		}
	}
	respondJSON(w, http.StatusOK, settings)
}

// UpdateKitchenSettings updates the settings for the current kitchen.
// PUT /api/v1/settings
func (h *Handlers) UpdateKitchenSettings(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())

	var req models.KitchenSettings
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Merge with existing settings (preserve API key if not sent)
	existing, _ := h.Store.GetKitchenSettings(r.Context(), kitchen)
	if existing != nil {
		if req.ValidationAPIKey == "" {
			req.ValidationAPIKey = existing.ValidationAPIKey
		}
		if req.ValidationProvider == "" {
			req.ValidationProvider = existing.ValidationProvider
		}
		if req.ValidationModel == "" {
			req.ValidationModel = existing.ValidationModel
		}
		if req.ValidationEndpoint == "" {
			req.ValidationEndpoint = existing.ValidationEndpoint
		}
	}

	req.KitchenID = kitchen
	req.UpdatedAt = time.Now().UTC()

	if err := h.Store.UpsertKitchenSettings(r.Context(), &req); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	log.Info().Str("kitchen", kitchen).Msg("Kitchen settings updated")

	// Mask API key in response
	if req.ValidationAPIKey != "" {
		if len(req.ValidationAPIKey) > 4 {
			req.ValidationAPIKey = req.ValidationAPIKey[:4] + "****"
		} else {
			req.ValidationAPIKey = "****"
		}
	}
	respondJSON(w, http.StatusOK, req)
}

// ══════════════════════════════════════════════════════════════
// ── Prompt Validation Handler ────────────────────────────────
// ══════════════════════════════════════════════════════════════

// ValidatePrompt runs the prompt validator on a specific prompt.
// POST /api/v1/prompts/{promptName}/validate
func (h *Handlers) ValidatePrompt(w http.ResponseWriter, r *http.Request) {
	promptName := chi.URLParam(r, "promptName")
	kitchen := middleware.GetKitchen(r.Context())

	prompt, err := h.Store.GetPrompt(r.Context(), kitchen, promptName)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	settings, _ := h.Store.GetKitchenSettings(r.Context(), kitchen)

	report, err := h.PromptValidator.Validate(r.Context(), prompt, settings)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Validation failed: "+err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"report":  report,
		"edition": h.PromptValidator.Edition(),
	})
}

// ══════════════════════════════════════════════════════════════
// ── Approval Handlers ────────────────────────────────────────
// ══════════════════════════════════════════════════════════════

// ListApprovals returns approval records for the current kitchen.
func (h *Handlers) ListApprovals(w http.ResponseWriter, r *http.Request) {
	kitchen := r.Header.Get("X-Kitchen")
	status := r.URL.Query().Get("status")
	approvals, err := h.Store.ListApprovals(r.Context(), kitchen, status, 100)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if approvals == nil {
		approvals = []models.ApprovalRecord{}
	}
	respondJSON(w, http.StatusOK, approvals)
}

// GetApproval returns a single approval record by gate key.
func (h *Handlers) GetApproval(w http.ResponseWriter, r *http.Request) {
	gateKey := chi.URLParam(r, "gateKey")
	record, err := h.Store.GetApproval(r.Context(), gateKey)
	if err != nil {
		respondError(w, http.StatusNotFound, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, record)
}

// ApproveGateWithMetadata approves or rejects a gate with full approver identity.
func (h *Handlers) ApproveGateWithMetadata(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runId")
	stepName := chi.URLParam(r, "stepName")

	var req struct {
		Approved      bool   `json:"approved"`
		ApproverID    string `json:"approver_id,omitempty"`
		ApproverEmail string `json:"approver_email,omitempty"`
		Channel       string `json:"channel,omitempty"`
		Comments      string `json:"comments,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	ok := h.Workflow.ApproveGateWithMetadata(runID, stepName, req.Approved, req.ApproverID, req.ApproverEmail, req.Channel, req.Comments)
	if !ok {
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
// ── Notification Channel Handlers ────────────────────────────
// ══════════════════════════════════════════════════════════════

// ListChannels returns notification channels for the current kitchen.
func (h *Handlers) ListChannels(w http.ResponseWriter, r *http.Request) {
	kitchen := r.Header.Get("X-Kitchen")
	channels, err := h.Store.ListChannels(r.Context(), kitchen)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if channels == nil {
		channels = []models.NotificationChannel{}
	}
	respondJSON(w, http.StatusOK, channels)
}

// GetChannel returns a single notification channel.
func (h *Handlers) GetChannel(w http.ResponseWriter, r *http.Request) {
	kitchen := r.Header.Get("X-Kitchen")
	name := chi.URLParam(r, "channelName")
	channel, err := h.Store.GetChannel(r.Context(), kitchen, name)
	if err != nil {
		respondError(w, http.StatusNotFound, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, channel)
}

// CreateChannel creates a new notification channel.
func (h *Handlers) CreateChannel(w http.ResponseWriter, r *http.Request) {
	var ch models.NotificationChannel
	if err := json.NewDecoder(r.Body).Decode(&ch); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	ch.Kitchen = r.Header.Get("X-Kitchen")
	if ch.Name == "" {
		respondError(w, http.StatusBadRequest, "Channel name is required")
		return
	}
	if ch.Kind == "" {
		respondError(w, http.StatusBadRequest, "Channel kind is required")
		return
	}

	ch.ID = uuid.New().String()
	now := time.Now().UTC()
	ch.CreatedAt = now
	ch.UpdatedAt = now
	ch.Active = true

	if err := h.Store.CreateChannel(r.Context(), &ch); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusCreated, ch)
}

// UpdateChannel updates an existing notification channel.
func (h *Handlers) UpdateChannel(w http.ResponseWriter, r *http.Request) {
	kitchen := r.Header.Get("X-Kitchen")
	name := chi.URLParam(r, "channelName")

	existing, err := h.Store.GetChannel(r.Context(), kitchen, name)
	if err != nil {
		respondError(w, http.StatusNotFound, err.Error())
		return
	}

	var update models.NotificationChannel
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	existing.Kind = update.Kind
	existing.URL = update.URL
	existing.Secret = update.Secret
	existing.Config = update.Config
	existing.Events = update.Events
	existing.Active = update.Active
	existing.UpdatedAt = time.Now().UTC()

	if err := h.Store.UpdateChannel(r.Context(), existing); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, existing)
}

// DeleteChannel removes a notification channel.
func (h *Handlers) DeleteChannel(w http.ResponseWriter, r *http.Request) {
	kitchen := r.Header.Get("X-Kitchen")
	name := chi.URLParam(r, "channelName")

	if err := h.Store.DeleteChannel(r.Context(), kitchen, name); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
}

// ══════════════════════════════════════════════════════════════
// ── Audit Event Handlers ─────────────────────────────────────
// ══════════════════════════════════════════════════════════════

// ListAuditEvents returns audit events for the current kitchen.
func (h *Handlers) ListAuditEvents(w http.ResponseWriter, r *http.Request) {
	kitchen := r.Header.Get("X-Kitchen")
	filter := models.AuditFilter{
		Kitchen: kitchen,
		Limit:   100,
	}
	if q := r.URL.Query().Get("action"); q != "" {
		filter.Action = q
	}
	if q := r.URL.Query().Get("user_id"); q != "" {
		filter.UserID = q
	}
	if q := r.URL.Query().Get("resource"); q != "" {
		filter.Resource = q
	}

	events, err := h.Store.ListAuditEvents(r.Context(), filter)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []models.AuditEvent{}
	}
	respondJSON(w, http.StatusOK, events)
}

// CountAuditEvents returns the count of audit events matching the filter.
func (h *Handlers) CountAuditEvents(w http.ResponseWriter, r *http.Request) {
	kitchen := r.Header.Get("X-Kitchen")
	filter := models.AuditFilter{Kitchen: kitchen}

	count, err := h.Store.CountAuditEvents(r.Context(), filter)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]int64{"count": count})
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

// ══════════════════════════════════════════════════════════════
// ── Model Catalog Handlers (R8) ─────────────────────────────
// ══════════════════════════════════════════════════════════════

// ListCatalog returns all known model capabilities from the catalog.
func (h *Handlers) ListCatalog(w http.ResponseWriter, r *http.Request) {
	if h.Catalog == nil {
		respondError(w, http.StatusServiceUnavailable, "model catalog not initialized")
		return
	}

	// Optional filter by provider kind
	providerKind := r.URL.Query().Get("provider")
	var caps []*models.ModelCapability
	if providerKind != "" {
		caps = h.Catalog.ListByProvider(providerKind)
	} else {
		caps = h.Catalog.ListAll()
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"models": caps,
		"count":  len(caps),
	})
}

// GetCatalogModel returns capability data for a specific model.
func (h *Handlers) GetCatalogModel(w http.ResponseWriter, r *http.Request) {
	if h.Catalog == nil {
		respondError(w, http.StatusServiceUnavailable, "model catalog not initialized")
		return
	}

	modelID := chi.URLParam(r, "modelID")
	if modelID == "" {
		respondError(w, http.StatusBadRequest, "modelID is required")
		return
	}

	// URL-decode the modelID (it may contain slashes encoded as %2F)
	cap := h.Catalog.LookupByID(modelID)
	if cap == nil {
		// Try with provider prefix
		providerKind := r.URL.Query().Get("provider")
		if providerKind != "" {
			cap = h.Catalog.Lookup(providerKind, modelID)
		}
	}
	if cap == nil {
		respondError(w, http.StatusNotFound, "model not found in catalog: "+modelID)
		return
	}

	respondJSON(w, http.StatusOK, cap)
}

// RefreshCatalog forces a refresh of the model catalog from LiteLLM.
func (h *Handlers) RefreshCatalog(w http.ResponseWriter, r *http.Request) {
	if h.Catalog == nil {
		respondError(w, http.StatusServiceUnavailable, "model catalog not initialized")
		return
	}

	go func() {
		ctx := context.Background()
		h.Catalog.Refresh(ctx)
	}()

	respondJSON(w, http.StatusAccepted, map[string]string{
		"status":  "refreshing",
		"message": "Catalog refresh started in background",
	})
}

// DiscoverModels triggers model discovery for a specific provider.
func (h *Handlers) DiscoverModels(w http.ResponseWriter, r *http.Request) {
	providerName := chi.URLParam(r, "providerName")

	// Look up the provider
	provider, err := h.Store.GetProvider(r.Context(), providerName)
	if err != nil {
		respondError(w, http.StatusNotFound, "provider not found: "+providerName)
		return
	}

	// Call the discovery driver
	discovered, err := h.Router.DiscoverModelsForProvider(r.Context(), provider)
	if err != nil {
		respondError(w, http.StatusBadRequest, "discovery failed: "+err.Error())
		return
	}

	// Merge into catalog
	if h.Catalog != nil {
		h.Catalog.RegisterDiscovered(discovered)
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"provider":   providerName,
		"discovered": discovered,
		"count":      len(discovered),
	})
}

// ══════════════════════════════════════════════════════════════
// ── Session Handlers (R8) ────────────────────────────────────
// ══════════════════════════════════════════════════════════════

// CreateSession starts a new multi-turn conversation session with an agent.
func (h *Handlers) CreateSession(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	agentName := chi.URLParam(r, "agentName")

	if h.Sessions == nil {
		respondError(w, http.StatusServiceUnavailable, "session store not initialized")
		return
	}

	// Verify agent exists
	agent, err := h.Store.GetAgent(r.Context(), kitchen, agentName)
	if err != nil {
		respondError(w, http.StatusNotFound, "agent not found: "+agentName)
		return
	}
	if agent.Status != models.AgentStatusReady {
		respondError(w, http.StatusConflict, "agent must be in ready status to create sessions")
		return
	}

	// Parse optional config from body
	var req struct {
		Metadata map[string]interface{} `json:"metadata,omitempty"`
		MaxTurns int                    `json:"max_turns,omitempty"`
	}
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&req) // optional body
	}

	session := &models.Session{
		ID:        uuid.New().String(),
		Kitchen:   kitchen,
		AgentName: agentName,
		Status:    models.SessionActive,
		Messages:  []models.ChatMessage{},
		TurnCount: 0,
		MaxTurns:  req.MaxTurns,
		Metadata:  req.Metadata,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	if err := h.Sessions.CreateSession(r.Context(), session); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, http.StatusCreated, session)
}

// GetSession retrieves session details.
func (h *Handlers) GetSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	if h.Sessions == nil {
		respondError(w, http.StatusServiceUnavailable, "session store not initialized")
		return
	}

	session, err := h.Sessions.GetSession(r.Context(), sessionID)
	if err != nil {
		respondError(w, http.StatusNotFound, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, session)
}

// ListSessions lists all sessions for an agent.
func (h *Handlers) ListSessions(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	agentName := chi.URLParam(r, "agentName")

	if h.Sessions == nil {
		respondError(w, http.StatusServiceUnavailable, "session store not initialized")
		return
	}

	sessions, err := h.Sessions.ListSessions(r.Context(), kitchen, agentName)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sessions == nil {
		sessions = []models.Session{}
	}

	respondJSON(w, http.StatusOK, sessions)
}

// DeleteSession removes a session.
func (h *Handlers) DeleteSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")

	if h.Sessions == nil {
		respondError(w, http.StatusServiceUnavailable, "session store not initialized")
		return
	}

	if err := h.Sessions.DeleteSession(r.Context(), sessionID); err != nil {
		respondError(w, http.StatusNotFound, err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// SendSessionMessage sends a user message to a session and gets the agent's response.
func (h *Handlers) SendSessionMessage(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	agentName := chi.URLParam(r, "agentName")
	sessionID := chi.URLParam(r, "sessionID")

	if h.Sessions == nil {
		respondError(w, http.StatusServiceUnavailable, "session store not initialized")
		return
	}

	// Parse user message
	var req models.SessionMessage
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Content == "" && len(req.ContentParts) == 0 {
		respondError(w, http.StatusBadRequest, "content is required")
		return
	}

	// Get existing session
	session, err := h.Sessions.GetSession(r.Context(), sessionID)
	if err != nil {
		respondError(w, http.StatusNotFound, err.Error())
		return
	}
	if session.Status != models.SessionActive {
		respondError(w, http.StatusConflict, "session is not active (status: "+string(session.Status)+")")
		return
	}
	if session.MaxTurns > 0 && session.TurnCount >= session.MaxTurns {
		respondError(w, http.StatusConflict, "session has reached max turns")
		return
	}

	// Verify agent
	agent, err := h.Store.GetAgent(r.Context(), kitchen, agentName)
	if err != nil {
		respondError(w, http.StatusNotFound, "agent not found: "+agentName)
		return
	}

	// Append user message to session history
	userMsg := models.ChatMessage{
		Role:    "user",
		Content: req.Content,
	}
	session.Messages = append(session.Messages, userMsg)

	// ── Input Guardrails (Session) ──────────────────────────
	if h.Guardrails != nil && len(agent.Guardrails) > 0 {
		eval, gErr := h.Guardrails.EvaluateInput(r.Context(), agent.Guardrails, req.Content)
		if gErr != nil {
			log.Warn().Err(gErr).Str("agent", agentName).Msg("Session input guardrail error")
		} else if !eval.Passed {
			// Remove the user message we just appended
			session.Messages = session.Messages[:len(session.Messages)-1]
			respondJSON(w, http.StatusForbidden, map[string]interface{}{
				"error":      "Input blocked by guardrails",
				"guardrails": eval.Results,
			})
			return
		}
	}

	// Route through model router with full conversation history
	startTime := time.Now()
	routeReq := &models.RouteRequest{
		Kitchen:   kitchen,
		Messages:  session.Messages,
		SessionID: sessionID,
		AgentRef:  agentName,
	}
	// Apply agent's model if configured
	if agent.ModelName != "" {
		routeReq.Model = agent.ModelName
	}

	// Use RouteWithBackup if agent has a backup provider configured
	var routeResp *models.RouteResponse
	if agent.BackupProvider != "" {
		routeResp, err = h.Router.RouteWithBackup(r.Context(), routeReq, agent.BackupProvider, agent.BackupModel)
	} else {
		routeResp, err = h.Router.Route(r.Context(), routeReq)
	}
	if err != nil {
		respondError(w, http.StatusInternalServerError, "model routing failed: "+err.Error())
		return
	}
	latencyMs := time.Since(startTime).Milliseconds()

	// ── Output Guardrails (Session) ─────────────────────────
	if h.Guardrails != nil && len(agent.Guardrails) > 0 {
		eval, gErr := h.Guardrails.EvaluateOutput(r.Context(), agent.Guardrails, routeResp.Content)
		if gErr != nil {
			log.Warn().Err(gErr).Str("agent", agentName).Msg("Session output guardrail error")
		} else if !eval.Passed {
			// Remove the user message we appended (response never reaches the user)
			session.Messages = session.Messages[:len(session.Messages)-1]
			respondJSON(w, http.StatusForbidden, map[string]interface{}{
				"error":      "Output blocked by guardrails",
				"guardrails": eval.Results,
			})
			return
		}
	}

	// Append assistant response to session
	assistantMsg := models.ChatMessage{
		Role:    "assistant",
		Content: routeResp.Content,
	}
	session.Messages = append(session.Messages, assistantMsg)
	session.TurnCount++
	session.TotalTokens += routeResp.Usage.TotalTokens
	session.TotalCost += routeResp.Usage.EstimatedCost
	session.UpdatedAt = time.Now().UTC()

	// Check if max turns reached
	if session.MaxTurns > 0 && session.TurnCount >= session.MaxTurns {
		session.Status = models.SessionCompleted
	}

	// Update session in store
	if err := h.Sessions.UpdateSession(r.Context(), session); err != nil {
		log.Error().Err(err).Str("session", sessionID).Msg("Failed to update session")
	}

	resp := models.SessionResponse{
		SessionID:    sessionID,
		TurnNumber:   session.TurnCount,
		Content:      routeResp.Content,
		FinishReason: routeResp.FinishReason,
		Usage:        routeResp.Usage,
		LatencyMs:    latencyMs,
		Status:       session.Status,
	}

	respondJSON(w, http.StatusOK, resp)
}

// ══════════════════════════════════════════════════════════════
// ── Agent Card Handler (R8) ──────────────────────────────────
// ══════════════════════════════════════════════════════════════

// GetAgentCard returns an A2A-compatible agent card for a specific agent.
func (h *Handlers) GetAgentCard(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	agentName := chi.URLParam(r, "agentName")

	agent, err := h.Store.GetAgent(r.Context(), kitchen, agentName)
	if err != nil {
		respondError(w, http.StatusNotFound, "agent not found: "+agentName)
		return
	}

	// Build agent card from agent metadata
	card := models.AgentCard{
		Name:        agent.Name,
		Description: agent.Description,
		Version:     agent.Version,
		Provider: models.AgentCardProvider{
			Organization: "AgentOven",
		},
	}

	// Build capabilities from agent's ingredients and status
	card.Capabilities = models.AgentCapabilities{
		Streaming: true,
		Sessions:  true,
	}

	// Determine supported input/output modes
	card.InputModes = []string{"text"}
	card.OutputModes = []string{"text"}

	// Check for tool use in ingredients
	for _, ing := range agent.Ingredients {
		if ing.Kind == "tool" {
			card.Capabilities.ToolCalling = true
			// Add as a skill
			card.Skills = append(card.Skills, models.AgentSkill{
				ID:          ing.Name,
				Name:        ing.Name,
				Description: "MCP tool: " + ing.Name,
			})
		}
	}

	// Check for vision support via catalog
	if h.Catalog != nil && agent.ModelName != "" {
		providerKind := ""
		if agent.ModelProvider != "" {
			if prov, err := h.Store.GetProvider(r.Context(), agent.ModelProvider); err == nil {
				providerKind = prov.Kind
			}
		}
		if cap := h.Catalog.Lookup(providerKind, agent.ModelName); cap != nil {
			if cap.SupportsVision {
				card.InputModes = append(card.InputModes, "image")
				card.Capabilities.Vision = true
			}
			if cap.SupportsJSON {
				card.Capabilities.StructuredOutput = true
			}
		}
	}

	// Set URL — always the stable control plane endpoint (ADR-0007).
	// Never expose subprocess/backend URLs in the agent card.
	card.URL = "/agents/" + agent.Name + "/a2a"

	respondJSON(w, http.StatusOK, card)
}

// ══════════════════════════════════════════════════════════════
// ── Discovery-Capable Providers Handler (R8) ────────────────
// ══════════════════════════════════════════════════════════════

// ListDiscoveryDrivers returns which provider kinds support model discovery.
func (h *Handlers) ListDiscoveryDrivers(w http.ResponseWriter, r *http.Request) {
	drivers := h.Router.ListDiscoveryCapableDrivers()
	var kinds []string
	for k := range drivers {
		kinds = append(kinds, k)
	}
	if kinds == nil {
		kinds = []string{}
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"discovery_capable": kinds,
	})
}
