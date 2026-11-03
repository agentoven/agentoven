package models

import (
	"errors"
	"fmt"
	"time"
)

// SpendQuery describes a model call about to be made, as a spend gate sees it.
type SpendQuery struct {
	Kitchen  string
	Agent    string // the agent the call is for, when there is one
	Provider string
	Model    string
	// MaxOutputTokens is the output cap the call carries, 0 for none.
	MaxOutputTokens int
	// EstimatedInputTokens is a rough count of the prompt (about four characters a token), for a
	// gate that reserves the worst-case cost of a call before it is made. It is an estimate, never
	// a bill: the usage a call reports afterwards is.
	EstimatedInputTokens int
}

// SpendRecord describes a model call that was made.
type SpendRecord struct {
	Kitchen  string
	Agent    string
	Provider string
	Model    string
	Usage    TokenUsage
}

// ErrSpendRefused is what a SpendRefusedError matches with errors.Is.
var ErrSpendRefused = errors.New("spend refused by budget")

// Units a budget is measured in.
const (
	SpendUnitUSD    = "usd"
	SpendUnitTokens = "tokens"
)

// SpendRefusedError is a spend gate saying no because a budget is used up. A run that meets it
// pauses at its budget (scope "kitchen", "provider", ...) instead of failing.
type SpendRefusedError struct {
	// Scope names the budget: "kitchen", "provider", "agent", "api_key".
	Scope string
	// Reason is for people ("monthly budget for provider openai is used up").
	Reason string
	// Spent and Cap are in Unit (dollars when it is empty), when the budget is measured in them.
	Spent, Cap float64
	// Unit is SpendUnitUSD (or empty) or SpendUnitTokens.
	Unit string
	// Period is the budget's period ("daily", "weekly", "monthly", "lifetime"), when it has one.
	Period string
	// ResetsAt is when the period ends and the budget starts again, nil for a lifetime budget.
	ResetsAt *time.Time
}

// Dimension is the run-budget dimension this refusal is about (BudgetDimCost or BudgetDimTokens), so a
// run that pauses at it says what ran out.
func (e *SpendRefusedError) Dimension() string {
	if e.Unit == SpendUnitTokens {
		return BudgetDimTokens
	}
	return BudgetDimCost
}

func (e *SpendRefusedError) Error() string {
	if e.Cap > 0 && e.Unit == SpendUnitTokens {
		return fmt.Sprintf("%s: %.0f of %.0f tokens used (%s)", ErrSpendRefused, e.Spent, e.Cap, e.Reason)
	}
	if e.Cap > 0 {
		return fmt.Sprintf("%s: $%.2f of $%.2f spent (%s)", ErrSpendRefused, e.Spent, e.Cap, e.Reason)
	}
	return fmt.Sprintf("%s: %s", ErrSpendRefused, e.Reason)
}

func (e *SpendRefusedError) Is(target error) bool { return target == ErrSpendRefused }

// ErrRateLimited is what a RateLimitedError matches with errors.Is.
var ErrRateLimited = errors.New("rate limited")

// RateLimitedError is a traffic limit saying not yet: the call was not made, and the same call is
// expected to be admitted after RetryAfter. Unlike a spend refusal it is about how fast, not how much,
// so it clears by itself. It says nothing about the provider's health, so the router neither counts it
// as a provider failure nor retries it against the same provider.
type RateLimitedError struct {
	// Scope names what is limited: "kitchen", "agent", "api_key" or "provider".
	Scope string
	// Limit is the dimension that ran out: "requests_per_minute", "tokens_per_minute" or
	// "invocations_per_minute".
	Limit string
	// RetryAfter is how long until the call would fit.
	RetryAfter time.Duration
	// Reason is for people.
	Reason string
}

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("%s: %s (retry in %s)", ErrRateLimited, e.Reason, e.RetryAfter.Round(time.Millisecond))
}

func (e *RateLimitedError) Is(target error) bool { return target == ErrRateLimited }

// RetryAfterSeconds is RetryAfter as the whole seconds a Retry-After header carries: rounded up, and
// at least one, so a client never retries into the same wall.
func (e *RateLimitedError) RetryAfterSeconds() int {
	s := int((e.RetryAfter + time.Second - 1) / time.Second)
	if s < 1 {
		s = 1
	}
	return s
}
