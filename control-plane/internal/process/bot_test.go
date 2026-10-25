package process

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

type fakeBots struct {
	prepared, stopped int
	launch            *contracts.BotLaunch
	err               error
}

func (f *fakeBots) Prepare(context.Context, *models.Agent) (*contracts.BotLaunch, error) {
	f.prepared++
	return f.launch, f.err
}
func (f *fakeBots) Invoked(context.Context, *models.Agent, string, string) error { return nil }
func (f *fakeBots) Stopped(context.Context, string, string)                      { f.stopped++ }

func botAgent() *models.Agent {
	return &models.Agent{Name: "shopper", Kitchen: "k", Runtime: models.RuntimeAgenticBot, ExecutionMode: models.ExecModeLocal}
}

func TestABotIsRefusedWithoutABotManager(t *testing.T) {
	m := NewManager()
	_, err := m.Start(context.Background(), botAgent())
	if !errors.Is(err, contracts.ErrBotsNotAvailable) {
		t.Fatalf("want ErrBotsNotAvailable, got %v", err)
	}
}

func TestAnUnconfinedLocalBotIsRefusedUnlessDevelopmentOptIn(t *testing.T) {
	t.Setenv("AGENTOVEN_BOT_ALLOW_LOCAL", "")
	m := NewManager()
	fb := &fakeBots{launch: &contracts.BotLaunch{}}
	m.SetBotControl(fb)
	_, err := m.Start(context.Background(), botAgent())
	if err == nil || !strings.Contains(err.Error(), "confined sandbox") {
		t.Fatalf("want a confinement refusal, got %v", err)
	}
	if fb.prepared != 0 {
		t.Fatal("the bot manager must not be asked to prepare a bot that cannot run")
	}
}

func TestABotManagerRefusalStopsTheStartBeforeAPortIsTaken(t *testing.T) {
	t.Setenv("AGENTOVEN_BOT_ALLOW_LOCAL", "true")
	m := NewManager()
	m.SetBotControl(&fakeBots{err: errors.New("kill switch is on")})
	_, err := m.Start(context.Background(), botAgent())
	if err == nil || !strings.Contains(err.Error(), "kill switch") {
		t.Fatalf("got %v", err)
	}
	if got := m.ports.Allocate(); got != 9100 {
		t.Fatalf("a refused start must not leak a port, next port is %d", got)
	}
}

func TestApplyBotLaunchDropsGeneralCredentialsAndNeedsTheGateway(t *testing.T) {
	info := &models.ProcessInfo{}
	env := map[string]string{
		"CONTROL_PLANE_TOKEN": "baker", "AGENTOVEN_API_KEY": "k", "AGENT_API_KEY": "provider-key",
	}
	launch := &contracts.BotLaunch{Env: map[string]string{"AGENT_BOT_TOKEN": "ao_bot_x"}, Sandbox: models.SandboxProfile{Confined: true}}

	if err := applyBotLaunch(botAgent(), env, launch, info); err == nil {
		t.Fatal("a bot whose model calls bypass the gateway must be refused")
	}

	env["AGENT_MODEL_GATEWAY"] = "openai"
	if err := applyBotLaunch(botAgent(), env, launch, info); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"CONTROL_PLANE_TOKEN", "AGENTOVEN_API_KEY"} {
		if _, ok := env[k]; ok {
			t.Errorf("%s must not reach a bot", k)
		}
	}
	if env["AGENT_BOT_TOKEN"] != "ao_bot_x" || info.Sandbox == nil || !info.Sandbox.Confined {
		t.Fatalf("launch not applied: env=%v sandbox=%v", env, info.Sandbox)
	}
}

func TestSandboxDockerArgs(t *testing.T) {
	if sandboxDockerArgs(nil) != nil || sandboxDockerArgs(&models.SandboxProfile{Memory: "1g"}) != nil {
		t.Fatal("an unconfined profile adds nothing")
	}
	args := sandboxDockerArgs(&models.SandboxProfile{
		Confined: true, ReadOnlyRootFS: true, User: "65532:65532", Memory: "2g", CPUs: "1.5",
		PidsLimit: 512, ShmSize: "1g", ExtraHosts: []string{"host.docker.internal:host-gateway"},
	})
	joined := " " + strings.Join(args, " ") + " "
	for _, want := range []string{
		" --cap-drop ALL ", " --security-opt no-new-privileges ", " --read-only ", " --user 65532:65532 ",
		" --tmpfs /tmp:rw,noexec,nosuid,nodev,size=512m ",
		" --tmpfs /home/bot:rw,noexec,nosuid,nodev,uid=65532,gid=65532,size=512m ",
		" --memory 2g ", " --cpus 1.5 ", " --pids-limit 512 ", " --shm-size 1g ", " --add-host host.docker.internal:host-gateway ",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %v", want, args)
		}
	}
}

