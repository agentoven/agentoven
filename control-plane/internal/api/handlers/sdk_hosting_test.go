package handlers_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/process"
	"github.com/agentoven/agentoven/control-plane/pkg/a2aauth"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// A Deep Agents graph served by the Python SDK, started by the real local process manager and
// reached through the control plane's handlers. It is the end-to-end check that an agent hosted
// with agentoven.runtime satisfies the contract the control plane relies on: it starts under
// AGENT_PORT, prints AGENT_READY, answers /health, takes the pod token, and gets its session as
// thread_id, with the guardrails enforced at the edge.
//
// It needs a Python with fastapi and uvicorn: set AGENTOVEN_SDK_PYTHON to run it, for example
//
//	AGENTOVEN_SDK_PYTHON=/path/to/venv/bin/python go test ./internal/api/handlers -run SDKHosted
const sdkAgentScript = `
from agentoven.runtime import AgentOvenRuntime, DeepAgentsAdapter, serve


class Graph:
    """Stands in for create_deep_agent(): the same ainvoke contract, no model."""

    async def ainvoke(self, state, config=None):
        text = state["messages"][-1]["content"]
        thread = ((config or {}).get("configurable") or {}).get("thread_id")
        reply = "echo[%s]: %s" % (thread, text)
        if "leak" in text:
            reply += " their SSN is 123-45-6789"
        return {
            "messages": state["messages"] + [{"role": "assistant", "content": reply,
                                             "usage_metadata": {"input_tokens": 3, "output_tokens": 4}}],
            "todos": [{"content": "answer", "status": "completed"}],
        }


runtime = AgentOvenRuntime()
serve(DeepAgentsAdapter(Graph()), runtime)
`

func TestSDKHostedDeepAgentsRunsUnderTheControlPlane(t *testing.T) {
	py := os.Getenv("AGENTOVEN_SDK_PYTHON")
	if py == "" {
		t.Skip("set AGENTOVEN_SDK_PYTHON to a Python with fastapi and uvicorn to run the SDK hosting test")
	}
	sdk, err := filepath.Abs("../../../../sdk/python")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "agent.py")
	if err := os.WriteFile(script, []byte(sdkAgentScript), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PYTHONPATH", sdk)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	secret := []byte("test-a2a-secret")
	agent := nativeAgent(models.RuntimeDeepAgents, fmt.Sprintf("http://127.0.0.1:%d", port))
	agent.Name = "deep"
	agent.Entrypoint = py + " " + script
	h, _ := newNativeHandlers(t, agent)
	h.A2ASecret = secret

	le := process.NewLocalExecutor()
	info := &models.ProcessInfo{AgentName: "deep", Kitchen: "default", Port: port, Endpoint: fmt.Sprintf("http://127.0.0.1:%d", port)}
	env := map[string]string{
		"AGENT_NAME": "deep", "AGENT_KITCHEN": "default", "AGENT_RUNTIME": "deepagents",
		"AGENT_PORT":        fmt.Sprint(port),
		a2aauth.PodTokenEnv: a2aauth.PodToken(secret, "default", "deep"),
	}
	if err := le.Start(context.Background(), agent, info, env); err != nil {
		t.Fatalf("the process did not start: %v", err)
	}
	t.Cleanup(func() { _ = le.Stop(context.Background(), info) })

	params := map[string]string{"agentName": "deep"}

	t.Run("a call reaches the agent with its session as the thread", func(t *testing.T) {
		rec := call(h.InvokeAgent, "/invoke", `{"message":"hello","session_id":"S-42"}`, params)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "echo[S-42]: hello") {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("a leaky answer is stopped at the edge", func(t *testing.T) {
		rec := call(h.InvokeAgent, "/invoke", `{"message":"please leak"}`, params)
		if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "123-45-6789") {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("a blocked input never reaches the agent", func(t *testing.T) {
		rec := call(h.InvokeAgent, "/invoke", `{"message":"please `+blockedWord+`"}`, params)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("a streamed leaky answer is held back", func(t *testing.T) {
		rec := call(h.StreamInvokeAgent, "/invoke/stream", `{"message":"please leak"}`, params)
		if strings.Contains(rec.Body.String(), "123-45-6789") {
			t.Fatalf("the SSN reached the caller:\n%s", rec.Body.String())
		}
	})

	t.Run("a clean stream is relayed", func(t *testing.T) {
		rec := call(h.StreamInvokeAgent, "/invoke/stream", `{"message":"hi"}`, params)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "echo[") || !strings.Contains(rec.Body.String(), `"done"`) {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("the process refuses a caller without its token", func(t *testing.T) {
		resp, err := http.Post(info.Endpoint+"/invoke", "application/json", strings.NewReader(`{"message":"hi"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("a direct call without the pod token should be 401, got %d", resp.StatusCode)
		}
	})

	t.Run("health is public", func(t *testing.T) {
		resp, err := http.Get(info.Endpoint + "/health")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("health: %d", resp.StatusCode)
		}
	})
}

func TestAHostedProcessIsOnlyGivenAPlainSessionToken(t *testing.T) {
	p := newFakeProcess(t, "fine")
	h, _ := newNativeHandlers(t, nativeAgent(models.RuntimeDeepAgents, p.srv.URL))
	params := map[string]string{"agentName": "native-agent"}

	rec := call(h.InvokeAgent, "/invoke", `{"message":"hi","session_id":"abc-123"}`, params)
	if rec.Code != http.StatusOK || strings.Join(p.threadIDs(), ",") != "abc-123" {
		t.Fatalf("a plain session ID is the thread: %d %v", rec.Code, p.threadIDs())
	}

	call(h.InvokeAgent, "/invoke", `{"message":"hi","session_id":"../../etc/passwd\n{evil}"}`, params)
	if got := strings.Join(p.threadIDs(), ","); got != "abc-123" {
		t.Fatalf("an ID that is not a plain token must not reach the framework's checkpointer, threads now %q", got)
	}

	call(h.StreamInvokeAgent, "/invoke/stream", `{"message":"hi","session_id":"stream-9"}`, params)
	if got := strings.Join(p.threadIDs(), ","); got != "abc-123,stream-9" {
		t.Fatalf("streaming continues the conversation too, threads %q", got)
	}
}
