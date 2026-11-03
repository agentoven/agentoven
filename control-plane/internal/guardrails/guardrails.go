// Package guardrails provides the community guardrail evaluation engine.
// It evaluates input and output messages against configured guardrail rules.
//
// Supported guardrail kinds:
//   - content_filter: keyword/phrase blocklist
//   - pii_detection: regex-based PII detection (emails, phone numbers, SSN, etc.)
//   - topic_restriction: allowed/blocked topic keywords
//   - max_length: character/token length limits
//   - regex_filter: custom regex pattern matching
//   - prompt_injection: heuristic prompt injection detection
//   - custom: no-op in OSS (Pro implements webhook/LLM-judge)
package guardrails

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/agentoven/agentoven/control-plane/pkg/netguard"
)

// ── Community Guardrail Service ─────────────────────────────
// The service itself, and how a stage is evaluated, is in engine.go.

// MergeWithWorkspace combines a kitchen's workspace (global) guardrails with an agent's
// own. Global rules are always applied; the agent's rules are applied on top of them.
//
//   - Every enabled workspace rule applies to every agent in the kitchen, whether or not
//     the agent attached anything, and nothing an agent defines replaces or switches off a
//     global rule — not a rule of the same kind and stage, not a disabled agent rule.
//   - The only way out is an approved exception for that specific agent (exceptions is
//     the list for the calling agent only). Expired exceptions are ignored.
//   - A workspace rule that is not Enabled applies to nobody. An agent's own rules are
//     appended as they are; the evaluator skips the ones that are not enabled.
//
// Guardrail.Overridable no longer has any effect: ADR-0013 once let an agent's rule of the
// same kind and stage replace an "overridable" workspace default, and that was withdrawn
// (2026-10-06) so that a global rule can never be weakened by the agent it governs.
//
// The caller should pass the result to EvaluateInput/EvaluateOutput.
func MergeWithWorkspace(workspaceRules, agentRules []models.Guardrail, exceptions []models.WorkspaceGuardrailException) []models.Guardrail {
	now := time.Now()
	exceptedIDs := make(map[string]struct{}, len(exceptions))
	for _, ex := range exceptions {
		if !ex.ExpiresAt.IsZero() && ex.ExpiresAt.Before(now) {
			continue // expired — treat as no exception
		}
		exceptedIDs[ex.GuardrailID] = struct{}{}
	}

	merged := make([]models.Guardrail, 0, len(workspaceRules)+len(agentRules))
	for _, r := range workspaceRules {
		if !r.Enabled {
			continue
		}
		if _, excepted := exceptedIDs[r.ID]; excepted {
			continue // an operator approved this agent being exempt from this rule
		}
		merged = append(merged, r)
	}
	return append(merged, agentRules...)
}

// evaluateOne dispatches a single guardrail evaluation.
func evaluateOne(g models.Guardrail, text string, stage string, sc *scan) models.GuardrailResult {
	switch g.Kind {
	case models.GuardrailContentFilter:
		return evalContentFilter(g, text, stage, sc)
	case models.GuardrailPIIDetection:
		return evalPIIDetection(g, text, stage, sc)
	case models.GuardrailTopicRestriction:
		return evalTopicRestriction(g, text, stage, sc)
	case models.GuardrailMaxLength:
		return evalMaxLength(g, text, stage)
	case models.GuardrailRegexFilter:
		return evalRegexFilter(g, text, stage, sc)
	case models.GuardrailPromptInjection:
		return evalPromptInjection(g, text, stage, sc)
	case models.GuardrailLlamaGuard:
		return evalLlamaGuard(g, text, stage)
	case models.GuardrailCustom:
		// A custom rule is judged by the installed CustomGuardrailEvaluator (engine.go); this is
		// only reached for the community edition's own evaluator, which has none.
		return models.GuardrailResult{Passed: true, Kind: g.Kind, Stage: stage}
	default:
		// A rule that cannot be evaluated is not a rule that passes: a typo in a kind ("llama_guard")
		// must not turn the rule into a no-op. Validate refuses such a rule when it is saved; this is
		// for one that was stored before it did.
		return models.GuardrailResult{Passed: false, Kind: g.Kind, Stage: stage, Message: "unknown guardrail kind " + string(g.Kind) + ": the rule cannot be applied"}
	}
}

