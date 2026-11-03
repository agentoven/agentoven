package refcheck

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Options say what the configuration is being checked for.
type Options struct {
	// Bake is set when the configuration is about to run (a bake, a re-cook, a run of a recipe). A draft may
	// be incomplete, so on a write the gaps that only matter at run time (a model provider with no model,
	// a managed agent with no model) are warnings; at a bake they are errors.
	Bake bool
	// Environment is the environment a run was asked for, checked against the kitchen's.
	Environment string
}

// checker collects problems.
type checker struct {
	c   *Catalog
	o   Options
	out []Problem
}

func (ck *checker) add(p Problem) {
	if p.Severity == "" {
		p.Severity = SeverityError
	}
	ck.out = append(ck.out, p)
}

// gap is a problem that only matters when the configuration runs.
func (ck *checker) gapSeverity() string {
	if ck.o.Bake {
		return SeverityError
	}
	return SeverityWarning
}

func (ck *checker) required(path, what string) {
	ck.add(Problem{Path: path, Code: CodeRequired, Message: fmt.Sprintf("%s: %s", path, what)})
}

func (ck *checker) invalid(path, value, message string, suggestions []string) {
	ck.add(Problem{Path: path, Code: CodeInvalidValue, Value: value, Message: fmt.Sprintf("%s: %s", path, message), Suggestions: suggestions})
}

func (ck *checker) wrongType(path, want string, got interface{}) {
	ck.add(Problem{Path: path, Code: CodeWrongType, Value: describe(got), Message: fmt.Sprintf("%s: must be %s, not %s", path, want, jsonTypeName(got))})
}

func (ck *checker) outOfRange(path string, got float64, lo, hi float64, whole bool) {
	what := "a number"
	if whole {
		what = "a whole number"
	}
	ck.add(Problem{Path: path, Code: CodeOutOfRange, Value: trimFloat(got), Message: fmt.Sprintf("%s: %s must be %s from %s to %s", path, trimFloat(got), what, trimFloat(lo), trimFloat(hi))})
}

// notFound reports a value that is not among the candidates, with the nearest ones.
func (ck *checker) notFound(path, kind, value, within string, candidates []string) {
	sugg := Nearest(value, candidates, maxSuggestions)
	what := "a " + kind + " in this kitchen"
	if kind == KindModel && within != "" {
		what = "a model of provider " + quote(within)
	} else if kind == KindModel {
		what = "a model of any provider in this kitchen"
	} else if kind == KindAgent {
		what = "an agent in this kitchen"
	} else if kind == KindStep {
		what = "a step of this recipe"
	} else if kind == KindIngredientKind {
		what = "an ingredient kind"
	} else if kind == KindBackend {
		what = "a vector store backend"
	} else if kind == KindStepKind {
		what = "a step kind"
	} else if kind == KindEmbedding || kind == KindVectorStore {
		what = "one of this agent's " + kind + " ingredients"
	} else if kind == KindEnvironment {
		what = "an environment of this kitchen"
	}
	msg := fmt.Sprintf("%s: %s is not %s", path, quote(value), what)
	switch {
	case len(sugg) > 0:
		msg += fmt.Sprintf(" (did you mean %s?)", joinOr(sugg))
	case len(candidates) == 0:
		msg += " (there are none)"
	case len(candidates) <= 8:
		msg += " (available: " + joinQuoted(candidates) + ")"
	default:
		msg += fmt.Sprintf(" (%d available)", len(candidates))
	}
	ck.add(Problem{Path: path, Code: CodeNotFound, Kind: kind, Value: value, Within: within, Message: msg, Suggestions: sugg})
}

func (ck *checker) notUsable(path, kind, value, why, severity string) {
	ck.add(Problem{Path: path, Code: CodeNotUsable, Severity: severity, Kind: kind, Value: value,
		Message: fmt.Sprintf("%s: %s %s %s", path, kind, quote(value), why)})
}

func joinOr(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = quote(s)
	}
	if len(q) == 1 {
		return q[0]
	}
	return strings.Join(q[:len(q)-1], ", ") + " or " + q[len(q)-1]
}

