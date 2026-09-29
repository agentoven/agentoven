// Package agentexec is the public entry point to AgentOven's managed-agent
// executor.
//
// The executor itself lives under internal/, which Go's visibility rule keeps
// closed to other modules. This package is the curated surface across that
// boundary — the same approach pkg/contracts already takes for the store — so
// an embedder can run an agent's agentic loop, and supply its own tool gateway,
// without reaching into internals.
//
// The tool gateway is the interesting parameter. Anything that can answer a
// tools/call can stand in for the real MCP gateway, which is what lets a caller
// run an agent against substituted tools while the executor stays unaware that
// anything was substituted.
package agentexec

import (
	"context"

	"github.com/agentoven/agentoven/control-plane/internal/executor"
	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// Types aliased so callers outside this module can name them. Aliases cross the
// internal boundary where imports cannot.
type (
	// ToolGateway dispatches a tool call and returns the result.
	ToolGateway = executor.ToolGateway
	// ExecutionTrace records the full execution history of one Execute call.
	ExecutionTrace = executor.ExecutionTrace
	// Turn is one iteration of the agentic loop.
	Turn = executor.Turn
	// ToolCall is a tool invocation requested by the model.
	ToolCall = executor.ToolCall
	// ToolResult is the result of executing a tool call.
	ToolResult = executor.ToolResult
	// ModelRouter routes LLM requests to configured providers.
	ModelRouter = router.ModelRouter
	// Store is the control plane's persistence interface.
	Store = store.Store
)

// Runner runs a managed agent through its agentic loop: prompt, model, tool,
// repeat, until a text response or the turn cap.
type Runner interface {
	Execute(
		ctx context.Context,
		agent *models.Agent,
		userMessage string,
		resolved *models.ResolvedIngredients,
		promptVars map[string]string,
		thinkingEnabled bool,
		sessionID ...string,
	) (string, *ExecutionTrace, error)
}

// New creates a Runner. Pass a custom ToolGateway to route the agent's tool
// calls somewhere other than the real MCP gateway; pass the gateway itself for
// ordinary execution.
func New(s Store, r *ModelRouter, gw ToolGateway, sess contracts.SessionStore) Runner {
	return executor.NewExecutor(s, r, gw, sess)
}