func TestSandboxedDeploymentIsConfined(t *testing.T) {
	dep := buildDeploymentManifest("agent-x", "ns", "img:1", "pull", "", nil)
	applySandboxToDeployment(dep, &models.SandboxProfile{Confined: true, ReadOnlyRootFS: true, User: "65532:65532", RuntimeClass: "gvisor", Memory: "3Gi"})
	spec := dep["spec"].(map[string]interface{})["template"].(map[string]interface{})["spec"].(map[string]interface{})
	if spec["runtimeClassName"] != "gvisor" || spec["automountServiceAccountToken"] != false {
		t.Fatalf("pod spec not confined: %v", spec)
	}
	sec := spec["securityContext"].(map[string]interface{})
	if sec["runAsNonRoot"] != true || sec["runAsUser"] != int64(65532) {
		t.Fatalf("pod security context: %v", sec)
	}
	c := spec["containers"].([]map[string]interface{})[0]
	csec := c["securityContext"].(map[string]interface{})
	if csec["allowPrivilegeEscalation"] != false || csec["readOnlyRootFilesystem"] != true {
		t.Fatalf("container security context: %v", csec)
	}
	if c["resources"].(map[string]interface{})["limits"].(map[string]string)["memory"] != "3Gi" {
		t.Fatalf("limits: %v", c["resources"])
	}

	plain := buildDeploymentManifest("agent-y", "ns", "img:1", "pull", "", nil)
	before := plain["spec"].(map[string]interface{})["template"].(map[string]interface{})["spec"].(map[string]interface{})
	applySandboxToDeployment(plain, nil)
	if _, ok := before["securityContext"]; ok {
		t.Fatal("a nil profile must leave the manifest alone")
	}
}

func TestBotNetworkPolicyClosesEgressExceptDNSAndTheControlPlane(t *testing.T) {
	old := resolveFunc
	resolveFunc = func(host string) ([]net.IP, error) {
		if host == "cp.internal" {
			return []net.IP{net.ParseIP("10.1.2.3"), net.ParseIP("fd00::5")}, nil
		}
		return nil, errors.New("nxdomain")
	}
	defer func() { resolveFunc = old }()

	np := buildBotNetworkPolicy("agent-x", "ns", []string{"cp.internal:8080", "192.0.2.9:8480", "gone.example:1", "bad"})
	spec := np["spec"].(map[string]interface{})
	if !reflect.DeepEqual(spec["policyTypes"], []string{"Egress"}) {
		t.Fatalf("policy types: %v", spec["policyTypes"])
	}
	egress := spec["egress"].([]map[string]interface{})
	if len(egress) != 3 { // DNS, cp.internal, 192.0.2.9; the unresolvable and malformed entries are dropped
		t.Fatalf("want 3 egress rules, got %d: %v", len(egress), egress)
	}
	to := egress[1]["to"].([]map[string]interface{})
	cidrs := []string{}
	for _, t := range to {
		cidrs = append(cidrs, t["ipBlock"].(map[string]interface{})["cidr"].(string))
	}
	if !reflect.DeepEqual(cidrs, []string{"10.1.2.3/32", "fd00::5/128"}) {
		t.Fatalf("cidrs: %v", cidrs)
	}
}

func TestSandboxScratchIsOwnedByTheBotUserWhateverItsUID(t *testing.T) {
	args := strings.Join(sandboxDockerArgs(&models.SandboxProfile{Confined: true, User: "1001:2002", TmpfsSize: "64m"}), " ")
	if !strings.Contains(args, "/home/bot:rw,noexec,nosuid,nodev,uid=1001,gid=2002,size=64m") {
		t.Fatalf("scratch must be owned by the bot's own uid:gid: %s", args)
	}
	// With no user set the default non-root identity of the image is used, never root.
	args = strings.Join(sandboxDockerArgs(&models.SandboxProfile{Confined: true}), " ")
	if !strings.Contains(args, "uid=65532,gid=65532") {
		t.Fatalf("default uid: %s", args)
	}
}
