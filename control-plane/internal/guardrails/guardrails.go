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
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// ── Community Guardrail Service ─────────────────────────────

// CommunityGuardrailService is the OSS implementation of contracts.GuardrailService.
// It evaluates guardrails using built-in heuristics and regex patterns.
type CommunityGuardrailService struct{}

// EvaluateInput runs input-stage guardrails against the user message.
func (s *CommunityGuardrailService) EvaluateInput(ctx context.Context, guardrails []models.Guardrail, message string) (*models.GuardrailEvaluation, error) {
	return evaluate(guardrails, message, "input")
}

// EvaluateOutput runs output-stage guardrails against the model response.
func (s *CommunityGuardrailService) EvaluateOutput(ctx context.Context, guardrails []models.Guardrail, response string) (*models.GuardrailEvaluation, error) {
	return evaluate(guardrails, response, "output")
}

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

// evaluate runs all applicable guardrails for the given stage.
func evaluate(guardrails []models.Guardrail, text string, stage string) (*models.GuardrailEvaluation, error) {
	eval := &models.GuardrailEvaluation{
		Passed:  true,
		Results: make([]models.GuardrailResult, 0),
	}

	for _, g := range guardrails {
		if !g.Enabled {
			continue
		}
		// Check if this guardrail applies to the current stage
		if !appliesToStage(g.Stage, stage) {
			continue
		}

		result := evaluateOne(g, text, stage)
		eval.Results = append(eval.Results, result)
		if !result.Passed {
			eval.Passed = false
		}
	}

	return eval, nil
}

// appliesToStage checks whether a guardrail applies to the given stage.
func appliesToStage(guardrailStage models.GuardrailStage, currentStage string) bool {
	switch guardrailStage {
	case models.GuardrailStageBoth:
		return true
	case models.GuardrailStageInput:
		return currentStage == "input"
	case models.GuardrailStageOutput:
		return currentStage == "output"
	default:
		return true // default: apply to all stages
	}
}

// evaluateOne dispatches a single guardrail evaluation.
func evaluateOne(g models.Guardrail, text string, stage string) models.GuardrailResult {
	switch g.Kind {
	case models.GuardrailContentFilter:
		return evalContentFilter(g, text, stage)
	case models.GuardrailPIIDetection:
		return evalPIIDetection(g, text, stage)
	case models.GuardrailTopicRestriction:
		return evalTopicRestriction(g, text, stage)
	case models.GuardrailMaxLength:
		return evalMaxLength(g, text, stage)
	case models.GuardrailRegexFilter:
		return evalRegexFilter(g, text, stage)
	case models.GuardrailPromptInjection:
		return evalPromptInjection(g, text, stage)
	case models.GuardrailLlamaGuard:
		return evalLlamaGuard(g, text, stage)
	case models.GuardrailCustom:
		// Custom guardrails are a no-op in OSS (Pro adds webhook/LLM-judge)
		return models.GuardrailResult{Passed: true, Kind: g.Kind, Stage: stage}
	default:
		return models.GuardrailResult{Passed: true, Kind: g.Kind, Stage: stage, Message: "unknown guardrail kind"}
	}
}

// ── Content Filter ──────────────────────────────────────────
// Config: { "blocked_words": ["word1", "word2"], "case_sensitive": false }

