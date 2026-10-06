package guardrails

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func rule(id string, kind models.GuardrailKind, stage models.GuardrailStage, enabled, overridable bool) models.Guardrail {
	return models.Guardrail{ID: id, Kind: kind, Stage: stage, Enabled: enabled, Overridable: overridable}
}

func ids(gs []models.Guardrail) []string {
	out := make([]string, len(gs))
	for i, g := range gs {
		out[i] = g.ID
	}
	return out
}

func same(t *testing.T, got []models.Guardrail, want ...string) {
	t.Helper()
	g := ids(got)
	if len(g) != len(want) {
		t.Fatalf("got %v, want %v", g, want)
	}
	for i := range want {
		if g[i] != want[i] {
			t.Fatalf("got %v, want %v", g, want)
		}
	}
}

const (
	cf  = models.GuardrailContentFilter
	pii = models.GuardrailPIIDetection
	in  = models.GuardrailStageInput
)

func TestEveryEnabledWorkspaceRuleAppliesToAnAgentThatNeverAttachedIt(t *testing.T) {
	ws := []models.Guardrail{rule("ws-cf", cf, in, true, false), rule("ws-pii", pii, in, true, true)}
	same(t, MergeWithWorkspace(ws, nil, nil), "ws-cf", "ws-pii") // Overridable no longer matters
}

func TestAnAgentRuleIsAppliedOnTopOfAGlobalRuleOfTheSameKindAndStage(t *testing.T) {
	ws := []models.Guardrail{rule("ws-cf", cf, in, true, false)}
	own := []models.Guardrail{rule("a-cf", cf, in, true, false)}
	same(t, MergeWithWorkspace(ws, own, nil), "ws-cf", "a-cf") // both run, the global one first
}

func TestNothingAnAgentDefinesCanSwitchOffOrReplaceAGlobalRule(t *testing.T) {
	ws := []models.Guardrail{rule("ws-cf", cf, in, true, true)} // even one marked overridable
	for name, own := range map[string][]models.Guardrail{
		"disabled rule of the same kind": {rule("a-cf", cf, in, false, false)},
		"enabled rule of the same kind":  {rule("a-cf", cf, in, true, false)},
	} {
		got := ids(MergeWithWorkspace(ws, own, nil))
		if len(got) == 0 || got[0] != "ws-cf" {
			t.Errorf("%s: the global rule must still be first and present, got %v", name, got)
		}
	}
}

func TestAnApprovedExceptionLiftsAGlobalRuleForThatAgentOnly(t *testing.T) {
	ws := []models.Guardrail{rule("ws-cf", cf, in, true, false)}
	own := []models.Guardrail{rule("a-cf", cf, in, true, false)}
	ex := []models.WorkspaceGuardrailException{{GuardrailID: "ws-cf", AgentName: "a"}}
	same(t, MergeWithWorkspace(ws, own, ex), "a-cf") // the agent keeps its own rule
	same(t, MergeWithWorkspace(ws, nil, ex))
}

func TestAnExpiredExceptionDoesNothing(t *testing.T) {
	ws := []models.Guardrail{rule("ws-cf", cf, in, true, false)}
	ex := []models.WorkspaceGuardrailException{{GuardrailID: "ws-cf", ExpiresAt: time.Now().Add(-time.Hour)}}
	same(t, MergeWithWorkspace(ws, nil, ex), "ws-cf")
}

func TestADisabledWorkspaceRuleAppliesToNobodyAndSuppressesNothing(t *testing.T) {
	ws := []models.Guardrail{rule("ws-cf", cf, in, false, false), rule("ws-pii", pii, in, false, true)}
	own := []models.Guardrail{rule("a-cf", cf, in, true, false)}
	same(t, MergeWithWorkspace(ws, own, nil), "a-cf")
	same(t, MergeWithWorkspace(ws, nil, nil))
}

type fakeSource struct {
	rules []models.Guardrail
	ex    []models.WorkspaceGuardrailException
	err   error
	exErr error
	asked []string
}

func (f *fakeSource) WorkspaceGuardrails(_ context.Context, kitchen string) ([]models.Guardrail, error) {
	f.asked = append(f.asked, "rules:"+kitchen)
	return f.rules, f.err
}
func (f *fakeSource) WorkspaceGuardrailExceptions(_ context.Context, kitchen, agent string) ([]models.WorkspaceGuardrailException, error) {
	f.asked = append(f.asked, "ex:"+kitchen+"/"+agent)
	return f.ex, f.exErr
}

func TestEffectiveWithNoSourceIsTheAgentsOwnRules(t *testing.T) {
	own := []models.Guardrail{rule("a", cf, in, true, false)}
	got, err := Effective(context.Background(), nil, "k", "a", own)
	if err != nil {
		t.Fatal(err)
	}
	same(t, got, "a")
}

func validCF(id string) models.Guardrail {
	g := rule(id, cf, in, true, false)
	g.Config = map[string]interface{}{"blocked_words": []interface{}{"x"}}
	return g
}

func TestEffectiveAppliesTheKitchensRulesOnTopOfTheAgentsAndAsksForThatAgentsExceptions(t *testing.T) {
	src := &fakeSource{rules: []models.Guardrail{validCF("ws-cf")}, ex: []models.WorkspaceGuardrailException{{GuardrailID: "none"}}}
	got, err := Effective(context.Background(), src, "kitchen1", "agent1", []models.Guardrail{rule("a-x", models.GuardrailMaxLength, in, true, false)})
	if err != nil {
		t.Fatal(err)
	}
	same(t, got, "ws-cf", "a-x")
	if len(src.asked) != 2 || src.asked[1] != "ex:kitchen1/agent1" {
		t.Fatalf("must ask for this agent's exceptions in this kitchen, asked %v", src.asked)
	}
}

