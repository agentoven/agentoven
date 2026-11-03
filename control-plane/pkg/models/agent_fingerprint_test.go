package models

import (
	"reflect"
	"strings"
	"testing"
)

// agentBehaviourFields lists every Agent field the fingerprint counts, by JSON name. A field added to
// Agent must be put here (it shapes what the agent says or does) or in agentStateFields (with the
// reason it does not): the test below fails until someone has decided, because a field that is left
// out by accident is a behaviour change that keeps its version label (ADR-0043).
var agentBehaviourFields = map[string]bool{
	"description": true, "framework": true, "mode": true, "execution_mode": true, "runtime": true,
	"entrypoint": true, "repo_url": true, "repo_branch": true, "max_turns": true, "behavior": true,
	"context_budget": true, "summary_model": true, "summary_provider": true, "reasoning_strategy": true, "output_schema": true,
	"approval_tools": true, "subagents": true, "bot": true, "budget": true, "backend_endpoint": true,
	"skills": true, "model_provider": true, "model_name": true, "backup_provider": true,
	"backup_model": true, "guardrails": true, "ingredients": true, "resolved_config": true,
}

func jsonName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	if tag == "" || tag == "-" {
		return ""
	}
	return strings.Split(tag, ",")[0]
}

func TestEveryAgentFieldIsClassified(t *testing.T) {
	typ := reflect.TypeOf(Agent{})
	for i := 0; i < typ.NumField(); i++ {
		name := jsonName(typ.Field(i))
		if name == "" {
			continue
		}
		_, state := agentStateFields[name]
		if state == agentBehaviourFields[name] {
			t.Errorf("Agent field %q must be in exactly one of agentBehaviourFields (this test) and agentStateFields (agent_fingerprint.go)", name)
		}
	}
	for name := range agentBehaviourFields {
		found := false
		for i := 0; i < typ.NumField(); i++ {
			found = found || jsonName(typ.Field(i)) == name
		}
		if !found {
			t.Errorf("agentBehaviourFields names %q, which is not an Agent field any more", name)
		}
	}
}

// differently returns a value of f's type that is not its zero value.
func differently(t *testing.T, f reflect.Value) {
	t.Helper()
	switch f.Kind() {
	case reflect.String:
		f.SetString("changed")
	case reflect.Int, reflect.Int64:
		f.SetInt(7)
	case reflect.Bool:
		f.SetBool(true)
	case reflect.Slice:
		f.Set(reflect.MakeSlice(f.Type(), 1, 1))
		if f.Type().Elem().Kind() == reflect.String {
			f.Index(0).SetString("changed")
		}
	case reflect.Map:
		m := reflect.MakeMap(f.Type())
		m.SetMapIndex(reflect.ValueOf("k"), reflect.ValueOf("v"))
		f.Set(m)
	case reflect.Ptr:
		f.Set(reflect.New(f.Type().Elem()))
		if f.Type().Elem() == reflect.TypeOf(BotConfig{}) {
			f.Interface().(*BotConfig).StartURL = "https://changed.example"
		}
		if f.Type().Elem() == reflect.TypeOf(ResolvedIngredients{}) {
			f.Interface().(*ResolvedIngredients).Prompt = &ResolvedPrompt{Template: "changed"}
		}
		if f.Type().Elem() == reflect.TypeOf(RunBudget{}) {
			f.Interface().(*RunBudget).MaxCostUSD = 1
		}
	default:
		t.Fatalf("no way to change a %s field: teach this test about it", f.Kind())
	}
}

// Changing any behaviour field changes the fingerprint; changing any state field does not.
func TestBakedFingerprintCoversEveryBehaviourField(t *testing.T) {
	typ := reflect.TypeOf(Agent{})
	base := Agent{Name: "a", Mode: AgentModeManaged}
	baseFP := BakedFingerprint(&base)
	for i := 0; i < typ.NumField(); i++ {
		name := jsonName(typ.Field(i))
		if name == "" {
			continue
		}
		a := base
		f := reflect.ValueOf(&a).Elem().Field(i)
		if !f.CanSet() {
			continue
		}
		if f.Type() == reflect.TypeOf(a.CreatedAt) {
			continue // a time: bookkeeping, covered by name in agentStateFields
		}
		if name == "backend_endpoint" {
			a.Mode = AgentModeExternal // it counts only for an agent AgentOven does not run
			baseExternal := base
			baseExternal.Mode = AgentModeExternal
			differently(t, f)
			if BakedFingerprint(&a) == BakedFingerprint(&baseExternal) {
				t.Errorf("the backend endpoint of an external agent is behaviour and is missing from the fingerprint")
			}
			managed := base
			managed.BackendEndpoint = "http://127.0.0.1:9000"
			if BakedFingerprint(&managed) != baseFP {
				t.Errorf("the endpoint a managed agent's process happened to start on is state")
			}
			continue
		}
		differently(t, f)
		changed := BakedFingerprint(&a) != baseFP
		if agentBehaviourFields[name] && !changed {
			t.Errorf("changing %q does not change the fingerprint: a behaviour change would keep its version", name)
		}
		if !agentBehaviourFields[name] && changed {
			t.Errorf("changing %q changes the fingerprint but it is classified as state", name)
		}
	}
}

