package contracts

import (
	"context"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// Two seams for tools that are installed rather than hand-registered (ADR-0031). The community
// edition installs neither: it serves tools that live behind an HTTP or MCP endpoint. An edition
// that ships tool packs (Pro) installs both.

// NativeToolRunner runs a tool whose Transport is models.MCPTransportNative: a tool that has no
// remote endpoint because the control plane itself talks to the system (a REST API described by an
// OpenAPI document, a database, a mail server). The gateway has already checked that the tool is
// enabled and, for a skill's tool, that the skill is active; the runner owns everything past that:
// reading the credentials the tool names, refusing destinations the kitchen has not allowed,
// bounding time and size, and keeping secrets out of what it returns.
type NativeToolRunner interface {
	RunNativeTool(ctx context.Context, tool *models.MCPTool, args map[string]interface{}) (*models.MCPToolResult, error)
}

// ToolApprovalPolicy says which tools need a person's approval before they run, on top of the
// agent's own Agent.ApprovalTools. It is asked per call, so switching a tool's approval on or off
// takes effect on the next call and not at the next bake. An implementation that cannot tell must
// answer true: a tool is never run unapproved because the answer was unavailable.
type ToolApprovalPolicy interface {
	RequiresApproval(ctx context.Context, kitchen, tool string) bool
}

type toolApprovedKey struct{}

// WithToolApproved marks ctx as belonging to a tool call that has passed the executor's approval
// gate: either no approval was needed or a person gave it. The gateway refuses a call to a tool
// that needs approval unless the mark is there, so a caller that reaches the gateway by another
// road (the MCP endpoint, a recipe) cannot run an approval-gated tool unapproved.
func WithToolApproved(ctx context.Context) context.Context {
	return context.WithValue(ctx, toolApprovedKey{}, true)
}

// ToolApproved reports whether ctx carries the mark WithToolApproved sets.
func ToolApproved(ctx context.Context) bool {
	ok, _ := ctx.Value(toolApprovedKey{}).(bool)
	return ok
}
