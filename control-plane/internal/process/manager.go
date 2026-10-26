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
	"encoding/json"
	"fmt"
	"github.com/agentoven/agentoven/control-plane/pkg/a2aauth"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"os"
	"sync"
	"time"

	aoauth "github.com/agentoven/agentoven/control-plane/internal/auth"
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
	mu              sync.RWMutex
	processes       map[string]*models.ProcessInfo // key: kitchen/agentName
	local           *LocalExecutor
	docker          *DockerExecutor
	k8s             *K8sExecutor
	ports           *portAllocator
	controlPlaneURL string       // injected for AGENTOVEN_CONTROL_PLANE_URL env var
	saSecret        string       // AGENTOVEN_SA_SECRET — used to mint per-pod service-account tokens
	a2aSecret       []byte       // key each pod's own A2A token is derived from (pkg/a2aauth)
	gateway         ModelGateway // optional: fronts the model providers a pod calls
	keys            ProviderKeys // reads a provider's key when a process starts
	bots            contracts.BotControl
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

// SetControlPlaneURL configures the URL injected as AGENTOVEN_CONTROL_PLANE_URL
// into every agent process environment. Called during server startup.
func (m *Manager) SetControlPlaneURL(u string) {
	m.controlPlaneURL = u
}

// SetA2ASecret stores the key a pod's A2A token is derived from. Each pod is started with a
// token for itself only, which it requires from whoever calls its A2A endpoint.
func (m *Manager) SetA2ASecret(secret []byte) {
	m.a2aSecret = secret
}

// ModelGateway fronts the model providers a hosted process calls. With one installed, a process
// is given the gateway's address and a credential for it, and never the provider's own key; the
// gateway, which sees each call, can meter it and refuse it when a budget is used up.
type ModelGateway interface {
	// EnvFor returns the OpenAI-compatible base URL and the bearer credential to give agent's
	// process. ok=false means the gateway does not front this agent and its process gets its
	// provider's key as before. An error means it should but cannot, and the process is then
	// given no model credential at all rather than the provider's own.
	EnvFor(agent *models.Agent) (endpoint, token string, ok bool, err error)
}

// ProviderKeys reads a model provider's key at the moment a hosted process starts. The key is not
// kept on the agent, so a key that was rotated or revoked is picked up the next time the process
// starts, and nothing stored with the agent can leak it.
type ProviderKeys interface {
	ProviderKey(ctx context.Context, kitchen, provider string) (string, error)
}

// SetProviderKeys installs the source of provider keys (see ProviderKeys). Without one a hosted
// process is started without a model credential, and says so in the log.
func (m *Manager) SetProviderKeys(k ProviderKeys) {
	m.keys = k
}

// SetModelGateway installs the model gateway (see ModelGateway). Pass nil to remove it.
func (m *Manager) SetModelGateway(g ModelGateway) {
	m.gateway = g
}

// SetBotControl installs the bot manager (see contracts.BotControl). Without one, an agent with
// runtime "agenticbot" is refused at start, so the community edition cannot run a bot by accident.
func (m *Manager) SetBotControl(b contracts.BotControl) {
	m.bots = b
}

// SetSASecret stores the HMAC secret used to mint per-pod service-account tokens.
// When set, each baked pod receives a CONTROL_PLANE_TOKEN env var containing a
// signed token scoped to its kitchen so it can call back to the control plane
// for agent delegation.
func (m *Manager) SetSASecret(secret string) {
	m.saSecret = secret
}

