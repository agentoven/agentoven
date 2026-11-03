package router

import (
	"context"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/audio"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/agentoven/agentoven/control-plane/pkg/realtime"
)

// Admission is a model call that is not a chat completion (speech to text, text to speech, a live voice
// session, a provider test). It goes through the same spend gate as a completion, which also holds the
// rate limits, so a kitchen's budget and its requests- and tokens-per-minute cover the whole of what it
// asks of its providers and a new kind of call cannot be metered by forgetting to.
type Admission struct {
	Provider *models.ModelProvider
	// Model is the model the call uses, when it names one (it prices the call).
	Model string
	// Agent is the agent the call is made for, when there is one.
	Agent string
	// InputTokens and OutputTokens size the call. They are estimates (speech is not billed in tokens);
	// the gate holds them while the call runs and settles with what the caller reports.
	InputTokens, OutputTokens int
}

// Admit asks the spend gate for leave to make the call and returns the function that ends the gate's part
// in it: pass the usage of a call that completed, nil for one that failed (nothing is charged). A refusal is
// a *models.SpendRefusedError or *models.RateLimitedError, as for a completion.
func (mr *ModelRouter) Admit(ctx context.Context, a Admission) (func(*models.TokenUsage), error) {
	max := a.OutputTokens
	req := &models.RouteRequest{Kitchen: a.Provider.Kitchen, AgentRef: a.Agent, Model: a.Model, MaxTokens: &max}
	q := spendQueryOf(a.Provider, req)
	q.EstimatedInputTokens = a.InputTokens
	end, err := mr.spendBeginQuery(ctx, q, a.Provider, req)
	if err != nil {
		return nil, err
	}
	return func(u *models.TokenUsage) {
		if u == nil {
			end(nil)
			return
		}
		usage := *u
		if usage.TotalTokens == 0 {
			usage.TotalTokens = usage.InputTokens + usage.OutputTokens
		}
		if usage.PriceSource == "" && usage.EstimatedCost == 0 && a.Model != "" {
			mr.settle(a.Provider, a.Model, &usage)
		}
		end(&models.RouteResponse{Usage: usage})
	}, nil
}

// meteredSpeech is what SpeechFor hands out: the provider's speech driver, with every engine it builds
// admitted through the spend gate. Speech is sized from the bytes and characters it moves (about four
// bytes of audio or text to a token), because providers bill it in minutes and characters, not tokens.
type meteredSpeech struct {
	mr    *ModelRouter
	inner audio.EngineProvider
}

func (m meteredSpeech) SpeechEngine(p *models.ModelProvider) (audio.Engine, error) {
	e, err := m.inner.SpeechEngine(p)
	if err != nil {
		return nil, err
	}
	return meteredEngine{mr: m.mr, p: p, inner: e}, nil
}

type meteredEngine struct {
	mr    *ModelRouter
	p     *models.ModelProvider
	inner audio.Engine
}

func (e meteredEngine) Transcribe(ctx context.Context, data []byte, mimeType string) (string, error) {
	n := len(data)/4 + 1
	end, err := e.mr.Admit(ctx, Admission{Provider: e.p, InputTokens: n, OutputTokens: 256})
	if err != nil {
		return "", err
	}
	text, err := e.inner.Transcribe(ctx, data, mimeType)
	if err != nil {
		end(nil)
		return "", err
	}
	end(&models.TokenUsage{InputTokens: int64(n), OutputTokens: int64(len(text)/4 + 1)})
	return text, nil
}

func (e meteredEngine) Speak(ctx context.Context, text, voice string) ([]byte, string, error) {
	n := len(text)/4 + 1
	end, err := e.mr.Admit(ctx, Admission{Provider: e.p, InputTokens: n, OutputTokens: n})
	if err != nil {
		return nil, "", err
	}
	data, mime, err := e.inner.Speak(ctx, text, voice)
	if err != nil {
		end(nil)
		return nil, "", err
	}
	end(&models.TokenUsage{InputTokens: int64(n), OutputTokens: int64(len(data)/4 + 1)})
	return data, mime, nil
}

// realtimeTokensPerSecond sizes a live session by how long it lasts: speech in both directions is a few
// tens of tokens a second on the providers that report it.
const (
	realtimeTokensPerSecond = 25
	realtimeHoldSeconds     = 120
)

// meteredRealtime is what RealtimeFor hands out: opening a session is admitted, and the session is settled
// when it ends with an estimate from how long it ran.
type meteredRealtime struct {
	mr    *ModelRouter
	inner realtime.Driver
}

func (m meteredRealtime) OpenRealtime(ctx context.Context, p *models.ModelProvider, cfg realtime.SessionConfig) (realtime.Upstream, error) {
	hold := realtimeHoldSeconds * realtimeTokensPerSecond
	end, err := m.mr.Admit(ctx, Admission{Provider: p, Model: cfg.Model, InputTokens: hold, OutputTokens: hold})
	if err != nil {
		return nil, err
	}
	up, err := m.inner.OpenRealtime(ctx, p, cfg)
	if err != nil {
		end(nil)
		return nil, err
	}
	return &meteredUpstream{Upstream: up, end: end, started: time.Now()}, nil
}

type meteredUpstream struct {
	realtime.Upstream
	end     func(*models.TokenUsage)
	started time.Time
}

func (u *meteredUpstream) Close() error {
	err := u.Upstream.Close()
	n := int64(time.Since(u.started).Seconds()+1) * realtimeTokensPerSecond
	u.end(&models.TokenUsage{InputTokens: n, OutputTokens: n})
	return err
}

// meteredEmbedder is what the router hands out for a driver that embeds: the driver, with every Embed
// admitted through the spend gate (the input is sized at about four characters a token, the usual way
// providers bill an embedding, and an embedding has no output).
type meteredEmbedder struct {
	EmbeddingCapableDriver
	mr *ModelRouter
}

func (e meteredEmbedder) Embed(ctx context.Context, provider *models.ModelProvider, model string, texts []string) ([][]float64, error) {
	n := 1
	for _, t := range texts {
		n += len(t)/4 + 1
	}
	end, err := e.mr.Admit(ctx, Admission{Provider: provider, Model: model, InputTokens: n})
	if err != nil {
		return nil, err
	}
	vectors, err := e.EmbeddingCapableDriver.Embed(ctx, provider, model, texts)
	if err != nil {
		end(nil)
		return nil, err
	}
	end(&models.TokenUsage{InputTokens: int64(n)})
	return vectors, nil
}

// UnderlyingEngine is the driver's own engine under the wrapper SpeechFor puts around it, for a test that
// looks at the engine's settings (endpoint, models). Production code has no use for it: the wrapper is what
// meters the calls.
func UnderlyingEngine(e audio.Engine) audio.Engine {
	if m, ok := e.(meteredEngine); ok {
		return m.inner
	}
	return e
}
