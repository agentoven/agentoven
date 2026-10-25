package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

type recordingBots struct {
	mu      sync.Mutex
	invoked []string // runID|message
	refuse  error
}

func (b *recordingBots) Prepare(context.Context, *models.Agent) (*contracts.BotLaunch, error) {
	return &contracts.BotLaunch{}, nil
}
func (b *recordingBots) Invoked(_ context.Context, _ *models.Agent, runID, message string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.invoked = append(b.invoked, runID+"|"+message)
	return b.refuse
}
func (b *recordingBots) Stopped(context.Context, string, string) {}

// botProcess is a bot's process as far as the control plane can see it: it reports the run_id it was given.
func botProcess(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	runIDs := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RunID   string `json:"run_id"`
			TraceID string `json:"trace_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		runIDs = append(runIDs, body.RunID)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"response": "done"})
	}))
	t.Cleanup(srv.Close)
	return srv, &runIDs
}

func TestABotInvokeRegistersItsRunWithTheBotManagerBeforeTheProcessSeesIt(t *testing.T) {
	proc, runIDs := botProcess(t)
	agent := nativeAgent(models.RuntimeAgenticBot, proc.URL)
	agent.Guardrails = nil
	h, _ := newNativeHandlers(t, agent)
	bots := &recordingBots{}
	h.Bots = bots

	rec := call(h.InvokeAgent, "/invoke", msg("find the cheapest flight"), map[string]string{"agentName": "native-agent"})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if len(bots.invoked) != 1 || !strings.HasSuffix(bots.invoked[0], "|find the cheapest flight") {
		t.Fatalf("bot manager not told about the invoke: %v", bots.invoked)
	}
	runID := strings.SplitN(bots.invoked[0], "|", 2)[0]
	if runID == "" || len(*runIDs) != 1 || (*runIDs)[0] != runID {
		t.Fatalf("the process must be handed the run id the manager was told (%q), got %v", runID, *runIDs)
	}
}

func TestABotManagerCanRefuseAnInvokeBeforeItReachesTheSandbox(t *testing.T) {
	proc, runIDs := botProcess(t)
	agent := nativeAgent(models.RuntimeAgenticBot, proc.URL)
	agent.Guardrails = nil
	h, _ := newNativeHandlers(t, agent)
	h.Bots = &recordingBots{refuse: errors.New("kill switch is on")}

	rec := call(h.InvokeAgent, "/invoke", msg("go"), map[string]string{"agentName": "native-agent"})
	if rec.Code == http.StatusOK || len(*runIDs) != 0 {
		t.Fatalf("a refused invoke must not reach the process: code=%d reached=%d", rec.Code, len(*runIDs))
	}
}

func TestABotIsNotInvokableWithoutABotManager(t *testing.T) {
	proc, runIDs := botProcess(t)
	agent := nativeAgent(models.RuntimeAgenticBot, proc.URL)
	agent.Guardrails = nil
	h, _ := newNativeHandlers(t, agent) // community edition: no Bots

	rec := call(h.InvokeAgent, "/invoke", msg("go"), map[string]string{"agentName": "native-agent"})
	if rec.Code == http.StatusOK || len(*runIDs) != 0 {
		t.Fatalf("got %d, process reached %d times", rec.Code, len(*runIDs))
	}
}

func TestABotRunStartedOverA2AIsRegisteredAndGivenItsRunID(t *testing.T) {
	var mu sync.Mutex
	var headerRun []string
	proc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		headerRun = append(headerRun, r.Header.Get("X-AgentOven-Run-Id"))
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": 1, "result": map[string]interface{}{
			"status": map[string]string{"state": "completed"}}})
	}))
	defer proc.Close()
	agent := nativeAgent(models.RuntimeAgenticBot, proc.URL)
	agent.Guardrails = nil
	h, _ := newNativeHandlers(t, agent)
	bots := &recordingBots{}
	h.Bots = bots

	call(h.A2AAgentEndpoint, "/agents/native-agent/a2a", a2aBody("book the flight"), map[string]string{"agentName": "native-agent"})
	if len(bots.invoked) != 1 || !strings.HasSuffix(bots.invoked[0], "|book the flight") {
		t.Fatalf("an A2A message must register the run with the bot manager: %v", bots.invoked)
	}
	runID := strings.SplitN(bots.invoked[0], "|", 2)[0]
	if len(headerRun) != 1 || headerRun[0] != runID || runID == "" {
		t.Fatalf("the process must be sent the registered run id %q, got %v", runID, headerRun)
	}

	// A call that starts nothing (tasks/get) is not a run.
	getBody := `{"jsonrpc":"2.0","method":"tasks/get","id":2,"params":{"id":"t1"}}`
	call(h.A2AAgentEndpoint, "/agents/native-agent/a2a", getBody, map[string]string{"agentName": "native-agent"})
	if len(bots.invoked) != 1 {
		t.Fatalf("tasks/get must not register a run: %v", bots.invoked)
	}

	// The manager can refuse an A2A run before it reaches the sandbox.
	bots.refuse = errors.New("kill switch is on")
	before := len(headerRun) // tasks/get legitimately reached the process
	rec := call(h.A2AAgentEndpoint, "/agents/native-agent/a2a", a2aBody("again"), map[string]string{"agentName": "native-agent"})
	if !strings.Contains(rec.Body.String(), "-32007") || len(headerRun) != before {
		t.Fatalf("a refused A2A run must not reach the process: %s", rec.Body.String())
	}
}

func TestRegisteringABotNeedsABotManagerAndAValidConfig(t *testing.T) {
	h, _ := newNativeHandlers(t, nativeAgent(models.RuntimeLangChain, "http://unused"))
	register := func(body string) *httptest.ResponseRecorder {
		return call(h.RegisterAgent, "/api/v1/agents", body, nil)
	}
	botBody := `{"name":"b1","runtime":"agenticbot","bot":{"start_url":"https://shop.example"}}`

	if rec := register(botBody); rec.Code != http.StatusForbidden {
		t.Fatalf("community edition: want 403, got %d %s", rec.Code, rec.Body.String())
	}
	h.Bots = &recordingBots{}
	if rec := register(botBody); rec.Code != http.StatusCreated {
		t.Fatalf("valid bot: want 201, got %d %s", rec.Code, rec.Body.String())
	}
	if rec := register(`{"name":"b2","runtime":"agenticbot","bot":{"start_url":"javascript:alert(1)"}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad start url: want 400, got %d %s", rec.Code, rec.Body.String())
	}
	if rec := register(`{"name":"b3","runtime":"agenticbot","mode":"external"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("external mode: want 400, got %d %s", rec.Code, rec.Body.String())
	}
	if rec := register(`{"name":"plain","runtime":"langchain"}`); rec.Code != http.StatusCreated {
		t.Fatalf("other runtimes are untouched: got %d %s", rec.Code, rec.Body.String())
	}
}
