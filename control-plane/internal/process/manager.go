package process

// Package process manages agent runtime processes (local, Docker, K8s).
//
// When an agent is baked, the ProcessManager spawns the agent as an
// independent process that listens for A2A JSON-RPC requests. The
// execution mode (local/docker/k8s) is determined by the agent's
// ExecutionMode field.
//
// Architecture:
//
//	BakeAgent handler
//	    └─► ProcessManager.Start(agent)
//	            ├─► LocalExecutor   (Python subprocess)
//	            ├─► DockerExecutor  (docker run)
//	            └─► K8sExecutor     (kubectl apply)
//	                    └─► ProcessInfo{port, endpoint, pid/container/pod}

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/rs/zerolog/log"
)

// portAllocator provides sequential ports for agent processes.
type portAllocator struct {
	mu       sync.Mutex
	nextPort int
	used     map[int]bool
}

func newPortAllocator(startPort int) *portAllocator {
	return &portAllocator{
		nextPort: startPort,
		used:     make(map[int]bool),
	}
}

func (pa *portAllocator) Allocate() int {
	pa.mu.Lock()
	defer pa.mu.Unlock()
	for pa.used[pa.nextPort] {
		pa.nextPort++
	}
	port := pa.nextPort
	pa.used[port] = true
	pa.nextPort++
	return port
}

func (pa *portAllocator) Release(port int) {
	pa.mu.Lock()
	defer pa.mu.Unlock()
	delete(pa.used, port)
}

// processKey returns a unique key for tracking a process.
func processKey(kitchen, agentName string) string {
	return kitchen + "/" + agentName
}

// Manager orchestrates agent process lifecycles across execution modes.
// It implements contracts.AgentProcessExecutor.
type Manager struct {
	mu        sync.RWMutex
	processes map[string]*models.ProcessInfo // key: kitchen/agentName
	local     *LocalExecutor
	docker    *DockerExecutor
	k8s       *K8sExecutor
	ports     *portAllocator
}

// NewManager creates a new ProcessManager with all executors initialized.
func NewManager() *Manager {
	ports := newPortAllocator(9100) // agent processes start at port 9100
	return &Manager{
		processes: make(map[string]*models.ProcessInfo),
		local:     NewLocalExecutor(),
		docker:    NewDockerExecutor(),
		k8s:       NewK8sExecutor(),
		ports:     ports,
	}
}

// Start spawns an agent process using the appropriate executor.
// It allocates a port, sets up environment variables from the agent's
// resolved config, and tracks the process for lifecycle management.
func (m *Manager) Start(ctx context.Context, agent *models.Agent) (*models.ProcessInfo, error) {
	key := processKey(agent.Kitchen, agent.Name)

	// Check if already running
	m.mu.RLock()
	if existing, ok := m.processes[key]; ok {
		if existing.Status == models.ProcessRunning {
			m.mu.RUnlock()
			return existing, nil // already running
		}
	}
	m.mu.RUnlock()

	// Determine execution mode
	mode := agent.ExecutionMode
	if mode == "" {
		mode = models.ExecModeLocal // default
	}

	// Allocate a port
	port := m.ports.Allocate()

	// Build process info
	info := &models.ProcessInfo{
		AgentName: agent.Name,
		Kitchen:   agent.Kitchen,
		Mode:      mode,
		Status:    models.ProcessStarting,
		Port:      port,
		Endpoint:  fmt.Sprintf("http://localhost:%d", port),
		StartedAt: time.Now().UTC(),
	}

	// Build environment from agent config
	env := m.buildEnvironment(agent, port)

	log.Info().
		Str("agent", agent.Name).
		Str("kitchen", agent.Kitchen).
		Str("mode", string(mode)).
		Int("port", port).
		Msg("Starting agent process")

	// Dispatch to the appropriate executor
	var err error
	switch mode {
	case models.ExecModeLocal:
		err = m.local.Start(ctx, agent, info, env)
	case models.ExecModeDocker:
		err = m.docker.Start(ctx, agent, info, env)
	case models.ExecModeK8s:
		err = m.k8s.Start(ctx, agent, info, env)
	default:
		m.ports.Release(port)
		return nil, fmt.Errorf("unknown execution mode: %s", mode)
	}

	if err != nil {
		m.ports.Release(port)
		info.Status = models.ProcessFailed
		info.Error = err.Error()
		m.mu.Lock()
		m.processes[key] = info
		m.mu.Unlock()
		return info, fmt.Errorf("failed to start agent process: %w", err)
	}

	info.Status = models.ProcessRunning
	m.mu.Lock()
	m.processes[key] = info
	m.mu.Unlock()

	log.Info().
		Str("agent", agent.Name).
		Str("mode", string(mode)).
		Int("port", port).
		Str("endpoint", info.Endpoint).
		Msg("Agent process started")

	return info, nil
}

