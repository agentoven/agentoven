package refcheck_test

import (
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/agentoven/agentoven/control-plane/pkg/refcheck"
)

func TestDecodeNamesUnknownFieldsAtAnyDepthAndWrongTypes(t *testing.T) {
	cases := []struct {
		name, body string
		wantPath   string
		wantCode   string
		wantSugg   string
	}{
		{"top level typo", `{"name":"a","summry_model":"x"}`, "summry_model", refcheck.CodeUnknownField, "summary_model"},
		{"inside a list of objects", `{"name":"a","ingredients":[{"kind":"tool","nam":"x"}]}`, "ingredients[0].nam", refcheck.CodeUnknownField, "name"},
		{"inside a nested object", `{"name":"a","budget":{"max_cost":3}}`, "budget.max_cost", refcheck.CodeUnknownField, "max_cost_usd"},
		{"wrong type", `{"name":"a","max_turns":"ten"}`, "max_turns", refcheck.CodeWrongType, ""},
	}
	for _, c := range cases {
		var a models.Agent
		ps, err := refcheck.Decode([]byte(c.body), &a, refcheck.ServerFields)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		found := false
		for _, p := range ps {
			if p.Path == c.wantPath && p.Code == c.wantCode {
				found = true
				if c.wantSugg != "" && (len(p.Suggestions) == 0 || p.Suggestions[0] != c.wantSugg) {
					t.Errorf("%s: suggestions %v", c.name, p.Suggestions)
				}
			}
		}
		if !found {
			t.Errorf("%s: %+v", c.name, ps)
		}
	}
}

func TestDecodeAcceptsWhatTheServerSendsAndFreeFormConfig(t *testing.T) {
	var a models.Agent
	body := `{"name":"a","created_epoch":1,"created_at":"2026-01-01T00:00:00Z","ingredients":[{"kind":"model","name":"m","config":{"anything":{"goes":1}}}],"tags":{"k":"v"}}`
	if ps, err := refcheck.Decode([]byte(body), &a, refcheck.ServerFields); err != nil || len(ps) != 0 {
		t.Fatalf("%v %+v", err, ps)
	}
	if _, err := refcheck.Decode([]byte(`{nope`), &a); err == nil {
		t.Fatal("a body that is not JSON is an error, not a problem list")
	}
}

func TestAnEditAcceptsAnAgentSentBackWholeButNotATypo(t *testing.T) {
	var edit struct {
		models.AgentEdit
		Tags map[string]string `json:"tags"`
	}
	ps, err := refcheck.Decode([]byte(`{"id":"x","status":"ready","version":"0.1.0","created_at":"2026-01-01T00:00:00Z","description":"d","summary_modle":"m"}`), &edit, models.Agent{}, refcheck.ServerFields)
	if err != nil || len(ps) != 1 || ps[0].Path != "summary_modle" {
		t.Fatalf("%v %+v", err, ps)
	}
}

func TestIntroducedKeepsOnlyWhatTheWriteMadeWorse(t *testing.T) {
	old := refcheck.Problem{Path: "skills[0]", Code: refcheck.CodeNotFound, Kind: "skill", Value: "ghost", Severity: refcheck.SeverityError}
	moved := old
	moved.Path = "skills[3]" // the list was reordered: the same problem
	fresh := refcheck.Problem{Path: "skills[1]", Code: refcheck.CodeNotFound, Kind: "skill", Value: "new-ghost", Severity: refcheck.SeverityError}
	warn := refcheck.Problem{Path: "x", Code: refcheck.CodeNotUsable, Severity: refcheck.SeverityWarning, Value: "w"}
	got := refcheck.Introduced([]refcheck.Problem{old}, []refcheck.Problem{moved, fresh, warn})
	if len(got) != 1 || got[0].Value != "new-ghost" {
		t.Fatalf("%+v", got)
	}
	// Two of the same value before, three after: one is new.
	got = refcheck.Introduced([]refcheck.Problem{old, old}, []refcheck.Problem{old, old, old})
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestErrorMessageNamesTheFirstProblemAndTheCount(t *testing.T) {
	if refcheck.New([]refcheck.Problem{{Severity: refcheck.SeverityWarning, Message: "w"}}) != nil {
		t.Fatal("warnings do not make an error")
	}
	e := refcheck.New([]refcheck.Problem{{Severity: refcheck.SeverityError, Message: "first"}, {Severity: refcheck.SeverityError, Message: "second"}})
	if e == nil || !strings.Contains(e.Error(), "first") || !strings.Contains(e.Error(), "1 more") {
		t.Fatalf("%v", e)
	}
}
