package router

import (
	"regexp"
	"strings"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// defaultThinkingBudget is the thinking budget, in tokens, for a request that turns thinking on
// without naming one.
const defaultThinkingBudget = 8192

// minAnthropicThinking is the smallest budget Anthropic accepts.
const minAnthropicThinking = 1024

// thinkingBudget is the budget a request asks for.
func thinkingBudget(req *models.RouteRequest) int {
	if req.ThinkingBudget > 0 {
		return req.ThinkingBudget
	}
	return defaultThinkingBudget
}

// reasoningEffort maps a token budget onto the low/medium/high scale OpenAI-style APIs use.
func reasoningEffort(budget int) string {
	switch {
	case budget <= 2048:
		return "low"
	case budget <= 8192:
		return "medium"
	default:
		return "high"
	}
}

var reasoningModel = regexp.MustCompile(`(^|[/-])(o[1-9]|gpt-5|deepseek-r1|qwq|gpt-oss|qwen3|magistral|grok-[34]|claude-(3-7|opus-4|sonnet-4|haiku-4))`)

// canThink reports whether a model is known to take a reasoning setting: the catalog says it
// supports thinking, or its name is one of the reasoning families. A request that turns thinking on
// for any other model is sent as it would be without it, rather than risk the provider refusing a
// parameter the model does not have.
func (mr *ModelRouter) canThink(kind, model string) bool {
	if mr.catalog != nil {
		if c := mr.catalog.Lookup(kind, model); c != nil && c.SupportsThinking {
			return true
		}
	}
	return reasoningModel.MatchString(strings.ToLower(model))
}

// thinkTags splits the <think>…</think> reasoning that some models (DeepSeek-R1 family, QwQ, local
// models) write into the answer text. It returns the answer without them and one block per tag.
var thinkTags = regexp.MustCompile(`(?s)<think>(.*?)</think>`)

func splitThinkTags(content, model, provider string) (string, []models.ThinkingBlock) {
	matches := thinkTags.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return content, nil
	}
	var blocks []models.ThinkingBlock
	for _, m := range matches {
		if text := strings.TrimSpace(m[1]); text != "" {
			blocks = append(blocks, models.ThinkingBlock{Content: text, Model: model, Provider: provider, Timestamp: time.Now().UTC()})
		}
	}
	return strings.TrimSpace(thinkTags.ReplaceAllString(content, "")), blocks
}

// openAIMessageBody is the reasoning-bearing part of an OpenAI-style response message. Providers
// disagree on the field: DeepSeek and LiteLLM use reasoning_content, OpenRouter, vLLM and Ollama use
// reasoning, and OpenRouter can also return reasoning_details.
type openAIReasoning struct {
	ReasoningContent string `json:"reasoning_content,omitempty"`
	Reasoning        string `json:"reasoning,omitempty"`
	ReasoningDetails []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Summary string `json:"summary"`
	} `json:"reasoning_details,omitempty"`
}

func (r openAIReasoning) text() string {
	if r.ReasoningContent != "" {
		return r.ReasoningContent
	}
	if r.Reasoning != "" {
		return r.Reasoning
	}
	var parts []string
	for _, d := range r.ReasoningDetails {
		switch {
		case d.Text != "":
			parts = append(parts, d.Text)
		case d.Summary != "":
			parts = append(parts, d.Summary)
		}
	}
	return strings.Join(parts, "\n")
}

// openAIResult reads an OpenAI-style response (OpenAI, Azure, OpenRouter, LiteLLM, Ollama's
// compatible endpoint) into the pieces the router returns: the answer, the model's reasoning, tool
// calls and usage. Usage counts the prompt in full (cached tokens included) and the output in full
// (thinking tokens included), which is how these providers bill, with the cached and thinking parts
// broken out so they can be priced and shown.
func openAIResult(resp openAIResponse, model, provider string) (content, finish string, thinking []models.ThinkingBlock, calls []models.ToolCallResult, usage models.TokenUsage) {
	reasoningTokens := resp.Usage.CompletionTokensDetails.ReasoningTokens
	if reasoningTokens == 0 {
		reasoningTokens = resp.Usage.ReasoningTokens
	}
	cached := resp.Usage.PromptTokensDetails.CachedTokens
	if cached == 0 {
		cached = resp.Usage.PromptCacheHitTokens
	}
	if len(resp.Choices) > 0 {
		c := resp.Choices[0]
		content, finish, calls = c.Message.Content, c.FinishReason, c.Message.ToolCalls
		if text := c.Message.text(); text != "" {
			thinking = append(thinking, models.ThinkingBlock{Content: text, TokenCount: reasoningTokens, Model: model, Provider: provider, Timestamp: time.Now().UTC()})
		}
		var tagged []models.ThinkingBlock
		content, tagged = splitThinkTags(content, model, provider)
		thinking = append(thinking, tagged...)
	}
	usage = models.TokenUsage{
		InputTokens:    resp.Usage.PromptTokens,
		OutputTokens:   resp.Usage.CompletionTokens,
		TotalTokens:    resp.Usage.TotalTokens,
		ThinkingTokens: reasoningTokens,
		CachedTokens:   cached,
	}
	if cached > 0 {
		usage.CacheHits = 1
	}
	return
}
