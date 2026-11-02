package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	pkgmw "github.com/agentoven/agentoven/control-plane/pkg/middleware"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func asRole(r *http.Request, role string) *http.Request {
	return r.WithContext(pkgmw.SetIdentity(r.Context(), &contracts.Identity{Subject: "u", Role: role}))
}

// Without a Pro check installed the role alone decides, as before: "admin" is owner and platform
// administrator, a Pro "head_chef" owns a kitchen but does not administer the platform.
func TestKitchenOwnerChecksKeepCommunityBehaviourWithoutAChecker(t *testing.T) {
	h, _ := newTestHandlers(t)
	call := func(role string, method string, fn http.HandlerFunc) int {
		req := withKitchen(httptest.NewRequest(method, "/api/v1/admin/cross-kitchen-grants", strings.NewReader(`{"source_kitchen":"a"}`)), "default")
		if role != "" {
			req = asRole(req, role)
		}
		rec := httptest.NewRecorder()
		fn(rec, req)
		return rec.Code
	}
	if c := call("", http.MethodGet, h.HandleListCrossKitchenGrants); c != http.StatusForbidden {
		t.Errorf("anonymous list: %d", c)
	}
	if c := call("admin", http.MethodGet, h.HandleListCrossKitchenGrants); c != http.StatusOK {
		t.Errorf("admin list: %d", c)
	}
	if c := call("head_chef", http.MethodGet, h.HandleListCrossKitchenGrants); c != http.StatusOK {
		t.Errorf("head chef list: %d", c)
	}
	if c := call("chef", http.MethodGet, h.HandleListCrossKitchenGrants); c != http.StatusForbidden {
		t.Errorf("chef list: %d", c)
	}
	if c := call("head_chef", http.MethodPost, h.HandleCreateCrossKitchenGrant); c != http.StatusForbidden {
		t.Errorf("head chef create: %d", c)
	}
	if c := call("admin", http.MethodPost, h.HandleCreateCrossKitchenGrant); c != http.StatusCreated {
		t.Errorf("admin create: %d", c)
	}
}

// A installed checker narrows the role: a Head Chef of kitchen A holds nothing in kitchen B, and
// the checker sees the kitchen the request names.
func TestInstalledKitchenOwnerCheckDecidesPerKitchen(t *testing.T) {
	h, _ := newTestHandlers(t)
	var seen []string
	h.SetKitchenOwnerCheck(func(_ *http.Request, kitchen string) bool {
		seen = append(seen, kitchen)
		return kitchen == "mine"
	})
	list := func(role, kitchen string) int {
		req := asRole(withKitchen(httptest.NewRequest(http.MethodGet, "/x", nil), kitchen), role)
		rec := httptest.NewRecorder()
		h.HandleListCrossKitchenGrants(rec, req)
		return rec.Code
	}
	if c := list("head_chef", "mine"); c != http.StatusOK {
		t.Errorf("own kitchen: %d", c)
	}
	if c := list("head_chef", "other"); c != http.StatusForbidden {
		t.Errorf("another kitchen: %d", c)
	}
	// The checker narrows, it never widens: a chef is refused by role before the checker is asked.
	seen = nil
	if c := list("chef", "mine"); c != http.StatusForbidden || len(seen) != 0 {
		t.Errorf("chef: %d, asked checker %v", c, seen)
	}
	create := func(role, kitchen string) int {
		req := asRole(withKitchen(httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"source_kitchen":"a"}`)), kitchen), role)
		rec := httptest.NewRecorder()
		h.HandleCreateCrossKitchenGrant(rec, req)
		return rec.Code
	}
	if c := create("restaurateur", "mine"); c != http.StatusCreated {
		t.Errorf("platform admin, own kitchen: %d", c)
	}
	if c := create("restaurateur", "other"); c != http.StatusForbidden {
		t.Errorf("checker refuses even a platform role for a kitchen it does not own: %d", c)
	}
}

// The describer of a recipe gate carries the step's own limits on who may decide it, so an edition
// that checks the signer first (Pro) can refuse an unlisted person before a signature is spent.
func TestRecipeGateSubjectCarriesTheStepsApproverConstraints(t *testing.T) {
	h, _ := runRig(t)
	s := h.Store
	ctx := context.Background()
	recipe := &models.Recipe{ID: "r-appr", Name: "release", Kitchen: "default", Steps: []models.Step{
		{Name: "open", Kind: models.StepHumanGate},
		{Name: "gate", Kind: models.StepHumanGate, ApproverRoles: []string{"head_chef"}, ApproverEmails: []string{"cy@x.test"}, ApproverDomain: "x.test"},
	}}
	if err := s.CreateRecipe(ctx, recipe); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRecipeRun(ctx, &models.RecipeRun{ID: "run-1", RecipeID: "r-appr", Kitchen: "default", Status: "paused", TriggeredBy: "ana@x.test", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"open", "gate"} {
		if err := s.CreateApproval(ctx, &models.ApprovalRecord{ID: "a-" + step, GateKey: "run-1:" + step, RunID: "run-1", StepName: step, Kitchen: "default", Status: "pending", RequestedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	subj, err := h.DescribeDecision(ctx, contracts.GateRecipeGate, "default", "run-1:gate", nil)
	if err != nil {
		t.Fatal(err)
	}
	if subj.Approvers == nil || len(subj.Approvers.Roles) != 1 || subj.Approvers.Roles[0] != "head_chef" ||
		len(subj.Approvers.Emails) != 1 || subj.Approvers.Domain != "x.test" {
		t.Fatalf("approvers = %+v", subj.Approvers)
	}
	if ok, _ := subj.Approvers.Admits("dee@other.test", "baker"); ok {
		t.Error("a baker the step does not name, outside its domain, is refused")
	}
	if ok, _ := subj.Approvers.Admits("dee@x.test", "restaurateur"); !ok {
		t.Error("a restaurateur is admitted where a head chef is")
	}
	open, err := h.DescribeDecision(ctx, contracts.GateRecipeGate, "default", "run-1:open", nil)
	if err != nil || open.Approvers != nil {
		t.Errorf("a step with no limits declares none: %+v %v", open, err)
	}
}
