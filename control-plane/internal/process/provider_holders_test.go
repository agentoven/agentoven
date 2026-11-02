package process

import (
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// Registers a running process the way Start does after it succeeds, without spawning anything.
func trackRunning(m *Manager, agent *models.Agent, env map[string]string) {
	key := processKey(agent.Kitchen, agent.Name)
	m.mu.Lock()
	m.processes[key] = &models.ProcessInfo{AgentName: agent.Name, Kitchen: agent.Kitchen, Mode: models.ExecModeLocal, Status: models.ProcessRunning}
	m.noteKeyHolderLocked(key, agent, env)
	m.mu.Unlock()
}

func agentOn(name, kitchen, provider string) *models.Agent {
	a := agentWithKey()
	a.Name, a.Kitchen = name, kitchen
	a.ResolvedConfig.Model.Provider = provider
	return a
}

func TestRunningOnProviderFindsOnlyProcessesHoldingThatProvidersKey(t *testing.T) {
	m := NewManager()
	withKey := map[string]string{"AGENT_API_KEY": "sk-1"}
	trackRunning(m, agentOn("holds", "k", "openai"), withKey)
	trackRunning(m, agentOn("other-provider", "k", "anthropic"), withKey)
	trackRunning(m, agentOn("other-kitchen", "k2", "openai"), withKey)
	trackRunning(m, agentOn("via-gateway", "k", "openai"), map[string]string{"AGENT_API_KEY": "ao_run", "AGENT_MODEL_GATEWAY": "openai"})
	trackRunning(m, agentOn("no-credential", "k", "openai"), map[string]string{})

	got := m.RunningOnProvider("k", "openai")
	if len(got) != 1 || got[0].AgentName != "holds" {
		t.Fatalf("only the process that holds openai's key in kitchen k qualifies, got %+v", got)
	}

	// A stopped process no longer holds anything, and the list is a copy.
	got[0].Status = models.ProcessStopped
	if len(m.RunningOnProvider("k", "openai")) != 1 {
		t.Error("changing the returned value must not change the manager's record")
	}
	m.mu.Lock()
	m.processes["k/holds"].Status = models.ProcessStopped
	m.mu.Unlock()
	if len(m.RunningOnProvider("k", "openai")) != 0 {
		t.Error("a stopped process holds no key")
	}
}

func TestAStartWithoutTheKeyForgetsAnEarlierHolder(t *testing.T) {
	m := NewManager()
	a := agentOn("a", "k", "openai")
	trackRunning(m, a, map[string]string{"AGENT_API_KEY": "sk-1"})
	trackRunning(m, a, map[string]string{"AGENT_API_KEY": "ao_run", "AGENT_MODEL_GATEWAY": "openai"}) // restarted behind a gateway
	if got := m.RunningOnProvider("k", "openai"); len(got) != 0 {
		t.Fatalf("a process now behind the gateway holds no provider key: %+v", got)
	}
}
