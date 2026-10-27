package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/a2aauth"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// An agent stored before agents had a mode has none, and has always been run as a managed one. The
// A2A proxy gave a pod token only to agents whose mode said "managed", so these agents' processes
// (which require it) refused every call from the control plane.
func TestAnAgentWithNoModeIsCalledLikeAManagedOne(t *testing.T) {
	var mu sync.Mutex
	var sawToken bool
	proc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sawToken = r.Header.Get(a2aauth.PodTokenHeader) != ""
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"status":{"state":"completed"},"artifacts":[{"parts":[{"type":"text","text":"ok"}]}]}}`))
	}))
	defer proc.Close()

	agent := nativeAgent(models.RuntimeAgentOven, proc.URL)
	agent.Guardrails = nil
	agent.Mode = "" // as stored by an older version
	h, _ := newNativeHandlers(t, agent)
	h.A2ASecret = []byte("test-secret")

	body := `{"jsonrpc":"2.0","id":1,"method":"tasks/send","params":{"message":{"role":"user","parts":[{"type":"text","text":"hi"}]}}}`
	rec := call(h.A2AAgentEndpoint, "/api/v1/agents/native-agent/a2a", body, map[string]string{"agentName": "native-agent"})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if !sawToken {
		t.Error("the call to the agent's process carried no pod token, so a process that requires one refuses it")
	}
}
