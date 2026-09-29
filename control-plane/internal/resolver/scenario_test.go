package resolver

import (
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func TestResolveScenario(t *testing.T) {
	rs, err := resolveScenario(models.Ingredient{Name: "refund-disputes", Kind: models.IngredientScenario})
	if err != nil || rs.Scenario != "refund-disputes" {
		t.Fatalf("the ingredient name is the scenario id by default: %+v %v", rs, err)
	}

	rs, err = resolveScenario(models.Ingredient{Name: "evals", Kind: models.IngredientScenario, Config: map[string]any{
		"scenario": "ontime_delivery_refund_refused", "rollouts": 10.0, "min_pass_rate": 0.9,
	}})
	if err != nil || rs.Scenario != "ontime_delivery_refund_refused" || rs.Rollouts != 10 || rs.MinPassRate != 0.9 {
		t.Fatalf("config overrides: %+v %v", rs, err)
	}

	for name, cfg := range map[string]map[string]any{
		"fractional rollouts": {"rollouts": 2.5},
		"zero rollouts":       {"rollouts": 0.0},
		"pass rate above one": {"min_pass_rate": 1.5},
		"pass rate as string": {"min_pass_rate": "high"},
	} {
		if _, err := resolveScenario(models.Ingredient{Name: "x", Kind: models.IngredientScenario, Config: cfg}); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
