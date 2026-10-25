package process

import (
	"reflect"
	"testing"
)

func firstContainer(t *testing.T, dep map[string]interface{}) map[string]interface{} {
	t.Helper()
	spec := dep["spec"].(map[string]interface{})["template"].(map[string]interface{})["spec"].(map[string]interface{})
	cs := spec["containers"].([]map[string]interface{})
	if len(cs) != 1 {
		t.Fatalf("expected one container, got %d", len(cs))
	}
	return cs[0]
}

func TestAnEntrypointBecomesTheContainersCommand(t *testing.T) {
	env := []map[string]interface{}{{"name": "AGENT_PORT", "value": "9000"}}
	c := firstContainer(t, buildDeploymentManifest("agent-x", "ns", "img:1", "pull", "python research_agent.py --serve", env))
	want := []string{"python", "research_agent.py", "--serve"}
	if !reflect.DeepEqual(c["command"], want) {
		t.Fatalf("command = %v, want %v", c["command"], want)
	}
}

func TestWithoutAnEntrypointTheImagesOwnCommandRuns(t *testing.T) {
	c := firstContainer(t, buildDeploymentManifest("agent-x", "ns", "img:1", "pull", "", nil))
	if _, set := c["command"]; set {
		t.Fatalf("no entrypoint means no command override, got %v", c["command"])
	}
	c = firstContainer(t, buildDeploymentManifest("agent-x", "ns", "img:1", "pull", "   ", nil))
	if _, set := c["command"]; set {
		t.Fatal("a blank entrypoint is no entrypoint")
	}
}

func TestTheAgentIsProbedOnHealth(t *testing.T) {
	c := firstContainer(t, buildDeploymentManifest("agent-x", "ns", "img:1", "pull", "python a.py", nil))
	for _, probe := range []string{"readinessProbe", "livenessProbe"} {
		get := c[probe].(map[string]interface{})["httpGet"].(map[string]interface{})
		if get["path"] != "/health" || get["port"] != 9000 {
			t.Fatalf("%s should hit /health on 9000: %v", probe, get)
		}
	}
}
