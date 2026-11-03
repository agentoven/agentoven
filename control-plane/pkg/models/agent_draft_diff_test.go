package models

import (
	"encoding/json"
	"strings"
	"testing"
)

func diffAgent() *Agent {
	return &Agent{
		Name: "a", Version: "0.1.0", ModelProvider: "p1", ModelName: "m1", MaxTurns: 5, Description: "be brief",
		Skills: []string{"web", "math"}, ApprovalTools: []string{"pay"}, Subagents: []string{"helper"},
		Guardrails: []Guardrail{
			{ID: "g1", Kind: GuardrailPromptInjection, Stage: GuardrailStageInput, Enabled: true},
			{ID: "g2", Kind: "llamaguard", Stage: GuardrailStageOutput, Enabled: true, Config: map[string]interface{}{"api_key": "sk-guard-real", "threshold": 0.5}},
		},
		Ingredients: []Ingredient{
			{Kind: IngredientTool, Name: "search", Config: map[string]interface{}{"url": "https://x.example/api", "api_key": "sk-tool-real-1"}},
			{Kind: IngredientTool, Name: "calc"},
		},
		ResolvedConfig: &ResolvedIngredients{Prompt: &ResolvedPrompt{Template: "p"}},
	}
}

func find(d AgentDraftDiff, field, key, kind string) *DraftChange {
	for i := range d.Changes {
		c := &d.Changes[i]
		if c.Field == field && c.Key == key && c.Kind == kind {
			return c
		}
	}
	return nil
}

func TestAgentWithoutADraftHasNoChanges(t *testing.T) {
	d := DiffAgentDraft(diffAgent())
	if d.HasDraft || !d.Baked || d.BakedVersion != "0.1.0" || d.ChangesBehaviour || d.Changes == nil || len(d.Changes) != 0 {
		t.Fatalf("%+v", d)
	}
	raw, _ := json.Marshal(d)
	if !strings.Contains(string(raw), `"changes":[]`) {
		t.Fatalf("an empty change list must be [] and not null: %s", raw)
	}
	// Never baked: edits apply at once, nothing waits.
	u := &Agent{Name: "u", ModelName: "m"}
	if d := DiffAgentDraft(u); d.Baked || d.BakedVersion != "" || d.HasDraft || len(d.Changes) != 0 {
		t.Fatalf("%+v", d)
	}
}

func TestDraftDiffScalarsAndListsByElement(t *testing.T) {
	a := diffAgent()
	a.Edit(AgentEdit{
		ModelName: "m2", MaxTurns: 9,
		Skills:        []string{"web", "code"},
		ApprovalTools: []string{}, // empty list: clears
		Guardrails: []Guardrail{
			{ID: "g1", Kind: GuardrailPromptInjection, Stage: GuardrailStageInput, Enabled: false}, // changed
			{ID: "g3", Kind: "pii", Stage: GuardrailStageInput, Enabled: true},                     // added; g2 removed
		},
	})
	d := DiffAgentDraft(a)
	if !d.HasDraft || !d.ChangesBehaviour {
		t.Fatalf("%+v", d)
	}
	if c := find(d, "model_name", "", "changed"); c == nil || c.Before != "m1" || c.After != "m2" {
		t.Errorf("model_name: %+v", c)
	}
	if c := find(d, "max_turns", "", "changed"); c == nil || c.Before != float64(5) || c.After != float64(9) {
		t.Errorf("max_turns: %+v", c)
	}
	if c := find(d, "skills", "", "added"); c == nil || c.After != "code" || c.Before != nil {
		t.Errorf("skills added: %+v", c)
	}
	if c := find(d, "skills", "", "removed"); c == nil || c.Before != "math" {
		t.Errorf("skills removed: %+v", c)
	}
	if find(d, "skills", "", "changed") != nil {
		t.Error("a list of names is diffed by name, not as one blob")
	}
	if c := find(d, "approval_tools", "", "removed"); c == nil || c.Before != "pay" {
		t.Errorf("approval_tools: %+v", c)
	}
	if find(d, "subagents", "", "removed") != nil || find(d, "description", "", "changed") != nil {
		t.Error("a field the draft leaves alone must not show")
	}
	g1 := find(d, "guardrails", "g1", "changed")
	if g1 == nil || len(g1.Details) != 1 || g1.Details[0].Field != "enabled" || g1.Details[0].Before != true || g1.Details[0].After != false {
		t.Errorf("g1: %+v", g1)
	}
	if find(d, "guardrails", "g2", "removed") == nil || find(d, "guardrails", "g3", "added") == nil {
		t.Errorf("g2 removed / g3 added: %+v", d.Changes)
	}
}

func TestDraftDiffIngredientsAreKeyedByKindAndName(t *testing.T) {
	a := diffAgent()
	a.Edit(AgentEdit{Ingredients: []Ingredient{
		{Kind: IngredientTool, Name: "search", Config: map[string]interface{}{"url": "https://x.example/v2", "api_key": "sk-tool-real-1"}},
		{Kind: IngredientTool, Name: "weather"},
	}})
	d := DiffAgentDraft(a)
	s := find(d, "ingredients", "tool:search", "changed")
	if s == nil || len(s.Details) != 1 || s.Details[0].Field != "config.url" || s.Details[0].After != "https://x.example/v2" {
		t.Errorf("search: %+v", s)
	}
	if find(d, "ingredients", "tool:calc", "removed") == nil || find(d, "ingredients", "tool:weather", "added") == nil {
		t.Errorf("%+v", d.Changes)
	}
}

