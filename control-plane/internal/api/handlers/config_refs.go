package handlers

import (
	"context"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/agentoven/agentoven/control-plane/internal/resolver"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/middleware"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/agentoven/agentoven/control-plane/pkg/refcheck"
)

// maxConfigBody bounds an agent or recipe body: a prompt and a long list of steps, not a file.
const maxConfigBody = 16 << 20

// readConfigBody reads a request body for a configuration write. A body that is too large or cannot be read
// is a 400; it answers for itself and returns false.
func readConfigBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxConfigBody))
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return nil, false
	}
	return body, true
}

// respondInvalidConfig answers a configuration that names things the kitchen does not have, or sets what
// cannot be set: 422 and the whole list, so one round trip shows every problem. The warnings ride along
// for the person reading it.
func respondInvalidConfig(w http.ResponseWriter, what string, problems []refcheck.Problem) {
	errs, warns := refcheck.Split(problems)
	msg := refcheck.Error{Problems: errs}
	respondJSON(w, http.StatusUnprocessableEntity, map[string]interface{}{
		"error":    what + " is not valid: " + msg.Error(),
		"code":     "invalid_configuration",
		"problems": errs,
		"warnings": nonNil(warns),
	})
}

func nonNil(ps []refcheck.Problem) []refcheck.Problem {
	if ps == nil {
		return []refcheck.Problem{}
	}
	return ps
}

// refCatalog reads what the kitchen has. A kitchen that cannot be read answers 503: the write is neither
// accepted on trust nor refused as if nothing existed.
func (h *Handlers) refCatalog(w http.ResponseWriter, ctx context.Context, kitchen string) (*refcheck.Catalog, bool) {
	cat, err := resolver.LoadCatalog(ctx, h.Store, kitchen, h.References)
	if err != nil {
		log.Error().Err(err).Str("kitchen", kitchen).Msg("could not read the kitchen to check references")
		respondError(w, http.StatusServiceUnavailable, "the kitchen's providers, tools and agents could not be read to check this configuration; try again")
		return nil, false
	}
	return cat, true
}

// checkAgentWrite refuses a write whose result names things the kitchen does not have. before is the agent as
// it stands (nil for a create) and after is what the write would leave: a problem the agent already had is
// not held against a write that did not cause it (refcheck.Introduced), so an agent saved before the checks
// existed can still be edited while its problems are listed by GET /agents/{name}/validate. fromBody are
// the problems found while reading the body (unknown fields, wrong types), which always count. It answers
// the request when it refuses and returns false.
func (h *Handlers) checkAgentWrite(w http.ResponseWriter, r *http.Request, kitchen string, before, after *models.Agent, fromBody []refcheck.Problem) bool {
	cat, ok := h.refCatalog(w, r.Context(), kitchen)
	if !ok {
		return false
	}
	problems := refcheck.CheckAgent(after, cat, refcheck.Options{})
	if before != nil {
		_, warns := refcheck.Split(problems)
		problems = append(refcheck.Introduced(refcheck.CheckAgent(before, cat, refcheck.Options{}), problems), warns...)
	}
	problems = append(append([]refcheck.Problem(nil), fromBody...), problems...)
	if refcheck.New(problems) != nil {
		respondInvalidConfig(w, "the agent's configuration", problems)
		return false
	}
	return true
}

// checkAgentBake is the same check at the moment the agent is about to run (a bake or a re-cook): nothing is
// inherited, because what is baked is what runs. The catalog is read by the caller before it reads the agent:
// a bake is a read-modify-write of one row, and reading the kitchen between the two would only widen the
// window in which another write makes it stale.
func (h *Handlers) checkAgentBake(w http.ResponseWriter, cat *refcheck.Catalog, agent *models.Agent) bool {
	problems := refcheck.CheckAgent(agent, cat, refcheck.Options{Bake: true})
	if refcheck.New(problems) != nil {
		respondInvalidConfig(w, "the agent cannot be baked: its configuration", problems)
		return false
	}
	return true
}

