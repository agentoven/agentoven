package handlers

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"

	"github.com/agentoven/agentoven/control-plane/pkg/audio"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// audioInput is spoken input for a turn: base64 audio and its MIME type.
type audioInput struct {
	Data     string `json:"data"`
	MimeType string `json:"mime_type"`
}

// agentProvider returns the provider and model an agent runs on. The agent's
// modalities are the modalities of this provider and no other.
func (h *Handlers) agentProvider(ctx context.Context, agent *models.Agent) (*models.ModelProvider, string, error) {
	name, model := agent.ModelProvider, agent.ModelName
	if rc := agent.ResolvedConfig; rc != nil && rc.Model != nil {
		name, model = rc.Model.Provider, rc.Model.Model
	}
	if name == "" {
		return nil, "", fmt.Errorf("agent %q has no model provider", agent.Name)
	}
	prov, err := h.Store.GetProvider(ctx, agent.Kitchen, name)
	if err != nil {
		return nil, "", fmt.Errorf("agent %q: model provider %q: %w", agent.Name, name, err)
	}
	return prov, model, nil
}

// speechEngineFor returns the speech engine of the agent's own provider. The
// driver owns the endpoint, key, TLS and model defaults; the provider's audio
// modality says whether it is on.
func (h *Handlers) speechEngineFor(ctx context.Context, agent *models.Agent) (audio.Engine, error) {
	prov, model, err := h.agentProvider(ctx, agent)
	if err != nil {
		return nil, err
	}
	d := h.Router.SpeechFor(prov)
	if d == nil {
		return nil, fmt.Errorf("agent %q cannot take or give speech: its provider %q (%s) does not offer the audio modality (it offers: %s)",
			agent.Name, prov.Name, prov.Kind, strings.Join(h.Router.Modalities(prov, model), ", "))
	}
	return d.SpeechEngine(prov)
}

func transcribeAudioInput(ctx context.Context, engine audio.Engine, in *audioInput) (string, error) {
	if in.Data == "" {
		return "", fmt.Errorf("audio.data is required")
	}
	if in.MimeType == "" {
		return "", fmt.Errorf("audio.mime_type is required")
	}
	raw, err := base64.StdEncoding.DecodeString(in.Data)
	if err != nil {
		return "", fmt.Errorf("audio.data is not valid base64: %w", err)
	}
	return engine.Transcribe(ctx, raw, in.MimeType)
}

// transcribeAudioParts turns inline audio parts into text and returns the
// remaining parts. Audio is transcribed here, not sent to the model, so every
// agent whose provider offers speech can take a voice turn, even when its model
// cannot take audio itself.
func (h *Handlers) transcribeAudioParts(ctx context.Context, agent *models.Agent, parts []models.ContentPart) (string, []models.ContentPart, error) {
	var rest []models.ContentPart
	var texts []string
	var engine audio.Engine
	for _, p := range parts {
		if p.Type != "audio" || p.Media == nil {
			rest = append(rest, p)
			continue
		}
		if p.Media.Data == "" {
			return "", nil, fmt.Errorf("audio parts must carry inline data (base64) for transcription")
		}
		if engine == nil {
			var err error
			engine, err = h.speechEngineFor(ctx, agent)
			if err != nil {
				return "", nil, err
			}
		}
		text, err := transcribeAudioInput(ctx, engine, &audioInput{Data: p.Media.Data, MimeType: p.Media.MimeType})
		if err != nil {
			return "", nil, fmt.Errorf("could not transcribe audio: %w", err)
		}
		texts = append(texts, text)
	}
	return strings.Join(texts, " "), rest, nil
}

// addSpeech finishes a voice turn's response: the transcript of what the caller
// said, and, when asked, the reply spoken by the agent's own provider. It is
// shared by every way an invoke can be answered (in-process executor, running
// pod) so voice behaves the same on both. It writes the error response and
// returns false when the reply cannot be spoken.
func (h *Handlers) addSpeech(w http.ResponseWriter, ctx context.Context, agent *models.Agent, out map[string]interface{}, response, transcript string, voiceOutput bool, voice string) bool {
	if transcript != "" {
		out["transcript"] = transcript
	}
	if !voiceOutput {
		return true
	}
	engine, err := h.speechEngineFor(ctx, agent)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return false
	}
	speech, mime, err := engine.Speak(ctx, response, voice)
	if err != nil {
		respondError(w, http.StatusBadGateway, "could not synthesize speech: "+err.Error())
		return false
	}
	out["audio"] = map[string]interface{}{
		"data":      base64.StdEncoding.EncodeToString(speech),
		"mime_type": mime,
	}
	return true
}