// The diff must never carry a secret, in either side, in an element or in its details; and a secret that
// changed must still show as changed although both masks read the same.
func TestDraftDiffMasksSecretsButStillSeesThemChange(t *testing.T) {
	a := diffAgent()
	a.Edit(AgentEdit{
		Ingredients: []Ingredient{
			{Kind: IngredientTool, Name: "search", Config: map[string]interface{}{"url": "https://x.example/api", "api_key": "sk-tool-real-2"}},
			{Kind: IngredientTool, Name: "calc"},
		},
		Guardrails: []Guardrail{
			{ID: "g1", Kind: GuardrailPromptInjection, Stage: GuardrailStageInput, Enabled: true},
			{ID: "g2", Kind: "llamaguard", Stage: GuardrailStageOutput, Enabled: true, Config: map[string]interface{}{"api_key": "sk-guard-new", "threshold": 0.5}},
			{ID: "g4", Kind: "x", Stage: GuardrailStageInput, Enabled: true, Config: map[string]interface{}{"password": "hunter2hunter2"}},
		},
	})
	d := DiffAgentDraft(a)
	raw, _ := json.Marshal(d)
	for _, secret := range []string{"sk-tool-real", "sk-guard", "hunter2"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("the diff leaks %q: %s", secret, raw)
		}
	}
	s := find(d, "ingredients", "tool:search", "changed")
	if s == nil || len(s.Details) != 1 || s.Details[0].Field != "config.api_key" || s.Details[0].Before != RedactedValue || s.Details[0].After != RedactedValue || s.Details[0].Kind != "changed" {
		t.Errorf("a changed secret is a change whose two sides are masked: %+v", s)
	}
	if find(d, "guardrails", "g2", "changed") == nil {
		t.Error("guardrail secret change not seen")
	}
	g4 := find(d, "guardrails", "g4", "added")
	if g4 == nil || g4.After.(map[string]interface{})["config"].(map[string]interface{})["password"] != RedactedValue {
		t.Errorf("an added element is masked too: %+v", g4)
	}
}

// A secret under a key the draft sets directly (a bot's credentials are references, but a URL token in a
// scalar field is not) is masked as well.
func TestDraftDiffMasksCredentialsInURLs(t *testing.T) {
	a := diffAgent()
	a.RepoURL = "https://user:oldtoken@git.example/r.git"
	a.Edit(AgentEdit{Description: "x"})
	d := DiffAgentDraft(a)
	if raw, _ := json.Marshal(d); strings.Contains(string(raw), "oldtoken") {
		t.Fatalf("%s", raw)
	}
}

func TestDraftDiffShowsClearedFields(t *testing.T) {
	a := diffAgent()
	a.Edit(AgentEdit{Cleared: []string{"description", "ingredients"}})
	d := DiffAgentDraft(a)
	if c := find(d, "description", "", "removed"); c == nil || c.Before != "be brief" || c.After != nil {
		t.Errorf("description: %+v", c)
	}
	if find(d, "ingredients", "tool:search", "removed") == nil || find(d, "ingredients", "tool:calc", "removed") == nil {
		t.Errorf("clearing the ingredients removes each of them: %+v", d.Changes)
	}
	if !d.ChangesBehaviour {
		t.Error("clearing the description changes the fingerprint")
	}
}

// A draft that sets what is already baked, or only an unordered detail, changes nothing and says so.
func TestDraftThatChangesNothingHasNoChanges(t *testing.T) {
	a := diffAgent()
	a.Edit(AgentEdit{ModelName: "m1", Skills: []string{"math", "web"}})
	d := DiffAgentDraft(a)
	if !d.HasDraft || len(d.Changes) != 0 {
		t.Fatalf("a reorder of names is no change: %+v", d)
	}
}

func TestDraftDiffNestedBotFieldsAndDuplicateKeys(t *testing.T) {
	a := diffAgent()
	a.Bot = &BotConfig{MaxActions: 10, AllowedDomains: []string{"a.com"}}
	a.Guardrails = append(a.Guardrails, Guardrail{ID: "g1", Kind: "dup", Stage: GuardrailStageInput}) // a stored list can hold two of one id
	a.Edit(AgentEdit{Bot: &BotConfig{MaxActions: 20, AllowedDomains: []string{"a.com", "b.com"}}, Guardrails: a.Guardrails[:2]})
	d := DiffAgentDraft(a)
	if c := find(d, "bot.max_actions", "", "changed"); c == nil || c.After != float64(20) {
		t.Errorf("%+v", d.Changes)
	}
	if c := find(d, "bot.allowed_domains", "", "added"); c == nil || c.After != "b.com" {
		t.Errorf("%+v", d.Changes)
	}
	if find(d, "guardrails", "g1#2", "removed") == nil || find(d, "guardrails", "g1", "removed") != nil {
		t.Errorf("the second guardrail with an id is told from the first: %+v", d.Changes)
	}
}

func TestDraftDiffDoesNotChangeTheAgent(t *testing.T) {
	a := diffAgent()
	a.Edit(AgentEdit{ModelName: "m2"})
	before, _ := json.Marshal(a)
	DiffAgentDraft(a)
	after, _ := json.Marshal(a)
	if string(before) != string(after) {
		t.Fatal("DiffAgentDraft must not touch the agent")
	}
}