// Start spawns an agent process using the appropriate executor.
// It allocates a port, sets up environment variables from the agent's
// resolved config, and tracks the process for lifecycle management.
func (m *Manager) Start(ctx context.Context, agent *models.Agent) (*models.ProcessInfo, error) {
	key := processKey(agent.Kitchen, agent.Name)

	// Determine execution mode; AGENTOVEN_DEFAULT_EXEC_MODE lets deployments
	// override the default without changing agent records (e.g. "k8s" in AKS).
	mode := agent.ExecutionMode
	if mode == "" {
		if dflt := models.ExecutionMode(os.Getenv("AGENTOVEN_DEFAULT_EXEC_MODE")); dflt != "" {
			mode = dflt
		} else {
			mode = models.ExecModeLocal
		}
	}

	// Check if already running.
	// For K8s mode we always re-apply the deployment so that env var changes
	// (e.g. a new model API key after a re-bake) take effect immediately via a
	// rolling update. For local/docker, re-starting a live process would be
	// destructive, so we keep the early-return guard.
	if mode != models.ExecModeK8s {
		m.mu.Lock()
		m.reapDeadLocked(key)
		if existing, ok := m.processes[key]; ok {
			if existing.Status == models.ProcessRunning {
				m.mu.Unlock()
				return existing, nil // already running
			}
		}
		m.mu.Unlock()
	}

	// A bot is spawned only through the bot manager, which decides how it is confined and what
	// it is given. Doing this before a port is taken means a refusal costs nothing.
	var launch *contracts.BotLaunch
	if agent.IsAgenticBot() {
		if m.bots == nil {
			return nil, contracts.ErrBotsNotAvailable
		}
		if mode == models.ExecModeLocal && os.Getenv("AGENTOVEN_BOT_ALLOW_LOCAL") != "true" {
			return nil, fmt.Errorf("an agentic bot must run in a confined sandbox: use execution mode docker or k8s " +
				"(AGENTOVEN_BOT_ALLOW_LOCAL=true permits an unconfined local process for development only)")
		}
		var err error
		if launch, err = m.bots.Prepare(ctx, agent); err != nil {
			return nil, err
		}
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
	if launch != nil {
		if err := applyBotLaunch(agent, env, launch, info); err != nil {
			m.ports.Release(port)
			m.bots.Stopped(ctx, agent.Kitchen, agent.Name)
			return nil, err
		}
		if mode == models.ExecModeLocal {
			// An unconfined local bot (development only) runs the ao-bot binary, not the Python runner.
			local := *agent
			if local.Entrypoint == "" {
				local.Entrypoint = botBinary()
			}
			agent = &local
		}
	}

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
		if launch != nil {
			m.bots.Stopped(ctx, agent.Kitchen, agent.Name)
		}
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
	if m.bots != nil && info.Sandbox != nil {
		m.bots.Stopped(ctx, kitchen, agentName)
	}

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
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reapDeadLocked(key)
	info, ok := m.processes[key]
	if !ok {
		return nil, nil
	}
	return info, nil
}

// reapDeadLocked marks a local process stopped when its subprocess has exited on its own
// (crash, OOM, killed). Without this a dead agent stays "running" forever and a re-bake returns
// early with the corpse. Caller holds m.mu for writing.
func (m *Manager) reapDeadLocked(key string) {
	info, ok := m.processes[key]
	if !ok || info.Status != models.ProcessRunning || info.Mode != models.ExecModeLocal || info.PID == 0 {
		return
	}
	if m.local.Alive(info.Kitchen, info.AgentName, info.PID) {
		return
	}
	info.Status = models.ProcessStopped
	info.Error = "process exited"
	m.ports.Release(info.Port)
	log.Warn().Str("agent", info.AgentName).Int("pid", info.PID).Msg("Agent process is no longer running; marked stopped")
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
	m.mu.Lock()
	defer m.mu.Unlock()
	var running []*models.ProcessInfo
	for key, info := range m.processes {
		m.reapDeadLocked(key)
		if info.Status == models.ProcessRunning {
			running = append(running, info)
		}
	}
	return running
}

// GetLogBuffer returns the log buffer for a running agent process.
// Returns nil if the agent is not running or not using local execution.
func (m *Manager) GetLogBuffer(kitchen, agentName string) *LogBuffer {
	return m.local.GetLogBuffer(kitchen, agentName)
}

// GetK8sRecentLogs returns recent pod logs for a k8s-managed agent.
func (m *Manager) GetK8sRecentLogs(ctx context.Context, kitchen, agentName string, tailLines int) ([]LogEntry, error) {
	key := processKey(kitchen, agentName)

	m.mu.RLock()
	info, ok := m.processes[key]
	m.mu.RUnlock()
	if ok && info.Mode != models.ExecModeK8s {
		return nil, fmt.Errorf("agent is not running in k8s mode")
	}

	podName := ""
	if ok {
		podName = info.PodName
	}
	if podName == "" {
		resolvedPod, err := m.k8s.FindPodForAgent(ctx, kitchen, agentName)
		if err != nil {
			return nil, fmt.Errorf("agent process not found")
		}
		podName = resolvedPod
	}

	return m.k8s.RecentLogs(ctx, podName, tailLines)
}

// SubscribeK8sLogs returns a channel of live pod logs for a k8s-managed agent.
func (m *Manager) SubscribeK8sLogs(ctx context.Context, kitchen, agentName string, tailLines int) (<-chan LogEntry, error) {
	key := processKey(kitchen, agentName)

	m.mu.RLock()
	info, ok := m.processes[key]
	m.mu.RUnlock()
	if ok && info.Mode != models.ExecModeK8s {
		return nil, fmt.Errorf("agent is not running in k8s mode")
	}

	podName := ""
	if ok {
		podName = info.PodName
	}
	if podName == "" {
		resolvedPod, err := m.k8s.FindPodForAgent(ctx, kitchen, agentName)
		if err != nil {
			return nil, fmt.Errorf("agent process not found")
		}
		podName = resolvedPod
	}

	return m.k8s.StreamLogs(ctx, podName, tailLines)
}

// providerKey reads the key of the provider agent runs on, from the provider, now.
func (m *Manager) providerKey(agent *models.Agent) string {
	provider := agent.ResolvedConfig.Model.Provider
	if m.keys == nil {
		log.Warn().Str("agent", agent.Name).Str("provider", provider).Msg("No source of provider keys is installed: the process is started without a model credential")
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	key, err := m.keys.ProviderKey(ctx, agent.Kitchen, provider)
	if err != nil {
		log.Warn().Err(err).Str("agent", agent.Name).Str("provider", provider).Msg("Provider key could not be read: the process is started without a model credential")
		return ""
	}
	return key
}

// buildEnvironment creates the env vars map for the agent process.
func (m *Manager) buildEnvironment(agent *models.Agent, port int) map[string]string {
	env := map[string]string{
		"AGENT_NAME":    agent.Name,
		"AGENT_KITCHEN": agent.Kitchen,
		"AGENT_PORT":    fmt.Sprintf("%d", port),
	}

	// Runtime — tells the SDK which adapter to use
	if agent.Runtime != "" {
		env["AGENT_RUNTIME"] = string(agent.Runtime)
	}

	// Description / system prompt
	if agent.Description != "" {
		env["AGENT_DESCRIPTION"] = agent.Description
	}

	// Model config — from resolved config first, then top-level fields
	if agent.ResolvedConfig != nil && agent.ResolvedConfig.Model != nil {
		env["AGENT_MODEL_PROVIDER"] = agent.ResolvedConfig.Model.Provider
		env["AGENT_MODEL_NAME"] = agent.ResolvedConfig.Model.Model
		if key := m.providerKey(agent); key != "" {
			env["AGENT_API_KEY"] = key
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

	// Model gateway: the process calls the gateway with a credential that only opens the
	// gateway, instead of calling the provider with the provider's own key.
	if m.gateway != nil && agent.ResolvedConfig != nil && agent.ResolvedConfig.Model != nil {
		endpoint, token, ok, err := m.gateway.EnvFor(agent)
		switch {
		case err != nil:
			delete(env, "AGENT_API_KEY")
			delete(env, "AGENT_API_ENDPOINT")
			log.Error().Err(err).Str("agent", agent.Name).Msg("Model gateway credential could not be minted; the process is started without a model credential")
		case ok:
			env["AGENT_API_ENDPOINT"] = endpoint
			env["AGENT_API_KEY"] = token
			env["AGENT_MODEL_GATEWAY"] = "openai"
			env["AGENT_GATEWAY_MODEL"] = agent.ResolvedConfig.Model.Provider + "/" + agent.ResolvedConfig.Model.Model
		}
	}

	// Tools — inject as JSON array so the SDK can build MCP tool wrappers
	if agent.ResolvedConfig != nil && len(agent.ResolvedConfig.Tools) > 0 {
		toolsJSON, err := json.Marshal(agent.ResolvedConfig.Tools)
		if err == nil {
			env["AGENT_TOOLS_JSON"] = string(toolsJSON)
		}
	}

	// Prompt template — inject raw template text
	if agent.ResolvedConfig != nil && agent.ResolvedConfig.Prompt != nil && agent.ResolvedConfig.Prompt.Template != "" {
		env["AGENT_PROMPT_TEMPLATE"] = agent.ResolvedConfig.Prompt.Template
	}

	// Data sources — inject as JSON array
	if agent.ResolvedConfig != nil && len(agent.ResolvedConfig.Data) > 0 {
		dataJSON, err := json.Marshal(agent.ResolvedConfig.Data)
		if err == nil {
			env["AGENT_DATA_SOURCES_JSON"] = string(dataJSON)
		}
	}

	// The pod's own A2A token: it accepts an A2A call only from a caller presenting it, and
	// the control plane presents it when it relays. Derived per kitchen and agent.
	if tok := a2aauth.PodToken(m.a2aSecret, agent.Kitchen, agent.Name); tok != "" {
		env[a2aauth.PodTokenEnv] = tok
	}

	// Control plane URL — SDK uses this for callbacks (e.g. Pro checkpointer)
	if cpURL := m.controlPlaneURL; cpURL != "" {
		env["AGENTOVEN_CONTROL_PLANE_URL"] = cpURL
	}

	// Service-account token — lets the pod call back to the control plane for
	// agent delegation (agentoven_delegate tool) using X-Service-Token auth.
	if m.saSecret != "" {
		tok, err := aoauth.GenerateToken(
			[]byte(m.saSecret),
			"agent:"+agent.Name,
			agent.Kitchen,
			"baker",
			365*24*time.Hour,
		)
		if err == nil {
			env["CONTROL_PLANE_TOKEN"] = tok
		} else {
			log.Warn().Err(err).Str("agent", agent.Name).Msg("failed to mint pod service-account token")
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

	// System prompt from resolved prompt ingredient (legacy path — also covered above)
	if agent.ResolvedConfig != nil && agent.ResolvedConfig.Prompt != nil {
		env["AGENT_DESCRIPTION"] = agent.ResolvedConfig.Prompt.Template
	}

	return env
}