// provider checks a provider name and returns it when it exists.
func (ck *checker) provider(path, name string) (Provider, bool) {
	p, ok := ck.c.provider(name)
	if !ok {
		ck.notFound(path, KindProvider, name, "", ck.c.providerNames())
		return Provider{}, false
	}
	if p.Healthy != nil && !*p.Healthy {
		ck.notUsable(path, KindProvider, name, "failed its last connectivity test", SeverityWarning)
	}
	return p, true
}

// model checks a model id against a provider that exists.
func (ck *checker) model(path string, p Provider, model string) {
	switch {
	case p.Has(model):
	case !p.Open():
		ck.notFound(path, KindModel, model, p.Name, sorted(p.Models))
	case len(p.Models) > 0:
		ck.add(Problem{Path: path, Code: CodeUnlisted, Severity: SeverityWarning, Kind: KindModel, Value: model, Within: p.Name,
			Message: fmt.Sprintf("%s: %s is not in provider %s's list; accepted because a %s provider serves models its list does not name", path, quote(model), quote(p.Name), p.Kind)})
	}
}

// modelPair checks a provider/model pair. providerRequired says whether a model without a provider is a
// problem (the primary model) or is looked up among every provider's models (the summarizer, whose provider
// is optional). modelRequired is whether a provider without a model is a gap.
func (ck *checker) modelPair(providerPath, modelPath, provider, model string, providerRequired, modelRequired bool) {
	if provider == "" && model == "" {
		return
	}
	if provider == "" {
		if providerRequired {
			// Nothing reads a model name without its provider (an external agent may carry one), so this
			// is a note, not a refusal; a managed agent with no model at all is refused at its bake.
			ck.add(Problem{Path: providerPath, Code: CodeRequired, Severity: SeverityWarning,
				Message: fmt.Sprintf("%s: %s has no effect without the provider that serves it", providerPath, modelPath)})
			return
		}
		for _, p := range ck.c.Providers {
			if p.Has(model) {
				return
			}
		}
		for _, p := range ck.c.Providers {
			if p.Open() {
				return // some provider serves any id; the model cannot be shown absent
			}
		}
		ck.notFound(modelPath, KindModel, model, "", ck.c.allModels())
		return
	}
	p, ok := ck.provider(providerPath, provider)
	if !ok {
		return
	}
	if model == "" {
		if modelRequired {
			ck.add(Problem{Path: modelPath, Code: CodeRequired, Severity: ck.gapSeverity(), Within: provider,
				Message: fmt.Sprintf("%s: provider %s is set and needs a model", modelPath, quote(provider))})
		}
		return
	}
	ck.model(modelPath, p, model)
}

// tool checks a tool name. needs is the capability the use requires ("tool" to call it, "notify" to
// send to it); required says whether a tool that exists but cannot be used is an error or a warning.
func (ck *checker) tool(path, name, needs string, required bool) {
	t, ok := ck.c.tool(name)
	if !ok {
		ck.notFound(path, KindTool, name, "", ck.c.toolNames(needs))
		return
	}
	sev := SeverityWarning
	if required {
		sev = SeverityError
	}
	switch {
	case !t.Enabled:
		ck.notUsable(path, KindTool, name, "is disabled", sev)
	case needs != "" && !hasCap(t.Capabilities, needs):
		ck.notUsable(path, KindTool, name, fmt.Sprintf("does not have the %q capability", needs), sev)
	}
}

func (ck *checker) skill(path, name string, required bool) {
	s, ok := ck.c.skill(name)
	if !ok {
		ck.notFound(path, KindSkill, name, "", ck.c.skillNames())
		return
	}
	if s.Status != "accepted" {
		sev := SeverityWarning
		if required {
			sev = SeverityError
		}
		ck.notUsable(path, KindSkill, name, fmt.Sprintf("is not accepted (status: %s)", s.Status), sev)
	}
}

