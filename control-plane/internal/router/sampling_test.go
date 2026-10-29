package router_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func params() (*float64, *float64, *int) {
	t, p, m := 0.2, 0.9, 77
	return &t, &p, &m
}

func send(t *testing.T, kind, model, respBody string, cat fakeCatalog, req models.RouteRequest) map[string]interface{} {
	t.Helper()
	var sent map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &sent)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()
	mr, _ := routerWith(t, cat, models.ModelProvider{Name: "p", Kind: kind, Endpoint: srv.URL, Models: []string{model}, Config: map[string]interface{}{"api_key": "k"}})
	req.Kitchen, req.Model = "default", model
	req.Messages = []models.ChatMessage{{Role: "user", Content: "hi"}}
	if _, err := mr.Route(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	return sent
}

const oaiOK = `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

// A temperature, a top_p, stop sequences and an output limit set on a request reach the provider. They were
// carried on the request and never sent, so a caller's setting, a replay's edit and the executor's budget
// headroom did nothing.
func TestSamplingParametersAndTheOutputLimitReachTheProvider(t *testing.T) {
	temp, topP, max := params()
	r := models.RouteRequest{Temperature: temp, TopP: topP, MaxTokens: max, StopSequences: []string{"END"}}

	got := send(t, "openai", "gpt-4o", oaiOK, nil, r)
	if got["temperature"] != 0.2 || got["top_p"] != 0.9 || got["max_completion_tokens"] != float64(77) || got["max_tokens"] != nil || got["stop"].([]interface{})[0] != "END" {
		t.Errorf("openai: %v", got)
	}
	got = send(t, "ollama", "llama3", oaiOK, nil, r)
	if got["temperature"] != 0.2 || got["max_tokens"] != float64(77) || got["max_completion_tokens"] != nil {
		t.Errorf("an OpenAI-compatible server takes max_tokens: %v", got)
	}
	got = send(t, "openai", "gpt-3.5-turbo", oaiOK, fakeCatalog{"gpt-3.5-turbo": {TokenParamName: "max_tokens"}}, r)
	if got["max_tokens"] != float64(77) {
		t.Errorf("the catalog names the model's own limit field: %v", got)
	}
	// Nothing set, nothing sent: a request that names no parameter is the request it always was.
	got = send(t, "openai", "gpt-4o", oaiOK, nil, models.RouteRequest{})
	for _, k := range []string{"temperature", "top_p", "stop", "max_tokens", "max_completion_tokens"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s was sent although the caller set none", k)
		}
	}
}

func TestAReasoningModelIsNotSentATemperatureItWouldRefuse(t *testing.T) {
	temp, topP, max := params()
	got := send(t, "openai", "o3-mini", oaiOK, fakeCatalog{"o3-mini": {SupportsThinking: true}}, models.RouteRequest{Temperature: temp, TopP: topP, MaxTokens: max, StopSequences: []string{"END"}})
	if got["temperature"] != nil || got["top_p"] != nil || got["stop"] != nil {
		t.Errorf("a reasoning model refuses sampling parameters: %v", got)
	}
	if got["max_completion_tokens"] != float64(77) {
		t.Errorf("the output limit is always honoured: %v", got)
	}
}

func TestAnthropicGetsSamplingExceptWithExtendedThinkingAndTheCallersLimit(t *testing.T) {
	temp, topP, max := params()
	body := `{"id":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	got := send(t, "anthropic", "claude-sonnet-4-5", body, nil, models.RouteRequest{Temperature: temp, TopP: topP, MaxTokens: max, StopSequences: []string{"END"}})
	if got["temperature"] != 0.2 || got["top_p"] != 0.9 || got["max_tokens"] != float64(77) || got["stop_sequences"].([]interface{})[0] != "END" {
		t.Errorf("anthropic: %v", got)
	}
	got = send(t, "anthropic", "claude-sonnet-4-5", body, fakeCatalog{"claude-sonnet-4-5": {SupportsThinking: true}}, models.RouteRequest{Temperature: temp, ThinkingEnabled: true, ThinkingBudget: 2000})
	if got["temperature"] != nil || got["thinking"] == nil {
		t.Errorf("extended thinking does not allow a temperature: %v", got)
	}
}

func TestGeminiGetsGenerationSettings(t *testing.T) {
	temp, topP, max := params()
	body := `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`
	got := send(t, "gemini", "gemini-2.5-flash", body, nil, models.RouteRequest{Temperature: temp, TopP: topP, MaxTokens: max, StopSequences: []string{"END"}})
	gc, _ := got["generationConfig"].(map[string]interface{})
	if gc["temperature"] != 0.2 || gc["topP"] != 0.9 || gc["maxOutputTokens"] != float64(77) || gc["stopSequences"].([]interface{})[0] != "END" {
		t.Errorf("gemini: %v", got)
	}
}
