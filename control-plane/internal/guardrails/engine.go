package guardrails

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// Bounds. Go's regexp is RE2: a match takes time linear in the text and the pattern, so no
// pattern can backtrack catastrophically and a user-supplied regex is safe to run. What an
// attacker can still do is send a huge text or define a huge number of rules, so both are
// capped, and a text over the cap is refused (fail closed) rather than half-scanned.
const (
	// MaxScanBytes is the longest text a span-finding rule scans. Go's regexp scans tens of megabytes
	// a second, and a rule may run a couple of dozen patterns, so this keeps one evaluation to well
	// under a second; 1 MiB is a quarter of a million tokens.
	MaxScanBytes = 1 << 20
	// MaxRules is the most enabled rules evaluated for one piece of text.
	MaxRules = 128
	// MaxPatternBytes is the longest regex or word list a rule may carry.
	MaxPatternBytes = 4096
	// MaxListEntries is the longest list in a rule (words, allowlist).
	MaxListEntries = 1000
)

var labelPattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,32}$`)

// CommunityGuardrailService is the OSS implementation of contracts.GuardrailService and
// contracts.StageGuardrailService. It evaluates guardrails using built-in heuristics and
// regex patterns, and hands rules of kind custom to the CustomGuardrailEvaluator if Pro
// installed one.
type CommunityGuardrailService struct {
	custom atomic.Pointer[customHolder]
}

type customHolder struct {
	ev contracts.CustomGuardrailEvaluator
}

var (
	_ contracts.GuardrailService      = (*CommunityGuardrailService)(nil)
	_ contracts.StageGuardrailService = (*CommunityGuardrailService)(nil)
)

// SetCustom installs the evaluator for rules of kind custom. Pass nil to remove it.
func (s *CommunityGuardrailService) SetCustom(ev contracts.CustomGuardrailEvaluator) {
	if ev == nil {
		s.custom.Store(nil)
		return
	}
	s.custom.Store(&customHolder{ev})
}

// EvaluateInput runs input-stage guardrails against the user message.
func (s *CommunityGuardrailService) EvaluateInput(ctx context.Context, guardrails []models.Guardrail, message string) (*models.GuardrailEvaluation, error) {
	return s.Evaluate(ctx, guardrails, message, models.GuardrailStageInput)
}

// EvaluateOutput runs output-stage guardrails against the model response.
func (s *CommunityGuardrailService) EvaluateOutput(ctx context.Context, guardrails []models.Guardrail, response string) (*models.GuardrailEvaluation, error) {
	return s.Evaluate(ctx, guardrails, response, models.GuardrailStageOutput)
}

// ValidateGuardrail implements contracts.StageGuardrailService.
func (s *CommunityGuardrailService) ValidateGuardrail(g models.Guardrail) error { return Validate(g) }

// appliesToStage reports whether a rule written for ruleStage runs at the stage being evaluated.
// A rule with no stage runs on input and output, as it always did; retrieval is only for rules
// that name it (retrieval or all), so rules written before chunks were guarded do not start
// dropping them.
func appliesToStage(ruleStage models.GuardrailStage, stage models.GuardrailStage) bool {
	if stage == models.GuardrailStageRetrieval {
		return ruleStage == models.GuardrailStageRetrieval || ruleStage == models.GuardrailStageAll
	}
	switch ruleStage {
	case models.GuardrailStageBoth, models.GuardrailStageAll:
		return true
	case models.GuardrailStageInput:
		return stage == models.GuardrailStageInput
	case models.GuardrailStageOutput:
		return stage == models.GuardrailStageOutput
	case models.GuardrailStageRetrieval:
		return false // only chunks
	default:
		return true
	}
}

// Evaluate runs every enabled rule that applies to stage on text.
//
// Rules that judge (block, warn, require_approval) all see the original text, so a rule that
// redacts cannot hide a match from a rule that blocks. Rules that rewrite (redact, mask and
// custom endpoints) then run in order, each on the output of the one before, and the result is
// the text that continues (GuardrailEvaluation.Apply). A custom endpoint therefore sees text
// the local detectors already cleaned, which is also less personal data sent to a third party.
func (s *CommunityGuardrailService) Evaluate(ctx context.Context, guardrails []models.Guardrail, text string, stage models.GuardrailStage) (*models.GuardrailEvaluation, error) {
	stageName := string(stage)
	eval := &models.GuardrailEvaluation{Passed: true, Results: make([]models.GuardrailResult, 0)}

	var rules []models.Guardrail
	for _, g := range guardrails {
		if g.Enabled && appliesToStage(g.Stage, stage) {
			rules = append(rules, g)
		}
	}
	if len(rules) > MaxRules {
		// Evaluating only some of the rules would be quietly skipping the rest.
		eval.Passed = false
		eval.Results = append(eval.Results, models.GuardrailResult{
			Passed: false, Stage: stageName, Action: models.GuardrailActionBlock,
			Message: fmt.Sprintf("%d guardrail rules apply, more than the %d that can be evaluated", len(rules), MaxRules),
		})
		return eval, nil
	}

	results := make([]models.GuardrailResult, len(rules))
	var rewrites, customs []int
	for i, g := range rules {
		switch {
		case g.Kind == models.GuardrailCustom:
			customs = append(customs, i)
		case isRewrite(g):
			rewrites = append(rewrites, i)
		default:
			results[i] = judge(g, text, stageName)
		}
	}
	rewrites = append(rewrites, customs...) // the endpoints last, on text the local detectors cleaned

	working := text
	for _, i := range rewrites {
		var res models.GuardrailResult
		res, working = s.rewrite(ctx, rules[i], working, stage)
		results[i] = res
	}

	approvalOnly := true
	for i, res := range results {
		g := rules[i]
		res.RuleID, res.RuleName = g.ID, g.Name
		if res.Action == "" && g.Action != "" {
			res.Action = g.Action
		}
		if res.Passed && res.Action == models.GuardrailActionWarn && res.Message != "" {
			eval.Warned++
		}
		eval.Redacted += res.Redacted
		if !res.Passed {
			eval.Passed = false
			if res.Action != models.GuardrailActionRequireApproval {
				approvalOnly = false
			}
		}
		eval.Results = append(eval.Results, res)
	}
	eval.ApprovalRequired = !eval.Passed && approvalOnly
	if eval.Redacted > 0 {
		eval.Text = working
	}
	return eval, nil
}

// Scrub implements contracts.StageGuardrailService: the redact and mask rules of the local
// detectors, nothing that blocks and nothing that calls out.
func (s *CommunityGuardrailService) Scrub(guardrails []models.Guardrail, text string, stage models.GuardrailStage) (string, int) {
	total := 0
	for _, g := range guardrails {
		if !g.Enabled || !appliesToStage(g.Stage, stage) || g.Kind == models.GuardrailCustom || !isRewrite(g) {
			continue
		}
		res, out := s.rewrite(context.Background(), g, text, stage)
		if res.Passed {
			text = out
			total += res.Redacted
		}
	}
	return text, total
}

// judge evaluates a rule that does not rewrite and applies its action to a failure: warn lets
// the text through and records that the rule fired, require_approval and block fail it (a
// caller that cannot wait for a person treats both as a block).
func judge(g models.Guardrail, text, stage string) models.GuardrailResult {
	res := evaluateOne(g, text, stage)
	if !res.Passed && g.EffectiveAction() == models.GuardrailActionWarn {
		res.Passed = true
		if res.Message == "" {
			res.Message = "rule matched"
		}
	}
	return res
}

// isRewrite reports whether the rule changes text instead of judging it.
func isRewrite(g models.Guardrail) bool {
	if g.Kind == models.GuardrailCustom {
		return true
	}
	act := g.EffectiveAction()
	return (act == models.GuardrailActionRedact || act == models.GuardrailActionMask) && canRewrite(g)
}

// canRewrite is whether the rule's kind finds spans or, for custom, returns replacement text.
// A redact action on any other kind is rejected by Validate and, if one gets here, judged as
// a block: the safe reading of a rule that cannot do what it says.
func canRewrite(g models.Guardrail) bool {
	switch g.Kind {
	case models.GuardrailPIIDetection, models.GuardrailContentFilter, models.GuardrailCustom:
		return true
	case models.GuardrailRegexFilter:
		b, ok := g.Config["block_on_match"].(bool)
		return !ok || b
	}
	return false
}

// rewrite applies a redact, mask or custom rule to text and returns the result and the text
// that continues.
func (s *CommunityGuardrailService) rewrite(ctx context.Context, g models.Guardrail, text string, stage models.GuardrailStage) (models.GuardrailResult, string) {
	base := models.GuardrailResult{Passed: true, Kind: g.Kind, Stage: string(stage)}
	if g.Kind == models.GuardrailCustom {
		return s.rewriteCustom(ctx, g, text, base)
	}
	spans, tooBig, err := spansFor(g, text)
	switch {
	case err != nil:
		base.Passed, base.Message = false, "rule cannot be applied: "+err.Error()
		return base, text
	case tooBig:
		return tooLargeResult(g, string(stage)), text
	case len(spans) == 0:
		return base, text
	}
	mask := g.EffectiveAction() == models.GuardrailActionMask
	var sb strings.Builder
	last := 0
	for _, sp := range spans {
		sb.WriteString(text[last:sp.start])
		sb.WriteString(replacement(sp, text[sp.start:sp.end], mask))
		last = sp.end
	}
	sb.WriteString(text[last:])
	base.Redacted = len(spans)
	base.Findings = findingsOf(spans)
	return base, sb.String()
}

// replacement is what stands in for a matched value: [LABEL], or under mask the value with all
// but its last four characters starred. A secret is never partly kept.
func replacement(sp span, value string, mask bool) string {
	if !mask || sp.det.secret {
		return "[" + sp.det.label + "]"
	}
	runes := []rune(value)
	if len(runes) <= 8 {
		return strings.Repeat("*", len(runes))
	}
	return strings.Repeat("*", len(runes)-4) + string(runes[len(runes)-4:])
}

// spansFor finds the spans a span-finding rule would replace in text.
func spansFor(g models.Guardrail, text string) (spans []span, tooBig bool, err error) {
	switch g.Kind {
	case models.GuardrailPIIDetection:
		return piiSpans(g, text)
	case models.GuardrailRegexFilter:
		return regexSpans(g, text)
	case models.GuardrailContentFilter:
		return contentSpans(g, text)
	}
	return nil, false, fmt.Errorf("%s cannot find spans", g.Kind)
}

func piiSpans(g models.Guardrail, text string) ([]span, bool, error) {
	if len(text) > MaxScanBytes {
		return nil, true, nil
	}
	names, _ := stringList(g.Config, "patterns")
	// A name that is not a detector is skipped, as it always was: Validate is what reports it.
	// Dropping the unknown names must not turn a list of only typos into "all four".
	known := make([]string, 0, len(names))
	for _, n := range names {
		_, isDet := detectors[n]
		_, isGroup := detectorGroups[n]
		if isDet || isGroup {
			known = append(known, n)
		}
	}
	if len(names) > 0 && len(known) == 0 {
		return nil, false, nil
	}
	dets, err := resolveDetectors(known)
	if err != nil {
		return nil, false, err
	}
	values, _ := stringList(g.Config, "allowlist")
	domains, _ := stringList(g.Config, "allow_domains")
	return findSpans(dets, text, newAllowlist(values, domains)), false, nil
}

func regexSpans(g models.Guardrail, text string) ([]span, bool, error) {
	pattern, _ := g.Config["pattern"].(string)
	re, err := compileBounded(pattern)
	if err != nil {
		return nil, false, err
	}
	if len(text) > MaxScanBytes {
		return nil, true, nil
	}
	det := &detector{name: regexLabel(g), label: strings.ToUpper(regexLabel(g))}
	var out []span
	for _, loc := range re.FindAllStringIndex(text, -1) {
		if loc[1] > loc[0] { // an empty match has nothing to replace
			out = append(out, span{loc[0], loc[1], det})
		}
	}
	return out, false, nil
}

func contentSpans(g models.Guardrail, text string) ([]span, bool, error) {
	words, _ := stringList(g.Config, "blocked_words")
	if len(words) == 0 {
		return nil, false, nil
	}
	quoted := make([]string, 0, len(words))
	for _, w := range words {
		if w != "" {
			quoted = append(quoted, regexp.QuoteMeta(w))
		}
	}
	prefix := "(?i)"
	if cs, _ := g.Config["case_sensitive"].(bool); cs {
		prefix = ""
	}
	// Longest words first so that "credit card" is replaced whole and not as "credit".
	sort.SliceStable(quoted, func(i, j int) bool { return len(quoted[i]) > len(quoted[j]) })
	re, err := compileBounded(prefix + "(?:" + strings.Join(quoted, "|") + ")")
	if err != nil {
		return nil, false, err
	}
	if len(text) > MaxScanBytes {
		return nil, true, nil
	}
	det := &detector{name: "blocked_word", label: "BLOCKED"}
	var out []span
	for _, loc := range re.FindAllStringIndex(text, -1) {
		out = append(out, span{loc[0], loc[1], det})
	}
	return out, false, nil
}

// regexLabel names what a regex rule found: its configured label, or REDACTED.
func regexLabel(g models.Guardrail) string {
	if l, _ := g.Config["label"].(string); l != "" {
		return l
	}
	return "REDACTED"
}

// findingsOf counts spans by detector name, in name order.
func findingsOf(spans []span) []models.GuardrailFinding {
	counts := map[string]int{}
	for _, sp := range spans {
		counts[sp.det.name]++
	}
	out := make([]models.GuardrailFinding, 0, len(counts))
	for name, n := range counts {
		out = append(out, models.GuardrailFinding{Type: name, Count: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

func tooLargeResult(g models.Guardrail, stage string) models.GuardrailResult {
	return models.GuardrailResult{
		Passed: false, Kind: g.Kind, Stage: stage,
		Message: fmt.Sprintf("text is longer than the %d bytes a rule scans, so it was refused", MaxScanBytes),
	}
}

// ── Regex cache ─────────────────────────────────────────────

var (
	regexMu    sync.Mutex
	regexCache = map[string]*regexp.Regexp{}
)

const regexCacheMax = 512

// compileBounded compiles a rule's pattern, once. The cache is bounded and emptied when full:
// the patterns come from stored rules, so there are few, but the bound means a flood of
// distinct patterns (a test endpoint) cannot grow it without limit.
func compileBounded(pattern string) (*regexp.Regexp, error) {
	if len(pattern) > MaxPatternBytes {
		return nil, fmt.Errorf("pattern is longer than %d bytes", MaxPatternBytes)
	}
	regexMu.Lock()
	re, ok := regexCache[pattern]
	regexMu.Unlock()
	if ok {
		return re, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	regexMu.Lock()
	if len(regexCache) >= regexCacheMax {
		regexCache = map[string]*regexp.Regexp{}
	}
	regexCache[pattern] = re
	regexMu.Unlock()
	return re, nil
}

// stringList reads config[key] as a list of strings. A missing key is an empty list.
func stringList(config map[string]interface{}, key string) ([]string, error) {
	raw, ok := config[key]
	if !ok || raw == nil {
		return nil, nil
	}
	list, ok := raw.([]interface{})
	if !ok {
		if strs, isStrs := raw.([]string); isStrs {
			return strs, nil
		}
		return nil, fmt.Errorf("%s must be a list of strings", key)
	}
	if len(list) > MaxListEntries {
		return nil, fmt.Errorf("%s is limited to %d entries", key, MaxListEntries)
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%s must be a list of strings", key)
		}
		out = append(out, s)
	}
	return out, nil
}
