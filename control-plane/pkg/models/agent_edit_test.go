package models

import (
	"encoding/json"
	"strings"
	"testing"
)

func bakedAgent() *Agent {
	return &Agent{Name: "a", ModelName: "m1", MaxTurns: 5, Guardrails: []Guardrail{{ID: "g1"}},
		Ingredients:    []Ingredient{{Kind: IngredientTool, Name: "t", Config: map[string]interface{}{"api_key": "sk-real-secret-value"}}},
		ResolvedConfig: &ResolvedIngredients{Prompt: &ResolvedPrompt{Template: "p"}}}
}

// An agent that has been baked runs what was baked: an edit waits in the draft.
func TestAnEditToABakedAgentWaitsInTheDraft(t *testing.T) {
	a := bakedAgent()
	before := BakedFingerprint(a)
	if !a.Edit(AgentEdit{ModelName: "m2", MaxTurns: 9, Guardrails: []Guardrail{}}) {
		t.Fatal("the edit must be kept as a draft")
	}
	if a.ModelName != "m1" || a.MaxTurns != 5 || len(a.Guardrails) != 1 {
		t.Fatalf("a draft must not change what runs: %+v", a)
	}
	if BakedFingerprint(a) != before {
		t.Fatal("a draft is not part of the fingerprint: it is not what runs")
	}
	// A second edit joins the first; an empty guardrail list in the draft survives a round trip.
	a.Edit(AgentEdit{Description: "d"})
	raw, _ := json.Marshal(a.Draft)
	var back AgentEdit
	if err := json.Unmarshal(raw, &back); err != nil || back.Guardrails == nil || len(back.Guardrails) != 0 || back.ModelName != "m2" || back.Description != "d" {
		t.Fatalf("draft lost something in storage: %s %v", raw, err)
	}
	if w := a.WithDraft(); w.ModelName != "m2" || len(w.Guardrails) != 0 || a.ModelName != "m1" {
		t.Fatal("WithDraft must show the agent as it will be, and leave the agent alone")
	}
	a.ApplyDraft()
	if a.Draft != nil || a.ModelName != "m2" || a.MaxTurns != 9 || len(a.Guardrails) != 0 || a.Description != "d" {
		t.Fatalf("a bake applies the whole draft and spends it: %+v", a)
	}
	if BakedFingerprint(a) == before {
		t.Fatal("what the draft changed is behaviour: the fingerprint must move once it is applied")
	}
}

func TestAnEditToAnUnbakedAgentAppliesAtOnce(t *testing.T) {
	a := &Agent{Name: "a", ModelName: "m1"}
	if a.Edit(AgentEdit{ModelName: "m2"}) || a.ModelName != "m2" || a.Draft != nil {
		t.Fatalf("nothing runs yet, so there is nothing to protect: %+v", a)
	}
}

// A client that reads an agent and writes it back has only seen masked secrets; the draft must hold the
// real one, and the API must not show it.
func TestTheDraftKeepsSecretsAndTheAPIHidesThem(t *testing.T) {
	a := bakedAgent()
	masked := RedactConfig(a.Ingredients[0].Config)
	a.Edit(AgentEdit{Ingredients: []Ingredient{{Kind: IngredientTool, Name: "t", Config: masked}}})
	if got := a.Draft.Ingredients[0].Config["api_key"]; got != "sk-real-secret-value" {
		t.Fatalf("the draft holds %v instead of the stored secret", got)
	}
	shown, _ := json.Marshal(a.Redacted())
	if strings.Contains(string(shown), "sk-real-secret-value") {
		t.Fatalf("a secret leaks through the draft: %s", shown)
	}
}

// JSON null clears an optional field of an agent that was never baked at once (owner decision A4).
func TestNullClearsAnOptionalFieldOfAnUnbakedAgent(t *testing.T) {
	a := &Agent{Name: "a", BackupProvider: "p2", BackupModel: "m2", Description: "d", MaxTurns: 7, ContextBudget: 900, SummaryModel: "s"}
	cleared, refused := NullFields([]byte(`{"backup_provider":null,"backup_model":null,"max_turns":null,"summary_model":null,"description":"kept"}`))
	if len(refused) != 0 || strings.Join(cleared, ",") != "backup_provider,backup_model,max_turns,summary_model" {
		t.Fatalf("cleared %v refused %v", cleared, refused)
	}
	a.Edit(AgentEdit{Description: "kept", Cleared: cleared})
	if a.BackupProvider != "" || a.BackupModel != "" || a.MaxTurns != 0 || a.SummaryModel != "" || a.Description != "kept" || a.ContextBudget != 900 {
		t.Fatalf("only the named fields clear and the rest stays: %+v", a)
	}
}

// On a baked agent the clear waits in the draft, survives storage, and a later edit that sets the field wins.
func TestNullOnABakedAgentWaitsInTheDraftAndASetLaterWins(t *testing.T) {
	a := bakedAgent()
	a.BackupProvider, a.BackupModel = "p2", "m2"
	if !a.Edit(AgentEdit{Cleared: []string{"backup_provider", "backup_model"}}) {
		t.Fatal("the clear must be kept as a draft")
	}
	if a.BackupProvider != "p2" || a.BackupModel != "m2" {
		t.Fatalf("a draft must not change what runs: %+v", a)
	}
	if w := a.WithDraft(); w.BackupProvider != "" || w.BackupModel != "" {
		t.Fatalf("the agent as it will be has no backup: %+v", w)
	}
	raw, _ := json.Marshal(a.Draft)
	var back AgentEdit
	if err := json.Unmarshal(raw, &back); err != nil || len(back.Cleared) != 2 {
		t.Fatalf("the clear is lost in storage: %s %v", raw, err)
	}
	// A second edit sets one of them again: it is no longer cleared.
	a.Edit(AgentEdit{BackupProvider: "p3"})
	w := a.WithDraft()
	if w.BackupProvider != "p3" || w.BackupModel != "" {
		t.Fatalf("the later set wins and the other stays cleared: %+v", w)
	}
	// And a clear after a set drops the set.
	a.Edit(AgentEdit{Cleared: []string{"backup_provider"}})
	if w := a.WithDraft(); w.BackupProvider != "" || a.Draft.BackupProvider != "" {
		t.Fatalf("a clear after a set removes it: %+v / %+v", w, a.Draft)
	}
	a.ApplyDraft()
	if a.BackupProvider != "" || a.BackupModel != "" || a.Draft != nil {
		t.Fatalf("a bake applies the clear: %+v", a)
	}
}

// The fields an agent cannot run without are refused, the lists keep their rule (null is "no opinion").
func TestNullFieldsRefusesRequiredAndLeavesLists(t *testing.T) {
	cleared, refused := NullFields([]byte(`{"model_name":null,"mode":null,"skills":null,"guardrails":null,"approval_tools":null,"backup_model":null}`))
	if strings.Join(refused, ",") != "model_name,mode" || strings.Join(cleared, ",") != "backup_model" {
		t.Fatalf("cleared %v refused %v", cleared, refused)
	}
	if c, r := NullFields([]byte(`not json`)); c != nil || r != nil {
		t.Fatal("a body that is not JSON clears nothing")
	}
	// A string "null" is a value, not a clear.
	if c, _ := NullFields([]byte(`{"description":"null"}`)); len(c) != 0 {
		t.Fatalf("the text null is not JSON null: %v", c)
	}
	// An edit that only clears is not empty.
	if (&AgentEdit{Cleared: []string{"description"}}).IsEmpty() {
		t.Fatal("a clear is an edit")
	}
}
