package contracts

import (
	"context"
	"errors"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// ErrBotsNotAvailable is returned when an agentic bot (runtime "agenticbot") is asked for on an
// edition that cannot manage one.
var ErrBotsNotAvailable = errors.New("agentic bots (computer-use agents) are a Pro feature — upgrade at https://agentoven.dev/pricing")

// BotLaunch is what the bot manager decides about one bot process before it starts.
type BotLaunch struct {
	// Env is added to the process environment. It carries the bot's own credential, the gate and
	// egress-proxy addresses, and the model-gateway settings. It replaces, never extends, the
	// general control-plane token: a bot is not given one.
	Env map[string]string
	// Sandbox is how the process is confined.
	Sandbox models.SandboxProfile
}

// BotControl is the sole manager and tracker of agentic bots (ao-bots). The control plane asks
// it before a bot process starts and tells it when the process stops; everything the bot does in
// between goes through the gate API that the implementation serves.
//
// OSS implementation: CommunityBotControl (refuses).
// Pro implementation: agentoven-pro/internal/bots.
type BotControl interface {
	// Prepare validates the agent's bot configuration, registers the bot, and returns the
	// environment and sandbox profile its process is started with. An error stops the bake.
	Prepare(ctx context.Context, agent *models.Agent) (*BotLaunch, error)
	// Invoked is called by the control plane just before it relays an invoke to a bot's process.
	// ctx carries the caller's identity (so the manager can record who the run is for) and
	// runID is the ID the bot will register its run under. Returning an error refuses the
	// invoke (kill switch on, too many concurrent runs) before it reaches the sandbox.
	Invoked(ctx context.Context, agent *models.Agent, runID, message string) error
	// Stopped is called after the bot's process has been stopped (or failed to start) so the
	// manager can end any run still open and revoke the process's credential.
	Stopped(ctx context.Context, kitchen, agentName string)
}

// ContainerConfiner is implemented by a BotControl that confines a bot's Docker container once it
// is running. The process manager calls it for a sandbox whose profile sets NetworkLockdown, after
// the container has started and before the process is reported running. An error stops the bot and
// removes the container: a sandbox whose confinement could not be applied is never left running.
type ContainerConfiner interface {
	ConfineContainer(ctx context.Context, containerID string, sb models.SandboxProfile) error
}

// CommunityBotControl refuses every bot: the community edition does not manage computer-use agents.
type CommunityBotControl struct{}

func (CommunityBotControl) Prepare(context.Context, *models.Agent) (*BotLaunch, error) {
	return nil, ErrBotsNotAvailable
}

func (CommunityBotControl) Invoked(context.Context, *models.Agent, string, string) error {
	return ErrBotsNotAvailable
}

func (CommunityBotControl) Stopped(context.Context, string, string) {}

// ToolInfo is one kitchen tool an agent may call, as a bot is told about it.
type ToolInfo struct {
	Name        string
	Description string
	Schema      map[string]interface{} // JSON Schema of the arguments
	// Source is "tool", "skill:<name>" or "builtin:delegate".
	Source string
}

// ToolRunResult is what running one tool call produced.
type ToolRunResult struct {
	Content string
	IsError bool
	// Unknown is true when the agent has no such tool; nothing was run.
	Unknown bool
}

// ToolRunner runs a kitchen tool for an agent under the harness's own checks: the argument schema,
// the workspace and agent guardrails on the arguments and on the result, the per-call timeout, the
// MCP gateway with its credential handling, and delegation limits. The caller owns approval: unlike
// the live-voice path, nothing here refuses an approval-gated tool, because a bot's gate has already
// had a person decide.
//
// OSS implementation: *executor.Executor, reached through Handlers.Tools.
type ToolRunner interface {
	// ToolsFor lists the tools agent may call, from its resolved configuration (its own tools and
	// those of the skills it uses). It never includes the virtual delegate tool unless the agent
	// names Subagents.
	ToolsFor(agent *models.Agent, resolved *models.ResolvedIngredients) []ToolInfo
	// RunToolCall runs one call. A name outside ToolsFor comes back Unknown without running.
	RunToolCall(ctx context.Context, agent *models.Agent, resolved *models.ResolvedIngredients, callID, name string, argsJSON []byte) ToolRunResult
}
