package handlers

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// ── In-Memory Store (Phase 1) ────────────────────────────────
// Production will use PostgreSQL via pgx; this enables a fully
// functional API without a database dependency.

var (
	agents     = make(map[string]*models.Agent)   // key: name
	recipes    = make(map[string]*models.Recipe)   // key: name
	kitchens   = make(map[string]*models.Kitchen)  // key: id
	traces     = make(map[string]*models.Trace)    // key: id
	storeMu    sync.RWMutex
)

func init() {
	// Seed default kitchen
	kitchens["default"] = &models.Kitchen{
		ID:          "default",
		Name:        "Default Kitchen",
		Description: "The default workspace",
		Owner:       "system",
		CreatedAt:   time.Now().UTC(),
	}
}

// ── Agent Handlers ───────────────────────────────────────────

func ListAgents(w http.ResponseWriter, r *http.Request) {
	storeMu.RLock()
	defer storeMu.RUnlock()

	result := make([]models.Agent, 0, len(agents))
	for _, a := range agents {
		result = append(result, *a)
	}
	respondJSON(w, http.StatusOK, result)
}

func RegisterAgent(w http.ResponseWriter, r *http.Request) {
	var req models.Agent
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Set server-side fields
	req.ID = uuid.New().String()
	req.Status = models.AgentStatusDraft
	req.CreatedAt = time.Now().UTC()
	req.UpdatedAt = time.Now().UTC()

	storeMu.Lock()
	agents[req.Name] = &req
	storeMu.Unlock()

	log.Info().Str("agent", req.Name).Str("id", req.ID).Msg("Agent registered")

	// Auto-generate A2A Agent Card metadata
	req.A2AEndpoint = "/agents/" + req.Name + "/a2a"

	respondJSON(w, http.StatusCreated, req)
}

func GetAgent(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")

	storeMu.RLock()
	agent, ok := agents[agentName]
	storeMu.RUnlock()

	if !ok {
		respondError(w, http.StatusNotFound, "Agent not found: "+agentName)
		return
	}
	respondJSON(w, http.StatusOK, agent)
}

func UpdateAgent(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")

	storeMu.Lock()
	defer storeMu.Unlock()

	agent, ok := agents[agentName]
	if !ok {
		respondError(w, http.StatusNotFound, "Agent not found: "+agentName)
		return
	}

	var req models.Agent
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Merge mutable fields
	if req.Description != "" {
		agent.Description = req.Description
	}
	if req.Framework != "" {
		agent.Framework = req.Framework
	}
	if len(req.Ingredients) > 0 {
		agent.Ingredients = req.Ingredients
	}
	agent.UpdatedAt = time.Now().UTC()

	respondJSON(w, http.StatusOK, agent)
}

func DeleteAgent(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")

	storeMu.Lock()
	defer storeMu.Unlock()

	if _, ok := agents[agentName]; !ok {
		respondError(w, http.StatusNotFound, "Agent not found: "+agentName)
		return
	}

	// Soft-delete: mark as retired
	agents[agentName].Status = models.AgentStatusRetired
	agents[agentName].UpdatedAt = time.Now().UTC()

	log.Info().Str("agent", agentName).Msg("Agent retired")
	w.WriteHeader(http.StatusNoContent)
}