// agentRef checks an agent a subagent list or a recipe step names.
func (ck *checker) agentRef(path, name string) {
	a, ok := ck.c.agent(name)
	if !ok {
		ck.notFound(path, KindAgent, name, "", ck.c.agentNames())
		return
	}
	switch a.Status {
	case "retired":
		ck.notUsable(path, KindAgent, name, "is retired: register it again to bring it back", SeverityError)
	case "burnt", "cooled":
		ck.notUsable(path, KindAgent, name, fmt.Sprintf("is not running (status: %s)", a.Status), SeverityWarning)
	}
}

func (ck *checker) fromSet(path, kind, name string, set []string) {
	if !contains(set, name) {
		ck.notFound(path, kind, name, "", sorted(set))
	}
}

// dupes reports the second and later occurrence of each value of a list.
func (ck *checker) dupes(path string, values []string) {
	seen := map[string]bool{}
	for i, v := range values {
		if seen[v] {
			ck.add(Problem{Path: fmt.Sprintf("%s[%d]", path, i), Code: CodeDuplicate, Value: v, Message: fmt.Sprintf("%s[%d]: %s is listed more than once", path, i, quote(v))})
		}
		seen[v] = true
	}
}

// enum checks a value against a fixed set. An empty value is the default and passes.
func (ck *checker) enum(path, value string, allowed []string) {
	if value == "" || contains(allowed, value) {
		return
	}
	sugg := Nearest(value, allowed, maxSuggestions)
	var msg string
	if len(sugg) > 0 {
		msg = fmt.Sprintf("%s is not valid (did you mean %s?)", quote(value), joinOr(sugg))
	} else {
		msg = fmt.Sprintf("%s is not valid: must be one of %s", quote(value), joinQuoted(allowed))
	}
	ck.invalid(path, value, msg, sugg)
}

// rangeInt checks an integer setting.
func (ck *checker) rangeInt(path string, v int64, lo, hi int64) {
	if v < lo || v > hi {
		ck.outOfRange(path, float64(v), float64(lo), float64(hi), true)
	}
}

// ── values read from a config map (JSON: numbers arrive as float64) ──

func num(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func str(m map[string]interface{}, key string) string {
	s, _ := m[key].(string)
	return s
}

// cfgString reads a string setting, reporting a value of another type.
func (ck *checker) cfgString(path string, m map[string]interface{}, key string) (string, bool) {
	v, present := m[key]
	if !present || v == nil {
		return "", false
	}
	s, ok := v.(string)
	if !ok {
		ck.wrongType(path+"."+key, "a string", v)
		return "", false
	}
	return s, true
}

// cfgInt reads a whole-number setting within [lo, hi]. ok is false when it is absent or wrong.
func (ck *checker) cfgInt(path string, m map[string]interface{}, key string, lo, hi int64) (int64, bool) {
	v, present := m[key]
	if !present || v == nil {
		return 0, false
	}
	f, ok := num(v)
	if !ok {
		ck.wrongType(path+"."+key, "a whole number", v)
		return 0, false
	}
	if f != float64(int64(f)) {
		ck.wrongType(path+"."+key, "a whole number", v)
		return 0, false
	}
	if int64(f) < lo || int64(f) > hi {
		ck.outOfRange(path+"."+key, f, float64(lo), float64(hi), true)
		return 0, false
	}
	return int64(f), true
}

func (ck *checker) cfgFloat(path string, m map[string]interface{}, key string, lo, hi float64) (float64, bool) {
	v, present := m[key]
	if !present || v == nil {
		return 0, false
	}
	f, ok := num(v)
	if !ok {
		ck.wrongType(path+"."+key, "a number", v)
		return 0, false
	}
	if f < lo || f > hi {
		ck.outOfRange(path+"."+key, f, lo, hi, false)
		return 0, false
	}
	return f, true
}

func describe(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	if len(b) > 60 {
		return string(b[:57]) + "..."
	}
	return string(b)
}

func jsonTypeName(v interface{}) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case map[string]interface{}:
		return "an object"
	case []interface{}:
		return "a list"
	}
	if _, ok := num(v); ok {
		return "a number"
	}
	return "another type"
}

func trimFloat(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%g", f)
}
