package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func subStep(name, recipe string) models.Step {
	return models.Step{Name: name, Kind: models.StepSubRecipe, RecipeRef: recipe}
}

func seedRecipe(t *testing.T, _ any, store interface {
	CreateRecipe(context.Context, *models.Recipe) error
}, name string, steps ...models.Step) {
	t.Helper()
	if err := store.CreateRecipe(context.Background(), &models.Recipe{ID: name, Name: name, Kitchen: "default", Steps: steps}); err != nil {
		t.Fatal(err)
	}
}

func recipeBody(name string, steps ...models.Step) string {
	b, _ := json.Marshal(models.Recipe{Name: name, Steps: steps})
	return string(b)
}

func TestARecipeCannotRunItself(t *testing.T) {
	h, s := newTestHandlers(t)
	w := callAgentAPI(h.CreateRecipe, http.MethodPost, "/api/v1/recipes", nil, recipeBody("loop", subStep("again", "loop")))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "cannot run itself") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if _, err := s.GetRecipe(context.Background(), "default", "loop"); err == nil {
		t.Error("the recipe was saved anyway")
	}
}

func TestACycleThroughOtherRecipesIsRefusedOnCreateAndOnUpdate(t *testing.T) {
	h, s := newTestHandlers(t)
	seedRecipe(t, h, s, "rb", subStep("to-c", "rc"))
	seedRecipe(t, h, s, "rc", models.Step{Name: "work", Kind: models.StepAgent, AgentRef: "x"})
	// ra -> rb -> rc is fine. Making rc call ra closes the loop.
	if w := callAgentAPI(h.CreateRecipe, http.MethodPost, "/api/v1/recipes", nil, recipeBody("ra", subStep("to-b", "rb"))); w.Code != http.StatusCreated {
		t.Fatalf("a chain is fine: %d %s", w.Code, w.Body.String())
	}
	w := callAgentAPI(h.UpdateRecipe, http.MethodPut, "/api/v1/recipes/rc", map[string]string{"recipeName": "rc"}, recipeBody("rc", subStep("to-a", "ra")))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "rc → ra → rb → rc") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	got, _ := s.GetRecipe(context.Background(), "default", "rc")
	if got.Steps[0].Kind != models.StepAgent {
		t.Error("the refused update changed the recipe")
	}
}

func TestAReferenceToARecipeThatDoesNotExistYetIsAllowed(t *testing.T) {
	h, _ := newTestHandlers(t)
	if w := callAgentAPI(h.CreateRecipe, http.MethodPost, "/api/v1/recipes", nil, recipeBody("early", subStep("later", "not-made-yet"))); w.Code != http.StatusCreated {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
