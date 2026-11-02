package process

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

type confinerBots struct {
	contracts.CommunityBotControl
	err   error
	calls []string
	sb    models.SandboxProfile
}

func (c *confinerBots) ConfineContainer(_ context.Context, id string, sb models.SandboxProfile) error {
	c.calls = append(c.calls, id)
	c.sb = sb
	return c.err
}

func TestConfineDockerBotOnlyActsOnASandboxThatAsksForIt(t *testing.T) {
	cb := &confinerBots{}
	m := NewManager()
	m.SetBotControl(cb)

	for _, info := range []*models.ProcessInfo{
		{Kitchen: "k", AgentName: "plain-oss-test"},
		{Kitchen: "k", AgentName: "plain-oss-test", Sandbox: &models.SandboxProfile{Confined: true}},
	} {
		if err := m.confineDockerBot(context.Background(), info); err != nil {
			t.Fatal(err)
		}
	}
	if len(cb.calls) != 0 {
		t.Fatalf("the confiner ran for a sandbox that did not ask: %v", cb.calls)
	}

	info := &models.ProcessInfo{Kitchen: "k", AgentName: "confine-oss-test", ContainerID: "abc123",
		Sandbox: &models.SandboxProfile{Confined: true, NetworkLockdown: true, AllowEgressTo: []string{"gate:8080"}}}
	if err := m.confineDockerBot(context.Background(), info); err != nil {
		t.Fatal(err)
	}
	if len(cb.calls) != 1 || cb.calls[0] != "abc123" || cb.sb.AllowEgressTo[0] != "gate:8080" {
		t.Fatalf("calls=%v sandbox=%+v", cb.calls, cb.sb)
	}
}

func TestConfineDockerBotFailsClosed(t *testing.T) {
	info := func() *models.ProcessInfo {
		return &models.ProcessInfo{Kitchen: "k", AgentName: "confine-oss-test2", ContainerID: "does-not-exist-0001",
			Sandbox: &models.SandboxProfile{Confined: true, NetworkLockdown: true}}
	}
	m := NewManager()
	m.SetBotControl(&confinerBots{err: errors.New("iptables is missing")})
	err := m.confineDockerBot(context.Background(), info())
	if err == nil || !strings.Contains(err.Error(), "iptables is missing") || !strings.Contains(err.Error(), "removed") {
		t.Fatalf("a confinement failure must stop the start and say why: %v", err)
	}
	// A bot manager that cannot confine at all is also a refusal, not a silent pass.
	m.SetBotControl(contracts.CommunityBotControl{})
	if err := m.confineDockerBot(context.Background(), info()); err == nil {
		t.Fatal("a sandbox that asks for lockdown started under a manager that cannot apply it")
	}
}

func TestSandboxLockdownIsInTheProfileJSON(t *testing.T) {
	// The flag is part of what the Pro manager returns and the executor reads; it must not be lost in a round trip.
	sb := models.SandboxProfile{Confined: true, NetworkLockdown: true}
	if !strings.Contains(mustJSON(t, sb), `"network_lockdown":true`) {
		t.Fatal(mustJSON(t, sb))
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
