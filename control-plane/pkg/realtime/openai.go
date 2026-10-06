package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/coder/websocket"
)

const defaultOpenAIRealtimeEndpoint = "wss://api.openai.com/v1/realtime"

// DialOpenAI opens a session over the OpenAI Realtime protocol. OpenAI speaks
// it natively and LiteLLM's proxy serves it too, so both drivers use this.
func DialOpenAI(ctx context.Context, o DialOptions, cfg SessionConfig) (Upstream, error) {
	endpoint := o.Endpoint
	if endpoint == "" {
		endpoint = defaultOpenAIRealtimeEndpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("realtime: bad endpoint: %w", err)
	}
	if cfg.Model != "" {
		q := u.Query()
		q.Set("model", cfg.Model)
		u.RawQuery = q.Encode()
	}

	conn, _, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{
		HTTPClient: o.HTTPClient,
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + o.APIKey}},
	})
	if err != nil {
		return nil, fmt.Errorf("realtime: connecting to openai: %w", err)
	}
	conn.SetReadLimit(16 << 20) // audio deltas are large frames

	up := &openAIUpstream{conn: conn}
	if err := up.send(ctx, sessionUpdate(cfg)); err != nil {
		conn.Close(websocket.StatusInternalError, "session setup failed")
		return nil, fmt.Errorf("realtime: configuring session: %w", err)
	}
	return up, nil
}

// sessionUpdate builds the session.update that configures voice, instructions,
// audio format, and tools.
func sessionUpdate(cfg SessionConfig) map[string]interface{} {
	voice := cfg.Voice
	if voice == "" {
		voice = "alloy"
	}
	session := map[string]interface{}{
		"type":              "realtime",
		"output_modalities": []string{"audio"},
		"instructions":      cfg.Instructions,
		"audio": map[string]interface{}{
			"input": map[string]interface{}{
				"format":         map[string]interface{}{"type": "audio/pcm", "rate": 24000},
				"transcription":  map[string]interface{}{"model": "whisper-1"},
				"turn_detection": map[string]interface{}{"type": "server_vad"},
			},
			"output": map[string]interface{}{
				"format": map[string]interface{}{"type": "audio/pcm", "rate": 24000},
				"voice":  voice,
			},
		},
	}
	if len(cfg.Tools) > 0 {
		tools := make([]map[string]interface{}, 0, len(cfg.Tools))
		for _, t := range cfg.Tools {
			params := t.Parameters
			if params == nil {
				params = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
			}
			tools = append(tools, map[string]interface{}{
				"type": "function", "name": t.Name, "description": t.Description, "parameters": params,
			})
		}
		session["tools"] = tools
		session["tool_choice"] = "auto"
	}
	return map[string]interface{}{"type": "session.update", "session": session}
}

type openAIUpstream struct {
	conn *websocket.Conn
}

func (u *openAIUpstream) send(ctx context.Context, v interface{}) error {
	return u.conn.Write(ctx, websocket.MessageText, mustJSON(v))
}

func (u *openAIUpstream) Send(ctx context.Context, ev ClientEvent) error {
	switch ev.Type {
	case ClientAudio:
		return u.send(ctx, map[string]interface{}{"type": "input_audio_buffer.append", "audio": ev.Audio})
	case ClientCommit:
		if err := u.send(ctx, map[string]interface{}{"type": "input_audio_buffer.commit"}); err != nil {
			return err
		}
		return u.send(ctx, map[string]interface{}{"type": "response.create"})
	case ClientText:
		if err := u.send(ctx, map[string]interface{}{
			"type": "conversation.item.create",
			"item": map[string]interface{}{
				"type": "message", "role": "user",
				"content": []map[string]interface{}{{"type": "input_text", "text": ev.Text}},
			},
		}); err != nil {
			return err
		}
		return u.send(ctx, map[string]interface{}{"type": "response.create"})
	case ClientInterrupt:
		if err := u.send(ctx, map[string]interface{}{"type": "response.cancel"}); err != nil {
			return err
		}
		return u.send(ctx, map[string]interface{}{"type": "input_audio_buffer.clear"})
	}
	return fmt.Errorf("realtime: unsupported client event %q", ev.Type)
}

func (u *openAIUpstream) SendToolResult(ctx context.Context, callID, output string) error {
	if err := u.send(ctx, map[string]interface{}{
		"type": "conversation.item.create",
		"item": map[string]interface{}{"type": "function_call_output", "call_id": callID, "output": output},
	}); err != nil {
		return err
	}
	return u.send(ctx, map[string]interface{}{"type": "response.create"})
}

func (u *openAIUpstream) Close() error {
	return u.conn.Close(websocket.StatusNormalClosure, "")
}

// Recv translates provider events into the harness vocabulary, skipping the
// ones the client has no use for. It returns ErrClosed on a normal close.
func (u *openAIUpstream) Recv(ctx context.Context) (UpstreamEvent, error) {
	for {
		_, raw, err := u.conn.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure || websocket.CloseStatus(err) == websocket.StatusGoingAway {
				return UpstreamEvent{}, ErrClosed
			}
			return UpstreamEvent{}, err
		}
		if ev, ok := translateOpenAIEvent(raw); ok {
			return ev, nil
		}
	}
}

// translateOpenAIEvent maps one OpenAI server event. Both the GA names
// (response.output_audio.*) and the earlier beta names (response.audio.*) are
// accepted, since deployments are on either.
func translateOpenAIEvent(raw []byte) (UpstreamEvent, bool) {
	var e struct {
		Type       string `json:"type"`
		Delta      string `json:"delta"`
		Transcript string `json:"transcript"`
		CallID     string `json:"call_id"`
		Name       string `json:"name"`
		Arguments  string `json:"arguments"`
		Error      struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return UpstreamEvent{}, false
	}
	switch e.Type {
	case "response.output_audio.delta", "response.audio.delta":
		return UpstreamEvent{Type: ServerAudio, Audio: e.Delta}, true
	case "response.output_audio_transcript.delta", "response.audio_transcript.delta":
		return UpstreamEvent{Type: ServerTranscript, Role: "assistant", Text: e.Delta}, true
	case "response.output_audio_transcript.done", "response.audio_transcript.done":
		return UpstreamEvent{Type: ServerTranscript, Role: "assistant", Text: e.Transcript, Final: true}, true
	case "conversation.item.input_audio_transcription.completed":
		return UpstreamEvent{Type: ServerTranscript, Role: "user", Text: e.Transcript, Final: true}, true
	case "response.function_call_arguments.done":
		return UpstreamEvent{Type: UpstreamToolCall, Call: &ToolCall{CallID: e.CallID, Name: e.Name, Arguments: e.Arguments}}, true
	case "input_audio_buffer.speech_started":
		return UpstreamEvent{Type: ServerInterrupted}, true
	case "response.done":
		return UpstreamEvent{Type: ServerTurnDone}, true
	case "error":
		msg := e.Error.Message
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		return UpstreamEvent{Type: ServerError, Error: msg}, true
	}
	return UpstreamEvent{}, false
}
