package executor

import (
	"os"
	"strconv"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// A background task runs without anyone watching, possibly for hours, so unlike a request a
// person is waiting on it is never allowed to be unbounded. TaskBudgetDefaults fills in
// whatever its agent's budget leaves unset, and TaskBudgetCeiling caps what an agent or a
// request may ask for. Both read from the environment so an operator sets their own limits:
//
//	AGENTOVEN_RUN_BUDGET_COST_USD / _TOKENS / _ACTIVE_SECONDS / _TURNS           defaults
//	AGENTOVEN_RUN_BUDGET_MAX_COST_USD / _MAX_TOKENS / _MAX_ACTIVE_SECONDS / _MAX_TURNS   ceilings
//
// The dollar default applies only when the model has a known price (see ApplyTaskBudget).
func TaskBudgetDefaults() models.RunBudget {
	return models.RunBudget{
		MaxCostUSD:       envFloat("AGENTOVEN_RUN_BUDGET_COST_USD", 5),
		MaxTokens:        envInt64("AGENTOVEN_RUN_BUDGET_TOKENS", 1_000_000),
		MaxActiveSeconds: envInt64("AGENTOVEN_RUN_BUDGET_ACTIVE_SECONDS", 2*60*60),
		MaxTurns:         int(envInt64("AGENTOVEN_RUN_BUDGET_TURNS", 200)),
	}
}

// TaskBudgetCeiling is the most any task may be given, whatever its agent or request asks.
func TaskBudgetCeiling() models.RunBudget {
	return models.RunBudget{
		MaxCostUSD:       envFloat("AGENTOVEN_RUN_BUDGET_MAX_COST_USD", 50),
		MaxTokens:        envInt64("AGENTOVEN_RUN_BUDGET_MAX_TOKENS", 20_000_000),
		MaxActiveSeconds: envInt64("AGENTOVEN_RUN_BUDGET_MAX_ACTIVE_SECONDS", 24*60*60),
		MaxTurns:         int(envInt64("AGENTOVEN_RUN_BUDGET_MAX_TURNS", 2000)),
	}
}

// ApplyTaskBudget returns the budget a task for an agent runs under: the agent's own limits,
// with each dimension it leaves unset taken from the defaults and every dimension held to
// the ceiling. priced says whether the model has a known price; without one a dollar limit
// could not be enforced, so none is added (an explicit one from the agent is kept, and the
// run is then refused up front rather than quietly run unprotected).
func ApplyTaskBudget(agent *models.Agent, priced bool) models.RunBudget {
	var b models.RunBudget
	if agent.Budget != nil {
		b = *agent.Budget
	}
	def, ceil := TaskBudgetDefaults(), TaskBudgetCeiling()

	if b.MaxCostUSD <= 0 && priced {
		b.MaxCostUSD = def.MaxCostUSD
	}
	if b.MaxTokens <= 0 {
		b.MaxTokens = def.MaxTokens
	}
	if b.MaxActiveSeconds <= 0 {
		b.MaxActiveSeconds = def.MaxActiveSeconds
	}
	if b.MaxTurns <= 0 {
		b.MaxTurns = def.MaxTurns
	}

	if ceil.MaxCostUSD > 0 && b.MaxCostUSD > ceil.MaxCostUSD {
		b.MaxCostUSD = ceil.MaxCostUSD
	}
	if ceil.MaxTokens > 0 && b.MaxTokens > ceil.MaxTokens {
		b.MaxTokens = ceil.MaxTokens
	}
	if ceil.MaxActiveSeconds > 0 && b.MaxActiveSeconds > ceil.MaxActiveSeconds {
		b.MaxActiveSeconds = ceil.MaxActiveSeconds
	}
	if ceil.MaxTurns > 0 && b.MaxTurns > ceil.MaxTurns {
		b.MaxTurns = ceil.MaxTurns
	}
	return b
}

func envFloat(name string, def float64) float64 {
	if v := os.Getenv(name); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			return f
		}
	}
	return def
}

func envInt64(name string, def int64) int64 {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			return n
		}
	}
	return def
}
