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
