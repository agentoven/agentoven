package audio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// OpenAIEngine implements Engine against the OpenAI audio API: Whisper for
// transcription and the TTS models for speech.
type OpenAIEngine struct {
	Endpoint string // base URL, e.g. https://api.openai.com/v1
	APIKey   string
	STTModel string // default whisper-1
	TTSModel string // default tts-1
	Client   *http.Client
}

// NewOpenAIEngine builds an engine with sensible defaults.
func NewOpenAIEngine(endpoint, apiKey string) *OpenAIEngine {
	if endpoint == "" {
		endpoint = "https://api.openai.com/v1"
	}
	return &OpenAIEngine{
		Endpoint: strings.TrimRight(endpoint, "/"),
		APIKey:   apiKey,
		STTModel: "whisper-1",
		TTSModel: "tts-1",
		Client:   &http.Client{Timeout: 2 * time.Minute},
	}
}

// Transcribe sends the audio to /audio/transcriptions and returns the text.
func (e *OpenAIEngine) Transcribe(ctx context.Context, data []byte, mimeType string) (string, error) {
	ext, err := fileExtension(mimeType)
	if err != nil {
		return "", err
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("model", e.STTModel); err != nil {
		return "", err
	}
	fw, err := mw.CreateFormFile("file", "audio."+ext)
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(data); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.Endpoint+"/audio/transcriptions", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+e.APIKey)

	resp, err := e.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("transcription request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("transcription failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("transcription response not understood: %w", err)
	}
	return out.Text, nil
}

// Speak sends text to /audio/speech and returns mp3 audio.
func (e *OpenAIEngine) Speak(ctx context.Context, text, voice string) ([]byte, string, error) {
	if strings.TrimSpace(text) == "" {
		return nil, "", fmt.Errorf("speech input must not be empty")
	}
	if voice == "" {
		voice = "alloy"
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"model":           e.TTSModel,
		"input":           text,
		"voice":           voice,
		"response_format": "mp3",
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.Endpoint+"/audio/speech", bytes.NewReader(payload))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.APIKey)

	resp, err := e.Client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("speech request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("speech failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return raw, "audio/mpeg", nil
}
