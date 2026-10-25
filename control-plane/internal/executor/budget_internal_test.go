package executor

import (
	"sync"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func TestParallelGrantsNeverPromiseMoreThanTheParentHasLeft(t *testing.T) {
	parent := newRunBudget(models.RunBudget{MaxTokens: 10000, MaxCostUSD: 10}, models.TokenUsage{TotalTokens: 2000, EstimatedCost: 2}, 0, 0, nil)

	var wg sync.WaitGroup
	var mu sync.Mutex
	var grants []*grant
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g, err := parent.grantFor(models.RunBudget{})
			if err != nil {
				return
			}
			mu.Lock()
			grants = append(grants, g)
			mu.Unlock()
		}()
	}
	wg.Wait()

	var tokens int64
	var cost float64
	for _, g := range grants {
		tokens += g.limits.MaxTokens
		cost += g.limits.MaxCostUSD
	}
	if tokens > 8000 || cost > 8.0000001 {
		t.Fatalf("8 children were promised %d tokens and $%.2f out of 8000 / $8 left", tokens, cost)
	}
	if len(grants) != 8 {
		t.Fatalf("every child should get something while the parent still has money, got %d grants", len(grants))
	}
}

func TestAChildsSpendDrawsDownItsReservationAndIsNotCountedTwice(t *testing.T) {
	parent := newRunBudget(models.RunBudget{MaxTokens: 1000}, models.TokenUsage{}, 0, 0, nil)
	g, err := parent.grantFor(models.RunBudget{})
	if err != nil {
		t.Fatal(err)
	}
	child := newRunBudget(g.limits, models.TokenUsage{}, 0, 0, nil)
	child.parent, child.grant = parent, g

	child.charge(models.TokenUsage{TotalTokens: 200})

	if parent.tokens != 200 {
		t.Fatalf("the parent should have been charged the child's 200, has %d", parent.tokens)
	}
	if _, reserved := parent.reservedLocked(); reserved != g.limits.MaxTokens-200 {
		t.Fatalf("the reservation should shrink by what was spent: reserved %d, granted %d", reserved, g.limits.MaxTokens)
	}
	// What the parent can still promise or spend is cap - spent - reserved: the 200 once, not twice.
	g2, err := parent.grantFor(models.RunBudget{})
	if err != nil {
		t.Fatal(err)
	}
	want := int64(float64(1000-200-(g.limits.MaxTokens-200)) * 0.5)
	if g2.limits.MaxTokens != want {
		t.Fatalf("second grant = %d, want %d", g2.limits.MaxTokens, want)
	}

	parent.release(g)
	if _, reserved := parent.reservedLocked(); reserved != g2.limits.MaxTokens {
		t.Fatalf("releasing a grant frees what it had left, reserved now %d", reserved)
	}
}

func TestAGrantIsRefusedWhenNothingIsLeft(t *testing.T) {
	parent := newRunBudget(models.RunBudget{MaxTokens: 100}, models.TokenUsage{TotalTokens: 100}, 0, 0, nil)
	if _, err := parent.grantFor(models.RunBudget{}); err == nil {
		t.Fatal("a parent with nothing left cannot fund a subagent")
	}
}

func TestAResumedRunDoesNotReannounceThresholdsItAlreadyCrossed(t *testing.T) {
	b := newRunBudget(models.RunBudget{MaxTokens: 1000}, models.TokenUsage{TotalTokens: 600}, 0, 0, nil)
	if w := b.charge(models.TokenUsage{TotalTokens: 10}); len(w) != 0 {
		t.Fatalf("50%% was crossed before the restart, got %+v", w)
	}
	if w := b.charge(models.TokenUsage{TotalTokens: 200}); len(w) != 1 || w[0].Pct != 80 {
		t.Fatalf("crossing 80%% should warn once, got %+v", w)
	}
}

func TestABatchOfGrantsPromisesNoMoreThanTheShareOfWhatIsLeft(t *testing.T) {
	parent := newRunBudget(models.RunBudget{MaxTokens: 10000, MaxCostUSD: 10, ChildShare: 0.5}, models.TokenUsage{TotalTokens: 2000, EstimatedCost: 2}, 0, 0, nil)
	for _, n := range []int{1, 2, 4, 7, 10} {
		gs, err := parent.grantBatch(n)
		if err != nil {
			t.Fatal(err)
		}
		var tokens int64
		var cost float64
		for _, g := range gs {
			tokens += g.limits.MaxTokens
			cost += g.limits.MaxCostUSD
		}
		// 8000 tokens and $8 are left; half of that is the most the children may be promised.
		if tokens > 4000 || cost > 4.0000001 {
			t.Fatalf("%d children were promised %d tokens and $%.2f, over half of what is left", n, tokens, cost)
		}
		// And they are equal: the last child is not left with the dregs.
		if gs[0].limits.MaxTokens != gs[len(gs)-1].limits.MaxTokens {
			t.Fatalf("unequal shares: %d vs %d", gs[0].limits.MaxTokens, gs[len(gs)-1].limits.MaxTokens)
		}
		for _, g := range gs {
			parent.release(g)
		}
	}
	if _, reserved := parent.reservedLocked(); reserved != 0 {
		t.Fatalf("releasing every grant frees the reservation, %d still held", reserved)
	}
}
