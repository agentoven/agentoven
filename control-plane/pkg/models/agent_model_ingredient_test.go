package models

import "testing"

func TestEnsureModelIngredientSynthesizesOneAndKeepsThePersonsOwn(t *testing.T) {
	a := &Agent{ModelProvider: "p", ModelName: "m"}
	a.EnsureModelIngredient()
	if len(a.Ingredients) != 1 || a.Ingredients[0].ID != AutoModelIngredientID || a.Ingredients[0].Config["model"] != "m" || !a.Ingredients[0].Required {
		t.Fatalf("%+v", a.Ingredients)
	}
	own := &Agent{ModelProvider: "p", ModelName: "m", Ingredients: []Ingredient{{ID: "mine", Kind: IngredientModel, Name: "x", Config: map[string]interface{}{"model": "other"}}}}
	own.EnsureModelIngredient()
	if len(own.Ingredients) != 1 || own.Ingredients[0].Config["model"] != "other" {
		t.Fatalf("an ingredient a person wrote is left as written: %+v", own.Ingredients)
	}
	none := &Agent{}
	none.EnsureModelIngredient()
	if len(none.Ingredients) != 0 {
		t.Fatal("an agent without a provider gets no model ingredient")
	}
}

// An edit of the model alone, applied by the next bake, must reach the ingredient the executor reads.
func TestEnsureModelIngredientFollowsAnEditOfTheModel(t *testing.T) {
	a := &Agent{ModelProvider: "p", ModelName: "m"}
	a.EnsureModelIngredient()
	stored := a.Ingredients // what a store would hand out and share
	a.ModelProvider, a.ModelName = "q", "m2"
	a.EnsureModelIngredient()
	if got := a.Ingredients[0]; got.Name != "q" || got.Config["model"] != "m2" || got.Config["provider"] != "q" {
		t.Fatalf("%+v", got)
	}
	if stored[0].Config["model"] != "m" || stored[0].Name != "p" {
		t.Fatalf("the list the agent held before must not change under its holder: %+v", stored[0])
	}
}

// The diff shows the agent as the bake leaves it: an edit that replaces the ingredients does not remove the
// model ingredient, and an edit of the model alone changes it.
func TestDraftDiffAgreesWithTheBakeAboutTheModelIngredient(t *testing.T) {
	a := diffAgent()
	a.Ingredients = nil
	a.EnsureModelIngredient() // as stored after the bake
	a.Edit(AgentEdit{ModelName: "m2"})
	d := DiffAgentDraft(a)
	c := find(d, "ingredients", "model:p1", "changed")
	if c == nil || len(c.Details) != 1 || c.Details[0].Field != "config.model" || c.Details[0].After != "m2" {
		t.Fatalf("%+v", d.Changes)
	}

	b := diffAgent()
	b.Ingredients = nil
	b.EnsureModelIngredient()
	b.Edit(AgentEdit{Ingredients: []Ingredient{{Kind: IngredientTool, Name: "t"}}})
	d = DiffAgentDraft(b)
	if find(d, "ingredients", "tool:t", "added") == nil || find(d, "ingredients", "model:p1", "removed") != nil || find(d, "ingredients", "model:p1", "changed") != nil {
		t.Fatalf("the bake gives the model ingredient back, so it is neither removed nor changed: %+v", d.Changes)
	}
}
