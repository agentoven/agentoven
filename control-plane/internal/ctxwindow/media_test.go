package ctxwindow

import (
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// A message that carries a file must never count as free, or a conversation
// that keeps attaching files would not be trimmed until the provider refused it.
func TestMediaPartsCountTowardsTheBudget(t *testing.T) {
	text := models.ChatMessage{Role: "user", Content: "look"}
	withImage := text
	withImage.ContentParts = []models.ContentPart{
		{Type: "image", Media: &models.MediaRef{MimeType: "image/png", BlobRef: "abc"}},
		{Type: "text", Text: "and this"},
	}
	if d := EstimateMessageTokens(withImage) - EstimateMessageTokens(text); d < MediaPartTokens {
		t.Fatalf("an attached image should add at least %d tokens, added %d", MediaPartTokens, d)
	}
	if EstimateTokensForMessages([]models.ChatMessage{withImage}) != EstimateMessageTokens(withImage) {
		t.Fatal("the conversation estimate must use the per-message estimate")
	}
}