func BakeAgent(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")

	var req struct {
		Version     string `json:"version"`
		Environment string `json:"environment"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	storeMu.Lock()
	agent, ok := agents[agentName]
	if !ok {
		storeMu.Unlock()
		respondError(w, http.StatusNotFound, "Agent not found: "+agentName)
		return
	}

	// Transition to baking state
	agent.Status = models.AgentStatusBaking
	agent.UpdatedAt = time.Now().UTC()
	if req.Version != "" {
		agent.Version = req.Version
	}
	agent.A2AEndpoint = "/agents/" + agentName + "/a2a"
	storeMu.Unlock()

	log.Info().
		Str("agent", agentName).
		Str("version", agent.Version).
		Str("environment", req.Environment).
		Msg("Agent baking started")

	// In a production system, this would trigger an async deployment.
	// For Phase 1, immediately transition to ready.
	go func() {
		time.Sleep(2 * time.Second)
		storeMu.Lock()
		if a, ok := agents[agentName]; ok && a.Status == models.AgentStatusBaking {
			a.Status = models.AgentStatusReady
			a.UpdatedAt = time.Now().UTC()
			log.Info().Str("agent", agentName).Msg("Agent is ready")
		}
		storeMu.Unlock()
	}()

	respondJSON(w, http.StatusAccepted, map[string]string{
		"name":        agentName,
		"version":     agent.Version,
		"environment": req.Environment,
		"status":      string(models.AgentStatusBaking),
		"agent_card":  "/agents/" + agentName + "/a2a/.well-known/agent-card.json",
	})
}

func CoolAgent(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")

	storeMu.Lock()
	defer storeMu.Unlock()

	agent, ok := agents[agentName]
	if !ok {
		respondError(w, http.StatusNotFound, "Agent not found: "+agentName)
		return
	}

	agent.Status = models.AgentStatusCooled
	agent.UpdatedAt = time.Now().UTC()

	log.Info().Str("agent", agentName).Msg("Agent cooled")
	respondJSON(w, http.StatusOK, map[string]string{"name": agentName, "status": string(models.AgentStatusCooled)})
}

func ListAgentVersions(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, []string{})
}

func GetAgentVersion(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]string{})
}

// ── Recipe Handlers ──────────────────────────────────────────

func ListRecipes(w http.ResponseWriter, r *http.Request) {
	storeMu.RLock()
	defer storeMu.RUnlock()

	result := make([]models.Recipe, 0, len(recipes))
	for _, rec := range recipes {
		result = append(result, *rec)
	}
	respondJSON(w, http.StatusOK, result)
}

func CreateRecipe(w http.ResponseWriter, r *http.Request) {
	var req models.Recipe
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	req.ID = uuid.New().String()
	req.CreatedAt = time.Now().UTC()
	req.UpdatedAt = time.Now().UTC()

	storeMu.Lock()
	recipes[req.Name] = &req
	storeMu.Unlock()

	log.Info().Str("recipe", req.Name).Str("id", req.ID).Msg("Recipe created")
	respondJSON(w, http.StatusCreated, req)
}

func GetRecipe(w http.ResponseWriter, r *http.Request) {
	recipeName := chi.URLParam(r, "recipeName")

	storeMu.RLock()
	recipe, ok := recipes[recipeName]
	storeMu.RUnlock()

	if !ok {
		respondError(w, http.StatusNotFound, "Recipe not found: "+recipeName)
		return
	}
	respondJSON(w, http.StatusOK, recipe)
}

func UpdateRecipe(w http.ResponseWriter, r *http.Request) {
	recipeName := chi.URLParam(r, "recipeName")

	storeMu.Lock()
	defer storeMu.Unlock()

	recipe, ok := recipes[recipeName]
	if !ok {
		respondError(w, http.StatusNotFound, "Recipe not found: "+recipeName)
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

	respondJSON(w, http.StatusOK, recipe)
}

func DeleteRecipe(w http.ResponseWriter, r *http.Request) {
	recipeName := chi.URLParam(r, "recipeName")

	storeMu.Lock()
	defer storeMu.Unlock()

	if _, ok := recipes[recipeName]; !ok {
		respondError(w, http.StatusNotFound, "Recipe not found: "+recipeName)
		return
	}
	delete(recipes, recipeName)
	w.WriteHeader(http.StatusNoContent)
}

func BakeRecipe(w http.ResponseWriter, r *http.Request) {
	recipeName := chi.URLParam(r, "recipeName")

	storeMu.RLock()
	recipe, ok := recipes[recipeName]
	storeMu.RUnlock()

	if !ok {
		respondError(w, http.StatusNotFound, "Recipe not found: "+recipeName)
		return
	}

	taskID := uuid.New().String()

	log.Info().
		Str("recipe", recipeName).
		Str("task_id", taskID).
		Int("steps", len(recipe.Steps)).
		Msg("Recipe execution started")

	// In production, this kicks off the workflow engine.
	// For Phase 1, return the task ID immediately.
	respondJSON(w, http.StatusAccepted, map[string]string{
		"recipe":  recipeName,
		"status":  "baking",
		"task_id": taskID,
	})
}

func RecipeHistory(w http.ResponseWriter, r *http.Request) {
	// Phase 1: return empty history
	respondJSON(w, http.StatusOK, []string{})
}

// ── Model Router Handlers ────────────────────────────────────

func ListProviders(w http.ResponseWriter, r *http.Request) {
	providers := []map[string]interface{}{
		{"name": "azure-openai", "status": "configured", "models": []string{"gpt-4o", "gpt-4o-mini"}},
		{"name": "anthropic", "status": "configured", "models": []string{"claude-sonnet-4-20250514", "claude-haiku"}},
		{"name": "ollama", "status": "available", "models": []string{"llama3.1", "mistral"}},
	}
	respondJSON(w, http.StatusOK, providers)
}

func RouteModel(w http.ResponseWriter, r *http.Request) {
	// Phase 1: Simple fallback routing — always pick the first configured provider.
	// Production will implement RoutingStrategy (cost-optimized, latency, round-robin, A/B).
	respondJSON(w, http.StatusOK, map[string]string{
		"routed_to": "azure-openai",
		"model":     "gpt-4o",
		"strategy":  "fallback",
	})
}

func GetCostSummary(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"total_cost_usd":    0.0,
		"total_tokens":      0,
		"period":            "24h",
		"by_agent":          map[string]float64{},
		"by_model":          map[string]float64{},
	})
}

// ── Trace Handlers ───────────────────────────────────────────

func ListTraces(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, []string{})
}

func GetTrace(w http.ResponseWriter, r *http.Request) {
	traceID := chi.URLParam(r, "traceId")
	respondJSON(w, http.StatusOK, map[string]string{"trace_id": traceID})
}

// ── Kitchen Handlers ─────────────────────────────────────────

func ListKitchens(w http.ResponseWriter, r *http.Request) {
	storeMu.RLock()
	defer storeMu.RUnlock()

	result := make([]models.Kitchen, 0, len(kitchens))
	for _, k := range kitchens {
		result = append(result, *k)
	}
	respondJSON(w, http.StatusOK, result)
}

func CreateKitchen(w http.ResponseWriter, r *http.Request) {
	var req models.Kitchen
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	req.ID = uuid.New().String()
	req.CreatedAt = time.Now().UTC()

	storeMu.Lock()
	kitchens[req.ID] = &req
	storeMu.Unlock()

	log.Info().Str("kitchen", req.Name).Str("id", req.ID).Msg("Kitchen created")
	respondJSON(w, http.StatusCreated, req)
}

func GetKitchen(w http.ResponseWriter, r *http.Request) {
	kitchenID := chi.URLParam(r, "kitchenId")

	storeMu.RLock()
	kitchen, ok := kitchens[kitchenID]
	storeMu.RUnlock()

	if !ok {
		respondError(w, http.StatusNotFound, "Kitchen not found: "+kitchenID)
		return
	}
	respondJSON(w, http.StatusOK, kitchen)
}

// ── A2A Gateway Handlers ─────────────────────────────────────

func A2AEndpoint(w http.ResponseWriter, r *http.Request) {
	// Handle A2A JSON-RPC requests at the gateway level.
	// Parse the JSON-RPC request and dispatch to the appropriate handler.
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

	// Phase 1: Return method-not-found for unimplemented methods.
	// Production will dispatch to registered agent handlers.
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

func ServeAgentCard(w http.ResponseWriter, r *http.Request) {
	// Serve the platform-level A2A Agent Card
	card := map[string]interface{}{
		"name":        "AgentOven Gateway",
		"description": "AgentOven control plane — multi-agent orchestration gateway",
		"version":     "0.1.0",
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

func A2AAgentEndpoint(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")

	storeMu.RLock()
	agent, ok := agents[agentName]
	storeMu.RUnlock()

	if !ok {
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

	// Route the JSON-RPC request to the agent's A2A endpoint
	log.Info().Str("agent", agentName).Msg("Routing A2A request to agent")
	respondJSON(w, http.StatusOK, map[string]string{
		"agent":  agentName,
		"status": "routing",
	})
}

func ServeAgentSpecificCard(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")

	storeMu.RLock()
	agent, ok := agents[agentName]
	storeMu.RUnlock()

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host

	if !ok {
		// Return a minimal card for unknown agents
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

	// Generate full A2A Agent Card from registry data
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
