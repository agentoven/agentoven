package models_test

import (
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func TestTightenKeepsTheStricterLimitPerDimension(t *testing.T) {
	cases := []struct {
		name string
		a, b models.RunBudget
		want models.RunBudget
	}{
		{"both set: lower wins", models.RunBudget{MaxCostUSD: 5, MaxTokens: 1000}, models.RunBudget{MaxCostUSD: 2, MaxTokens: 5000},
			models.RunBudget{MaxCostUSD: 2, MaxTokens: 1000}},
		{"a zero never loosens a set limit (b zero)", models.RunBudget{MaxCostUSD: 5}, models.RunBudget{},
			models.RunBudget{MaxCostUSD: 5}},
		{"a zero never loosens a set limit (a zero)", models.RunBudget{}, models.RunBudget{MaxTokens: 100},
			models.RunBudget{MaxTokens: 100}},
		{"dimensions merge independently", models.RunBudget{MaxCostUSD: 5}, models.RunBudget{MaxTokens: 100, MaxTurns: 7},
			models.RunBudget{MaxCostUSD: 5, MaxTokens: 100, MaxTurns: 7}},
		{"negative is not a limit", models.RunBudget{MaxCostUSD: -1, MaxTurns: 4}, models.RunBudget{MaxCostUSD: 3},
			models.RunBudget{MaxCostUSD: 3, MaxTurns: 4}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.a.Tighten(c.b)
			if got.MaxCostUSD != c.want.MaxCostUSD || got.MaxTokens != c.want.MaxTokens ||
				got.MaxActiveSeconds != c.want.MaxActiveSeconds || got.MaxTurns != c.want.MaxTurns {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestTightenIsCommutativeOnLimits(t *testing.T) {
	a := models.RunBudget{MaxCostUSD: 5, MaxActiveSeconds: 60}
	b := models.RunBudget{MaxCostUSD: 2, MaxTokens: 9, MaxTurns: 3}
	ab, ba := a.Tighten(b), b.Tighten(a)
	if ab.MaxCostUSD != ba.MaxCostUSD || ab.MaxTokens != ba.MaxTokens || ab.MaxActiveSeconds != ba.MaxActiveSeconds || ab.MaxTurns != ba.MaxTurns {
		t.Fatalf("order changed the result: %+v vs %+v", ab, ba)
	}
}

func TestExtendRaisesOnlyDimensionsThatHaveALimit(t *testing.T) {
	b := models.RunBudget{MaxCostUSD: 1}
	got := b.Extend(models.BudgetExtension{AddCostUSD: 2, AddTokens: 500, AddTurns: 10})
	if got.MaxCostUSD != 3 {
		t.Fatalf("cost cap should rise to 3, got %v", got.MaxCostUSD)
	}
	// Extending a dimension that was unlimited must not turn "no cap" into a small cap.
	if got.MaxTokens != 0 || got.MaxTurns != 0 {
		t.Fatalf("unlimited dimensions must stay unlimited, got tokens=%d turns=%d", got.MaxTokens, got.MaxTurns)
	}
}

func TestExtensionIsZero(t *testing.T) {
	if !(models.BudgetExtension{}).IsZero() || !(models.BudgetExtension{AddCostUSD: -5}).IsZero() {
		t.Fatal("an empty or negative extension grants nothing")
	}
	if (models.BudgetExtension{AddTokens: 1}).IsZero() {
		t.Fatal("a token grant is not zero")
	}
}