// foldedText is the text a judging rule reads: folded (fold.go), so a zero-width space or a full-width
// letter does not change what a phrase says. A nil result means the budget is spent.
func foldedText(text string, sc *scan) (string, bool) {
	f := sc.fold(text)
	if f == nil {
		return "", false
	}
	return f.text, true
}

// ── Content Filter ──────────────────────────────────────────
// Config: { "blocked_words": ["word1", "word2"], "case_sensitive": false }

func evalContentFilter(g models.Guardrail, text string, stage string, sc *scan) models.GuardrailResult {
	blockedRaw, _ := g.Config["blocked_words"].([]interface{})
	caseSensitive, _ := g.Config["case_sensitive"].(bool)

	checkText, ok := foldedText(text, sc)
	if !ok {
		return budgetResult(g, stage)
	}
	if !caseSensitive {
		checkText = strings.ToLower(checkText)
	}

	for _, bRaw := range blockedRaw {
		word, ok := bRaw.(string)
		if !ok {
			continue
		}
		checkWord := fold(word).text // the words are read the way the text is
		if !caseSensitive {
			checkWord = strings.ToLower(word)
		}
		if strings.Contains(checkText, checkWord) {
			return models.GuardrailResult{
				Passed:  false,
				Kind:    g.Kind,
				Stage:   stage,
				Message: "Blocked content detected: contains prohibited word/phrase",
			}
		}
	}

	return models.GuardrailResult{Passed: true, Kind: g.Kind, Stage: stage}
}

// ── PII Detection ───────────────────────────────────────────
// Config: { "patterns": ["email", "iban", "secrets", ...], "allowlist": [...], "allow_domains": [...] }
// With no "patterns" the four that existed first are checked: email, phone, ssn, credit_card.
// The detectors, groups and their validation are in detectors.go.

func evalPIIDetection(g models.Guardrail, text string, stage string, sc *scan) models.GuardrailResult {
	spans, tooBig, err := piiSpans(g, text, sc)
	switch {
	case errors.Is(err, errScanBudget):
		return budgetResult(g, stage)
	case err != nil:
		return models.GuardrailResult{Passed: false, Kind: g.Kind, Stage: stage, Message: "PII rule cannot be applied: " + err.Error()}
	case tooBig:
		return tooLargeResult(g, stage)
	case len(spans) == 0:
		return models.GuardrailResult{Passed: true, Kind: g.Kind, Stage: stage}
	}
	return models.GuardrailResult{
		Passed:   false,
		Kind:     g.Kind,
		Stage:    stage,
		Message:  "PII detected: " + spans[0].det.name + " pattern matched",
		Findings: findingsOf(spans),
	}
}

// ── Topic Restriction ───────────────────────────────────────
// Config: { "allowed_topics": [...], "blocked_topics": [...] }
// Keyword-based matching. If allowed_topics is set, text must contain
// at least one allowed topic keyword to pass. blocked_topics always blocks.

