package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// ── Agent Handlers ───────────────────────────────────────────

func ListAgents(w http.ResponseWriter, r *http.Request) {
	// TODO: Fetch from database with tenant filtering
	agents := []models.Agent{}
	respondJSON(w, http.StatusOK, agents)
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

	// TODO: Persist to database
	// TODO: Auto-generate A2A Agent Card

	respondJSON(w, http.StatusCreated, req)
}

func GetAgent(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	// TODO: Fetch from database
	respondJSON(w, http.StatusOK, map[string]string{
		"name":   agentName,
		"status": "not_found",
	})
}

func UpdateAgent(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	// TODO: Update in database
	respondJSON(w, http.StatusOK, map[string]string{"name": agentName, "updated": "true"})
}

func DeleteAgent(w http.ResponseWriter, r *http.Request) {
	// TODO: Soft-delete in database
	w.WriteHeader(http.StatusNoContent)
}

func BakeAgent(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")

	var req struct {
		Version     string `json:"version"`
		Environment string `json:"environment"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	// TODO: Trigger deployment
	// TODO: Generate/update A2A Agent Card
	// TODO: Register with A2A gateway

	respondJSON(w, http.StatusAccepted, map[string]string{
		"name":        agentName,
		"version":     req.Version,
		"environment": req.Environment,
		"status":      "baking",
		"agent_card":  "/agents/" + agentName + "/a2a/.well-known/agent-card.json",
	})
}

func CoolAgent(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	// TODO: Pause agent deployment
	respondJSON(w, http.StatusOK, map[string]string{"name": agentName, "status": "cooled"})
}

func ListAgentVersions(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, []string{})
}

func GetAgentVersion(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]string{})
}

// ── Recipe Handlers ──────────────────────────────────────────

func ListRecipes(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, []models.Recipe{})
}

func CreateRecipe(w http.ResponseWriter, r *http.Request) {
	var req models.Recipe
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	req.ID = uuid.New().String()
	req.CreatedAt = time.Now().UTC()
	respondJSON(w, http.StatusCreated, req)
}

func GetRecipe(w http.ResponseWriter, r *http.Request) {
	recipeName := chi.URLParam(r, "recipeName")
	respondJSON(w, http.StatusOK, map[string]string{"name": recipeName})
}

func UpdateRecipe(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]string{})
}

func DeleteRecipe(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func BakeRecipe(w http.ResponseWriter, r *http.Request) {
	recipeName := chi.URLParam(r, "recipeName")
	// TODO: Execute the recipe workflow via A2A
	respondJSON(w, http.StatusAccepted, map[string]string{
		"recipe":  recipeName,
		"status":  "baking",
		"task_id": uuid.New().String(),
	})
}

func RecipeHistory(w http.ResponseWriter, r *http.Request) {
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
	// TODO: Intelligent model routing based on strategy
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
	kitchens := []map[string]string{
		{"id": "default", "name": "Default Kitchen"},
	}
	respondJSON(w, http.StatusOK, kitchens)
}

func CreateKitchen(w http.ResponseWriter, r *http.Request) {
	var req map[string]string
	json.NewDecoder(r.Body).Decode(&req)
	req["id"] = uuid.New().String()
	respondJSON(w, http.StatusCreated, req)
}

func GetKitchen(w http.ResponseWriter, r *http.Request) {
	kitchenID := chi.URLParam(r, "kitchenId")
	respondJSON(w, http.StatusOK, map[string]string{"id": kitchenID})
}

// ── A2A Gateway Handlers ─────────────────────────────────────

func A2AEndpoint(w http.ResponseWriter, r *http.Request) {
	// TODO: Handle A2A JSON-RPC requests (SendMessage, GetTask, etc.)
	respondJSON(w, http.StatusOK, map[string]string{
		"jsonrpc": "2.0",
		"error":   "not_implemented",
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
	// TODO: Route A2A requests to specific registered agents
	agentName := chi.URLParam(r, "agentName")
	respondJSON(w, http.StatusOK, map[string]string{
		"agent":  agentName,
		"status": "routing",
	})
}

func ServeAgentSpecificCard(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agentName")
	// TODO: Generate from registry data
	card := map[string]interface{}{
		"name":        agentName,
		"description": "Agent managed by AgentOven",
		"supportedInterfaces": []map[string]string{
			{
				"url":             r.Host + "/agents/" + agentName + "/a2a",
				"protocolBinding": "jsonrpc-http",
			},
		},
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
