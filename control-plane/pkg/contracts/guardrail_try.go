package contracts

import (
	"context"
	"fmt"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

const (
	// MaxGuardrailTryRules is the most rules one "test this text" request may carry.
	MaxGuardrailTryRules = 50
	// MaxGuardrailTryText is the longest text a test may carry. A test is a debugging aid; the limit
	// keeps it from being a way to make the server scan megabytes for anyone who can read the rules.
	MaxGuardrailTryText = 256 << 10
)

// GuardrailTryRequest asks what a set of rules would do with a piece of text.
type GuardrailTryRequest struct {
	Text  string                `json:"text"`
	Stage models.GuardrailStage `json:"stage"` // input (default), output or retrieval
	Rules []models.Guardrail    `json:"rules"`
}

// GuardrailTryResponse says what the rules would do, without doing it.
type GuardrailTryResponse struct {
	// Outcome is "pass", "redacted", "warned", "approval_required" or "blocked".
	Outcome          string `json:"outcome"`
	Passed           bool   `json:"passed"`
	ApprovalRequired bool   `json:"approval_required,omitempty"`
	Redacted         int    `json:"redacted"`
	Warned           int    `json:"warned"`
	// Text is the text that would continue: the input with every redaction applied.
	Text    string                   `json:"text"`
	Results []models.GuardrailResult `json:"results"`
}

// GuardrailOutcome names what an evaluation did, for a "test this text" box.
func GuardrailOutcome(e *models.GuardrailEvaluation) string {
	switch {
	case e.ApprovalRequired:
		return "approval_required"
	case !e.Passed:
		return "blocked"
	case e.Redacted > 0:
		return "redacted"
	case e.Warned > 0:
		return "warned"
	}
	return "pass"
}

// TryGuardrails validates the rules (the check a saved rule gets) and evaluates the text against
// them. Both editions' test routes use it, so a test answers the same everywhere. Nothing is
// stored; a validation failure is returned as an error naming the rule.
func TryGuardrails(ctx context.Context, svc StageGuardrailService, req GuardrailTryRequest) (*GuardrailTryResponse, error) {
	switch req.Stage {
	case "":
		req.Stage = models.GuardrailStageInput
	case models.GuardrailStageInput, models.GuardrailStageOutput, models.GuardrailStageRetrieval:
	default:
		return nil, fmt.Errorf("stage must be input, output or retrieval")
	}
	if len(req.Text) > MaxGuardrailTryText {
		return nil, fmt.Errorf("a test text is limited to %d bytes", MaxGuardrailTryText)
	}
	if len(req.Rules) == 0 || len(req.Rules) > MaxGuardrailTryRules {
		return nil, fmt.Errorf("send between 1 and %d rules", MaxGuardrailTryRules)
	}
	rules := append([]models.Guardrail(nil), req.Rules...)
	for i := range rules {
		if rules[i].ID == "" {
			rules[i].ID = fmt.Sprintf("rule-%d", i+1)
		}
		if err := svc.ValidateGuardrail(rules[i]); err != nil {
			return nil, fmt.Errorf("rule %q: %w", rules[i].ID, err)
		}
	}
	eval, err := svc.Evaluate(ctx, rules, req.Text, req.Stage)
	if err != nil {
		return nil, err
	}
	return &GuardrailTryResponse{
		Outcome: GuardrailOutcome(eval), Passed: eval.Passed, ApprovalRequired: eval.ApprovalRequired,
		Redacted: eval.Redacted, Warned: eval.Warned, Text: eval.Apply(req.Text), Results: eval.Results,
	}, nil
}
