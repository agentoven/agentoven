package process

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func startRunner(t *testing.T, env ...string) string {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	l, _ := net.Listen("tcp4", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, py, "templates/agent_runner.py")
	cmd.Env = append(os.Environ(), append([]string{fmt.Sprintf("AGENT_PORT=%d", port), "AGENT_NAME=tool-test", "AGENT_MODEL_GATEWAY=openai", "AGENT_GATEWAY_MODEL=p/m", "AGENT_API_KEY=ao_run_x"}, env...)...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = cmd.Wait() })
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if resp, err := http.Get(base + "/health"); err == nil {
			resp.Body.Close()
			return base
		}
		if time.Now().After(deadline) {
			t.Fatal("the runner did not come up")
		}
	}
}

// The runner asks the control plane to run a tool; it never calls the tool's own endpoint, and the token it presents is the one
// the control plane put on the relayed call (ADR-0044).
func TestRunnerAsksTheControlPlaneToRunToolsAndNeverCallsTheEndpoint(t *testing.T) {
	var direct int32
	toolSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&direct, 1) }))
	defer toolSrv.Close()

	var mu sync.Mutex
	var cpAuth, cpPath string
	var cpBody map[string]interface{}
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		cpAuth, cpPath = r.Header.Get("Authorization"), r.URL.Path
		_ = json.Unmarshal(raw, &cpBody)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true,"content":"order A-1 shipped"}`))
	}))
	defer cp.Close()

	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Messages []map[string]interface{} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		sawTool := false
		for _, m := range in.Messages {
			if m["role"] == "tool" {
				sawTool = true
			}
		}
		if sawTool {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"done"},"finish_reason":"stop"}],"usage":{"total_tokens":1}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"lookup_order","arguments":"{\"order_id\":\"A-1\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"total_tokens":1}}`))
	}))
	defer model.Close()

	tools, _ := json.Marshal([]models.ResolvedTool{{Name: "lookup_order", Endpoint: toolSrv.URL, Transport: "http", Schema: map[string]interface{}{"type": "object"}}})
	base := startRunner(t, "AGENT_API_ENDPOINT="+model.URL+"/v1", "AGENT_TOOLS_JSON="+string(tools), "AGENTOVEN_CONTROL_PLANE_URL="+cp.URL)

	call := func(token string) (int, string) {
		req, _ := http.NewRequest(http.MethodPost, base+"/invoke", bytes.NewReader([]byte(`{"message":"where is A-1"}`)))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("X-AgentOven-Tool-Token", token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}

	if code, body := call("aotc_the-relayed-token"); code != 200 || !strings.Contains(body, "done") {
		t.Fatalf("%d %s", code, body)
	}
	mu.Lock()
	if cpPath != "/api/v1/internal/tool-calls" || cpAuth != "Bearer aotc_the-relayed-token" || cpBody["name"] != "lookup_order" {
		t.Errorf("the control plane was asked %q with %q: %v", cpPath, cpAuth, cpBody)
	}
	args, _ := cpBody["arguments"].(map[string]interface{})
	if args["order_id"] != "A-1" {
		t.Errorf("arguments: %v", cpBody)
	}
	cpPath = ""
	mu.Unlock()

	// A call that carries no token has no way to run a tool: the model is told so, nothing is sent anywhere.
	if code, body := call(""); code != 200 || !strings.Contains(body, "done") {
		t.Fatalf("%d %s", code, body)
	}
	mu.Lock()
	if cpPath != "" {
		t.Error("a call without a token must not reach the control plane")
	}
	mu.Unlock()
	if atomic.LoadInt32(&direct) != 0 {
		t.Fatal("the runner called the tool's own endpoint")
	}
}

// What a process is told about where its tools live: nothing, unless it is not the embedded runner.
func TestAProcessIsNotToldWhereItsToolsLive(t *testing.T) {
	a := &models.Agent{Name: "a", ResolvedConfig: &models.ResolvedIngredients{Tools: []models.ResolvedTool{
		{Name: "t", Endpoint: "http://internal.example/mcp", Transport: "http", Schema: map[string]interface{}{"type": "object"}}}}}
	for _, got := range toolsForProcess(a) {
		if got.Endpoint != "" || got.Name != "t" || got.Schema == nil {
			t.Fatalf("the embedded runner needs the name and schema only: %+v", got)
		}
	}
	if a.ResolvedConfig.Tools[0].Endpoint == "" {
		t.Fatal("the stored agent was changed")
	}
	a.Entrypoint = "python my_agent.py"
	if toolsForProcess(a)[0].Endpoint == "" {
		t.Fatal("an agent with its own entrypoint reads its tools as before")
	}
	b := &models.Agent{Name: "b", Runtime: "langgraph", ResolvedConfig: a.ResolvedConfig}
	if toolsForProcess(b)[0].Endpoint == "" {
		t.Fatal("a framework-native agent reads its tools as before")
	}
}