func evalTopicRestriction(g models.Guardrail, text string, stage string, sc *scan) models.GuardrailResult {
	folded, ok := foldedText(text, sc)
	if !ok {
		return budgetResult(g, stage)
	}
	lower := strings.ToLower(folded)

	// Check blocked topics first
	blockedRaw, _ := g.Config["blocked_topics"].([]interface{})
	for _, bRaw := range blockedRaw {
		topic, ok := bRaw.(string)
		if !ok {
			continue
		}
		if strings.Contains(lower, strings.ToLower(topic)) {
			return models.GuardrailResult{
				Passed:  false,
				Kind:    g.Kind,
				Stage:   stage,
				Message: "Blocked topic detected: " + topic,
			}
		}
	}

	// Check allowed topics (if configured)
	allowedRaw, _ := g.Config["allowed_topics"].([]interface{})
	if len(allowedRaw) > 0 {
		found := false
		for _, aRaw := range allowedRaw {
			topic, ok := aRaw.(string)
			if !ok {
				continue
			}
			if strings.Contains(lower, strings.ToLower(topic)) {
				found = true
				break
			}
		}
		if !found {
			return models.GuardrailResult{
				Passed:  false,
				Kind:    g.Kind,
				Stage:   stage,
				Message: "Message does not match any allowed topic",
			}
		}
	}

	return models.GuardrailResult{Passed: true, Kind: g.Kind, Stage: stage}
}

// ── Max Length ───────────────────────────────────────────────
// Config: { "max_characters": 5000, "max_words": 1000 }

func evalMaxLength(g models.Guardrail, text string, stage string) models.GuardrailResult {
	if maxChars, ok := getIntConfig(g.Config, "max_characters"); ok && maxChars > 0 {
		if utf8.RuneCountInString(text) > maxChars {
			return models.GuardrailResult{
				Passed:  false,
				Kind:    g.Kind,
				Stage:   stage,
				Message: "Message exceeds maximum character limit",
			}
		}
	}

	if maxWords, ok := getIntConfig(g.Config, "max_words"); ok && maxWords > 0 {
		wordCount := len(strings.Fields(text))
		if wordCount > maxWords {
			return models.GuardrailResult{
				Passed:  false,
				Kind:    g.Kind,
				Stage:   stage,
				Message: "Message exceeds maximum word limit",
			}
		}
	}

	return models.GuardrailResult{Passed: true, Kind: g.Kind, Stage: stage}
}

// ── Regex Filter ────────────────────────────────────────────
// Config: { "pattern": "regex_string", "block_on_match": true }

func evalRegexFilter(g models.Guardrail, text string, stage string, sc *scan) models.GuardrailResult {
	pattern, _ := g.Config["pattern"].(string)
	if pattern == "" {
		return models.GuardrailResult{Passed: true, Kind: g.Kind, Stage: stage}
	}

	re, err := compileBounded(pattern)
	if err != nil {
		// Fail closed: a pattern that does not compile checks nothing, and a rule that checks nothing
		// must not read as one that found nothing. Validate refuses it when it is saved.
		return models.GuardrailResult{
			Passed:  false,
			Kind:    g.Kind,
			Stage:   stage,
			Message: "Invalid regex pattern: " + err.Error(),
		}
	}
	if sc.tooBig(len(text)) {
		return tooLargeResult(g, stage)
	}
	if !sc.charge(2 * len(text)) { // a match and a count of the matches
		return budgetResult(g, stage)
	}

	blockOnMatch := true // default: block when regex matches
	if b, ok := g.Config["block_on_match"].(bool); ok {
		blockOnMatch = b
	}

	matched := re.MatchString(text)
	if matched && blockOnMatch {
		return models.GuardrailResult{
			Passed:  false,
			Kind:    g.Kind,
			Stage:   stage,
			Message: "Content matched blocked regex pattern",
			// The count only; the matched text is never kept.
			Findings: []models.GuardrailFinding{{Type: regexLabel(g), Count: len(re.FindAllStringIndex(text, -1))}},
		}
	}
	if !matched && !blockOnMatch {
		return models.GuardrailResult{
			Passed:  false,
			Kind:    g.Kind,
			Stage:   stage,
			Message: "Content did not match required regex pattern",
		}
	}

	return models.GuardrailResult{Passed: true, Kind: g.Kind, Stage: stage}
}

