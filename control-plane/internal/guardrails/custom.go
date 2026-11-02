package guardrails

import (
	"context"
	"fmt"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// Verdict actions a custom endpoint may answer with, weakest first. A rule's own action caps
// what the endpoint can do: a rule set to warn turns an endpoint's "block" into a warning, which
// is how a new endpoint is run in shadow mode before it is allowed to refuse anything.
var verdictRank = map[string]int{"allow": 0, "warn": 1, "redact": 2, "require_approval": 3, "block": 4}

var verdictNames = []string{"allow", "warn", "redact", "require_approval", "block"}

func actionRank(a models.GuardrailAction) int {
	switch a {
	case models.GuardrailActionWarn:
		return verdictRank["warn"]
	case models.GuardrailActionRedact, models.GuardrailActionMask:
		return verdictRank["redact"]
	case models.GuardrailActionRequireApproval:
		return verdictRank["require_approval"]
	}
	return verdictRank["block"]
}

// rewriteCustom asks the installed CustomGuardrailEvaluator about text. With none installed the
// rule passes: that is what custom has always done in the community edition.
//
// An endpoint that cannot be reached, or answers with something this does not understand, is
// handled by the rule's fail_open setting: closed by default (the text is blocked), open only
// when the rule says so. A shadow-mode rule (action warn) never blocks, even when it fails.
func (s *CommunityGuardrailService) rewriteCustom(ctx context.Context, g models.Guardrail, text string, base models.GuardrailResult) (models.GuardrailResult, string) {
	holder := s.custom.Load()
	if holder == nil {
		return base, text
	}
	stage := models.GuardrailStage(base.Stage)
	verdict, err := holder.ev.EvaluateCustom(ctx, g, text, stage)
	if err == nil {
		res, out, verr := applyVerdict(g, verdict, text, base)
		if verr == nil {
			return res, out
		}
		err = verr
	}
	failOpen, _ := g.Config["fail_open"].(bool)
	if failOpen || g.EffectiveAction() == models.GuardrailActionWarn {
		base.Message = "custom guardrail unavailable, text allowed because the rule fails open: " + err.Error()
		base.Action = models.GuardrailActionWarn
		return base, text
	}
	base.Passed = false
	base.Message = "custom guardrail unavailable, text refused because the rule fails closed: " + err.Error()
	return base, text
}

// applyVerdict turns an endpoint's answer into a result, within the rule's cap.
func applyVerdict(g models.Guardrail, v models.GuardrailVerdict, text string, base models.GuardrailResult) (models.GuardrailResult, string, error) {
	rank, ok := verdictRank[v.Action]
	if !ok {
		return base, text, fmt.Errorf("unknown action %q in the response", v.Action)
	}
	if v.Action == "redact" {
		if v.Text == "" && text != "" {
			return base, text, fmt.Errorf("a redact response must carry the replacement text")
		}
		if len(v.Text) > MaxScanBytes {
			return base, text, fmt.Errorf("the replacement text is too long")
		}
	}
	if cap := actionRank(g.Action); rank > cap {
		// Down to the strongest thing the rule allows. A cap of redact cannot be met by an
		// endpoint that sent no replacement text, so that becomes a warning.
		if cap == verdictRank["redact"] {
			cap = verdictRank["warn"]
		}
		v.Action = verdictNames[cap]
	}
	base.Message = v.Reason
	switch v.Action {
	case "allow":
		base.Message = ""
		return base, text, nil
	case "warn":
		base.Action = models.GuardrailActionWarn
		if base.Message == "" {
			base.Message = "custom guardrail warned"
		}
		return base, text, nil
	case "redact":
		base.Action = models.GuardrailActionRedact
		if v.Text != text {
			base.Redacted = 1
		}
		return base, v.Text, nil
	case "require_approval":
		base.Passed = false
		base.Action = models.GuardrailActionRequireApproval
		if base.Message == "" {
			base.Message = "custom guardrail requires approval"
		}
		return base, text, nil
	default: // block
		base.Passed = false
		base.Action = models.GuardrailActionBlock
		if base.Message == "" {
			base.Message = "blocked by custom guardrail"
		}
		return base, text, nil
	}
}
