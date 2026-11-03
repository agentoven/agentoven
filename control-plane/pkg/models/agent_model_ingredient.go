package models

// AutoModelIngredientID names the model ingredient a bake synthesizes for an agent that only names a provider
// and a model at the top level.
const AutoModelIngredientID = "auto-model"

// EnsureModelIngredient is the step of a bake that gives an agent with a ModelProvider its model ingredient:
// one is synthesized when the agent has none, and one a bake synthesized earlier is brought up to date with
// the provider and model the agent names now. Without the second half an edit of model_name alone, applied by
// the next bake, left the ingredient (and so the resolved model the executor runs) on the old model while the
// version said the configuration had changed. An ingredient a person wrote is left as written.
//
// It is also what the draft diff applies to the agent it shows as "after the next bake", so the diff and the
// bake agree on which ingredients the agent will have.
func (a *Agent) EnsureModelIngredient() {
	if a.ModelProvider == "" {
		return
	}
	config := func() map[string]interface{} {
		return map[string]interface{}{"provider": a.ModelProvider, "model": a.ModelName}
	}
	for i, ing := range a.Ingredients {
		if ing.Kind != IngredientModel {
			continue
		}
		if ing.ID == AutoModelIngredientID {
			updated := append([]Ingredient(nil), a.Ingredients...)
			updated[i].Name, updated[i].Config = a.ModelProvider, config()
			a.Ingredients = updated
		}
		return
	}
	a.Ingredients = append(append([]Ingredient(nil), a.Ingredients...), Ingredient{ID: AutoModelIngredientID, Name: a.ModelProvider, Kind: IngredientModel, Required: true, Config: config()})
}
