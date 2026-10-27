package handlers

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/rs/zerolog/log"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// A baked agent's process is not part of what the store keeps. After the control plane restarts, or
// when a process crashes, the agent is still "ready" and still has the endpoint it was baked with,
// but nothing is listening there: an A2A call is refused ("connection refused") and so is every
// recipe step that uses the agent. Rather than start every agent at boot (a demo kitchen has dozens,
// and most are never called), the process is brought back the first time something needs it.

var reviveLocks sync.Map // "kitchen/agent" -> *sync.Mutex

// ownsProcess reports whether this control plane is the one that runs the agent's process. External
// agents are somebody else's, and agents on Kubernetes belong to the cluster and its operator:
// starting them from here would roll their deployment.
func (h *Handlers) ownsProcess(agent *models.Agent) bool {
	if h.ProcessManager == nil || agent.Mode == models.AgentModeExternal {
		return false
	}
	mode := agent.ExecutionMode
	if mode == "" {
		mode = models.ExecutionMode(os.Getenv("AGENTOVEN_DEFAULT_EXEC_MODE"))
	}
	return mode != models.ExecModeK8s
}

// ensureProcess makes sure a ready agent's process is running, starting it if it is not, and updates
// the agent (as given, and in the store) to point at it. It returns the reason it could not, or nil
// when there is nothing to do or the process is up. Concurrent callers for one agent start it once.
func (h *Handlers) ensureProcess(ctx context.Context, agent *models.Agent) error {
	if !h.ownsProcess(agent) || agent.Status != models.AgentStatusReady {
		return nil
	}
	if info, _ := h.ProcessManager.Status(ctx, agent.Kitchen, agent.Name); info != nil && info.Status == models.ProcessRunning {
		h.pointAt(ctx, agent, info)
		return nil
	}

	key := agent.Kitchen + "/" + agent.Name
	mu, _ := reviveLocks.LoadOrStore(key, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()

	// Someone else may have started it while this call waited.
	if info, _ := h.ProcessManager.Status(ctx, agent.Kitchen, agent.Name); info != nil && info.Status == models.ProcessRunning {
		h.pointAt(ctx, agent, info)
		return nil
	}

	log.Info().Str("agent", agent.Name).Str("kitchen", agent.Kitchen).Msg("Agent process is not running; starting it")
	// The process outlives the request that needed it.
	info, err := h.ProcessManager.Start(context.WithoutCancel(ctx), agent)
	if err != nil {
		log.Warn().Err(err).Str("agent", agent.Name).Msg("Could not start the agent's process")
		return fmt.Errorf("the agent's process is not running and could not be started: %w", err)
	}
	h.pointAt(ctx, agent, info)
	return nil
}

// pointAt records where the agent's process is, on the agent and in the store, when that changed.
func (h *Handlers) pointAt(ctx context.Context, agent *models.Agent, info *models.ProcessInfo) {
	if info == nil {
		return
	}
	changed := agent.BackendEndpoint != info.Endpoint || agent.Process == nil || agent.Process.Status != models.ProcessRunning ||
		agent.Process.Endpoint != info.Endpoint
	agent.Process = info
	if info.Endpoint != "" {
		agent.BackendEndpoint = info.Endpoint
	}
	if !changed {
		return
	}
	if stored, err := h.Store.GetAgent(ctx, agent.Kitchen, agent.Name); err == nil && stored != nil {
		stored.Process = info
		if info.Endpoint != "" {
			stored.BackendEndpoint = info.Endpoint
		}
		if err := h.Store.UpdateAgent(ctx, stored); err != nil {
			log.Warn().Err(err).Str("agent", agent.Name).Msg("Could not record the agent's new process endpoint")
		}
	}
}

// liveBackend is ResolveBackendEndpoint for a call that is about to use the backend: it brings the
// process back first when this control plane runs it, so the endpoint it returns is one that answers.
func (h *Handlers) liveBackend(ctx context.Context, agent *models.Agent) (string, string) {
	if err := h.ensureProcess(ctx, agent); err != nil {
		return "", "managed_process_failed_to_start: " + err.Error()
	}
	if h.ownsProcess(agent) && agent.Process != nil && agent.Process.Status == models.ProcessRunning && agent.Process.Endpoint != "" {
		return agent.Process.Endpoint, "managed_process_endpoint"
	}
	return h.ResolveBackendEndpoint(agent)
}
