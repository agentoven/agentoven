package process

import (
	"errors"
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

func agentWithKey() *models.Agent {
	return &models.Agent{
		Name: "a", Kitchen: "k", Runtime: models.RuntimeDeepAgents,
		ResolvedConfig: &models.ResolvedIngredients{Model: &models.ResolvedModel{
			Provider: "openai", Model: "gpt-4o", APIKey: "sk-REAL-PROVIDER-KEY", Endpoint: "https://api.openai.com/v1"}},
	}
}

func TestWithoutAGatewayTheProcessGetsTheProvidersKey(t *testing.T) {
	env := NewManager().buildEnvironment(agentWithKey(), 9100)
	if env["AGENT_API_KEY"] != "sk-REAL-PROVIDER-KEY" || env["AGENT_API_ENDPOINT"] != "https://api.openai.com/v1" {
		t.Fatalf("the community behaviour is unchanged: %v", env)
	}
	if _, set := env["AGENT_MODEL_GATEWAY"]; set {
		t.Fatal("no gateway, no gateway mode")
	}
}

func TestWithAGatewayTheProcessNeverSeesTheProvidersKey(t *testing.T) {
	m := NewManager()
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
	m := NewManager()
	g := &fakeGateway{ok: false}
	m.SetModelGateway(g)
	env := m.buildEnvironment(agentWithKey(), 9100)
	if g.calls != 1 || env["AGENT_API_KEY"] != "sk-REAL-PROVIDER-KEY" {
		t.Fatalf("calls=%d env=%v", g.calls, env)
	}
}

func TestAGatewayThatFailsToMintLeavesTheProcessWithNoCredentialRatherThanTheProvidersKey(t *testing.T) {
	m := NewManager()
	m.SetModelGateway(&fakeGateway{err: errors.New("no signing key")})
	env := m.buildEnvironment(agentWithKey(), 9100)
	if _, set := env["AGENT_API_KEY"]; set {
		t.Fatalf("falling back to the provider's key would defeat the gateway: %v", env["AGENT_API_KEY"])
	}
	if _, set := env["AGENT_API_ENDPOINT"]; set {
		t.Fatal("and the provider's endpoint is dropped with it")
	}
}
