package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/agentoven/agentoven/control-plane/internal/api/middleware"
	"github.com/agentoven/agentoven/control-plane/internal/embeddings"
	ragpkg "github.com/agentoven/agentoven/control-plane/internal/rag"
	"github.com/agentoven/agentoven/control-plane/internal/vectorstore"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// RAGHandlers holds dependencies for RAG/embedding/vectorstore API handlers.
type RAGHandlers struct {
	Embeddings  *embeddings.Registry
	VectorStore *vectorstore.Registry
	RAGRegistry *ragpkg.Registry // orchestrator: dispatches to registered RAG services
}

// ══════════════════════════════════════════════════════════════
// ── RAG Query / Ingest ───────────────────────────────────────
// ══════════════════════════════════════════════════════════════

// RAGQuery handles POST /api/v1/rag/query
// Optionally accepts "provider" field to target a specific RAG service.
func (h *RAGHandlers) RAGQuery(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())

	var req models.RAGQueryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.Question == "" {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "question is required"})
		return
	}

	if h.RAGRegistry == nil {
		respondJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "RAG service not configured"})
		return
	}

	// Resolve provider: use request field, query param, or default
	providerName := r.URL.Query().Get("provider")
	if providerName == "" {
		providerName = "built-in" // default
	}

	svc, err := h.RAGRegistry.Get(providerName)
	if err != nil {
		// Fall back to default provider
		svc, err = h.RAGRegistry.Default()
		if err != nil {
			respondJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no RAG services registered"})
			return
		}
	}

	result, err := svc.Query(r.Context(), kitchen, req)
	if err != nil {
		log.Error().Err(err).Str("kitchen", kitchen).Str("provider", svc.Name()).Msg("RAG query failed")
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, result)
}

// RAGIngest handles POST /api/v1/rag/ingest
// Optionally accepts "provider" query param to target a specific RAG service.
func (h *RAGHandlers) RAGIngest(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())

	var req models.RAGIngestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if len(req.Documents) == 0 {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "documents array is required"})
		return
	}

	if h.RAGRegistry == nil {
		respondJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "RAG service not configured"})
		return
	}

	providerName := r.URL.Query().Get("provider")
	if providerName == "" {
		providerName = "built-in"
	}

	svc, err := h.RAGRegistry.Get(providerName)
	if err != nil {
		svc, err = h.RAGRegistry.Default()
		if err != nil {
			respondJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no RAG services registered"})
			return
		}
	}

	result, err := svc.Ingest(r.Context(), kitchen, req)
	if err != nil {
		log.Error().Err(err).Str("kitchen", kitchen).Str("provider", svc.Name()).Msg("RAG ingest failed")
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, result)
}

// ══════════════════════════════════════════════════════════════
// ── RAG Providers ────────────────────────────────────────────
// ══════════════════════════════════════════════════════════════

// ListRAGProviders handles GET /api/v1/rag/providers
func (h *RAGHandlers) ListRAGProviders(w http.ResponseWriter, r *http.Request) {
	if h.RAGRegistry == nil {
		respondJSON(w, http.StatusOK, []interface{}{})
		return
	}

	names := h.RAGRegistry.List()
	type providerInfo struct {
		Name       string   `json:"name"`
		Strategies []string `json:"strategies"`
	}
	providers := make([]providerInfo, 0, len(names))
	for _, name := range names {
		svc, err := h.RAGRegistry.Get(name)
		if err != nil {
			continue
		}
		strategies := svc.Strategies()
		strs := make([]string, len(strategies))
		for i, s := range strategies {
			strs[i] = string(s)
		}
		providers = append(providers, providerInfo{Name: name, Strategies: strs})
	}
	respondJSON(w, http.StatusOK, providers)
}

// RAGHealth handles GET /api/v1/rag/health
func (h *RAGHandlers) RAGHealth(w http.ResponseWriter, r *http.Request) {
	if h.RAGRegistry == nil {
		respondJSON(w, http.StatusOK, map[string]string{})
		return
	}
	results := h.RAGRegistry.HealthCheckAll(r.Context())
	status := make(map[string]string, len(results))
	for name, err := range results {
		if err != nil {
			status[name] = "error: " + err.Error()
		} else {
			status[name] = "ok"
		}
	}
	respondJSON(w, http.StatusOK, status)
}

