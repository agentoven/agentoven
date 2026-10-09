package router

import (
	"context"
	"fmt"
	"strings"

	"github.com/agentoven/agentoven/control-plane/pkg/audio"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/agentoven/agentoven/control-plane/pkg/realtime"
)

// Voice is optional driver behaviour, discovered by type assertion like the
// router's streaming and embedding capabilities: a driver that implements
// realtime.Driver can run live speech-to-speech sessions, and one that
// implements audio.EngineProvider can do speech-to-text and text-to-speech.
// RealtimeFor and SpeechFor (modalities.go) look them up per provider, so
// there is no list of kinds to keep in sync.

// pick returns the first non-empty value: the explicit one, then the one the
// provider's modalities config sets, then the driver default.
func pick(explicit, configured, fallback string) string {
	if explicit != "" {
		return explicit
	}
	if configured != "" {
		return configured
	}
	return fallback
}

// wsURL turns a chat base URL (https://host/v1) into a WebSocket URL with the
// given path suffix (wss://host/v1/realtime).
func wsURL(base, suffix string) string {
	b := strings.TrimRight(base, "/")
	switch {
	case strings.HasPrefix(b, "https://"):
		b = "wss://" + strings.TrimPrefix(b, "https://")
	case strings.HasPrefix(b, "http://"):
		b = "ws://" + strings.TrimPrefix(b, "http://")
	}
	return b + suffix
}

// ── OpenAI and LiteLLM: the OpenAI realtime protocol ─────────

// openAIRealtime opens a session over the OpenAI realtime protocol. endpoint is
// the ws URL, or "" for OpenAI's own.
func (mr *ModelRouter) openAIRealtime(ctx context.Context, p *models.ModelProvider, cfg realtime.SessionConfig, endpoint, defaultModel string) (realtime.Upstream, error) {
	cfg.Model = pick(cfg.Model, p.ModalitySetting(models.ModalityRealtime, "model"), defaultModel)
	if cfg.Model == "" {
		return nil, fmt.Errorf("provider %q needs a realtime model: pass ?model= or set config.modalities.realtime.model on the provider", p.Name)
	}
	return realtime.DialOpenAI(ctx, realtime.DialOptions{
		Endpoint: endpoint, APIKey: mr.SelectAPIKey(p), HTTPClient: mr.clientFor(p),
	}, cfg)
}

func (d *OpenAIDriver) OpenRealtime(ctx context.Context, p *models.ModelProvider, cfg realtime.SessionConfig) (realtime.Upstream, error) {
	endpoint := ""
	if p.Endpoint != "" {
		endpoint = wsURL(p.Endpoint, "/realtime")
	}
	return d.router.openAIRealtime(ctx, p, cfg, endpoint, "gpt-realtime")
}

// OpenRealtime for LiteLLM goes through the proxy's OpenAI-compatible
// /v1/realtime, which translates to whichever backend the model alias names.
// There is no default endpoint (it is the operator's proxy) and no default
// model (aliases are the operator's too).
func (d *LiteLLMDriver) OpenRealtime(ctx context.Context, p *models.ModelProvider, cfg realtime.SessionConfig) (realtime.Upstream, error) {
	if p.Endpoint == "" {
		return nil, fmt.Errorf("litellm provider %q has no endpoint", p.Name)
	}
	return d.router.openAIRealtime(ctx, p, cfg, wsURL(p.Endpoint, "/realtime"), "")
}

// ── Gemini Live ──────────────────────────────────────────────

const geminiLivePath = "/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"

func (d *GeminiDriver) OpenRealtime(ctx context.Context, p *models.ModelProvider, cfg realtime.SessionConfig) (realtime.Upstream, error) {
	cfg.Model = pick(cfg.Model, p.ModalitySetting(models.ModalityRealtime, "model"), "gemini-3.8-live")
	cfg.WebSearch = d.router.WebSearchOn(p)
	return realtime.DialGemini(ctx, realtime.DialOptions{
		Endpoint: geminiLiveEndpoint(p.Endpoint), APIKey: d.router.SelectAPIKey(p), HTTPClient: d.router.clientFor(p),
	}, cfg)
}

// geminiLiveEndpoint maps a custom Gemini base URL (a proxy) to its Live
// WebSocket service. Google's own endpoint, or none, stays "" so the driver
// uses its default.
func geminiLiveEndpoint(chatEndpoint string) string {
	if chatEndpoint == "" || strings.Contains(chatEndpoint, "generativelanguage.googleapis.com") {
		return ""
	}
	base := wsURL(chatEndpoint, "")
	if i := strings.Index(base, "/v1"); i != -1 {
		base = base[:i]
	}
	return base + geminiLivePath
}

// ── Speech (cascaded voice) ──────────────────────────────────
//
// OpenAI, OpenRouter, and LiteLLM all expose the OpenAI audio API shape
// (multipart transcription, JSON speech), so one engine serves all three; only
// the endpoint default and the model names differ. A provider's config can
// override the models with modalities.audio.stt_model / tts_model — LiteLLM names models by
// whatever alias its operator configured, so there is no universal default.

func (mr *ModelRouter) speechEngine(p *models.ModelProvider, endpoint, stt, tts string) (audio.Engine, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("provider %q has no endpoint", p.Name)
	}
	e := audio.NewOpenAIEngine(endpoint, mr.SelectAPIKey(p))
	e.Client = mr.clientFor(p)
	e.STTModel = pick("", p.ModalitySetting(models.ModalityAudio, "stt_model"), stt)
	e.TTSModel = pick("", p.ModalitySetting(models.ModalityAudio, "tts_model"), tts)
	return e, nil
}

func (d *OpenAIDriver) SpeechEngine(p *models.ModelProvider) (audio.Engine, error) {
	endpoint := p.Endpoint
	if endpoint == "" {
		endpoint = "https://api.openai.com/v1"
	}
	return d.router.speechEngine(p, endpoint, "whisper-1", "tts-1")
}

func (d *OpenRouterDriver) SpeechEngine(p *models.ModelProvider) (audio.Engine, error) {
	endpoint := p.Endpoint
	if endpoint == "" {
		endpoint = "https://openrouter.ai/api/v1"
	}
	return d.router.speechEngine(p, endpoint, "openai/whisper-1", "openai/gpt-4o-mini-tts-2025-12-15")
}

func (d *LiteLLMDriver) SpeechEngine(p *models.ModelProvider) (audio.Engine, error) {
	return d.router.speechEngine(p, p.Endpoint, "whisper-1", "tts-1")
}
