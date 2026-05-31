package server

import (
	"context"
	"fmt"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// RunAgentTest executes a single-shot agent test in-process for internal callers
// such as the Pro test suite runner.
func (s *Server) RunAgentTest(ctx context.Context, kitchen, agentName, message string) (*models.AgentTestResult, error) {
	if s.Handlers == nil {
		return nil, fmt.Errorf("handlers not initialized")
	}
	return s.Handlers.RunAgentTest(ctx, kitchen, agentName, message)
}