func TestEffectiveSkipsTheExceptionLookupWhenThereAreNoWorkspaceRules(t *testing.T) {
	src := &fakeSource{}
	own := []models.Guardrail{rule("a", cf, in, true, false)}
	got, err := Effective(context.Background(), src, "k", "a", own)
	if err != nil {
		t.Fatal(err)
	}
	same(t, got, "a")
	if len(src.asked) != 1 {
		t.Fatalf("no workspace rules means no second query, asked %v", src.asked)
	}
}

func requirePolicyError(t *testing.T, got []models.Guardrail, err error) {
	t.Helper()
	var pe *PolicyError
	if !errors.As(err, &pe) {
		t.Fatalf("expected a *PolicyError, got %v", err)
	}
	if got != nil {
		t.Fatalf("no rules may be returned alongside the failure (the agent must not run), got %v", ids(got))
	}
}

func TestEffectiveFailsClosedWhenTheWorkspaceRulesCannotBeRead(t *testing.T) {
	src := &fakeSource{err: errors.New("db down")}
	got, err := Effective(context.Background(), src, "k", "a", []models.Guardrail{rule("a", cf, in, true, false)})
	requirePolicyError(t, got, err)
}

func TestEffectiveFailsClosedWhenTheExceptionsCannotBeRead(t *testing.T) {
	src := &fakeSource{rules: []models.Guardrail{validCF("ws-cf")}, exErr: errors.New("db down")}
	got, err := Effective(context.Background(), src, "k", "a", nil)
	requirePolicyError(t, got, err)
}

func TestEffectiveFailsClosedOnAnEnabledWorkspaceRuleThatCannotBeApplied(t *testing.T) {
	bad := map[string]models.Guardrail{
		"unknown kind":             rule("r", "telepathy", in, true, false),
		"unknown stage":            func() models.Guardrail { g := validCF("r"); g.Stage = "sideways"; return g }(),
		"content filter, no words": rule("r", cf, in, true, false),
		"invalid regex": func() models.Guardrail {
			g := rule("r", models.GuardrailRegexFilter, in, true, false)
			g.Config = map[string]interface{}{"pattern": "("}
			return g
		}(),
		"regex, no pattern": rule("r", models.GuardrailRegexFilter, in, true, false),
		"max length of zero": func() models.Guardrail {
			g := rule("r", models.GuardrailMaxLength, in, true, false)
			g.Config = map[string]interface{}{"max_characters": 0}
			return g
		}(),
		"llamaguard, no endpoint": rule("r", models.GuardrailLlamaGuard, in, true, false),
		"unknown PII pattern": func() models.Guardrail {
			g := rule("r", pii, in, true, false)
			g.Config = map[string]interface{}{"patterns": []interface{}{"passport"}}
			return g
		}(),
		"topic rule with no topics": rule("r", models.GuardrailTopicRestriction, in, true, false),
	}
	for name, r := range bad {
		src := &fakeSource{rules: []models.Guardrail{r}}
		got, err := Effective(context.Background(), src, "k", "a", []models.Guardrail{rule("a", cf, in, true, false)})
		if err == nil {
			t.Errorf("%s: an enabled global rule that cannot work must stop the agent", name)
			continue
		}
		requirePolicyError(t, got, err)
	}
}

func TestADisabledWorkspaceRuleThatCannotBeAppliedIsIgnored(t *testing.T) {
	src := &fakeSource{rules: []models.Guardrail{rule("r", "telepathy", in, false, false)}}
	got, err := Effective(context.Background(), src, "k", "a", []models.Guardrail{rule("a", cf, in, true, false)})
	if err != nil {
		t.Fatalf("a disabled rule is not applied, so it cannot stop anyone: %v", err)
	}
	same(t, got, "a")
}

func TestWellFormedRulesOfEveryKindValidate(t *testing.T) {
	ok := []models.Guardrail{
		validCF("a"),
		func() models.Guardrail {
			g := rule("b", pii, in, true, false)
			g.Config = map[string]interface{}{"patterns": []interface{}{"ssn"}}
			return g
		}(),
		rule("c", pii, in, true, false), // no list: all built-in patterns
		func() models.Guardrail {
			g := rule("d", models.GuardrailTopicRestriction, in, true, false)
			g.Config = map[string]interface{}{"allowed_topics": []interface{}{"x"}}
			return g
		}(),
		func() models.Guardrail {
			g := rule("e", models.GuardrailMaxLength, in, true, false)
			g.Config = map[string]interface{}{"max_words": float64(50)}
			return g
		}(),
		func() models.Guardrail {
			g := rule("f", models.GuardrailRegexFilter, in, true, false)
			g.Config = map[string]interface{}{"pattern": `\bSSN\b`}
			return g
		}(),
		rule("g", models.GuardrailPromptInjection, in, true, false),
		rule("h", models.GuardrailCustom, in, true, false),
		func() models.Guardrail {
			g := rule("i", models.GuardrailLlamaGuard, in, true, false)
			g.Config = map[string]interface{}{"endpoint": "http://x"}
			return g
		}(),
	}
	for _, g := range ok {
		if err := Validate(g); err != nil {
			t.Errorf("%s (%s) should be valid: %v", g.ID, g.Kind, err)
		}
	}
}
