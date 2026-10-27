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
	"sync"
	"testing"
	"time"
)

// The hosted runner must speak the model gateway's protocol when the control plane fronts its model
// calls: the gateway's own base URL (which already ends in /v1), the credential it was given, the
// model named "provider/model", and the run it belongs to. It used to build "<base>/v1/chat/completions"
// with the provider's own wire format, so every call was refused and the agent never answered.
func TestRunnerSpeaksTheGatewayProtocol(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}

	var mu sync.Mutex
	var got struct {
		path, auth, run string
		body            map[string]interface{}
	}
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		got.path, got.auth, got.run = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("X-AgentOven-Run-Id")
		_ = json.Unmarshal(raw, &got.body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hello from the gateway"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`))
	}))
	defer gw.Close()

	l, _ := net.Listen("tcp4", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, py, "templates/agent_runner.py")
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("AGENT_PORT=%d", port), "AGENT_NAME=gw-test",
		"AGENT_MODEL_PROVIDER=anthropic", "AGENT_MODEL_NAME=claude-sonnet-4-5", // the provider kind must not decide the wire format
		"AGENT_MODEL_GATEWAY=openai", "AGENT_GATEWAY_MODEL=anthropic/claude-sonnet-4-5",
		"AGENT_API_ENDPOINT="+gw.URL+"/v1", "AGENT_API_KEY=ao_run_test-credential")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = cmd.Wait() }()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(15 * time.Second)
	for {
		if resp, err := http.Get(base + "/health"); err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the runner did not come up")
		}
		time.Sleep(100 * time.Millisecond)
	}

	req, _ := http.NewRequest(http.MethodPost, base+"/invoke", bytes.NewReader([]byte(`{"message":"hi"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-AgentOven-Run-Id", "run-123")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Response string `json:"response"`
		Error    string `json:"error"`
		Turns    []struct {
			Model string `json:"model"`
		} `json:"turns"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != 200 || out.Response != "hello from the gateway" {
		t.Fatalf("status %d, response %q, error %q", resp.StatusCode, out.Response, out.Error)
	}

	mu.Lock()
	defer mu.Unlock()
	if got.path != "/v1/chat/completions" {
		t.Errorf("path = %q, want /v1/chat/completions (the gateway's base already ends in /v1)", got.path)
	}
	if got.auth != "Bearer ao_run_test-credential" {
		t.Errorf("authorization = %q", got.auth)
	}
	if got.body["model"] != "anthropic/claude-sonnet-4-5" {
		t.Errorf("model = %v, want the gateway's provider/model name", got.body["model"])
	}
	if got.run != "run-123" {
		t.Errorf("run header = %q: the gateway cannot hold the call to its run's budget", got.run)
	}
	if _, anthropicShaped := got.body["system"]; anthropicShaped || got.body["messages"] == nil {
		t.Errorf("the request was not in the OpenAI shape: %v", got.body)
	}
}
