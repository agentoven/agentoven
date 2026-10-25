package models

// RunBudget bounds one agent run. A zero field means no limit on that dimension.
//
// Limits are enforced by the runtime between model calls, never by the model: an agent
// cannot talk its way past one. Dollar figures are estimates (catalog prices drift, cache
// and thinking rates are not modelled), so MaxTokens is the exact guardrail and
// MaxCostUSD the one a person reads on a bill. A run's overshoot is bounded by a single
// model call, not zero.
type RunBudget struct {
	MaxCostUSD float64 `json:"max_cost_usd,omitempty"`
	MaxTokens  int64   `json:"max_tokens,omitempty"`
	// MaxActiveSeconds counts time the run is actually working. Time spent paused,
	// waiting for a person, or down after a crash is not counted.
	MaxActiveSeconds int64 `json:"max_active_seconds,omitempty"`
	// MaxTurns replaces the agent's own max_turns when set. Hitting it pauses the run
	// for a decision instead of ending it with a "[Max turns reached]" answer.
	MaxTurns int `json:"max_turns,omitempty"`
	// WarnPct are the usage percentages that raise a warning event, once each.
	// Default 50 and 80; the cap itself (100) is the pause.
	WarnPct []int `json:"warn_pct,omitempty"`
	// ChildShare is the fraction of this run's remaining budget one delegated subagent
	// may be granted. Default 0.5.
	ChildShare float64 `json:"child_share,omitempty"`
}

// IsZero reports whether the budget sets no limit at all.
func (b RunBudget) IsZero() bool {
	return b.MaxCostUSD == 0 && b.MaxTokens == 0 && b.MaxActiveSeconds == 0 && b.MaxTurns == 0
}

// Tighten returns the stricter of b and o per dimension. A zero never loosens: a limit
// set on either side survives the merge. Only a BudgetExtension, approved at resume,
// raises a cap.
func (b RunBudget) Tighten(o RunBudget) RunBudget {
	out := b
	out.MaxCostUSD = minPositiveFloat(b.MaxCostUSD, o.MaxCostUSD)
	out.MaxTokens = minPositiveInt64(b.MaxTokens, o.MaxTokens)
	out.MaxActiveSeconds = minPositiveInt64(b.MaxActiveSeconds, o.MaxActiveSeconds)
	out.MaxTurns = int(minPositiveInt64(int64(b.MaxTurns), int64(o.MaxTurns)))
	if len(out.WarnPct) == 0 {
		out.WarnPct = o.WarnPct
	}
	if out.ChildShare == 0 {
		out.ChildShare = o.ChildShare
	}
	return out
}

// Extend returns b with ext added to each dimension that has a limit. A dimension with no
// limit stays unlimited: extending it would otherwise turn "no cap" into a small one.
func (b RunBudget) Extend(ext BudgetExtension) RunBudget {
	out := b
	if out.MaxCostUSD > 0 {
		out.MaxCostUSD += ext.AddCostUSD
	}
	if out.MaxTokens > 0 {
		out.MaxTokens += ext.AddTokens
	}
	if out.MaxActiveSeconds > 0 {
		out.MaxActiveSeconds += ext.AddActiveSeconds
	}
	if out.MaxTurns > 0 {
		out.MaxTurns += ext.AddTurns
	}
	return out
}

func minPositiveFloat(a, b float64) float64 {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	case a < b:
		return a
	}
	return b
}

func minPositiveInt64(a, b int64) int64 {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	case a < b:
		return a
	}
	return b
}

// BudgetExtension is a human's grant of more budget to a run that paused at its cap. ID
// makes it idempotent: a double click, or a retry after a crash that landed between
// recording the grant and continuing, applies it once.
type BudgetExtension struct {
	ID               string  `json:"id,omitempty"`
	AddCostUSD       float64 `json:"add_cost_usd,omitempty"`
	AddTokens        int64   `json:"add_tokens,omitempty"`
	AddActiveSeconds int64   `json:"add_active_seconds,omitempty"`
	AddTurns         int     `json:"add_turns,omitempty"`
	Reason           string  `json:"reason,omitempty"`
	// By is set by the server from the caller's identity, never taken from the request.
	By string `json:"by,omitempty"`
}

// IsZero reports whether the extension grants nothing.
func (e BudgetExtension) IsZero() bool {
	return e.AddCostUSD <= 0 && e.AddTokens <= 0 && e.AddActiveSeconds <= 0 && e.AddTurns <= 0
}

// Budget dimensions, as reported in a BudgetPause and a warning event.
const (
	BudgetDimCost   = "cost"
	BudgetDimTokens = "tokens"
	BudgetDimActive = "active_time"
	BudgetDimTurns  = "turns"
)

// BudgetPause records why and where a run stopped at its cap.
type BudgetPause struct {
	Turn      int     `json:"turn"` // the turn that was about to start
	Dimension string  `json:"dimension"`
	Spent     float64 `json:"spent"`
	Cap       float64 `json:"cap"`
	// Projected is what the blocked call was expected to add, when the stop was a
	// projection rather than an already-crossed cap.
	Projected float64 `json:"projected,omitempty"`
	Scope     string  `json:"scope"` // "run" today; "kitchen" when a Pro spend gate says so
}

// BudgetStatus is a run's budget as it stands, for responses and events.
type BudgetStatus struct {
	Limits RunBudget `json:"limits"`
	// Spent and Remaining use the same units as the limits.
	SpentCostUSD    float64 `json:"spent_cost_usd"`
	SpentTokens     int64   `json:"spent_tokens"`
	SpentActiveSecs int64   `json:"spent_active_seconds"`
	Turns           int     `json:"turns"`
}

// RunOutcome is what accounting for a hosted process's model calls found when a call to it
// ended: what they cost, how many there were, and whether a budget stopped them.
type RunOutcome struct {
	Usage TokenUsage `json:"usage"`
	Calls int        `json:"calls"`
	// Stopped is set when a model call was refused because the run's budget was used up.
	Stopped *BudgetPause `json:"stopped,omitempty"`
}
