package handlers

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/audio"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func TestTranscribeAudioPartsKeepsOtherPartsAndReturnsTranscript(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"text":"book a table"}`))
	}))
	defer srv.Close()

	os.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	defer s.Close()
	ctx := context.Background()
	if err := s.CreateProvider(ctx, &models.ModelProvider{Name: "oai", Kitchen: "default", Kind: "openai",
		Endpoint: srv.URL, Models: []string{"gpt-4o"}, IsDefault: true, Config: map[string]interface{}{"api_key": "sk"}}); err != nil {
		t.Fatal(err)
	}
	h := &Handlers{Store: s, Router: router.NewModelRouter(s)}
	agent := &models.Agent{Name: "a", Kitchen: "default", ModelProvider: "oai", ModelName: "gpt-4o"}

	parts := []models.ContentPart{
		{Type: "audio", Media: &models.MediaRef{MimeType: "audio/wav", Data: base64.StdEncoding.EncodeToString([]byte("RIFF"))}},
		{Type: "image", Media: &models.MediaRef{MimeType: "image/png", Data: "aGk="}},
	}
	transcript, rest, err := h.transcribeAudioParts(ctx, agent, parts)
	if err != nil {
		t.Fatal(err)
	}
	if transcript != "book a table" {
		t.Fatalf("unexpected transcript %q", transcript)
	}
	if len(rest) != 1 || rest[0].Type != "image" {
		t.Fatalf("audio must be consumed and other parts kept, got %+v", rest)
	}
}

func TestTranscribeAudioPartsRequiresInlineData(t *testing.T) {
	os.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	defer s.Close()
	h := &Handlers{Store: s, Router: router.NewModelRouter(s)}
	_, _, err := h.transcribeAudioParts(context.Background(), &models.Agent{Name: "a", Kitchen: "default"}, []models.ContentPart{
		{Type: "audio", Media: &models.MediaRef{MimeType: "audio/wav", URL: "https://example.com/a.wav"}},
	})
	if err == nil || !strings.Contains(err.Error(), "inline data") {
		t.Fatalf("expected a clear error for URL-only audio, got %v", err)
	}
}

// speechEngine builds the engine voice would use for an agent whose provider
// is agentProvider, in a kitchen holding the given providers.
func speechEngine(t *testing.T, agentProvider string, providers ...models.ModelProvider) (*audio.OpenAIEngine, error) {
	t.Helper()
	os.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	t.Cleanup(func() { s.Close() })
	for i := range providers {
		providers[i].Kitchen = "default"
		if err := s.CreateProvider(context.Background(), &providers[i]); err != nil {
			t.Fatal(err)
		}
	}
	h := &Handlers{Store: s, Router: router.NewModelRouter(s)}
	agent := &models.Agent{Name: "a", Kitchen: "default", ModelProvider: agentProvider, ModelName: "gpt-4o"}
	eng, err := h.speechEngineFor(context.Background(), agent)
	if err != nil {
		return nil, err
	}
	return eng.(*audio.OpenAIEngine), nil
}

func withKey(p models.ModelProvider) models.ModelProvider {
	p.Config = map[string]interface{}{"api_key": "k"}
	return p
}

func TestOpenRouterVoiceDefaultsItsEndpointAndUsesItsModelSlugs(t *testing.T) {
	eng, err := speechEngine(t, "or", withKey(models.ModelProvider{Name: "or", Kind: "openrouter"}))
	if err != nil {
		t.Fatal(err)
	}
	if eng.Endpoint != "https://openrouter.ai/api/v1" {
		t.Fatalf("OpenRouter must default to its own endpoint, got %q", eng.Endpoint)
	}
	if eng.STTModel != "openai/whisper-1" || !strings.HasPrefix(eng.TTSModel, "openai/gpt-4o-mini-tts") {
		t.Fatalf("OpenRouter needs provider-prefixed model slugs, got stt=%q tts=%q", eng.STTModel, eng.TTSModel)
	}
}

