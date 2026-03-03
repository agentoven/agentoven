// Package ctxwindow provides shared context-window management utilities
// used by both the Executor (agentic invoke) and SendSessionMessage (session handler).
package ctxwindow

import (
	"fmt"
	"strings"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// DefaultBudget is the default context window budget when not set on the agent.
const DefaultBudget = 16000

// EstimateTokens returns a rough token count for a string.
// Uses the ~4 chars per token heuristic. Replace with tiktoken-go for accuracy.
func EstimateTokens(text string) int {
	if len(text) == 0 {
		return 0
	}
	return len(text) / 4
}

// EstimateTokensForMessages returns the total estimated tokens across all messages.
func EstimateTokensForMessages(messages []models.ChatMessage) int {
	total := 0
	for _, msg := range messages {
		total += EstimateTokens(msg.Content) + 4
	}
	return total
}

// EffectiveBudget returns the context budget to use for an agent.
// Prefers agent.ContextBudget, falls back to modelLimit, then DefaultBudget.
func EffectiveBudget(agentBudget int, modelLimit int) int {
	if agentBudget > 0 {
		return agentBudget
	}
	if modelLimit > 0 {
		return modelLimit
	}
	return DefaultBudget
}

// BuildReport computes a ContextBudgetReport for the given state.
func BuildReport(modelLimit, budget, usedTokens int, summarised bool, cacheHits, tokensSaved int) *models.ContextBudgetReport {
	remaining := budget - usedTokens
	if remaining < 0 {
		remaining = 0
	}
	pct := 0.0
	if budget > 0 {
		pct = float64(usedTokens) / float64(budget) * 100.0
		pct = float64(int(pct*10)) / 10.0
	}
	return &models.ContextBudgetReport{
		ModelLimit:     modelLimit,
		Budget:         budget,
		Used:           usedTokens,
		Remaining:      remaining,
		UtilizationPct: pct,
		Summarised:     summarised,
		CacheHits:      cacheHits,
		TokensSaved:    tokensSaved,
	}
}

// MarkCacheBreakpoints adds CacheControl hints to messages for prompt caching.
// System messages and the first message (often a context summary) get marked
// with "ephemeral" which Anthropic uses to create cache breakpoints.
// For OpenAI, prefix caching is automatic so these hints are ignored.
func MarkCacheBreakpoints(messages []models.ChatMessage) []models.ChatMessage {
	if len(messages) == 0 {
		return messages
	}

	result := make([]models.ChatMessage, len(messages))
	copy(result, messages)

	lastSystemIdx := -1
	for i := range result {
		if result[i].Role == "system" {
			lastSystemIdx = i
		}
	}
	if lastSystemIdx >= 0 {
		result[lastSystemIdx].CacheControl = "ephemeral"
	}

	return result
}

// TrimToSlidingWindow keeps the system prompt and as many recent messages
// as fit within the budget. Returns (kept, toSummarize).
// The caller is responsible for generating the actual summary.
func TrimToSlidingWindow(allMessages []models.ChatMessage, budget int) (kept []models.ChatMessage, toSummarize []models.ChatMessage) {
	totalTokens := EstimateTokensForMessages(allMessages)
	if totalTokens <= budget {
		return allMessages, nil
	}

	var systemMsgs []models.ChatMessage
	var convMsgs []models.ChatMessage
	for _, msg := range allMessages {
		if msg.Role == "system" {
			systemMsgs = append(systemMsgs, msg)
		} else {
			convMsgs = append(convMsgs, msg)
		}
	}

	systemTokens := EstimateTokensForMessages(systemMsgs)
	availableForConv := budget - systemTokens - 500

	if availableForConv <= 0 {
		return allMessages, nil
	}

	var recentMsgs []models.ChatMessage
	recentTokens := 0
	for i := len(convMsgs) - 1; i >= 0; i-- {
		msgTokens := EstimateTokens(convMsgs[i].Content) + 4
		if recentTokens+msgTokens > availableForConv {
			break
		}
		recentMsgs = append([]models.ChatMessage{convMsgs[i]}, recentMsgs...)
		recentTokens += msgTokens
	}

	numRecent := len(recentMsgs)
	numToSummarize := len(convMsgs) - numRecent
	if numToSummarize <= 0 {
		return allMessages, nil
	}

	toSummarize = convMsgs[:numToSummarize]

	kept = make([]models.ChatMessage, 0, len(systemMsgs)+len(recentMsgs))
	kept = append(kept, systemMsgs...)
	kept = append(kept, recentMsgs...)

	return kept, toSummarize
}

// FormatSummaryMessage builds the summary text for summarizable messages.
// This is a simple text-based summary (no LLM call). Use this as fallback.
func FormatSummaryMessage(messages []models.ChatMessage) string {
	if len(messages) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("[Earlier conversation summary]\n")
	for _, msg := range messages {
		text := msg.Content
		if len(text) > 200 {
			text = text[:200] + "..."
		}
		sb.WriteString(fmt.Sprintf("%s: %s\n", msg.Role, text))
	}
	result := sb.String()
	if len(result) > 2000 {
		return result[:2000] + "..."
	}
	return result
}