// ── Prompt Injection Detection ──────────────────────────────
// Heuristic-based detection of common prompt injection patterns.
// Config: { "sensitivity": "high" | "medium" | "low" }

var injectionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)ignore\s+(all\s+)?(previous|prior|above)\s+(instructions?|prompts?|rules?|directions?)`),
	regexp.MustCompile(`(?i)disregard\s+(all\s+)?(previous|prior|above)\s+(instructions?|prompts?|rules?)`),
	regexp.MustCompile(`(?i)forget\s+(all\s+)?(previous|prior|above|your)\s+(instructions?|prompts?|rules?|context)`),
	regexp.MustCompile(`(?i)you\s+are\s+now\s+(a|an|my)\s+`),
	regexp.MustCompile(`(?i)new\s+instructions?:\s*`),
	regexp.MustCompile(`(?i)system\s*:\s*you\s+are`),
	regexp.MustCompile(`(?i)\bdo\s+anything\s+now\b`),
	regexp.MustCompile(`(?i)\bjailbreak\b`),
	regexp.MustCompile(`(?i)pretend\s+you\s+(are|have)\s+no\s+(restrictions?|rules?|guidelines?)`),
	regexp.MustCompile(`(?i)act\s+as\s+if\s+you\s+have\s+no\s+(restrictions?|rules?|filters?)`),
}

// Additional high-sensitivity patterns
var highSensitivityPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)override\s+(your|the|all)\s+`),
	regexp.MustCompile(`(?i)bypass\s+(your|the|all)\s+`),
	regexp.MustCompile(`(?i)reveal\s+(your|the)\s+(system\s+)?(prompt|instructions?)`),
	regexp.MustCompile(`(?i)what\s+(is|are)\s+your\s+(system\s+)?(prompt|instructions?|rules?)`),
	regexp.MustCompile(`(?i)repeat\s+(your|the)\s+(system\s+)?(prompt|instructions?)\s+verbatim`),
}

func evalPromptInjection(g models.Guardrail, text string, stage string, sc *scan) models.GuardrailResult {
	text, ok := foldedText(text, sc)
	if !ok || !sc.charge(len(injectionPatterns)*len(text)) {
		return budgetResult(g, stage)
	}
	sensitivity, _ := g.Config["sensitivity"].(string)
	if sensitivity == "" {
		sensitivity = "medium"
	}

	// Always check base patterns
	for _, re := range injectionPatterns {
		if re.MatchString(text) {
			return models.GuardrailResult{
				Passed:  false,
				Kind:    g.Kind,
				Stage:   stage,
				Message: "Potential prompt injection detected",
			}
		}
	}

	// High sensitivity also checks additional patterns
	if sensitivity == "high" {
		for _, re := range highSensitivityPatterns {
			if re.MatchString(text) {
				return models.GuardrailResult{
					Passed:  false,
					Kind:    g.Kind,
					Stage:   stage,
					Message: "Potential prompt injection detected (high sensitivity)",
				}
			}
		}
	}

	return models.GuardrailResult{Passed: true, Kind: g.Kind, Stage: stage}
}

// ── Helpers ─────────────────────────────────────────────────

// getIntConfig extracts an integer from a config map (handles float64 from JSON).
func getIntConfig(config map[string]interface{}, key string) (int, bool) {
	v, ok := config[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	default:
		return 0, false
	}
}

// PolicyError means the workspace guardrails that must apply to an agent could not be
// established: the source could not be read, or an enabled workspace rule cannot be
// evaluated (see Validate). The agent must not run: a global rule that cannot be applied
// is not a rule that can be skipped.
type PolicyError struct {
	Kitchen, Agent string
	Err            error
}

func (e *PolicyError) Error() string {
	return "workspace guardrails could not be applied to agent " + e.Agent + " in kitchen " + e.Kitchen + ": " + e.Err.Error()
}

func (e *PolicyError) Unwrap() error { return e.Err }

