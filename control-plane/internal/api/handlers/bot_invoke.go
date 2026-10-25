package handlers

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// Tools returns the runner for kitchen tools a bot's gate uses (the Go executor, which applies
// the schema, guardrail, timeout, credential and delegation checks). Nil if there is no executor.
func (h *Handlers) Tools() contracts.ToolRunner {
	if h.Executor == nil {
		return nil
	}
	return h.Executor
}

// SetBotControl installs the bot manager on both the process manager (which asks it before a
// bot's process starts) and the invoke path (which tells it who is invoking a bot).
func (h *Handlers) SetBotControl(b contracts.BotControl) {
	h.Bots = b
	if h.Executor != nil {
		h.Executor.SetHostedInvoker(func(ctx context.Context, agent *models.Agent, message string) (string, error) {
			resp, _, err := h.proxyToProcess(ctx, agent, message, nil, nil, agent.Kitchen)
			return resp, err
		})
	}
	if h.ProcessManager != nil {
		h.ProcessManager.SetBotControl(b)
	}
}

// admitBotInvoke tells the bot manager a bot is about to be invoked, and lets it refuse. The
// community edition has no manager, so a bot invoked there is refused outright.
func (h *Handlers) admitBotInvoke(ctx context.Context, agent *models.Agent, runID, message string) error {
	if h.Bots == nil {
		return contracts.ErrBotsNotAvailable
	}
	return h.Bots.Invoked(ctx, agent, runID, message)
}

// botInvokeTimeout is how long a relayed invoke to a bot may run. A bot works through a task
// action by action, so it takes minutes, not the seconds other hosted agents take.
// AGENTOVEN_BOT_INVOKE_TIMEOUT overrides it (a Go duration, e.g. "45m").
func botInvokeTimeout() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("AGENTOVEN_BOT_INVOKE_TIMEOUT")); err == nil && d > 0 {
		return d
	}
	return 30 * time.Minute
}

// checkBotAgent refuses a definition that cannot become a working bot, as early as it can: when
// the agent is saved rather than when someone first tries to bake it. It returns the HTTP status
// to answer with. Agents of every other runtime pass untouched.
func (h *Handlers) checkBotAgent(agent *models.Agent) (int, error) {
	if !agent.IsAgenticBot() {
		return 0, nil
	}
	if h.Bots == nil {
		return http.StatusForbidden, contracts.ErrBotsNotAvailable
	}
	if agent.Mode != "" && agent.Mode != models.AgentModeManaged {
		return http.StatusBadRequest, fmt.Errorf("an agentic bot runs in a sandbox AgentOven starts, so its mode must be managed")
	}
	if agent.Bot != nil {
		if err := agent.Bot.Validate(); err != nil {
			return http.StatusBadRequest, err
		}
	}
	return 0, nil
}
