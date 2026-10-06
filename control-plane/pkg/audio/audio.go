// Package audio is the speech layer of the cascaded voice pipeline:
// speech-to-text in front of an agent turn, text-to-speech behind it.
//
// Both halves are plain HTTP calls to a provider, so they work on every
// deployment target (Kubernetes, Lambda, Functions, Cloud Run) — there is
// no sidecar and no local media tooling involved.
package audio

import (
	"context"
	"errors"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// ErrUnsupported is returned when a provider has no speech API.
var ErrUnsupported = errors.New("audio: provider does not offer this speech capability")

// Transcriber turns spoken audio into text.
type Transcriber interface {
	Transcribe(ctx context.Context, data []byte, mimeType string) (text string, err error)
}

// Speaker turns text into spoken audio.
type Speaker interface {
	Speak(ctx context.Context, text, voice string) (data []byte, mimeType string, err error)
}

// Engine is both halves for one provider.
type Engine interface {
	Transcriber
	Speaker
}

// fileExtension maps an audio MIME type to the filename extension providers
// use to detect the container format.
func fileExtension(mimeType string) (string, error) {
	switch mimeType {
	case "audio/wav", "audio/x-wav", "audio/wave":
		return "wav", nil
	case "audio/mpeg", "audio/mp3":
		return "mp3", nil
	case "audio/mp4", "audio/m4a", "audio/x-m4a":
		return "m4a", nil
	case "audio/webm":
		return "webm", nil
	case "audio/ogg":
		return "ogg", nil
	case "audio/flac":
		return "flac", nil
	}
	return "", errors.New("audio: unsupported audio format " + mimeType)
}

// EngineProvider is implemented, optionally, by a provider driver whose API
// has speech endpoints. The driver builds an Engine from the provider's own
// settings (endpoint, rotated key, TLS, modalities.audio.stt_model/tts_model), so callers
// never need to know which vendor they are speaking to.
type EngineProvider interface {
	SpeechEngine(p *models.ModelProvider) (Engine, error)
}