// effective is Effective before the secrets sealed into the rules are read back (sealed.go). It returns the guardrails that apply to an agent: its own rules (own — its list,
// or that list adjusted by an environment policy) with the kitchen's workspace rules from
// src applied on top (MergeWithWorkspace). With no source, or no workspace rules for the
// kitchen, it is own unchanged.
//
// It fails closed. If the source cannot be read, or any enabled workspace rule is
// unreadable, it returns a *PolicyError and the caller must refuse to run the agent.
func effective(ctx context.Context, src contracts.WorkspaceGuardrailSource, kitchen, agent string, own []models.Guardrail) ([]models.Guardrail, error) {
	if src == nil {
		if err := ValidateOwn(own); err != nil {
			return nil, &PolicyError{Kitchen: kitchen, Agent: agent, Err: err}
		}
		if err := CheckOwnEndpoints(ctx, kitchen, agent, own); err != nil {
			return nil, &PolicyError{Kitchen: kitchen, Agent: agent, Err: err}
		}
		return stamp(own, kitchen, agent), nil
	}
	fail := func(err error) ([]models.Guardrail, error) {
		return nil, &PolicyError{Kitchen: kitchen, Agent: agent, Err: err}
	}
	// The agent's own rules get the check the workspace's get: one that cannot be applied stops the
	// agent. It used to be a rule that passed everything.
	if err := ValidateOwn(own); err != nil {
		return fail(err)
	}
	// A custom endpoint the agent names itself runs only once it is approved: an unapproved one stops the
	// agent, as a workspace rule that cannot be applied does (owner decision S7).
	if err := CheckOwnEndpoints(ctx, kitchen, agent, own); err != nil {
		return fail(err)
	}
	workspace, err := src.WorkspaceGuardrails(ctx, kitchen)
	if err != nil {
		return fail(err)
	}
	if len(workspace) == 0 {
		return stamp(own, kitchen, agent), nil
	}
	for _, r := range workspace {
		if !r.Enabled {
			continue
		}
		if err := Validate(r); err != nil {
			return fail(fmt.Errorf("rule %q (%s): %w", r.ID, r.Kind, err))
		}
	}
	exceptions, err := src.WorkspaceGuardrailExceptions(ctx, kitchen, agent)
	if err != nil {
		return fail(err)
	}
	return stamp(MergeWithWorkspace(workspace, own, exceptions), kitchen, agent), nil
}

// stamp records the kitchen and the agent on each rule, overwriting whatever the rule carried: the rule list
// of an agent comes from a client, and the kitchen and agent it runs for are the server's to say. The
// caller's slice is not changed.
func stamp(rules []models.Guardrail, kitchen, agent string) []models.Guardrail {
	if len(rules) == 0 {
		return rules
	}
	out := make([]models.Guardrail, len(rules))
	for i, r := range rules {
		r.KitchenID, r.AgentName = kitchen, agent
		out[i] = r
	}
	return out
}