func TestLiteLLMVoiceUsesTheProxyAndHonoursConfiguredAliases(t *testing.T) {
	eng, err := speechEngine(t, "gw", withKey(models.ModelProvider{Name: "gw", Kind: "litellm", Endpoint: "http://litellm:4000/v1"}))
	if err != nil {
		t.Fatal(err)
	}
	if eng.Endpoint != "http://litellm:4000/v1" || eng.STTModel != "whisper-1" || eng.TTSModel != "tts-1" {
		t.Fatalf("expected the proxy endpoint with default aliases, got %+v", eng)
	}

	eng, err = speechEngine(t, "gw", models.ModelProvider{Name: "gw", Kind: "litellm", Endpoint: "http://litellm:4000/v1",
		Config: map[string]interface{}{"api_key": "k", "modalities": map[string]interface{}{
			"audio": map[string]interface{}{"stt_model": "groq-whisper", "tts_model": "eleven-tts"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if eng.STTModel != "groq-whisper" || eng.TTSModel != "eleven-tts" {
		t.Fatalf("configured model aliases must win, got stt=%q tts=%q", eng.STTModel, eng.TTSModel)
	}
}

func TestVoiceFailsForALiteLLMProviderWithNoEndpoint(t *testing.T) {
	if _, err := speechEngine(t, "gw", withKey(models.ModelProvider{Name: "gw", Kind: "litellm"})); err == nil {
		t.Fatal("a LiteLLM provider with no endpoint cannot serve voice")
	}
}

// The agent is tied to its provider: other providers in the kitchen, even the
// default one, are never borrowed for its voice.
func TestVoiceUsesTheAgentsOwnProviderNeverAnotherInTheKitchen(t *testing.T) {
	gw := withKey(models.ModelProvider{Name: "gw", Kind: "litellm", Endpoint: "http://litellm:4000/v1"})
	oai := withKey(models.ModelProvider{Name: "oai", Kind: "openai", Endpoint: "http://openai.test/v1", IsDefault: true})

	eng, err := speechEngine(t, "gw", gw, oai)
	if err != nil {
		t.Fatal(err)
	}
	if eng.Endpoint != "http://litellm:4000/v1" {
		t.Fatalf("the agent's own provider must serve its voice, got %q", eng.Endpoint)
	}
}

func TestVoiceRefusesAnAgentWhoseProviderHasNoAudioEvenIfTheKitchenDoes(t *testing.T) {
	claude := withKey(models.ModelProvider{Name: "claude", Kind: "anthropic", Models: []string{"claude-sonnet-4"}})
	oai := withKey(models.ModelProvider{Name: "oai", Kind: "openai", Endpoint: "http://openai.test/v1", IsDefault: true})
	_, err := speechEngine(t, "claude", claude, oai)
	if err == nil || !strings.Contains(err.Error(), `provider "claude" (anthropic) does not offer the audio modality`) {
		t.Fatalf("an Anthropic agent must be refused voice with a clear reason, got %v", err)
	}
}

func TestVoiceRefusesWhenTheProviderSwitchedAudioOff(t *testing.T) {
	oai := models.ModelProvider{Name: "oai", Kind: "openai", Config: map[string]interface{}{"api_key": "k",
		"modalities": map[string]interface{}{"audio": map[string]interface{}{"enabled": false}}}}
	_, err := speechEngine(t, "oai", oai)
	if err == nil || !strings.Contains(err.Error(), "does not offer the audio modality") {
		t.Fatalf("audio switched off on the provider must refuse voice, got %v", err)
	}
	if strings.Contains(err.Error(), "audio,") || strings.HasSuffix(err.Error(), "audio)") {
		t.Fatalf("the error must not list audio among what the provider offers: %v", err)
	}
}

func TestAnAgentWithNoProviderCannotDoVoice(t *testing.T) {
	if _, err := speechEngine(t, ""); err == nil || !strings.Contains(err.Error(), "no model provider") {
		t.Fatalf("expected a clear error, got %v", err)
	}
}
