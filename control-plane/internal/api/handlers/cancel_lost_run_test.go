package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/workflow"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// A run left "waiting" by a restart has no process holding it. Cancel ends it in the record instead of answering 404.
func TestCancelEndsARunNoProcessHolds(t *testing.T) {
	h, s := newTestHandlers(t)
	h.Workflow = workflow.NewEngine(s, nil, "")
	ctx := context.Background()
	for id, status := range map[string]models.RecipeRunStatus{"lost": models.RecipeRunPaused, "done": models.RecipeRunCompleted} {
		if err := s.CreateRecipeRun(ctx, &models.RecipeRun{ID: id, RecipeID: "r", Kitchen: "default", Status: status, StartedAt: time.Now().Add(-48 * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	cancel := func(id string) int {
		req := withChiParam(withKitchen(httptest.NewRequest(http.MethodPost, "/runs/"+id+"/cancel", nil), "default"), "runId", id)
		rec := httptest.NewRecorder()
		h.CancelRecipeRun(rec, req)
		return rec.Code
	}
	if code := cancel("lost"); code != 200 {
		t.Fatalf("a lost run is cancelable: %d", code)
	}
	if run, _ := s.GetRecipeRun(ctx, "lost"); run.Status != models.RecipeRunCanceled || run.CompletedAt == nil {
		t.Fatalf("the record must say canceled: %+v", run)
	}
	if code := cancel("done"); code != 404 {
		t.Fatalf("a finished run is not cancelable: %d", code)
	}
	if code := cancel("nope"); code != 404 {
		t.Fatalf("an unknown run: %d", code)
	}
}
