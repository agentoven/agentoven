package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/internal/workflow"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

type handlersT struct {
	h *handlers.Handlers
	s store.Store
}

func recipeHandlers(t *testing.T) (*handlersT, func(string) models.RecipeRun) {
	t.Helper()
	h, s := newTestHandlers(t)
	h.Workflow = workflow.NewEngine(s, nil, "http://localhost:0")
	wait := func(id string) models.RecipeRun {
		for i := 0; i < 100; i++ {
			run, err := s.GetRecipeRun(context.Background(), id)
			if err == nil && (run.Status == models.RecipeRunCompleted || run.Status == models.RecipeRunFailed) {
				return *run
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("run %s did not finish", id)
		return models.RecipeRun{}
	}
	return &handlersT{h: h, s: s}, wait
}

func makeRecipe(t *testing.T, x *handlersT, name string) {
	t.Helper()
	rec := callAgentAPI(x.h.CreateRecipe, http.MethodPost, "/api/v1/recipes", nil,
		`{"name":"`+name+`","description":"d","steps":[{"name":"go","kind":"fan_out"}]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
}

func listNames(t *testing.T, x *handlersT, query string) []string {
	t.Helper()
	rec := callAgentAPI(x.h.ListRecipes, http.MethodGet, "/api/v1/recipes"+query, nil, "")
	var out []models.Recipe
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range out {
		names = append(names, r.Name)
	}
	return names
}

func startRun(t *testing.T, x *handlersT, name, body string) string {
	t.Helper()
	rec := callAgentAPI(x.h.BakeRecipe, http.MethodPost, "/api/v1/recipes/"+name+"/bake", map[string]string{"recipeName": name}, body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("bake: %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out["run_id"]
}

// Deleting a recipe archives it: it is hidden, cannot be run or edited, and keeps its runs, which are
// records of what models did and must stay readable.
func TestDeletingARecipeArchivesItAndKeepsItsRuns(t *testing.T) {
	x, wait := recipeHandlers(t)
	makeRecipe(t, x, "refunds")
	runID := startRun(t, x, "refunds", `{"input":{"ticket":"T-1"}}`)
	wait(runID)

	rec := callAgentAPI(x.h.DeleteRecipe, http.MethodDelete, "/api/v1/recipes/refunds", map[string]string{"recipeName": "refunds"}, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}

	if got := listNames(t, x, ""); len(got) != 0 {
		t.Errorf("an archived recipe is hidden from the list: %v", got)
	}
	if got := listNames(t, x, "?archived=include"); len(got) != 1 {
		t.Errorf("archived=include shows it: %v", got)
	}
	if got := listNames(t, x, "?archived=only"); len(got) != 1 || got[0] != "refunds" {
		t.Errorf("archived=only shows just archived ones: %v", got)
	}
	rec = callAgentAPI(x.h.GetRecipe, http.MethodGet, "/api/v1/recipes/refunds", map[string]string{"recipeName": "refunds"}, "")
	if !strings.Contains(rec.Body.String(), `"archived_at"`) {
		t.Errorf("an archived recipe says so: %s", rec.Body.String())
	}

	// Its runs are still there.
	run, err := x.s.GetRecipeRun(context.Background(), runID)
	if err != nil || run.Input["ticket"] != "T-1" {
		t.Fatalf("the run was lost: %v %+v", err, run)
	}
	rec = callAgentAPI(x.h.RecipeHistory, http.MethodGet, "/api/v1/recipes/refunds/runs", map[string]string{"recipeName": "refunds"}, "")
	if !strings.Contains(rec.Body.String(), runID) {
		t.Errorf("the run history of an archived recipe stays readable: %s", rec.Body.String())
	}

	// It cannot be run, edited or have its name taken.
	if rec = callAgentAPI(x.h.BakeRecipe, http.MethodPost, "/api/v1/recipes/refunds/bake", map[string]string{"recipeName": "refunds"}, `{}`); rec.Code != http.StatusConflict {
		t.Errorf("running an archived recipe: %d", rec.Code)
	}
	if rec = callAgentAPI(x.h.UpdateRecipe, http.MethodPut, "/api/v1/recipes/refunds", map[string]string{"recipeName": "refunds"}, `{"description":"x"}`); rec.Code != http.StatusConflict {
		t.Errorf("editing an archived recipe: %d", rec.Code)
	}
	if rec = callAgentAPI(x.h.CreateRecipe, http.MethodPost, "/api/v1/recipes", nil, `{"name":"refunds","steps":[{"name":"a","kind":"fan_out"}]}`); rec.Code != http.StatusConflict {
		t.Errorf("reusing an archived recipe's name: %d", rec.Code)
	}

	// Archiving again is harmless; restoring brings it back, runnable.
	if rec = callAgentAPI(x.h.DeleteRecipe, http.MethodDelete, "/api/v1/recipes/refunds", map[string]string{"recipeName": "refunds"}, ""); rec.Code != http.StatusNoContent {
		t.Errorf("archiving twice: %d", rec.Code)
	}
	if rec = callAgentAPI(x.h.RestoreRecipe, http.MethodPost, "/api/v1/recipes/refunds/restore", map[string]string{"recipeName": "refunds"}, ""); rec.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", rec.Code, rec.Body.String())
	}
	if got := listNames(t, x, ""); len(got) != 1 {
		t.Errorf("a restored recipe is listed again: %v", got)
	}
	wait(startRun(t, x, "refunds", `{}`))
}

// "Run again" starts the recipe with the same input and records which run it repeats.
func TestARunCanBeRunAgainWithItsInputAndRecordsWhatItRepeats(t *testing.T) {
	x, wait := recipeHandlers(t)
	makeRecipe(t, x, "refunds")
	first := startRun(t, x, "refunds", `{"input":{"ticket":"T-1"}}`)
	wait(first)

	rec := call(x.h.RerunRecipeRun, "/api/v1/recipes/refunds/runs/"+first+"/rerun", ``, map[string]string{"recipeName": "refunds", "runId": first})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("rerun: %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["rerun_of"] != first || out["run_id"] == "" || out["run_id"] == first {
		t.Fatalf("response: %v", out)
	}
	again := wait(out["run_id"])
	if again.RerunOf != first || again.Input["ticket"] != "T-1" {
		t.Errorf("the repeat should carry the original input and say what it repeats: %+v", again)
	}

	// The input can be changed for the repeat; a run of another recipe cannot be repeated through this one.
	rec = call(x.h.RerunRecipeRun, "/x", `{"input":{"ticket":"T-2"}}`, map[string]string{"recipeName": "refunds", "runId": first})
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if edited := wait(out["run_id"]); edited.Input["ticket"] != "T-2" || edited.RerunOf != first {
		t.Errorf("edited repeat: %+v", edited)
	}
	makeRecipe(t, x, "other")
	if rec = call(x.h.RerunRecipeRun, "/x", ``, map[string]string{"recipeName": "other", "runId": first}); rec.Code != http.StatusNotFound {
		t.Errorf("a run is repeated through its own recipe only: %d", rec.Code)
	}
	// Not once the recipe is archived.
	callAgentAPI(x.h.DeleteRecipe, http.MethodDelete, "/x", map[string]string{"recipeName": "refunds"}, "")
	if rec = call(x.h.RerunRecipeRun, "/x", ``, map[string]string{"recipeName": "refunds", "runId": first}); rec.Code != http.StatusConflict {
		t.Errorf("repeating a run of an archived recipe: %d", rec.Code)
	}
}

type refusingExecutor struct{ err error }

func (r refusingExecutor) ExecuteRecipe(context.Context, *models.Recipe, string, map[string]interface{}, string) (string, error) {
	return "", r.err
}

// An edition's recipe executor refuses with "permission_denied: <why>". That is a 403 for the caller, not
// a 500 (the check compared the first 16 characters of the message with a 17-character word, so only the
// bare word ever matched), and a caller that retries on a 5xx would otherwise retry a refusal for good.
func TestAnExecutorsPermissionRefusalIsA403NotA500(t *testing.T) {
	x, _ := recipeHandlers(t)
	makeRecipe(t, x, "gated")
	if err := x.s.CreateEnvironment(context.Background(), &models.Environment{ID: "e1", Slug: "prod", Name: "Production", Kitchen: "default"}); err != nil {
		t.Fatal(err)
	}
	for err, want := range map[string]int{
		"permission_denied": http.StatusForbidden,
		"permission_denied: environment requires approval":      http.StatusForbidden,
		"permission_denied_but_longer than the word":            http.StatusForbidden,
		"something else went wrong":                             http.StatusInternalServerError,
		"not a permission_denied, only mentioning it somewhere": http.StatusInternalServerError,
	} {
		x.h.RecipeExecutor = refusingExecutor{errors.New(err)}
		rec := callAgentAPI(x.h.BakeRecipe, http.MethodPost, "/api/v1/recipes/gated/bake", map[string]string{"recipeName": "gated"}, `{"environment":"prod"}`)
		if rec.Code != want {
			t.Errorf("%q: %d, want %d", err, rec.Code, want)
		}
	}
}
