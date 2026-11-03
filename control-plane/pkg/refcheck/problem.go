// Package refcheck checks that what an agent or a recipe names exists in the kitchen that holds it.
//
// An agent's configuration is full of names: the provider and model it runs on, the model that
// summarizes its history, the tools, prompts and skills it uses, the agents it may delegate to. A recipe
// names agents, steps, channels, credentials, approver roles. A typo in any of them used to surface much
// later (at bake, or on the first run), as a string the operator had to decode. This package reads such a
// configuration against a Catalog (a snapshot of what the kitchen has) and returns every problem at once,
// each with the field it is in, the value that is wrong and the nearest values that are right.
//
// It is pure: no store, no network, no clock. The server builds the Catalog from the kitchen's store and
// refuses a write on the errors; the SDKs and the CLI build one from the API and run the same checks
// before sending (the server stays the authority). The cases in sdk/fixtures/refcheck-cases.json pin the
// three implementations to one behaviour.
package refcheck

import (
	"fmt"
	"strings"
)

// Severity of a problem. Only errors refuse a write or a bake; a warning says something a person should
// see (a disabled optional tool, a provider whose last test failed, a model id the provider does not list
// but whose kind accepts any).
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// Codes say what is wrong. They are stable: clients switch on them.
const (
	// CodeNotFound: the thing named does not exist in this kitchen (another kitchen's counts as absent).
	CodeNotFound = "not_found"
	// CodeNotUsable: it exists but cannot be used as it is (a disabled tool, a skill nobody approved, a
	// retired agent).
	CodeNotUsable = "not_usable"
	// CodeUnlisted: a model id the provider's list does not hold, accepted because the provider's kind
	// serves models outside its list. Always a warning.
	CodeUnlisted = "unlisted"
	// CodeUnknownField: a field the API has no such name for.
	CodeUnknownField = "unknown_field"
	// CodeWrongType: a value of the wrong JSON type.
	CodeWrongType = "wrong_type"
	// CodeOutOfRange: a number outside what the setting allows.
	CodeOutOfRange = "out_of_range"
	// CodeDuplicate: the same thing twice where it can be given once.
	CodeDuplicate = "duplicate"
	// CodeRequired: a value the setting needs and does not have.
	CodeRequired = "required"
	// CodeInvalidValue: a value outside the fixed set the setting accepts.
	CodeInvalidValue = "invalid_value"
)

// Kinds say what a reference should have pointed at.
const (
	KindProvider       = "provider"
	KindModel          = "model"
	KindPrompt         = "prompt"
	KindTool           = "tool"
	KindSkill          = "skill"
	KindAgent          = "agent"
	KindRecipe         = "recipe"
	KindScenario       = "scenario"
	KindChannel        = "channel"
	KindEnvironment    = "environment"
	KindCredential     = "credential"
	KindRole           = "role"
	KindStep           = "step"
	KindIngredientKind = "ingredient_kind"
	KindStepKind       = "step_kind"
	KindEmbedding      = "embedding"
	KindVectorStore    = "vectorstore"
	KindBackend        = "vectorstore_backend"
)

// Problem is one thing wrong with a configuration.
type Problem struct {
	// Path is the field, in the JSON the API takes: "summary_model", "ingredients[2].config.provider",
	// "steps[1].agent_ref".
	Path     string `json:"path"`
	Code     string `json:"code"`
	Severity string `json:"severity"`
	// Kind is what the value should have named (empty for problems that are not references).
	Kind string `json:"kind,omitempty"`
	// Value is what was given, as text.
	Value string `json:"value,omitempty"`
	// Within is what Value should have been found in: the provider whose model list was searched.
	Within string `json:"within,omitempty"`
	// Message is the sentence for a person.
	Message string `json:"message"`
	// Suggestions are the nearest valid values, nearest first (at most three).
	Suggestions []string `json:"suggestions,omitempty"`
}

// Blocking reports whether the problem refuses a write.
func (p Problem) Blocking() bool { return p.Severity != SeverityWarning }

// Error is a list of problems that refused a write or a bake. It is the one error every caller returns
// for a configuration that names things the kitchen does not have, so every route answers it the same way
// (HTTP 422 with the list).
type Error struct{ Problems []Problem }

func (e *Error) Error() string {
	if e == nil || len(e.Problems) == 0 {
		return "the configuration is not valid"
	}
	if len(e.Problems) == 1 {
		return e.Problems[0].Message
	}
	return fmt.Sprintf("%s (and %d more)", e.Problems[0].Message, len(e.Problems)-1)
}

// Split separates the problems that refuse a write from the warnings.
func Split(ps []Problem) (errs, warns []Problem) {
	for _, p := range ps {
		if p.Blocking() {
			errs = append(errs, p)
		} else {
			warns = append(warns, p)
		}
	}
	return errs, warns
}

// New returns the error for the blocking problems in ps, or nil when there are none.
func New(ps []Problem) *Error {
	errs, _ := Split(ps)
	if len(errs) == 0 {
		return nil
	}
	return &Error{Problems: errs}
}

// Introduced returns the blocking problems of after that were not already in before. A write that does
// not make anything worse is not refused for what the configuration already held: an agent saved before
// the checks existed keeps working, and a person can still change its description while the dashboard
// shows what to fix. A bake takes no such leave. Two problems are the same when they agree on code, kind and
// value, wherever in the list they sit (an ingredient that moved is not a new problem).
func Introduced(before, after []Problem) []Problem {
	held := map[string]int{}
	for _, p := range before {
		if p.Blocking() {
			held[sameKey(p)]++
		}
	}
	var out []Problem
	for _, p := range after {
		if !p.Blocking() {
			continue
		}
		if k := sameKey(p); held[k] > 0 {
			held[k]--
			continue
		}
		out = append(out, p)
	}
	return out
}

func sameKey(p Problem) string {
	return p.Code + "\x00" + p.Kind + "\x00" + p.Value + "\x00" + p.Within
}

// quote renders a value for a message.
func quote(s string) string { return fmt.Sprintf("%q", s) }

func joinQuoted(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = quote(s)
	}
	return strings.Join(q, ", ")
}
