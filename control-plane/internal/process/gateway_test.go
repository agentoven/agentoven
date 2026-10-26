package process

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

type fakeGateway struct {
	ok    bool
	err   error
	calls int
}

func (g *fakeGateway) EnvFor(a *models.Agent) (string, string, bool, error) {
	g.calls++
	return "http://cp:8080/v1", "ao_run_token", g.ok, g.err
}

// fakeKeys stands in for the provider store: the key lives with the provider, not on the agent.
type fakeKeys struct {
	key   string
	err   error
	asked []string
}

func (f *fakeKeys) ProviderKey(_ context.Context, kitchen, provider string) (string, error) {
	f.asked = append(f.asked, kitchen+"/"+provider)
	return f.key, f.err
}

func agentWithKey() *models.Agent {
	return &models.Agent{
		Name: "a", Kitchen: "k", Runtime: models.RuntimeDeepAgents,
		ResolvedConfig: &models.ResolvedIngredients{Model: &models.ResolvedModel{
			Provider: "openai", Model: "gpt-4o", Endpoint: "https://api.openai.com/v1"}},
	}
}

func managerWithKey(key string) *Manager {
	m := NewManager()
	m.SetProviderKeys(&fakeKeys{key: key})
	return m
}

func TestWithoutAGatewayTheProcessGetsTheProvidersKey(t *testing.T) {
	env := managerWithKey("sk-REAL-PROVIDER-KEY").buildEnvironment(agentWithKey(), 9100)
	if env["AGENT_API_KEY"] != "sk-REAL-PROVIDER-KEY" || env["AGENT_API_ENDPOINT"] != "https://api.openai.com/v1" {
		t.Fatalf("the community behaviour is unchanged: %v", env)
	}
	if _, set := env["AGENT_MODEL_GATEWAY"]; set {
		t.Fatal("no gateway, no gateway mode")
	}
}

func TestWithAGatewayTheProcessNeverSeesTheProvidersKey(t *testing.T) {
	m := managerWithKey("sk-REAL-PROVIDER-KEY")
	m.SetModelGateway(&fakeGateway{ok: true})
	env := m.buildEnvironment(agentWithKey(), 9100)

	if env["AGENT_API_KEY"] != "ao_run_token" || env["AGENT_API_ENDPOINT"] != "http://cp:8080/v1" {
		t.Fatalf("the process is pointed at the gateway: %v", env)
	}
	for k, v := range env {
		if v == "sk-REAL-PROVIDER-KEY" {
			t.Fatalf("the provider's key leaked into %s", k)
		}
	}
	if env["AGENT_MODEL_GATEWAY"] != "openai" || env["AGENT_GATEWAY_MODEL"] != "openai/gpt-4o" {
		t.Fatalf("the process is told how to name its model to the gateway: %v", env)
	}
}

func TestAGatewayThatDeclinesAnAgentLeavesItAsItWas(t *testing.T) {
	m := managerWithKey("sk-REAL-PROVIDER-KEY")
	g := &fakeGateway{ok: false}
	m.SetModelGateway(g)
	env := m.buildEnvironment(agentWithKey(), 9100)
	if g.calls != 1 || env["AGENT_API_KEY"] != "sk-REAL-PROVIDER-KEY" {
		t.Fatalf("calls=%d env=%v", g.calls, env)
	}
}

func TestAGatewayThatFailsToMintLeavesTheProcessWithNoCredentialRatherThanTheProvidersKey(t *testing.T) {
	m := managerWithKey("sk-REAL-PROVIDER-KEY")
	m.SetModelGateway(&fakeGateway{err: errors.New("no signing key")})
	env := m.buildEnvironment(agentWithKey(), 9100)
	if _, set := env["AGENT_API_KEY"]; set {
		t.Fatalf("falling back to the provider's key would defeat the gateway: %v", env["AGENT_API_KEY"])
	}
	if _, set := env["AGENT_API_ENDPOINT"]; set {
		t.Fatal("and the provider's endpoint is dropped with it")
	}
}

// The key is read from the provider each time a process starts, so a rotated key is used by the next
// start and nothing about the agent holds a copy.
func TestAProcessGetsTheKeyTheProviderHasWhenItStarts(t *testing.T) {
	keys := &fakeKeys{key: "sk-first"}
	m := NewManager()
	m.SetProviderKeys(keys)
	a := agentWithKey()
	if got := m.buildEnvironment(a, 9100)["AGENT_API_KEY"]; got != "sk-first" {
		t.Fatalf("got %q", got)
	}
	keys.key = "sk-rotated"
	if got := m.buildEnvironment(a, 9100)["AGENT_API_KEY"]; got != "sk-rotated" {
		t.Errorf("a rotated key should reach the next process without baking again, got %q", got)
	}
	if len(keys.asked) != 2 || keys.asked[0] != "k/openai" {
		t.Errorf("the key is asked for by the agent's kitchen and provider: %v", keys.asked)
	}
}

func TestAProcessIsStartedWithoutACredentialWhenTheKeyCannotBeRead(t *testing.T) {
	for name, m := range map[string]*Manager{
		"no source installed": NewManager(),
		"provider gone": func() *Manager {
			m := NewManager()
			m.SetProviderKeys(&fakeKeys{err: errors.New("provider \"openai\" not found")})
			return m
		}(),
		"provider has no key": managerWithKey(""),
	} {
		t.Run(name, func(t *testing.T) {
			env := m.buildEnvironment(agentWithKey(), 9100)
			if v, set := env["AGENT_API_KEY"]; set {
				t.Errorf("no credential should be set, got %q", v)
			}
			if env["AGENT_MODEL_NAME"] != "gpt-4o" {
				t.Errorf("the rest of the environment is still built: %v", env)
			}
		})
	}
}

func TestTheAgentRecordNeverHoldsTheKeyTheProcessIsGiven(t *testing.T) {
	a := agentWithKey()
	_ = managerWithKey("sk-REAL-PROVIDER-KEY").buildEnvironment(a, 9100)
	if strings.Contains(fmtJSON(t, a), "sk-REAL-PROVIDER-KEY") {
		t.Error("starting a process must not copy the key onto the agent")
	}
}

func fmtJSON(t *testing.T, v interface{}) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