// RegisterExternalRAGProvider handles POST /api/v1/rag/providers
func (h *RAGHandlers) RegisterExternalRAGProvider(w http.ResponseWriter, r *http.Request) {
	if h.RAGRegistry == nil {
		respondJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "RAG registry not initialized"})
		return
	}

	var cfg ragpkg.ExternalRAGConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	svc, err := ragpkg.NewExternalRAGService(cfg)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	h.RAGRegistry.Register(cfg.Name, svc)
	log.Info().Str("name", cfg.Name).Str("endpoint", cfg.Endpoint).Msg("External RAG provider registered")

	respondJSON(w, http.StatusCreated, map[string]string{
		"name":     cfg.Name,
		"endpoint": cfg.Endpoint,
		"status":   "registered",
	})
}

// DeleteRAGProvider handles DELETE /api/v1/rag/providers/{name}
func (h *RAGHandlers) DeleteRAGProvider(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "built-in" {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot delete built-in provider"})
		return
	}
	if h.RAGRegistry == nil {
		respondJSON(w, http.StatusNotFound, map[string]string{"error": "RAG registry not initialized"})
		return
	}
	h.RAGRegistry.Remove(name)
	respondJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
}

// ══════════════════════════════════════════════════════════════
// ── Embedding Drivers ────────────────────────────────────────
// ══════════════════════════════════════════════════════════════

// ListEmbeddingDrivers handles GET /api/v1/embeddings
func (h *RAGHandlers) ListEmbeddingDrivers(w http.ResponseWriter, r *http.Request) {
	if h.Embeddings == nil {
		respondJSON(w, http.StatusOK, []string{})
		return
	}
	drivers := h.Embeddings.List()
	respondJSON(w, http.StatusOK, drivers)
}

// EmbedText handles POST /api/v1/embeddings/{driver}/embed
func (h *RAGHandlers) EmbedText(w http.ResponseWriter, r *http.Request) {
	driverName := chi.URLParam(r, "driver")

	driver, err := h.Embeddings.Get(driverName)
	if err != nil {
		respondJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}

	var body struct {
		Texts []string `json:"texts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if len(body.Texts) == 0 {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "texts array is required"})
		return
	}

	vectors, err := driver.Embed(r.Context(), body.Texts)
	if err != nil {
		log.Error().Err(err).Str("driver", driverName).Msg("Embedding failed")
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"driver":     driverName,
		"dimensions": driver.Dimensions(),
		"count":      len(vectors),
		"vectors":    vectors,
	})
}

// EmbeddingHealth handles GET /api/v1/embeddings/health
// Always returns 200 with per-driver status in the body.
// Previously returned 503 when any driver was unhealthy, which broke the dashboard.
func (h *RAGHandlers) EmbeddingHealth(w http.ResponseWriter, r *http.Request) {
	if h.Embeddings == nil {
		respondJSON(w, http.StatusOK, map[string]string{})
		return
	}
	results := h.Embeddings.HealthCheckAll(r.Context())
	status := make(map[string]string, len(results))
	for name, err := range results {
		if err != nil {
			status[name] = "error: " + err.Error()
		} else {
			status[name] = "ok"
		}
	}
	respondJSON(w, http.StatusOK, status)
}

// ══════════════════════════════════════════════════════════════
// ── Vector Store Drivers ─────────────────────────────────────
// ══════════════════════════════════════════════════════════════

// ListVectorStoreDrivers handles GET /api/v1/vectorstores
func (h *RAGHandlers) ListVectorStoreDrivers(w http.ResponseWriter, r *http.Request) {
	if h.VectorStore == nil {
		respondJSON(w, http.StatusOK, []string{})
		return
	}
	drivers := h.VectorStore.List()
	respondJSON(w, http.StatusOK, drivers)
}

// VectorStoreHealth handles GET /api/v1/vectorstores/health
// Always returns 200 with per-driver status in the body.
// Previously returned 503 when any driver was unhealthy, which broke the dashboard.
func (h *RAGHandlers) VectorStoreHealth(w http.ResponseWriter, r *http.Request) {
	if h.VectorStore == nil {
		respondJSON(w, http.StatusOK, map[string]string{})
		return
	}
	results := h.VectorStore.HealthCheckAll(r.Context())
	status := make(map[string]string, len(results))
	for name, err := range results {
		if err != nil {
			status[name] = "error: " + err.Error()
		} else {
			status[name] = "ok"
		}
	}
	respondJSON(w, http.StatusOK, status)
}

// ══════════════════════════════════════════════════════════════
// ── Data Connector CRUD ──────────────────────────────────────
// ══════════════════════════════════════════════════════════════

// ListConnectors handles GET /api/v1/connectors
func (h *RAGHandlers) ListConnectors(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	// Connectors are stored via the main handlers' Store.
	// This handler is a placeholder — wired when the store is injected.
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"kitchen":    kitchen,
		"connectors": []interface{}{},
		"note":       "data connectors require Pro license",
	})
}
