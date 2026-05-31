package guardrails

// LlamaGuard 3 content-safety evaluator.
//
// Calls any OpenAI-compatible chat-completions endpoint backed by a
// LlamaGuard 3 model.  Works with:
//   - Ollama:       endpoint = "http://ollama:11434",  model = "llama-guard3:8b"
//   - Together.ai:  endpoint = "https://api.together.xyz", model = "meta-llama/LlamaGuard-2-8b"
//   - Groq:         endpoint = "https://api.groq.com/openai", model = "llama-guard-3-8b"
//   - Self-hosted vLLM: endpoint = "http://vllm:8000"
//
// Config keys on the Guardrail:
//
//	endpoint    (required) base URL of the inference server
//	model       (optional) defaults to "llama-guard3:1b" (CPU-friendly sidecar);
//	            use "llama-guard3:8b" for higher accuracy when a GPU is available
//	api_key     (optional) Bearer token for hosted endpoints
//	timeout_secs (optional) request timeout, default 10
//	categories  (optional) comma-separated subset e.g. "S1,S10,S11"; default = all 14
//	fail_open   (optional) bool; true = pass through on network errors (default false = fail-closed)

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// defaultLlamaGuardPolicy lists all 14 hazard categories defined in LlamaGuard 3.
const defaultLlamaGuardPolicy = `S1: Violent Crimes.
S2: Non-Violent Crimes.
S3: Sex Crimes.
S4: Child Exploitation.
S5: Defamation.
S6: Specialised Advice.
S7: Privacy.
S8: Intellectual Property.
S9: Indiscriminate Weapons.
S10: Hate.
S11: Self-Harm.
S12: Sexual Content.
S13: Elections.
S14: Code Interpreter Abuse.`

// llamaGuardSystemTemplate is the exact prompt structure LlamaGuard 3 expects.
const llamaGuardSystemTemplate = `Task: Check if there is unsafe content in the 'User' message according to the safety policy with the below categories.
<BEGIN UNSAFE CONTENT CATEGORIES>
%s
<END UNSAFE CONTENT CATEGORIES>
Provide your safety assessment for ONLY THE LAST User message in the above conversation:
 - First line must read 'safe' or 'unsafe'.
 - If unsafe, a second line must include a comma-separated list of violated categories.`

// evalLlamaGuard calls a LlamaGuard-compatible inference endpoint.
// It returns fail-closed (blocked) by default when the server is unreachable,
// unless fail_open=true is set in the guardrail config.
func evalLlamaGuard(g models.Guardrail, text, stage string) models.GuardrailResult {
	endpoint, _ := g.Config["endpoint"].(string)
	if endpoint == "" {
		return llmGuardError(g, stage, "llamaguard: endpoint not configured in guardrail config")
	}

	model, _ := g.Config["model"].(string)
	if model == "" {
		model = "llama-guard3:1b" // 1B: ~700 MB RAM, runs on CPU; set "llama-guard3:8b" for GPU
	}

	timeoutSecs := 10
	if ts, ok := g.Config["timeout_secs"].(float64); ok && ts > 0 {
		timeoutSecs = int(ts)
	}

	failOpen, _ := g.Config["fail_open"].(bool)

	// Build the safety policy — optionally restrict to a subset of categories.
	policy := buildPolicy(g.Config)

	systemPrompt := fmt.Sprintf(llamaGuardSystemTemplate, policy)

	// OpenAI-compat chat completions request body.
	reqBody := map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": text},
		},
		"max_tokens":  20,
		"temperature": 0,
		"stream":      false,
	}

	raw, err := json.Marshal(reqBody)
	if err != nil {
		return transientError(g, stage, "llamaguard: marshal: "+err.Error(), failOpen)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSecs)*time.Second)
	defer cancel()

	url := strings.TrimRight(endpoint, "/") + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return transientError(g, stage, "llamaguard: build request: "+err.Error(), failOpen)
	}
	req.Header.Set("Content-Type", "application/json")

	if apiKey, ok := g.Config["api_key"].(string); ok && apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: time.Duration(timeoutSecs+1) * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return transientError(g, stage, "llamaguard: http: "+err.Error(), failOpen)
	}
	defer resp.Body.Close()

	var respBody struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
		return transientError(g, stage, "llamaguard: decode: "+err.Error(), failOpen)
	}
	if respBody.Error != nil {
		return transientError(g, stage, "llamaguard: api error: "+respBody.Error.Message, failOpen)
	}
	if len(respBody.Choices) == 0 {
		return transientError(g, stage, "llamaguard: empty choices", failOpen)
	}

	return parseVerdict(g, stage, respBody.Choices[0].Message.Content)
}

// parseVerdict interprets LlamaGuard's response:
//
//	"safe"          → pass
//	"unsafe\nS1,S10" → block with category list
func parseVerdict(g models.Guardrail, stage, content string) models.GuardrailResult {
	verdict := strings.TrimSpace(strings.ToLower(content))
	lines := strings.SplitN(verdict, "\n", 2)

	if lines[0] == "safe" {
		return models.GuardrailResult{
			Passed: true,
			Kind:   models.GuardrailLlamaGuard,
			Stage:  stage,
		}
	}

	// unsafe
	msg := "llamaguard: content classified as unsafe"
	if len(lines) == 2 {
		cats := strings.TrimSpace(lines[1])
		if cats != "" {
			msg += " (categories: " + cats + ")"
		}
	}
	return models.GuardrailResult{
		Passed:  false,
		Kind:    models.GuardrailLlamaGuard,
		Stage:   stage,
		Message: msg,
	}
}

// buildPolicy returns the safety policy string, filtered to the categories
// listed in config["categories"] if set (e.g. "S1,S10,S11").
func buildPolicy(cfg map[string]interface{}) string {
	cats, _ := cfg["categories"].(string)
	if cats == "" {
		return defaultLlamaGuardPolicy
	}

	allowed := make(map[string]struct{})
	for _, c := range strings.Split(cats, ",") {
		allowed[strings.ToUpper(strings.TrimSpace(c))] = struct{}{}
	}

	var lines []string
	for _, line := range strings.Split(defaultLlamaGuardPolicy, "\n") {
		prefix := strings.SplitN(line, ":", 2)[0]
		if _, ok := allowed[prefix]; ok {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return defaultLlamaGuardPolicy // fallback: all categories if filter was wrong
	}
	return strings.Join(lines, "\n")
}

// llmGuardError returns a permanent (config) error — always fail-closed.
func llmGuardError(g models.Guardrail, stage, message string) models.GuardrailResult {
	return models.GuardrailResult{
		Passed:  false,
		Kind:    models.GuardrailLlamaGuard,
		Stage:   stage,
		Message: message,
	}
}

// transientError returns a result for network/runtime errors, respecting fail_open.
func transientError(g models.Guardrail, stage, message string, failOpen bool) models.GuardrailResult {
	return models.GuardrailResult{
		Passed:  failOpen, // fail-closed by default; set fail_open=true to pass-through on errors
		Kind:    models.GuardrailLlamaGuard,
		Stage:   stage,
		Message: message,
	}
}