func evalContentFilter(g models.Guardrail, text string, stage string) models.GuardrailResult {
	blockedRaw, _ := g.Config["blocked_words"].([]interface{})
	caseSensitive, _ := g.Config["case_sensitive"].(bool)

	checkText := text
	if !caseSensitive {
		checkText = strings.ToLower(text)
	}

	for _, bRaw := range blockedRaw {
		word, ok := bRaw.(string)
		if !ok {
			continue
		}
		checkWord := word
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
// Config: { "patterns": ["email", "phone", "ssn", "credit_card"] }
// If "patterns" is empty, all built-in patterns are checked.

var builtInPIIPatterns = map[string]*regexp.Regexp{
	"email":       regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`),
	"phone":       regexp.MustCompile(`(\+?1[-.\s]?)?\(?\d{3}\)?[-.\s]?\d{3}[-.\s]?\d{4}`),
	"ssn":         regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`),
	"credit_card": regexp.MustCompile(`\b(?:\d{4}[-\s]?){3}\d{4}\b`),
}

func evalPIIDetection(g models.Guardrail, text string, stage string) models.GuardrailResult {
	patternsRaw, _ := g.Config["patterns"].([]interface{})

	// Determine which patterns to check
	var patternsToCheck []string
	if len(patternsRaw) > 0 {
		for _, p := range patternsRaw {
			if s, ok := p.(string); ok {
				patternsToCheck = append(patternsToCheck, s)
			}
		}
	} else {
		// Check all built-in patterns
		for k := range builtInPIIPatterns {
			patternsToCheck = append(patternsToCheck, k)
		}
	}

	for _, name := range patternsToCheck {
		re, ok := builtInPIIPatterns[name]
		if !ok {
			continue
		}
		if re.MatchString(text) {
			return models.GuardrailResult{
				Passed:  false,
				Kind:    g.Kind,
				Stage:   stage,
				Message: "PII detected: " + name + " pattern matched",
			}
		}
	}

	return models.GuardrailResult{Passed: true, Kind: g.Kind, Stage: stage}
}

// ── Topic Restriction ───────────────────────────────────────
// Config: { "allowed_topics": [...], "blocked_topics": [...] }
// Keyword-based matching. If allowed_topics is set, text must contain
// at least one allowed topic keyword to pass. blocked_topics always blocks.

func evalTopicRestriction(g models.Guardrail, text string, stage string) models.GuardrailResult {
	lower := strings.ToLower(text)

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

func evalRegexFilter(g models.Guardrail, text string, stage string) models.GuardrailResult {
	pattern, _ := g.Config["pattern"].(string)
	if pattern == "" {
		return models.GuardrailResult{Passed: true, Kind: g.Kind, Stage: stage}
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return models.GuardrailResult{
			Passed:  true,
			Kind:    g.Kind,
			Stage:   stage,
			Message: "Invalid regex pattern: " + err.Error(),
		}
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

func evalPromptInjection(g models.Guardrail, text string, stage string) models.GuardrailResult {
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

// Effective returns the guardrails that apply to an agent: its own rules (own — its list,
// or that list adjusted by an environment policy) with the kitchen's workspace rules from
// src applied on top (MergeWithWorkspace). With no source, or no workspace rules for the
// kitchen, it is own unchanged.
//
// It fails closed. If the source cannot be read, or any enabled workspace rule is
// unreadable, it returns a *PolicyError and the caller must refuse to run the agent.
func Effective(ctx context.Context, src contracts.WorkspaceGuardrailSource, kitchen, agent string, own []models.Guardrail) ([]models.Guardrail, error) {
	if src == nil {
		return own, nil
	}
	fail := func(err error) ([]models.Guardrail, error) {
		return nil, &PolicyError{Kitchen: kitchen, Agent: agent, Err: err}
	}
	workspace, err := src.WorkspaceGuardrails(ctx, kitchen)
	if err != nil {
		return fail(err)
	}
	if len(workspace) == 0 {
		return own, nil
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
	return MergeWithWorkspace(workspace, own, exceptions), nil
}

// Validate reports why a guardrail could not do its job as configured: an unknown kind or
// stage, or a config the evaluator would read as "nothing to check" (which it passes
// silently). Effective applies it to enabled workspace rules so that one that cannot work
// stops the agent instead of quietly doing nothing.
func Validate(g models.Guardrail) error {
	switch g.Stage {
	case "", models.GuardrailStageInput, models.GuardrailStageOutput, models.GuardrailStageBoth:
	default:
		return fmt.Errorf("unknown stage %q", g.Stage)
	}
	switch g.Kind {
	case models.GuardrailContentFilter:
		return needStrings(g.Config, "blocked_words")
	case models.GuardrailPIIDetection:
		raw, ok := g.Config["patterns"]
		if !ok {
			return nil // no list means all built-in patterns
		}
		list, ok := raw.([]interface{})
		if !ok {
			return fmt.Errorf("patterns must be a list of pattern names")
		}
		for _, p := range list {
			name, _ := p.(string)
			if _, known := builtInPIIPatterns[name]; !known {
				return fmt.Errorf("unknown PII pattern %v", p)
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
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("invalid pattern: %w", err)
		}
		return nil
	case models.GuardrailPromptInjection, models.GuardrailCustom:
		return nil
	case models.GuardrailLlamaGuard:
		if endpoint, _ := g.Config["endpoint"].(string); endpoint == "" {
			return fmt.Errorf("needs an endpoint")
		}
		return nil
	default:
		return fmt.Errorf("unknown guardrail kind")
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
