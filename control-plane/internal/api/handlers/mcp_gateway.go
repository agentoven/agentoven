package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// A call that comes in through POST /mcp is made by a person or a key, not by an agent run, so nothing
// of what makes an agent run safe has happened to it yet. This is what the endpoint adds before the
// gateway runs the tool: the kitchen's workspace guardrails on the arguments (the same direction and
// the same rules as an agent's own tool call), and an audit event naming who called what.

// guardMCPCall judges the arguments of a tools/call against the kitchen's workspace rules and returns the
// request to run, with redactions applied. A non-nil response is the JSON-RPC refusal to send instead. It
// fails closed: rules that cannot be established, or an evaluator that errors, stop the call.
func (h *Handlers) guardMCPCall(ctx context.Context, kitchen string, req *models.MCPRequest, params *models.MCPToolCallParams) (*models.MCPRequest, *models.MCPResponse) {
	refuse := func(code int, msg, data string) (*models.MCPRequest, *models.MCPResponse) {
		return nil, &models.MCPResponse{Jsonrpc: "2.0", Error: &models.MCPError{Code: code, Message: msg, Data: data}, ID: req.ID}
	}
	if h.Guardrails == nil {
		return req, nil
	}
	rules, err := h.workspaceRules(ctx, kitchen)
	if err != nil {
		log.Error().Err(err).Str("kitchen", kitchen).Msg("MCP tool call refused: workspace guardrails could not be applied")
		h.emitGuardrailAudit(ctx, kitchen, "", "policy", nil, err)
		return refuse(-32603, "Guardrail policy unavailable", "the guardrail policy could not be applied, so the tool was not called")
	}
	if len(rules) == 0 || len(params.Arguments) == 0 {
		return req, nil
	}
	raw, _ := json.Marshal(params.Arguments)
	eval, err := h.Guardrails.EvaluateOutput(ctx, rules, string(raw))
	if err != nil || eval == nil {
		h.emitGuardrailAudit(ctx, kitchen, "", "tool_args", nil, err)
		return refuse(-32603, "Guardrail evaluation failed", "the arguments could not be checked, so the tool was not called")
	}
	if !eval.Passed {
		h.emitGuardrailAudit(ctx, kitchen, "", "tool_args", eval, nil)
		return refuse(-32005, "Blocked by guardrails", "the tool call arguments were blocked by a guardrail")
	}
	h.noteGuardrails(ctx, kitchen, "", "tool_args", eval)
	if eval.Redacted == 0 {
		return req, nil
	}
	var args map[string]interface{}
	if json.Unmarshal([]byte(eval.Text), &args) != nil {
		return refuse(-32005, "Blocked by guardrails", "the redaction of the tool call arguments could not be applied")
	}
	params.Arguments = args
	rewritten, _ := json.Marshal(params)
	out := *req
	out.Params = rewritten
	return &out, nil
}

// auditMCPCall records a tool call made through the gateway endpoint: who, which tool, how it ended.
// Arguments and results are never in the event.
func (h *Handlers) auditMCPCall(ctx context.Context, kitchen, tool, outcome string) {
	if h.Store == nil {
		return
	}
	userID, userEmail, displayName, provider, actorType := auditActorFromIdentity(ctx)
	status := http.StatusOK
	if outcome != "ok" {
		status = http.StatusForbidden
	}
	if err := h.Store.CreateAuditEvent(ctx, &models.AuditEvent{
		ID: uuid.New().String(), Timestamp: time.Now().UTC(),
		UserID: userID, UserEmail: userEmail, ActorType: actorType, ActorDisplayName: displayName,
		Action: "mcp.tool_call", Resource: "tool", ResourceID: tool, Kitchen: kitchen,
		Details:        map[string]interface{}{"tool": tool, "outcome": outcome, "provider": provider},
		ResponseStatus: status, RegulationTags: []string{"access"}, DataClassification: "internal",
	}); err != nil {
		log.Warn().Err(err).Str("tool", tool).Msg("could not audit an MCP tool call")
	}
}

// mcpOutcome says how a tools/call ended, from the gateway's answer.
func mcpOutcome(resp *models.MCPResponse) string {
	switch {
	case resp == nil:
		return "ok"
	case resp.Error != nil:
		return "refused"
	}
	if res, ok := resp.Result.(*models.MCPToolResult); ok && res != nil && res.IsError {
		return "error"
	}
	if res, ok := resp.Result.(models.MCPToolResult); ok && res.IsError {
		return "error"
	}
	return "ok"
}
