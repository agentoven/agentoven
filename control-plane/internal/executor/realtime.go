package executor

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// RealtimeSetup returns what a realtime voice session needs from an agent: its
// system prompt (the same one a text turn would get, minus the user message)
// and its tool definitions. The realtime provider runs the conversation; the
// harness keeps owning what the agent is and what it can do.
func (e *Executor) RealtimeSetup(ctx context.Context, agent *models.Agent, resolved *models.ResolvedIngredients, promptVars map[string]string) (string, []models.ToolDefinition) {
	prompt := e.buildSystemPrompt(ctx, agent, resolved, "", promptVars)
	return prompt, e.buildToolDefinitions(agent, allResolvedTools(resolved))
}

// RunTool executes one tool call the realtime model made, with the same
// argument validation, guardrails, timeout, and delegation limits as a text
// turn. It returns the output text and whether it is an error, so the model
// sees failures and can recover.
//
// A tool the agent lists in ApprovalTools is refused: approval pauses a run
// until a human decides, and a live voice call has no way to wait that long.
func (e *Executor) RunTool(ctx context.Context, agent *models.Agent, resolved *models.ResolvedIngredients, callID, name, argsJSON string) (string, bool) {
	if requiresApproval(agent, name) {
		return fmt.Sprintf("the %s tool needs human approval and cannot run during a live voice session", name), true
	}
	args := map[string]interface{}{}
	if argsJSON != "" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "tool arguments were not valid JSON: " + err.Error(), true
		}
	}
	schemas := make(map[string]map[string]interface{})
	for _, t := range allResolvedTools(resolved) {
		schemas[t.Name] = t.Schema
	}
	res := e.dispatchToolCall(ensureDelegationRoot(ctx, agent.Name), agent, ToolCall{ID: callID, Name: name, Arguments: args}, schemas[name])
	return res.Content, res.IsError
}