func TestBakedFingerprintTreatsEmptyAndAbsentAlike(t *testing.T) {
	a := Agent{Name: "a"}
	b := Agent{Name: "a", Guardrails: []Guardrail{}, Skills: []string{}, Ingredients: []Ingredient{}}
	if BakedFingerprint(&a) != BakedFingerprint(&b) {
		t.Fatal("an empty list and an absent one are the same configuration")
	}
	withZero := Agent{Name: "a", Ingredients: []Ingredient{{Name: "m", Config: map[string]interface{}{"temperature": 0.0}}}}
	without := Agent{Name: "a", Ingredients: []Ingredient{{Name: "m", Config: map[string]interface{}{}}}}
	if BakedFingerprint(&withZero) == BakedFingerprint(&without) {
		t.Fatal("a temperature of 0 is a value, not an unset one")
	}
}

// The same agent before and after a store fills in its defaults is the same configuration.
func TestBakedFingerprintIgnoresDefaultsAStoreFillsIn(t *testing.T) {
	bare := Agent{Name: "a"}
	filled := Agent{Name: "a", Runtime: RuntimeAgentOven, Mode: AgentModeManaged}
	if BakedFingerprint(&bare) != BakedFingerprint(&filled) {
		t.Fatal("an agent with no runtime or mode is agentoven and managed: that is not a change")
	}
	other := Agent{Name: "a", Runtime: RuntimeLangChain}
	if BakedFingerprint(&bare) == BakedFingerprint(&other) {
		t.Fatal("a different runtime is a change")
	}
}

func TestValidVersion(t *testing.T) {
	for _, ok := range []string{"0.1.0", "1.2.3", "10.0.0", "999999999.0.1"} {
		if !ValidVersion(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "1", "1.2", "1.2.3.4", "01.2.3", "-1.2.3", "+1.2.3", "1.2.3-beta", "9.9.9\nx", "../../etc", "<script>", " ", "1.2.3 ",
		"1.2.3\n", strings.Repeat("1", 5000) + ".0.0", "1000000000.0.0"} {
		if ValidVersion(bad) {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestDecideNamedVersion(t *testing.T) {
	baked := func(prompt string) *Agent {
		return &Agent{Name: "a", ResolvedConfig: &ResolvedIngredients{Prompt: &ResolvedPrompt{Template: prompt}}}
	}
	latest, same, changed := baked("one"), baked("one"), baked("two")
	stored := []string{"0.1.0", "0.2.0"}
	cases := []struct {
		name      string
		requested string
		current   string
		latest    *Agent
		next      *Agent
		isNew     bool
		err       string // "", "bad", "conflict"
	}{
		{"a malformed name", "9.9.9\nx", "0.2.0", latest, changed, false, "bad"},
		{"the current version, same configuration", "0.2.0", "0.2.0", latest, same, false, ""},
		{"the current version, other configuration", "0.2.0", "0.2.0", latest, changed, false, "conflict"},
		{"an older version that exists", "0.1.0", "0.2.0", latest, changed, false, "conflict"},
		{"an older version that does not exist", "0.0.9", "0.2.0", latest, changed, false, "conflict"},
		{"a newer version", "1.0.0", "0.2.0", latest, changed, true, ""},
		{"the first bake names the registered version", "0.1.0", "0.1.0", &Agent{Name: "a"}, changed, false, ""},
	}
	for _, c := range cases {
		isNew, err := DecideNamedVersion(c.requested, c.current, stored, c.latest, c.next)
		var ve *VersionError
		got := ""
		if err != nil {
			got = "bad"
			if e, ok := err.(*VersionError); ok {
				ve = e
				if ve.Conflict {
					got = "conflict"
				}
			}
		}
		if got != c.err || isNew != c.isNew {
			t.Errorf("%s: isNew=%v err=%v, want isNew=%v err=%q", c.name, isNew, err, c.isNew, c.err)
		}
	}
}