// Validate reports why a guardrail could not do its job as configured: an unknown kind, stage
// or action, or a config the evaluator would read as "nothing to check" (which it passes
// silently). Effective applies it to enabled workspace rules so that one that cannot work
// stops the agent instead of quietly doing nothing, and Pro applies it when a rule is saved.
func Validate(g models.Guardrail) error {
	switch g.Stage {
	case "", models.GuardrailStageInput, models.GuardrailStageOutput, models.GuardrailStageBoth,
		models.GuardrailStageRetrieval, models.GuardrailStageAll:
	default:
		return fmt.Errorf("unknown stage %q", g.Stage)
	}
	if err := validateAction(g); err != nil {
		return err
	}
	switch g.Kind {
	case models.GuardrailContentFilter:
		if err := needStrings(g.Config, "blocked_words"); err != nil {
			return err
		}
		if list, _ := g.Config["blocked_words"].([]interface{}); len(list) > MaxListEntries {
			return fmt.Errorf("blocked_words is limited to %d entries", MaxListEntries)
		}
		return nil
	case models.GuardrailPIIDetection:
		if raw, ok := g.Config["patterns"]; ok {
			list, ok := raw.([]interface{})
			if !ok {
				return fmt.Errorf("patterns must be a list of pattern names")
			}
			for _, p := range list {
				name, _ := p.(string)
				if _, isGroup := detectorGroups[name]; isGroup {
					continue
				}
				if _, known := detectors[name]; !known {
					return fmt.Errorf("unknown PII pattern %v (known: %s)", p, strings.Join(DetectorNames(), ", "))
				}
			}
		}
		for _, key := range []string{"allowlist", "allow_domains"} {
			if _, present := g.Config[key]; !present {
				continue
			}
			if _, err := stringList(g.Config, key); err != nil {
				return err
			}
		}
		return nil
	case models.GuardrailTopicRestriction:
		if needStrings(g.Config, "blocked_topics") != nil && needStrings(g.Config, "allowed_topics") != nil {
			return fmt.Errorf("needs blocked_topics or allowed_topics, a non-empty list of strings")
		}
		return nil
	case models.GuardrailMaxLength:
		chars, okC := getIntConfig(g.Config, "max_characters")
		words, okW := getIntConfig(g.Config, "max_words")
		if !(okC && chars > 0) && !(okW && words > 0) {
			return fmt.Errorf("needs max_characters or max_words greater than zero")
		}
		return nil
	case models.GuardrailRegexFilter:
		pattern, _ := g.Config["pattern"].(string)
		if pattern == "" {
			return fmt.Errorf("needs a pattern")
		}
		if _, err := compileBounded(pattern); err != nil {
			return fmt.Errorf("invalid pattern: %w", err)
		}
		if label, ok := g.Config["label"].(string); ok && !labelPattern.MatchString(label) {
			return fmt.Errorf("label must be 1 to 32 letters, digits or underscores")
		}
		return nil
	case models.GuardrailPromptInjection:
		return nil
	case models.GuardrailCustom:
		if v, ok := g.Config["fail_open"]; ok {
			if _, isBool := v.(bool); !isBool {
				return fmt.Errorf("fail_open must be true or false")
			}
		}
		return nil
	case models.GuardrailLlamaGuard:
		return validateLlamaGuard(g.Config)
	default:
		return fmt.Errorf("unknown guardrail kind")
	}
}

// validateLlamaGuard checks the parts of a llamaguard rule that can be judged without the network:
// the endpoint is an http(s) URL with a host and no credentials, and the numbers and flags have the
// types the evaluator reads. Whether the host may be reached from here is the egress guard's answer
// (CheckEndpoints, when the rule is saved, and the dial, every time it runs).
func validateLlamaGuard(cfg map[string]interface{}) error {
	endpoint, _ := cfg["endpoint"].(string)
	if endpoint == "" {
		return fmt.Errorf("needs an endpoint")
	}
	u, err := url.Parse(endpoint)
	switch {
	case err != nil || u.Host == "" || u.Hostname() == "":
		return fmt.Errorf("endpoint must be an http or https URL with a host")
	case u.Scheme != "http" && u.Scheme != "https":
		return fmt.Errorf("endpoint must be an http or https URL, not %q", u.Scheme)
	case u.User != nil:
		return fmt.Errorf("endpoint must not carry credentials in the URL; use api_key")
	}
	if v, ok := cfg["timeout_secs"]; ok {
		secs, isNum := v.(float64)
		if !isNum || secs <= 0 || secs > maxLlamaGuardTimeout.Seconds() {
			return fmt.Errorf("timeout_secs must be a number of seconds from 1 to %d", int(maxLlamaGuardTimeout.Seconds()))
		}
	}
	if v, ok := cfg["fail_open"]; ok {
		if _, isBool := v.(bool); !isBool {
			return fmt.Errorf("fail_open must be true or false")
		}
	}
	for _, key := range []string{"api_key", "model", "categories"} {
		if v, ok := cfg[key]; ok {
			if _, isStr := v.(string); !isStr {
				return fmt.Errorf("%s must be a string", key)
			}
		}
	}
	return nil
}

