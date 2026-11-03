package handlers_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/internal/workflow"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/go-chi/chi/v5"
)

// startWaitingRun starts a recipe with one long wait step in a kitchen and returns the run id once the
// step shows as waiting.
func startWaitingRun(t *testing.T, kitchen string) (*handlers.Handlers, store.Store, string) {
	t.Helper()
	h, s := newTestHandlers(t)
	h.Workflow = workflow.NewEngine(s, nil, "")
	h.RegisterOwnDecisionGates()
	recipe := &models.Recipe{ID: "wr-" + kitchen, Name: "wr-" + kitchen, Kitchen: kitchen, Steps: []models.Step{
		{Name: "hold", Kind: models.StepWait, Config: map[string]interface{}{"duration": "1h"}}}}
	if err := s.CreateRecipe(context.Background(), recipe); err != nil {
		t.Fatal(err)
	}
	runID, err := h.Workflow.ExecuteRecipe(context.Background(), recipe, kitchen, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if run, _ := s.GetRecipeRun(context.Background(), runID); run != nil {
			for _, r := range run.StepResults {
				if r.Status == "waiting" {
					return h, s, runID
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the step never showed as waiting")
	return nil, nil, ""
}

func release(h *handlers.Handlers, kitchen, runID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/", strings.NewReader("{}"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("recipeName", "wr-gx")
	rctx.URLParams.Add("runId", runID)
	rctx.URLParams.Add("stepName", "hold")
	req = withKitchen(req, kitchen)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.ReleaseWait(rec, req)
	return rec
}

// A person signed the release of a run of a GxP kitchen while addressing another kitchen they belong to:
// the describer read nothing, the signature was recorded in the wrong kitchen's chain, and the engine's
// release map has no kitchen at all.
func TestAWaitOfAnotherKitchenCannotBeReleasedSignedOrNot(t *testing.T) {
	// Unsigned (the community edition).
	h, _, runID := startWaitingRun(t, "gx")
	if rec := release(h, "other", runID); rec.Code != http.StatusNotFound {
		t.Fatalf("unsigned, from another kitchen: %d %s", rec.Code, rec.Body)
	}
	if !h.Workflow.ReleaseWait("gx", runID, "hold") {
		t.Fatal("the wait must still be waiting after the refused release")
	}

	// Signed: the describer refuses before anyone is asked to sign.
	h, _, runID = startWaitingRun(t, "gx")
	fs := &fakeSigner{signer: "bo"}
	h.SetDecisionSigner(fs)
	if rec := release(h, "other", runID); rec.Code != http.StatusNotFound || len(fs.got) != 0 || len(fs.applied) != 0 {
		t.Fatalf("signed, from another kitchen: %d %s (signer asked %d times)", rec.Code, rec.Body, len(fs.got))
	}
	if _, err := h.DescribeDecision(context.Background(), contracts.GateWaitRelease, "other", runID+":hold", nil); err == nil {
		t.Fatal("the describer must refuse a run of another kitchen")
	}
	if _, err := h.DescribeDecision(context.Background(), contracts.GateWaitRelease, "gx", runID+":nothere", nil); err == nil {
		t.Fatal("the describer must refuse a step that is not waiting")
	}
	// The right kitchen: signed, applied and reported.
	if rec := release(h, "gx", runID); rec.Code != http.StatusOK || len(fs.got) != 1 || fs.got[0].Kitchen != "gx" {
		t.Fatalf("signed, own kitchen: %d %s", rec.Code, rec.Body)
	}
	if len(fs.applied) != 1 || fs.applied[0] != nil {
		t.Fatalf("applied = %v", fs.applied)
	}
}

// Every gate this package signs reads a record by an id, and an id is not a secret. Each describer must
// answer a second kitchen exactly as it answers an id that does not exist.
func TestEveryGateThisPackageSignsRefusesAnotherKitchensRecord(t *testing.T) {
	ctx := context.Background()

	// tool_approval, budget_extension, resume_guidance: a paused run of the default kitchen.
	h, j := runRig(t)
	h.RegisterOwnDecisionGates()
	beginRun(t, j, "run-x")
	if err := j.MarkPaused(ctx, "run-x", executor.PendingApproval{Turn: 1, GatedNames: []string{"refund"},
		ToolCalls: []executor.ToolCall{{ID: "c1", Name: "refund"}}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := h.DescribeDecision(ctx, contracts.GateToolApproval, "default", "run-x", nil); err != nil {
		t.Fatalf("its own kitchen: %v", err)
	}
	for _, gate := range []string{contracts.GateToolApproval, contracts.GateBudgetExtension, contracts.GateResumeGuidance} {
		if _, err := h.DescribeDecision(ctx, gate, "another", "run-x", nil); err == nil {
			t.Errorf("%s: a run of another kitchen was described", gate)
		}
	}

	// recipe_gate: an approval record, with and without a kitchen of its own.
	hh, s := newTestHandlers(t)
	hh.RegisterOwnDecisionGates()
	run := &models.RecipeRun{ID: "run-g", RecipeID: "r", Kitchen: "gx", Status: models.RecipeRunPaused}
	if err := s.CreateRecipeRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	for key, kitchen := range map[string]string{"run-g:step": "gx", "run-g:legacy": ""} {
		if err := s.CreateApproval(ctx, &models.ApprovalRecord{ID: key, GateKey: key, RunID: "run-g", StepName: strings.TrimPrefix(key, "run-g:"), Kitchen: kitchen, Status: "waiting", RequestedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if _, err := hh.DescribeDecision(ctx, contracts.GateRecipeGate, "gx", key, nil); err != nil {
			t.Errorf("%s: its own kitchen: %v", key, err)
		}
		for _, other := range []string{"another", "default"} {
			if _, err := hh.DescribeDecision(ctx, contracts.GateRecipeGate, other, key, nil); err == nil {
				t.Errorf("%s: described to kitchen %q", key, other)
			}
		}
	}

	// skill_approval: a skill needing review.
	if err := s.CreateSkill(ctx, &models.Skill{ID: "s1", Kitchen: "gx", Name: "pdf", Status: models.SkillStatusNeedsReview, Manifest: &models.SkillManifest{Name: "pdf"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := hh.DescribeDecision(ctx, contracts.GateSkillApproval, "gx", "pdf", nil); err != nil {
		t.Fatalf("skill, own kitchen: %v", err)
	}
	if _, err := hh.DescribeDecision(ctx, contracts.GateSkillApproval, "another", "pdf", nil); err == nil {
		t.Error("a skill of another kitchen was described")
	}
}

// A run whose state cannot be read must not be decided unsigned: signRunDecision used to return "nothing to
// sign" for any error, and the resume path then read the journal a second time.
type brokenJournal struct{ executor.Journal }

func (brokenJournal) Load(context.Context, string) (*executor.JournalState, error) {
	return nil, errors.New("disk i/o error")
}

func TestARunWhoseStateCannotBeReadIsNotDecidedUnsigned(t *testing.T) {
	h, j := runRig(t)
	h.RegisterOwnDecisionGates()
	beginRun(t, j, "run-b")
	fs := &fakeSigner{signer: "bo"}
	h.SetDecisionSigner(fs)

	// A run that does not exist is a 404 and nothing is signed.
	if rec := resume(h, "no-such-run", `{"approved":true}`); rec.Code != http.StatusNotFound || len(fs.got) != 0 {
		t.Fatalf("no such run: %d %s", rec.Code, rec.Body)
	}
	// A journal that fails is a 503, and the decision is not made.
	h.Executor.SetJournal(brokenJournal{j})
	if rec := resume(h, "run-b", `{"approved":true}`); rec.Code != http.StatusServiceUnavailable || len(fs.got) != 0 || len(fs.applied) != 0 {
		t.Fatalf("unreadable journal: %d %s", rec.Code, rec.Body)
	}
}
