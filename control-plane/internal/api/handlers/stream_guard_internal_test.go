package handlers

import (
	"regexp"
	"testing"
)

// A real Deep Agents run streamed the key once as a token and again in the "done" frame. Joined
// without a separator the two copies ran together and the \b in the pattern stopped matching, so
// a held-back stream released a reply the rule should have blocked.
func TestSSEVisibleTextsKeepsTheStreamedReplyAndTheFinalFrameApart(t *testing.T) {
	body := []byte("data: {\"type\": \"token\", \"content\": \"AKIAIOSFODNN7EXAMPLE\"}\n\n" +
		"data: {\"type\": \"done\", \"content\": \"AKIAIOSFODNN7EXAMPLE\", \"usage\": {\"total_tokens\": 9}}\n\n")
	got := sseVisibleTexts(body)
	if len(got) != 2 || got[0] != "AKIAIOSFODNN7EXAMPLE" || got[1] != "AKIAIOSFODNN7EXAMPLE" {
		t.Fatalf("each channel must be checked on its own, got %q", got)
	}
	re := regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)
	for _, text := range got {
		if !re.MatchString(text) {
			t.Fatalf("the rule must match %q", text)
		}
	}
}

func TestSSEVisibleTextsJoinsChunksOfOneReplyAndIgnoresTraces(t *testing.T) {
	body := []byte("data: {\"type\": \"token\", \"content\": \"AKIA\"}\n\n" +
		"data: {\"type\": \"token\", \"content\": \"IOSFODNN7EXAMPLE\"}\n\n" +
		"data: {\"choices\": [{\"delta\": {\"content\": \"!\"}}]}\n\n" +
		"data: {\"type\": \"done\", \"usage\": {\"input_tokens\": 3}, \"trace\": {\"input\": \"the user's own words\"}}\n\n")
	got := sseVisibleTexts(body)
	if len(got) != 1 || got[0] != "AKIAIOSFODNN7EXAMPLE!" {
		t.Fatalf("chunks of one reply are joined, a trace is not read, got %q", got)
	}
}