// numericHost is a host written as a number the way inet_aton reads it (2130706433, 0x7f.1, 017700000001, 127.1):
// it has one meaning whatever the resolver says, so it can be judged without the network. A name is not one.
var numericHost = regexp.MustCompile(`^(?:0[xX][0-9a-fA-F]*|[0-9]+)(?:\.(?:0[xX][0-9a-fA-F]*|[0-9]+))*$`)

// literalHost is whether a host is an IP address (either family) or such a number.
func literalHost(host string) bool {
	_, err := netip.ParseAddr(host)
	return err == nil || numericHost.MatchString(host)
}

// CheckEndpoints refuses, when a rule is saved, a llamaguard endpoint written as an address the egress
// guard would refuse when the rule runs (netguard.Vet): the mistake is reported to the person who made
// it, and not as a rule that blocks everything later. A host name is not resolved here: the answer
// would tell whoever saves the rule which internal names exist, and the dial is what enforces (it
// resolves, vets the address it connects to, and answers with one message whatever the reason).
func CheckEndpoints(ctx context.Context, kitchen string, rules []models.Guardrail) error {
	for _, g := range rules {
		if g.Kind != models.GuardrailLlamaGuard || !g.Enabled {
			continue
		}
		endpoint, _ := g.Config["endpoint"].(string)
		u, err := url.Parse(endpoint)
		if err != nil || u.Hostname() == "" {
			continue // Validate reports it
		}
		if host := u.Hostname(); literalHost(host) {
			if _, err := netguard.Default().Vet(ctx, kitchen, host); err != nil {
				return fmt.Errorf("rule %q: the llamaguard endpoint is not an address this server may call (loopback, private, link-local and metadata addresses need an egress allowlist entry)", g.ID)
			}
		}
	}
	return nil
}

// ValidateOwn validates an agent's own rules, the ones it carries in its definition: every enabled
// rule must be one the evaluator can apply. Run when an agent is saved, so a typo is an error for the
// person who made it instead of a rule that quietly does nothing.
func ValidateOwn(rules []models.Guardrail) error {
	for i, r := range rules {
		if !r.Enabled {
			continue
		}
		if err := Validate(r); err != nil {
			return fmt.Errorf("guardrail %d (%q, kind %q): %w", i+1, r.Name, r.Kind, err)
		}
	}
	return nil
}

// validateAction checks the rule's action is one it can carry out. Redacting and masking need
// a rule that finds spans (or a custom endpoint that returns the new text); a regex rule that
// requires a match (block_on_match false) has no span to replace.
func validateAction(g models.Guardrail) error {
	switch g.Action {
	case "", models.GuardrailActionBlock, models.GuardrailActionWarn, models.GuardrailActionRequireApproval:
		return nil
	case models.GuardrailActionRedact, models.GuardrailActionMask:
		if !canRewrite(g) {
			return fmt.Errorf("action %q needs a rule that finds spans: pii_detection, content_filter, regex_filter (with block_on_match true) or custom; %s cannot redact", g.Action, g.Kind)
		}
		return nil
	default:
		return fmt.Errorf("unknown action %q (block, redact, mask, warn or require_approval)", g.Action)
	}
}

// needStrings requires config[key] to be a non-empty list of non-empty strings.
func needStrings(config map[string]interface{}, key string) error {
	list, _ := config[key].([]interface{})
	if len(list) == 0 {
		return fmt.Errorf("needs %s, a non-empty list of strings", key)
	}
	for _, v := range list {
		if s, ok := v.(string); !ok || s == "" {
			return fmt.Errorf("%s must contain only non-empty strings", key)
		}
	}
	return nil
}
