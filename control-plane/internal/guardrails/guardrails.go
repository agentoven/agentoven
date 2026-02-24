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
	"regexp"
	"strings"
	"unicode/utf8"

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
