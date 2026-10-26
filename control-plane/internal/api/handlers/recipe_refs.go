package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// checkSubRecipeRefs refuses a recipe whose sub_recipe steps would lead back to itself, directly or
// through other recipes. The engine stops such a run at a nesting depth of five, but only after it has
// done five rounds of work and failed; saying so when the recipe is saved is kinder.
//
// A reference to a recipe that does not exist yet is allowed: recipes are often created in the order
// that suits the author, and the engine reports a missing one when the step runs.
func (h *Handlers) checkSubRecipeRefs(ctx context.Context, kitchen, name string, steps []models.Step) error {
	refs := func(ss []models.Step) []string {
		var out []string
		for _, s := range ss {
			if s.Kind == models.StepSubRecipe && s.RecipeRef != "" {
				out = append(out, s.RecipeRef)
			}
		}
		return out
	}
	direct := refs(steps)
	if len(direct) == 0 {
		return nil
	}
	existing, err := h.Store.ListRecipes(ctx, kitchen)
	if err != nil {
		return nil // the check is a courtesy; a store that cannot list does not block saving
	}
	graph := map[string][]string{}
	for _, r := range existing {
		if r.Name != name {
			graph[r.Name] = refs(r.Steps)
		}
	}
	for _, first := range direct {
		if path := pathBack(graph, first, name, []string{name}, map[string]bool{}); path != nil {
			if len(path) == 2 {
				return fmt.Errorf("recipe %q cannot run itself as a step", name)
			}
			return fmt.Errorf("recipe %q would end up running itself: %s", name, strings.Join(path, " → "))
		}
	}
	return nil
}

// pathBack walks the sub-recipe graph from `at` looking for a way back to `target`. It returns the path
// taken (starting at the target) or nil.
func pathBack(graph map[string][]string, at, target string, path []string, seen map[string]bool) []string {
	path = append(append([]string(nil), path...), at)
	if at == target {
		return path
	}
	if seen[at] {
		return nil
	}
	seen[at] = true
	for _, next := range graph[at] {
		if p := pathBack(graph, next, target, path, seen); p != nil {
			return p
		}
	}
	return nil
}
