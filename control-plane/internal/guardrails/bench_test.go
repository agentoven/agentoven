package guardrails

import (
	"context"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// A text that is mostly digits, spaces and letters is the worst case for the detectors: every
// candidate pattern has something to try at most positions.
func BenchmarkAllDetectorsOnOneMiB(b *testing.B) {
	text := strings.Repeat("order 4111 1111 1111 1112 ref AB12 3456 7890 ann@example.com 10.0.0.1 eyJ ", 30000)[:1<<20]
	s := &CommunityGuardrailService{}
	rule := models.Guardrail{ID: "a", Kind: models.GuardrailPIIDetection, Stage: models.GuardrailStageInput, Action: models.GuardrailActionRedact, Enabled: true,
		Config: map[string]interface{}{"patterns": []interface{}{"all"}}}
	b.SetBytes(int64(len(text)))
	for i := 0; i < b.N; i++ {
		if _, err := s.Evaluate(context.Background(), []models.Guardrail{rule}, text, models.GuardrailStageInput); err != nil {
			b.Fatal(err)
		}
	}
}
