package handlers

import (
	"fmt"
	"net/http"

	"github.com/agentoven/agentoven/control-plane/internal/api/middleware"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// Resources that are addressed by an id alone (a run id, a gate key) belong to a kitchen all the same.
// The id is not a secret that grants access: a caller in another kitchen who learns it (from a log, an
// export, a link) must be answered exactly as if it did not exist. These helpers are the one place that
// comparison is made for the recipe-run family; TestByIDRoutesAreKitchenScoped walks the routes so a new
// by-id route cannot skip it.

// kitchenOwns reports whether a resource recorded under owner is in the request's kitchen. An empty owner
// is the "default" kitchen (rows written before kitchens were recorded), never a wildcard.
func kitchenOwns(r *http.Request, owner string) bool {
	return ownedBy(owner, middleware.GetKitchen(r.Context()))
}

// ownedBy is kitchenOwns for code that holds the kitchen as a name (a decision describer).
func ownedBy(owner, kitchen string) bool {
	if owner == "" {
		owner = "default"
	}
	return owner == kitchen
}

// runInKitchen loads a recipe run and checks it belongs to the request's kitchen. A run of another
// kitchen is reported as not found.
func (h *Handlers) runInKitchen(r *http.Request, runID string) (*models.RecipeRun, error) {
	run, err := h.Store.GetRecipeRun(r.Context(), runID)
	if err != nil {
		return nil, err
	}
	if !kitchenOwns(r, run.Kitchen) {
		return nil, &store.ErrNotFound{Entity: "recipe run", Key: runID}
	}
	return run, nil
}

// sessionInKitchen loads the session a request names and checks it belongs to the request's kitchen and to the
// agent in the URL. A session of another kitchen or agent is reported as not found.
func (h *Handlers) sessionInKitchen(r *http.Request, sessionID, agentName string) (*models.Session, error) {
	sess, err := h.Sessions.GetSession(r.Context(), sessionID)
	if err != nil {
		return nil, err
	}
	if !kitchenOwns(r, sess.Kitchen) || sess.AgentName != agentName {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	return sess, nil
}