// Stop terminates a running agent process.
func (m *Manager) Stop(ctx context.Context, kitchen, agentName string) error {
	key := processKey(kitchen, agentName)

	m.mu.Lock()
	info, ok := m.processes[key]
	if !ok {
		m.mu.Unlock()
		return nil // not tracked, nothing to stop
	}
	if info.Status != models.ProcessRunning && info.Status != models.ProcessStarting {
		m.mu.Unlock()
		return nil // already stopped
	}
	m.mu.Unlock()

	log.Info().
		Str("agent", agentName).
		Str("kitchen", kitchen).
		Str("mode", string(info.Mode)).
		Msg("Stopping agent process")

	var err error
	switch info.Mode {
	case models.ExecModeLocal:
		err = m.local.Stop(ctx, info)
	case models.ExecModeDocker:
		err = m.docker.Stop(ctx, info)
	case models.ExecModeK8s:
		err = m.k8s.Stop(ctx, info)
	}

	m.ports.Release(info.Port)

	m.mu.Lock()
	info.Status = models.ProcessStopped
	if err != nil {
		info.Error = err.Error()
	}
	m.mu.Unlock()

	return err
}

// Status returns the current process info for an agent.
func (m *Manager) Status(_ context.Context, kitchen, agentName string) (*models.ProcessInfo, error) {
	key := processKey(kitchen, agentName)
	m.mu.RLock()
	defer m.mu.RUnlock()
	info, ok := m.processes[key]
	if !ok {
		return nil, nil
	}
	return info, nil
}

// StopAll terminates all running processes. Called on server shutdown.
func (m *Manager) StopAll(ctx context.Context) error {
	m.mu.RLock()
	keys := make([]string, 0, len(m.processes))
	for k, info := range m.processes {
		if info.Status == models.ProcessRunning || info.Status == models.ProcessStarting {
			keys = append(keys, k)
		}
	}
	m.mu.RUnlock()

	var lastErr error
	for _, key := range keys {
		m.mu.RLock()
		info := m.processes[key]
		m.mu.RUnlock()
		if err := m.Stop(ctx, info.Kitchen, info.AgentName); err != nil {
			log.Warn().Err(err).Str("agent", info.AgentName).Msg("Failed to stop agent process during shutdown")
			lastErr = err
		}
	}

	log.Info().Int("count", len(keys)).Msg("All agent processes stopped")
	return lastErr
}

// ListRunning returns all currently running processes.
func (m *Manager) ListRunning() []*models.ProcessInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var running []*models.ProcessInfo
	for _, info := range m.processes {
		if info.Status == models.ProcessRunning {
			running = append(running, info)
		}
	}
	return running
}

// buildEnvironment creates the env vars map for the agent process.
func (m *Manager) buildEnvironment(agent *models.Agent, port int) map[string]string {
	env := map[string]string{
		"AGENT_NAME":    agent.Name,
		"AGENT_KITCHEN": agent.Kitchen,
		"AGENT_PORT":    fmt.Sprintf("%d", port),
	}

	// Description / system prompt
	if agent.Description != "" {
		env["AGENT_DESCRIPTION"] = agent.Description
	}

	// Model config — from resolved config first, then top-level fields
	if agent.ResolvedConfig != nil && agent.ResolvedConfig.Model != nil {
		env["AGENT_MODEL_PROVIDER"] = agent.ResolvedConfig.Model.Provider
		env["AGENT_MODEL_NAME"] = agent.ResolvedConfig.Model.Model
		if agent.ResolvedConfig.Model.APIKey != "" {
			env["AGENT_API_KEY"] = agent.ResolvedConfig.Model.APIKey
		}
		if agent.ResolvedConfig.Model.Endpoint != "" {
			env["AGENT_API_ENDPOINT"] = agent.ResolvedConfig.Model.Endpoint
		}
	} else {
		if agent.ModelProvider != "" {
			env["AGENT_MODEL_PROVIDER"] = agent.ModelProvider
		}
		if agent.ModelName != "" {
			env["AGENT_MODEL_NAME"] = agent.ModelName
		}
	}

	// Skills
	if len(agent.Skills) > 0 {
		skills := ""
		for i, s := range agent.Skills {
			if i > 0 {
				skills += ","
			}
			skills += s
		}
		env["AGENT_SKILLS"] = skills
	}

	// Max turns
	if agent.MaxTurns > 0 {
		env["AGENT_MAX_TURNS"] = fmt.Sprintf("%d", agent.MaxTurns)
	}

	// System prompt from resolved prompt ingredient
	if agent.ResolvedConfig != nil && agent.ResolvedConfig.Prompt != nil {
		env["AGENT_DESCRIPTION"] = agent.ResolvedConfig.Prompt.Template
	}

	return env
}
