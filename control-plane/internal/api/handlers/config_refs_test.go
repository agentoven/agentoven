package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/internal/resolver"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/agentoven/agentoven/control-plane/pkg/refcheck"
)

// refKitchen is a kitchen with what the checks look for: two providers, a prompt, an enabled and a disabled
// tool, an accepted and a pending skill, a ready and a retired agent, a channel and a recipe.
func refKitchen(t *testing.T) (*handlers.Handlers, store.Store) {
	t.Helper()
	h, s := newTestHandlers(t)
	h.Resolver = resolver.NewResolver(s)
	ctx := context.Background()
	now := time.Now().UTC()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.CreateProvider(ctx, &models.ModelProvider{ID: "p1", Name: "anthropic", Kind: "anthropic", Kitchen: "default", Models: []string{"claude-haiku-4-5", "claude-sonnet-4-5"}, CreatedAt: now}))
	must(s.CreateProvider(ctx, &models.ModelProvider{ID: "p2", Name: "router", Kind: "openrouter", Kitchen: "default", Models: []string{"anthropic/claude-haiku-4.5"}, CreatedAt: now}))
	// Another kitchen's provider must not be visible from "default".
	must(s.CreateProvider(ctx, &models.ModelProvider{ID: "p3", Name: "elsewhere", Kind: "openai", Kitchen: "other", Models: []string{"gpt-4o"}, CreatedAt: now}))
	must(s.CreatePrompt(ctx, &models.Prompt{ID: "pr1", Name: "support", Kitchen: "default", Template: "be kind", Version: 1}))
	must(s.CreateTool(ctx, &models.MCPTool{ID: "t1", Name: "search", Kitchen: "default", Enabled: true, Capabilities: []string{"tool"}, CreatedAt: now, UpdatedAt: now}))
	must(s.CreateTool(ctx, &models.MCPTool{ID: "t2", Name: "crm", Kitchen: "default", Enabled: false, Capabilities: []string{"tool"}, CreatedAt: now, UpdatedAt: now}))
	must(s.CreateSkill(ctx, &models.Skill{ID: "s1", Name: "pdf-reader", Kitchen: "default", Status: models.SkillStatusAccepted}))
	must(s.CreateSkill(ctx, &models.Skill{ID: "s2", Name: "shady", Kitchen: "default", Status: models.SkillStatusPending}))
	seedAgent(t, s, "default", "writer")
	must(s.CreateAgent(ctx, &models.Agent{ID: "a-old", Name: "old-bot", Kitchen: "default", Status: models.AgentStatusRetired, Mode: models.AgentModeManaged, Version: "0.1.0", CreatedAt: now, UpdatedAt: now}))
	return h, s
}

