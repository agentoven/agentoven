package handlers_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/sessions"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// fakeOpenAI stands in for one OpenAI-compatible provider: chat, Whisper and
// TTS. It records the user text the chat endpoint was asked to answer, so a
// test can prove the transcript — not the raw audio — is what the agent got.
type fakeOpenAI struct {
	*httptest.Server
	mu         sync.Mutex
	lastUser   string
	transcript string // what Whisper "heard"; default "what is the weather"
}

func newFakeOpenAI(t *testing.T) *fakeOpenAI {
	t.Helper()
	f := &fakeOpenAI{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			var req struct {
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
			}
			_ = json.Unmarshal(body, &req)
			f.mu.Lock()
			for _, m := range req.Messages {
				if m.Role == "user" {
					f.lastUser = m.Content
				}
			}
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"it is sunny"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":3,"total_tokens":6}}`))
		case strings.HasSuffix(r.URL.Path, "/audio/transcriptions"):
			said := f.transcript
			if said == "" {
				said = "what is the weather"
			}
			_, _ = w.Write([]byte(`{"text":` + strconv.Quote(said) + `}`))
		case strings.HasSuffix(r.URL.Path, "/audio/speech"):
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write([]byte("MP3BYTES"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func TestInvokeAgentVoiceInAndVoiceOut(t *testing.T) {
	t.Setenv("AGENTOVEN_JOURNAL_DISABLE", "true")
	oai := newFakeOpenAI(t)

	os.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	t.Cleanup(func() { s.Close() })

	mr := router.NewModelRouter(s)

	ctx := context.Background()
	// One provider serves the agent's chat and its speech: the agent's voice
	// comes from its own provider.
	if err := s.CreateProvider(ctx, &models.ModelProvider{
		Name: "oai", Kitchen: "default", Kind: "openai", Endpoint: oai.URL,
		Models: []string{"gpt-4o"}, IsDefault: true, Config: map[string]interface{}{"api_key": "sk-test"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAgent(ctx, &models.Agent{
		Name: "voice-agent", Kitchen: "default", Mode: models.AgentModeManaged,
		Status: models.AgentStatusReady, MaxTurns: 3,
		ResolvedConfig: &models.ResolvedIngredients{Model: &models.ResolvedModel{Provider: "oai", Kind: "openai", Model: "gpt-4o", Endpoint: oai.URL, APIKey: "sk-test"}},
	}); err != nil {
		t.Fatal(err)
	}

	h := handlers.New(s, mr, nil, nil, nil, sessions.NewMemorySessionStore(), nil)

	audioIn := base64.StdEncoding.EncodeToString([]byte("RIFF....WAVE"))
	body, _ := json.Marshal(map[string]interface{}{
		"audio":        map[string]string{"data": audioIn, "mime_type": "audio/wav"},
		"voice_output": true,
		"voice":        "nova",
	})
	req := httptest.NewRequest(http.MethodPost, "/invoke", strings.NewReader(string(body)))
	req = withKitchen(req, "default")
	req = withChiParam(req, "agentName", "voice-agent")
	rec := httptest.NewRecorder()
	h.InvokeAgent(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}

	if out["transcript"] != "what is the weather" {
		t.Fatalf("expected the transcript in the response, got %v", out["transcript"])
	}
	if out["response"] != "it is sunny" {
		t.Fatalf("expected the agent's text reply, got %v", out["response"])
	}
	if oai.lastUser != "what is the weather" {
		t.Fatalf("the agent must answer the transcript, got user text %q", oai.lastUser)
	}
	audioOut, ok := out["audio"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected an audio output object, got %v", out["audio"])
	}
	speech, _ := base64.StdEncoding.DecodeString(audioOut["data"].(string))
	if string(speech) != "MP3BYTES" || audioOut["mime_type"] != "audio/mpeg" {
		t.Fatalf("unexpected speech output: %q (%v)", speech, audioOut["mime_type"])
	}
}

func TestInvokeAgentVoiceNeedsASpeechCapableProvider(t *testing.T) {
	t.Setenv("AGENTOVEN_JOURNAL_DISABLE", "true")
	os.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	t.Cleanup(func() { s.Close() })

	ctx := context.Background()
	_ = s.CreateProvider(ctx, &models.ModelProvider{Name: "claude", Kitchen: "default", Kind: "anthropic", Models: []string{"claude-sonnet-4"}, IsDefault: true})
	_ = s.CreateAgent(ctx, &models.Agent{Name: "a", Kitchen: "default", Mode: models.AgentModeManaged, Status: models.AgentStatusReady,
		ResolvedConfig: &models.ResolvedIngredients{Model: &models.ResolvedModel{Provider: "claude", Model: "claude-sonnet-4"}}})

	h := handlers.New(s, router.NewModelRouter(s), nil, nil, nil, sessions.NewMemorySessionStore(), nil)
	body := `{"audio":{"data":"UklGRg==","mime_type":"audio/wav"}}`
	req := httptest.NewRequest(http.MethodPost, "/invoke", strings.NewReader(body))
	req = withKitchen(req, "default")
	req = withChiParam(req, "agentName", "a")
	rec := httptest.NewRecorder()
	h.InvokeAgent(rec, req)

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "(anthropic) does not offer the audio modality") {
		t.Fatalf("voice on an Anthropic agent should fail with a clear 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// An Anthropic agent stays without voice even when the kitchen has an OpenAI
// provider: the agent has what its own provider offers, nothing borrowed.
func TestInvokeAgentVoiceDoesNotBorrowAnotherProvidersSpeech(t *testing.T) {
	t.Setenv("AGENTOVEN_JOURNAL_DISABLE", "true")
	oai := newFakeOpenAI(t)
	os.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	t.Cleanup(func() { s.Close() })

	ctx := context.Background()
	_ = s.CreateProvider(ctx, &models.ModelProvider{Name: "claude", Kitchen: "default", Kind: "anthropic", Models: []string{"claude-sonnet-4"}, IsDefault: true})
	_ = s.CreateProvider(ctx, &models.ModelProvider{Name: "oai", Kitchen: "default", Kind: "openai", Endpoint: oai.URL, Config: map[string]interface{}{"api_key": "k"}})
	_ = s.CreateAgent(ctx, &models.Agent{Name: "a", Kitchen: "default", Mode: models.AgentModeManaged, Status: models.AgentStatusReady,
		ResolvedConfig: &models.ResolvedIngredients{Model: &models.ResolvedModel{Provider: "claude", Model: "claude-sonnet-4"}}})

	h := handlers.New(s, router.NewModelRouter(s), nil, nil, nil, sessions.NewMemorySessionStore(), nil)
	req := httptest.NewRequest(http.MethodPost, "/invoke", strings.NewReader(`{"audio":{"data":"UklGRg==","mime_type":"audio/wav"}}`))
	req = withKitchen(req, "default")
	req = withChiParam(req, "agentName", "a")
	rec := httptest.NewRecorder()
	h.InvokeAgent(rec, req)

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "does not offer the audio modality") {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// A voice turn answered by a running pod must carry the transcript and the
// spoken reply, exactly as one answered by the in-process executor does.
func TestInvokeAgentVoiceThroughARunningPod(t *testing.T) {
	t.Setenv("AGENTOVEN_JOURNAL_DISABLE", "true")
	oai := newFakeOpenAI(t) // serves this agent's provider speech
	pod := newFakeProcess(t, "it is sunny")

	os.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	_ = s.CreateProvider(ctx, &models.ModelProvider{Name: "oai", Kitchen: "default", Kind: "openai", Endpoint: oai.URL,
		Models: []string{"gpt-4o"}, IsDefault: true, Config: map[string]interface{}{"api_key": "k"}})
	_ = s.CreateAgent(ctx, &models.Agent{Name: "podded", Kitchen: "default", Mode: models.AgentModeManaged, Status: models.AgentStatusReady,
		ModelProvider: "oai", ModelName: "gpt-4o",
		Process:        &models.ProcessInfo{Status: models.ProcessRunning, Endpoint: pod.srv.URL},
		ResolvedConfig: &models.ResolvedIngredients{Model: &models.ResolvedModel{Provider: "oai", Kind: "openai", Model: "gpt-4o"}}})

	h := handlers.New(s, router.NewModelRouter(s), nil, nil, nil, sessions.NewMemorySessionStore(), nil)
	body, _ := json.Marshal(map[string]interface{}{
		"audio":        map[string]string{"data": base64.StdEncoding.EncodeToString([]byte("RIFF....WAVE")), "mime_type": "audio/wav"},
		"voice_output": true,
	})
	req := withChiParam(withKitchen(httptest.NewRequest(http.MethodPost, "/invoke", strings.NewReader(string(body))), "default"), "agentName", "podded")
	rec := httptest.NewRecorder()
	h.InvokeAgent(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)

	if pod.calls() != 1 || pod.seen[0] != "what is the weather" {
		t.Fatalf("the pod must be sent the transcript, saw %v", pod.seen)
	}
	if out["transcript"] != "what is the weather" || out["response"] != "it is sunny" {
		t.Fatalf("transcript and reply expected, got %v", out)
	}
	audioOut, ok := out["audio"].(map[string]interface{})
	if !ok {
		t.Fatalf("a pod-answered voice turn must still return spoken audio: %v", out)
	}
	if speech, _ := base64.StdEncoding.DecodeString(audioOut["data"].(string)); string(speech) != "MP3BYTES" {
		t.Fatalf("unexpected speech %q", speech)
	}
}
