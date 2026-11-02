package process

import (
	"context"
	"fmt"
	"net"
	"os"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// botBinary is the ao-bot executable an unconfined local bot runs (development only).
func botBinary() string {
	if b := os.Getenv("AGENTOVEN_BOT_BINARY"); b != "" {
		return b
	}
	return "ao-bot"
}

// applyBotLaunch makes a bot's environment what the bot manager decided, and nothing wider.
//
// A bot renders hostile web pages, so it is held to a narrower contract than other hosted
// agents. It never receives the general control-plane token (CONTROL_PLANE_TOKEN is dropped even
// though every other hosted agent gets one), and it is refused unless its model calls go through
// the metered model gateway, so a provider's own key is never inside the sandbox.
func applyBotLaunch(agent *models.Agent, env map[string]string, launch *contracts.BotLaunch, info *models.ProcessInfo) error {
	delete(env, "CONTROL_PLANE_TOKEN")
	delete(env, "AGENTOVEN_API_KEY")
	delete(env, "AGENTOVEN_PRO_TOKEN")
	// The kitchen's tools run in the control plane, which holds their credentials; the sandbox is
	// told what it may call by the gate (StartRunResponse.Tools), not by the environment.
	delete(env, "AGENT_TOOLS_JSON")
	delete(env, "AGENT_DATA_SOURCES_JSON")
	delete(env, "AGENT_SKILLS")
	if env["AGENT_MODEL_GATEWAY"] != "openai" {
		return fmt.Errorf("agentic bot %q cannot start: its model calls must go through the metered model gateway "+
			"so no provider key enters the sandbox (is the model gateway enabled and does the agent have a model?)", agent.Name)
	}
	for k, v := range launch.Env {
		env[k] = v
	}
	sb := launch.Sandbox
	info.Sandbox = &sb
	return nil
}

// sandboxDockerArgs turns a sandbox profile into `docker run` flags. A nil or unconfined profile
// adds nothing, which is what every non-bot agent gets.
func sandboxDockerArgs(sb *models.SandboxProfile) []string {
	if sb == nil || !sb.Confined {
		return nil
	}
	size := sb.TmpfsSize
	if size == "" {
		size = "512m"
	}
	// The scratch areas are owned by the user the bot runs as (a tmpfs is root-owned otherwise, and a
	// non-root bot could not write its workspace or browser profile) and cannot execute files.
	uid, gid, ok := parseUIDGID(sb.User)
	if !ok {
		uid, gid = 65532, 65532
	}
	args := []string{
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=" + size,
		"--tmpfs", fmt.Sprintf("/home/bot:rw,noexec,nosuid,nodev,uid=%d,gid=%d,size=%s", uid, gid, size),
	}
	if sb.ReadOnlyRootFS {
		args = append(args, "--read-only")
	}
	if sb.User != "" {
		args = append(args, "--user", sb.User)
	}
	if sb.Memory != "" {
		args = append(args, "--memory", sb.Memory)
	}
	if sb.CPUs != "" {
		args = append(args, "--cpus", sb.CPUs)
	}
	if sb.PidsLimit > 0 {
		args = append(args, "--pids-limit", fmt.Sprintf("%d", sb.PidsLimit))
	}
	if sb.ShmSize != "" {
		args = append(args, "--shm-size", sb.ShmSize)
	}
	for _, h := range sb.ExtraHosts {
		args = append(args, "--add-host", h)
	}
	return args
}

// sandboxPodSecurity returns the pod-level and container-level security settings for a
// confined sandbox, or nils when there is nothing to apply.
func sandboxPodSecurity(sb *models.SandboxProfile) (pod map[string]interface{}, container map[string]interface{}) {
	if sb == nil || !sb.Confined {
		return nil, nil
	}
	pod = map[string]interface{}{
		"runAsNonRoot":   true,
		"seccompProfile": map[string]interface{}{"type": "RuntimeDefault"},
	}
	container = map[string]interface{}{
		"allowPrivilegeEscalation": false,
		"readOnlyRootFilesystem":   sb.ReadOnlyRootFS,
		"capabilities":             map[string]interface{}{"drop": []string{"ALL"}},
	}
	if uid, gid, ok := parseUIDGID(sb.User); ok {
		pod["runAsUser"] = uid
		pod["runAsGroup"] = gid
		pod["fsGroup"] = gid
	}
	return pod, container
}

func parseUIDGID(s string) (uid, gid int64, ok bool) {
	var u, g int64
	if n, err := fmt.Sscanf(s, "%d:%d", &u, &g); err == nil && n == 2 {
		return u, g, true
	}
	if n, err := fmt.Sscanf(s, "%d", &u); err == nil && n == 1 {
		return u, u, true
	}
	return 0, 0, false
}

// applySandboxToDeployment confines a Deployment manifest built by buildDeploymentManifest.
// It mutates dep in place and is a no-op for an unconfined profile.
func applySandboxToDeployment(dep map[string]interface{}, sb *models.SandboxProfile) {
	podSec, ctrSec := sandboxPodSecurity(sb)
	if podSec == nil {
		return
	}
	tmplSpec := dep["spec"].(map[string]interface{})["template"].(map[string]interface{})["spec"].(map[string]interface{})
	tmplSpec["securityContext"] = podSec
	tmplSpec["automountServiceAccountToken"] = false // a bot has no business talking to the Kubernetes API
	if sb.RuntimeClass != "" {
		tmplSpec["runtimeClassName"] = sb.RuntimeClass
	}
	size := sb.TmpfsSize
	if size == "" {
		size = "512Mi"
	}
	shm := sb.ShmSize
	if shm == "" {
		shm = "1Gi"
	}
	tmplSpec["volumes"] = []map[string]interface{}{
		{"name": "tmp", "emptyDir": map[string]interface{}{"sizeLimit": size}},
		{"name": "home", "emptyDir": map[string]interface{}{"sizeLimit": size}},
		{"name": "shm", "emptyDir": map[string]interface{}{"medium": "Memory", "sizeLimit": shm}},
	}
	c := tmplSpec["containers"].([]map[string]interface{})[0]
	c["securityContext"] = ctrSec
	c["volumeMounts"] = []map[string]interface{}{
		{"name": "tmp", "mountPath": "/tmp"},
		{"name": "home", "mountPath": "/home/bot"},
		{"name": "shm", "mountPath": "/dev/shm"},
	}
	limits := map[string]string{"memory": firstNonEmpty(sb.Memory, "2Gi"), "cpu": firstNonEmpty(sb.CPUs, "1")}
	c["resources"] = map[string]interface{}{
		"requests": map[string]string{"memory": "512Mi", "cpu": "250m"},
		"limits":   limits,
	}
	// A bot's pod is never scheduled with host access or auto-mounted credentials.
	if sb.Image != "" {
		c["image"] = sb.Image
	}
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// resolveFunc resolves a host name to addresses; replaced in tests.
var resolveFunc = net.LookupIP

// buildBotNetworkPolicy returns a NetworkPolicy that closes a bot pod's egress except DNS and the
// listed host:port destinations (the control plane's gate and egress proxy), each resolved to
// the addresses it has now. Ingress is open to the control plane only through the Service port.
func buildBotNetworkPolicy(deployName, namespace string, allow []string) map[string]interface{} {
	egress := []map[string]interface{}{
		{ // DNS
			"ports": []map[string]interface{}{
				{"protocol": "UDP", "port": 53},
				{"protocol": "TCP", "port": 53},
			},
		},
	}
	for _, dest := range allow {
		host, portStr, err := net.SplitHostPort(dest)
		if err != nil {
			continue
		}
		var port int
		if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil || port <= 0 {
			continue
		}
		var cidrs []map[string]interface{}
		if ip := net.ParseIP(host); ip != nil {
			cidrs = append(cidrs, map[string]interface{}{"ipBlock": map[string]interface{}{"cidr": hostCIDR(ip)}})
		} else if ips, err := resolveFunc(host); err == nil {
			for _, ip := range ips {
				cidrs = append(cidrs, map[string]interface{}{"ipBlock": map[string]interface{}{"cidr": hostCIDR(ip)}})
			}
		}
		if len(cidrs) == 0 {
			continue
		}
		egress = append(egress, map[string]interface{}{
			"to":    cidrs,
			"ports": []map[string]interface{}{{"protocol": "TCP", "port": port}},
		})
	}
	return map[string]interface{}{
		"apiVersion": "networking.k8s.io/v1",
		"kind":       "NetworkPolicy",
		"metadata": map[string]interface{}{
			"name":      deployName + "-egress",
			"namespace": namespace,
			"labels":    map[string]string{"app": deployName, "agentoven.dev/component": "agent-sandbox"},
		},
		"spec": map[string]interface{}{
			"podSelector": map[string]interface{}{"matchLabels": map[string]string{"app": deployName}},
			"policyTypes": []string{"Egress"},
			"egress":      egress,
		},
	}
}

func hostCIDR(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return v4.String() + "/32"
	}
	return ip.String() + "/128"
}

// confineDockerBot applies the bot manager's network confinement to a container that just started.
// It does nothing for a sandbox that did not ask for it. If the confinement cannot be applied the
// container is removed and the start fails: a bot is never left running with the egress it was
// supposed to lose.
func (m *Manager) confineDockerBot(ctx context.Context, info *models.ProcessInfo) error {
	if info.Sandbox == nil || !info.Sandbox.NetworkLockdown {
		return nil
	}
	c, ok := m.bots.(contracts.ContainerConfiner)
	if !ok {
		_ = m.docker.Stop(ctx, info)
		return fmt.Errorf("the sandbox asks for network confinement but the bot manager cannot apply it")
	}
	if err := c.ConfineContainer(ctx, info.ContainerID, *info.Sandbox); err != nil {
		_ = m.docker.Stop(ctx, info)
		return fmt.Errorf("could not confine the sandbox's network, so it was removed: %w", err)
	}
	return nil
}
