package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/middleware"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// GetAgentDraftDiff says what the next bake would change: the field-level difference between the agent as
// it was baked and the agent as it will be once its draft is applied (ADR-0043, owner decision A1). It
// changes nothing and runs nothing. An agent with no draft answers has_draft:false and no changes; one in
// another kitchen is a 404 like any agent route. Secrets in an ingredient's or a guardrail's config are
// masked on both sides (models.DiffAgentDraft).
// GET /api/v1/agents/{agentName}/draft/diff
func (h *Handlers) GetAgentDraftDiff(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	agent, err := h.Store.GetAgent(r.Context(), kitchen, chi.URLParam(r, "agentName"))
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, "could not read the agent")
		}
		return
	}
	respondJSON(w, http.StatusOK, models.DiffAgentDraft(agent))
}