// checkRecipeWrite is checkAgentWrite for a recipe.
func (h *Handlers) checkRecipeWrite(w http.ResponseWriter, r *http.Request, kitchen string, before, after *models.Recipe, fromBody []refcheck.Problem) bool {
	cat, ok := h.refCatalog(w, r.Context(), kitchen)
	if !ok {
		return false
	}
	problems := refcheck.CheckRecipe(after, cat, refcheck.Options{})
	if before != nil {
		_, warns := refcheck.Split(problems)
		problems = append(refcheck.Introduced(refcheck.CheckRecipe(before, cat, refcheck.Options{}), problems), warns...)
	}
	problems = append(append([]refcheck.Problem(nil), fromBody...), problems...)
	if refcheck.New(problems) != nil {
		respondInvalidConfig(w, "the recipe", problems)
		return false
	}
	return true
}

// checkRecipeRun is the check before a recipe runs ("bake"): the agents, recipes, channels and roles its steps
// name must still exist, and the environment asked for must be one of the kitchen's.
func (h *Handlers) checkRecipeRun(w http.ResponseWriter, r *http.Request, kitchen string, recipe *models.Recipe, environment string) bool {
	cat, ok := h.refCatalog(w, r.Context(), kitchen)
	if !ok {
		return false
	}
	problems := refcheck.CheckRecipe(recipe, cat, refcheck.Options{Bake: true, Environment: environment})
	if refcheck.New(problems) != nil {
		respondInvalidConfig(w, "the recipe cannot run: it", problems)
		return false
	}
	return true
}

// ValidateAgent lists the problems an agent's configuration has now, as the next bake would judge it
// (the draft applied). It changes nothing and runs nothing; an agent that was saved before the checks existed
// keeps running whatever this says, and the dashboard shows it as a badge to fix.
// GET /api/v1/agents/{agentName}/validate
func (h *Handlers) ValidateAgent(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	name := chi.URLParam(r, "agentName")
	agent, err := h.Store.GetAgent(r.Context(), kitchen, name)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, "could not read the agent")
		}
		return
	}
	cat, ok := h.refCatalog(w, r.Context(), kitchen)
	if !ok {
		return
	}
	problems := refcheck.CheckAgent(agent.WithDraft(), cat, refcheck.Options{Bake: true})
	respondValidation(w, "agent", name, problems)
}

// ValidateRecipe is ValidateAgent for a recipe. GET /api/v1/recipes/{recipeName}/validate
func (h *Handlers) ValidateRecipe(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	name := chi.URLParam(r, "recipeName")
	recipe, err := h.Store.GetRecipe(r.Context(), kitchen, name)
	if err != nil {
		if _, ok := err.(*store.ErrNotFound); ok {
			respondError(w, http.StatusNotFound, err.Error())
		} else {
			respondError(w, http.StatusInternalServerError, "could not read the recipe")
		}
		return
	}
	cat, ok := h.refCatalog(w, r.Context(), kitchen)
	if !ok {
		return
	}
	respondValidation(w, "recipe", name, refcheck.CheckRecipe(recipe, cat, refcheck.Options{Bake: true}))
}

func respondValidation(w http.ResponseWriter, kind, name string, problems []refcheck.Problem) {
	errs, warns := refcheck.Split(problems)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		kind:       name,
		"valid":    len(errs) == 0,
		"problems": nonNil(errs),
		"warnings": nonNil(warns),
	})
}

// decodeConfig decodes a configuration body and returns what the decode itself found wrong (see
// refcheck.Decode). A body that is not JSON is a 400 and answers for itself.
func decodeConfig(w http.ResponseWriter, body []byte, dst interface{}, alsoKnown ...interface{}) ([]refcheck.Problem, bool) {
	problems, err := refcheck.Decode(body, dst, alsoKnown...)
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body")
		return nil, false
	}
	return problems, true
}
