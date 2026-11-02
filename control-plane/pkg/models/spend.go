package models

import (
	"errors"
	"fmt"
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

// SpendRefusedError is a spend gate saying no because a budget is used up. A run that meets it
// pauses at its budget (scope "kitchen", "provider", ...) instead of failing.
type SpendRefusedError struct {
	// Scope names the budget: "kitchen", "provider", ...
	Scope string
	// Reason is for people ("monthly budget for provider openai is used up").
	Reason string
	// Spent and Cap are in dollars, when the budget is measured in them.
	Spent, Cap float64
}

func (e *SpendRefusedError) Error() string {
	if e.Cap > 0 {
		return fmt.Sprintf("%s: $%.2f of $%.2f spent (%s)", ErrSpendRefused, e.Spent, e.Cap, e.Reason)
	}
	return fmt.Sprintf("%s: %s", ErrSpendRefused, e.Reason)
}

func (e *SpendRefusedError) Is(target error) bool { return target == ErrSpendRefused }