func problemsOf(t *testing.T, w *httptest.ResponseRecorder, want int) []refcheck.Problem {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status %d, want %d: %s", w.Code, want, w.Body.String())
	}
	var body struct {
		Code     string             `json:"code"`
		Problems []refcheck.Problem `json:"problems"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if want == http.StatusUnprocessableEntity && body.Code != "invalid_configuration" {
		t.Fatalf("a 422 carries code invalid_configuration: %s", w.Body.String())
	}
	return body.Problems
}

func hasProblem(ps []refcheck.Problem, path, code string) *refcheck.Problem {
	for i := range ps {
		if ps[i].Path == path && ps[i].Code == code {
			return &ps[i]
		}
	}
	return nil
}

func TestCreatingAnAgentThatNamesWhatTheKitchenLacksIsRefusedWithTheWholeList(t *testing.T) {
	h, _ := refKitchen(t)
	body := `{"name":"new","model_provider":"antropic","model_name":"x",
	  "summary_provider":"anthropic","summary_model":"claude-hiku-4-5",
	  "skills":["pdf-reader","pdf-reeder"],"approval_tools":["serch"],"subagents":["writer","old-bot"],
	  "ingredients":[{"kind":"tool","name":"search","required":true},{"kind":"prompt","name":"suport","required":true}]}`
	ps := problemsOf(t, callAgentAPI(h.RegisterAgent, http.MethodPost, "/api/v1/agents", nil, body), http.StatusUnprocessableEntity)
	for _, want := range [][2]string{
		{"model_provider", "not_found"}, {"summary_model", "not_found"}, {"skills[1]", "not_found"},
		{"approval_tools[0]", "not_found"}, {"subagents[1]", "not_usable"}, {"ingredients[1].name", "not_found"},
	} {
		if hasProblem(ps, want[0], want[1]) == nil {
			t.Errorf("missing %s %s in %+v", want[0], want[1], ps)
		}
	}
	if p := hasProblem(ps, "summary_model", "not_found"); p != nil && (len(p.Suggestions) != 1 || p.Suggestions[0] != "claude-haiku-4-5" || p.Within != "anthropic") {
		t.Errorf("the typo must come back with the nearest model: %+v", p)
	}
	if p := hasProblem(ps, "model_provider", "not_found"); p != nil && (len(p.Suggestions) == 0 || p.Suggestions[0] != "anthropic") {
		t.Errorf("provider suggestion: %+v", p)
	}
	if len(ps) != 6 {
		t.Errorf("exactly the six problems, got %d: %+v", len(ps), ps)
	}
}

func TestAnAgentThatNamesWhatExistsIsAccepted(t *testing.T) {
	h, s := refKitchen(t)
	body := `{"name":"fine","model_provider":"anthropic","model_name":"claude-haiku-4-5","summary_provider":"anthropic","summary_model":"claude-haiku-4-5",
	  "skills":["pdf-reader"],"approval_tools":["search"],"subagents":["writer"],
	  "ingredients":[{"kind":"tool","name":"search","required":true},{"kind":"prompt","name":"support","required":true}]}`
	if w := callAgentAPI(h.RegisterAgent, http.MethodPost, "/api/v1/agents", nil, body); w.Code != http.StatusCreated {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if _, err := s.GetAgent(context.Background(), "default", "fine"); err != nil {
		t.Fatal(err)
	}
}

func TestAnotherKitchensProviderDoesNotExist(t *testing.T) {
	h, _ := refKitchen(t)
	w := callAgentAPI(h.RegisterAgent, http.MethodPost, "/api/v1/agents", nil, `{"name":"x","model_provider":"elsewhere","model_name":"gpt-4o"}`)
	ps := problemsOf(t, w, http.StatusUnprocessableEntity)
	if p := hasProblem(ps, "model_provider", "not_found"); p == nil {
		t.Fatalf("%+v", ps)
	} else if strings.Contains(w.Body.String(), "gpt-4o") && len(p.Suggestions) > 0 && p.Suggestions[0] == "elsewhere" {
		t.Fatalf("another kitchen's provider leaked as a suggestion: %s", w.Body.String())
	}
}

func TestUnknownFieldsAndWrongTypesAreNamedNotIgnored(t *testing.T) {
	h, _ := refKitchen(t)
	ps := problemsOf(t, callAgentAPI(h.RegisterAgent, http.MethodPost, "/api/v1/agents", nil, `{"name":"x","summry_model":"m","max_turns":"ten"}`), http.StatusUnprocessableEntity)
	if p := hasProblem(ps, "summry_model", "unknown_field"); p == nil || len(p.Suggestions) == 0 || p.Suggestions[0] != "summary_model" {
		t.Errorf("a typo of a field name says what was meant: %+v", ps)
	}
	if hasProblem(ps, "max_turns", "wrong_type") == nil {
		t.Errorf("a string where a number belongs is named: %+v", ps)
	}
	// A body that is not JSON stays a plain 400.
	if w := callAgentAPI(h.RegisterAgent, http.MethodPost, "/api/v1/agents", nil, `{not json`); w.Code != http.StatusBadRequest {
		t.Errorf("not JSON: %d", w.Code)
	}
}

func TestAnUpdateIsCheckedButDoesNotAnswerForWhatTheAgentAlreadyHad(t *testing.T) {
	h, s := refKitchen(t)
	// An agent saved before the checks existed, holding a skill nobody has.
	seed := &models.Agent{ID: "legacy", Name: "legacy", Kitchen: "default", Mode: models.AgentModeManaged, Status: models.AgentStatusDraft, Version: "0.1.0",
		Skills: []string{"ghost-skill"}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := s.CreateAgent(context.Background(), seed); err != nil {
		t.Fatal(err)
	}
	params := map[string]string{"agentName": "legacy"}
	put := func(body string) *httptest.ResponseRecorder {
		return callAgentAPI(h.UpdateAgent, http.MethodPut, "/api/v1/agents/legacy", params, body)
	}
	if w := put(`{"description":"only the description"}`); w.Code != http.StatusOK {
		t.Fatalf("an edit that makes nothing worse is accepted: %d %s", w.Code, w.Body.String())
	}
	// Sending the same bad skill back (a client that read the agent and writes it again) is not new either.
	if w := put(`{"description":"again","skills":["ghost-skill"]}`); w.Code != http.StatusOK {
		t.Fatalf("an unchanged problem is not held against the edit: %d %s", w.Code, w.Body.String())
	}
	// A new one is.
	ps := problemsOf(t, put(`{"skills":["ghost-skill","another-ghost"]}`), http.StatusUnprocessableEntity)
	if len(ps) != 1 || ps[0].Value != "another-ghost" {
		t.Fatalf("only the new problem is reported: %+v", ps)
	}
	// And the way out is accepted: clear the list.
	if w := put(`{"skills":[]}`); w.Code != http.StatusOK {
		t.Fatalf("an empty list clears the skills: %d %s", w.Code, w.Body.String())
	}
	got, _ := s.GetAgent(context.Background(), "default", "legacy")
	if len(got.Skills) != 0 {
		t.Fatalf("the skills were not cleared: %v", got.Skills)
	}
}

func TestTheDraftIsCheckedWhenItIsSavedAndTheSettingsPutUsedToDropAreKept(t *testing.T) {
	h, s := refKitchen(t)
	base := `{"name":"baked1","mode":"external"}` // external: nothing to start, and no provider test in the background step
	if w := callAgentAPI(h.RegisterAgent, http.MethodPost, "/api/v1/agents", nil, base); w.Code != http.StatusCreated {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if w := callAgentAPI(h.BakeAgent, http.MethodPost, "/api/v1/agents/baked1/bake", map[string]string{"agentName": "baked1"}, ""); w.Code != http.StatusAccepted {
		t.Fatalf("bake: %d %s", w.Code, w.Body.String())
	}
	params := map[string]string{"agentName": "baked1"}
	// The agent is baked, so an edit becomes a draft; the draft must already name what exists.
	bad := callAgentAPI(h.UpdateAgent, http.MethodPut, "/api/v1/agents/baked1", params, `{"summary_provider":"anthropic","summary_model":"claude-hiku-4-5"}`)
	problemsOf(t, bad, http.StatusUnprocessableEntity)
	good := callAgentAPI(h.UpdateAgent, http.MethodPut, "/api/v1/agents/baked1", params, `{"summary_provider":"anthropic","summary_model":"claude-haiku-4-5","subagents":["writer"],"context_budget":4000}`)
	if good.Code != http.StatusOK {
		t.Fatalf("%d %s", good.Code, good.Body.String())
	}
	got, _ := s.GetAgent(context.Background(), "default", "baked1")
	if got.Draft == nil || got.Draft.SummaryModel == nil || *got.Draft.SummaryModel != "claude-haiku-4-5" || len(got.Draft.Subagents) != 1 || got.Draft.ContextBudget != 4000 {
		t.Fatalf("the settings were not kept in the draft: %+v", got.Draft)
	}
	// The next bake takes them up.
	if w := callAgentAPI(h.BakeAgent, http.MethodPost, "/api/v1/agents/baked1/bake", params, ""); w.Code != http.StatusAccepted {
		t.Fatalf("bake: %d %s", w.Code, w.Body.String())
	}
	got, _ = s.GetAgent(context.Background(), "default", "baked1")
	if got.SummaryModel != "claude-haiku-4-5" || got.SummaryProvider != "anthropic" || len(got.Subagents) != 1 {
		t.Fatalf("the bake did not apply the draft: %+v", got)
	}
}

func TestABakeRefusesWhatTheKitchenLostSinceTheAgentWasSaved(t *testing.T) {
	h, s := refKitchen(t)
	if w := callAgentAPI(h.RegisterAgent, http.MethodPost, "/api/v1/agents", nil, `{"name":"later","model_provider":"anthropic","model_name":"claude-haiku-4-5","skills":["pdf-reader"]}`); w.Code != http.StatusCreated {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if err := s.DeleteSkill(context.Background(), "default", "pdf-reader"); err != nil {
		t.Fatal(err)
	}
	w := callAgentAPI(h.BakeAgent, http.MethodPost, "/api/v1/agents/later/bake", map[string]string{"agentName": "later"}, "")
	ps := problemsOf(t, w, http.StatusUnprocessableEntity)
	if hasProblem(ps, "skills[0]", "not_found") == nil {
		t.Fatalf("%+v", ps)
	}
	// A managed agent with no model at all is refused at its bake, not when it is saved.
	if w := callAgentAPI(h.RegisterAgent, http.MethodPost, "/api/v1/agents", nil, `{"name":"nomodel"}`); w.Code != http.StatusCreated {
		t.Fatalf("a draft may be incomplete: %d %s", w.Code, w.Body.String())
	}
	ps = problemsOf(t, callAgentAPI(h.BakeAgent, http.MethodPost, "/api/v1/agents/nomodel/bake", map[string]string{"agentName": "nomodel"}, ""), http.StatusUnprocessableEntity)
	if hasProblem(ps, "model_provider", "required") == nil {
		t.Fatalf("%+v", ps)
	}
}

func TestValidateListsTheProblemsOfAnAgentThatIsStillRunnable(t *testing.T) {
	h, s := refKitchen(t)
	legacy := &models.Agent{ID: "legacy", Name: "legacy", Kitchen: "default", Mode: models.AgentModeManaged, Status: models.AgentStatusReady, Version: "0.1.0",
		ModelProvider: "anthropic", ModelName: "claude-haiku-4-5", Skills: []string{"ghost", "shady"}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := s.CreateAgent(context.Background(), legacy); err != nil {
		t.Fatal(err)
	}
	w := callAgentAPI(h.ValidateAgent, http.MethodGet, "/api/v1/agents/legacy/validate", map[string]string{"agentName": "legacy"}, "")
	var out struct {
		Valid    bool               `json:"valid"`
		Problems []refcheck.Problem `json:"problems"`
		Warnings []refcheck.Problem `json:"warnings"`
	}
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Valid || hasProblem(out.Problems, "skills[0]", "not_found") == nil || hasProblem(out.Warnings, "skills[1]", "not_usable") == nil {
		t.Fatalf("%s", w.Body.String())
	}
	if w := callAgentAPI(h.ValidateAgent, http.MethodGet, "/api/v1/agents/nope/validate", map[string]string{"agentName": "nope"}, ""); w.Code != http.StatusNotFound {
		t.Fatalf("an unknown agent is a 404: %d", w.Code)
	}
	// Nothing was changed.
	got, _ := s.GetAgent(context.Background(), "default", "legacy")
	if got.Status != models.AgentStatusReady || len(got.Skills) != 2 {
		t.Fatalf("validate changed the agent: %+v", got)
	}
}

func TestRecipesNameAgentsStepsChannelsAndRecipesThatExist(t *testing.T) {
	h, s := refKitchen(t)
	if err := s.CreateChannel(context.Background(), &models.NotificationChannel{ID: "c1", Name: "ops", Kind: models.ChannelWebhook, Kitchen: "default"}); err != nil {
		t.Fatal(err)
	}
	bad := `{"name":"flow","steps":[
	  {"name":"a","kind":"agent","agent_ref":"writr"},
	  {"name":"b","kind":"agent","agent_ref":"old-bot","depends_on":["aa"]},
	  {"name":"n","kind":"notify","config":{"message":"hi","channels":["opps"]}}]}`
	ps := problemsOf(t, callAgentAPI(h.CreateRecipe, http.MethodPost, "/api/v1/recipes", nil, bad), http.StatusUnprocessableEntity)
	for _, want := range [][2]string{{"steps[0].agent_ref", "not_found"}, {"steps[1].agent_ref", "not_usable"}, {"steps[1].depends_on[0]", "not_found"}, {"steps[2].config.channels[0]", "not_found"}} {
		if hasProblem(ps, want[0], want[1]) == nil {
			t.Errorf("missing %v in %+v", want, ps)
		}
	}
	good := `{"name":"flow","steps":[{"name":"a","kind":"agent","agent_ref":"writer"},{"name":"n","kind":"notify","depends_on":["a"],"config":{"message":"hi","channels":["ops"]}}]}`
	if w := callAgentAPI(h.CreateRecipe, http.MethodPost, "/api/v1/recipes", nil, good); w.Code != http.StatusCreated {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	// An update is judged the same way, and a recipe that has lost its agent cannot run.
	put := callAgentAPI(h.UpdateRecipe, http.MethodPut, "/api/v1/recipes/flow", map[string]string{"recipeName": "flow"}, `{"steps":[{"name":"a","kind":"agent","agent_ref":"writr"}]}`)
	problemsOf(t, put, http.StatusUnprocessableEntity)
	if err := s.DeleteAgent(context.Background(), "default", "writer"); err != nil {
		t.Fatal(err)
	}
	run := callAgentAPI(h.BakeRecipe, http.MethodPost, "/api/v1/recipes/flow/bake", map[string]string{"recipeName": "flow"}, `{}`)
	if ps := problemsOf(t, run, http.StatusUnprocessableEntity); hasProblem(ps, "steps[0].agent_ref", "not_found") == nil && hasProblem(ps, "steps[0].agent_ref", "not_usable") == nil {
		t.Fatalf("%+v", ps)
	}
	val := callAgentAPI(h.ValidateRecipe, http.MethodGet, "/api/v1/recipes/flow/validate", map[string]string{"recipeName": "flow"}, "")
	if val.Code != http.StatusOK || !strings.Contains(val.Body.String(), `"valid":false`) {
		t.Fatalf("%d %s", val.Code, val.Body.String())
	}
}

type fakeReferences struct{ scenarios, roles []string }

func (f fakeReferences) Scenarios(context.Context, string) ([]string, error) { return f.scenarios, nil }
func (f fakeReferences) Roles(context.Context, string) ([]string, error)     { return f.roles, nil }

func TestAnEditionThatCanListScenariosAndRolesGetsThemChecked(t *testing.T) {
	h, _ := refKitchen(t)
	h.References = fakeReferences{scenarios: []string{"refund-flow"}, roles: []string{"head_chef", "chef"}}
	body := `{"name":"sc","ingredients":[{"kind":"scenario","name":"refund-flw"}]}`
	ps := problemsOf(t, callAgentAPI(h.RegisterAgent, http.MethodPost, "/api/v1/agents", nil, body), http.StatusUnprocessableEntity)
	if p := hasProblem(ps, "ingredients[0].name", "not_found"); p == nil || p.Suggestions[0] != "refund-flow" {
		t.Fatalf("%+v", ps)
	}
	rec := `{"name":"gate","steps":[{"name":"g","kind":"human_gate","approver_roles":["chief"]}]}`
	ps = problemsOf(t, callAgentAPI(h.CreateRecipe, http.MethodPost, "/api/v1/recipes", nil, rec), http.StatusUnprocessableEntity)
	if hasProblem(ps, "steps[0].approver_roles[0]", "not_found") == nil {
		t.Fatalf("%+v", ps)
	}
	// Without such an edition the same references are not judged.
	h.References = nil
	if w := callAgentAPI(h.RegisterAgent, http.MethodPost, "/api/v1/agents", nil, body); w.Code != http.StatusCreated {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
