package handlers_test

import (
	"context"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/internal/guardrails"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func TestRunAgentTest_InputGuardrailBlocksBeforeRouting(t *testing.T) {
	s := store.NewMemoryStore()
	t.Cleanup(func() { s.Close() })

	h := &handlers.Handlers{
		Store:      s,
		Guardrails: &guardrails.CommunityGuardrailService{},
	}
	agent := &models.Agent{
		Name:          "guarded-agent",
		Kitchen:       "default",
		Mode:          models.AgentModeManaged,
		ModelProvider: "my-openai",
		ModelName:     "gpt-5-mini",
		Guardrails: []models.Guardrail{
			{
				ID:      "block-secret",
				Kind:    models.GuardrailContentFilter,
				Stage:   models.GuardrailStageInput,
				Enabled: true,
				Config: map[string]interface{}{
					"blocked_words": []interface{}{"forbidden"},
				},
			},
		},
	}
	if err := s.CreateAgent(context.Background(), agent); err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}

	_, err := h.RunAgentTest(context.Background(), "default", "guarded-agent", "this contains forbidden text")
	if err == nil {
		t.Fatal("RunAgentTest() error = nil, want guardrail block")
	}
	if !strings.Contains(err.Error(), "input blocked by guardrails") {
		t.Fatalf("RunAgentTest() error = %q, want guardrail block", err.Error())
	}
}
