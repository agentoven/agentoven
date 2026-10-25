package executor

import (
	"context"
	"encoding/json"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

var _ contracts.ToolRunner = (*Executor)(nil)

// ToolsFor lists the tools an agent may call: its own resolved tools and its skills' tools, plus
// agentoven_delegate when it names Subagents. This is the allowlist RunToolCall enforces.
func (e *Executor) ToolsFor(agent *models.Agent, resolved *models.ResolvedIngredients) []contracts.ToolInfo {
	if resolved == nil {
		return nil
	}
	var out []contracts.ToolInfo
	seen := map[string]bool{}
	add := func(t models.ResolvedTool, source string) {
		if seen[t.Name] {
			return
		}
		seen[t.Name] = true
		desc, _ := t.Schema["description"].(string)
		out = append(out, contracts.ToolInfo{Name: t.Name, Description: desc, Schema: t.Schema, Source: source})
	}
	for _, t := range resolved.Tools {
		add(t, "tool")
	}
	for _, sk := range resolved.Skills {
		for _, t := range sk.Tools {
			add(t, "skill:"+sk.Name)
		}
	}
	if len(agent.Subagents) > 0 {
		out = append(out, contracts.ToolInfo{
			Name:        "agentoven_delegate",
			Description: "Hand a subtask to another agent in the same kitchen and get its answer.",
			Source:      "builtin:delegate",
			Schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"agent":   map[string]interface{}{"type": "string", "enum": toInterfaceSlice(agent.Subagents)},
					"message": map[string]interface{}{"type": "string"},
				},
				"required": []interface{}{"agent", "message"},
			},
		})
	}
	return out
}

// RunToolCall runs one tool call through dispatchToolCall, the same path a turn of the Go loop
// takes, after checking the name against ToolsFor.
func (e *Executor) RunToolCall(ctx context.Context, agent *models.Agent, resolved *models.ResolvedIngredients, callID, name string, argsJSON []byte) contracts.ToolRunResult {
	var schema map[string]interface{}
	found := false
	for _, t := range e.ToolsFor(agent, resolved) {
		if t.Name == name {
			schema, found = t.Schema, true
			break
		}
	}
	if !found {
		return contracts.ToolRunResult{Unknown: true, IsError: true, Content: "no such tool: " + name}
	}
	if name == "agentoven_delegate" {
		schema = nil // the virtual tool is validated by its own handler
	}
	args := map[string]interface{}{}
	if len(argsJSON) > 0 {
		if err := json.Unmarshal(argsJSON, &args); err != nil {
			return contracts.ToolRunResult{IsError: true, Content: "tool arguments were not valid JSON: " + err.Error()}
		}
	}
	res := e.dispatchToolCall(ensureDelegationRoot(ctx, agent.Name), agent, ToolCall{ID: callID, Name: name, Arguments: args}, schema)
	return contracts.ToolRunResult{Content: res.Content, IsError: res.IsError}
}
