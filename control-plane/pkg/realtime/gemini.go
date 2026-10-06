package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/coder/websocket"
)

const defaultGeminiLiveEndpoint = "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"

// geminiInputMime declares the rate of the audio the protocol carries. Gemini
// natively takes 16 kHz but resamples any declared rate, so the 24 kHz the
// harness protocol uses goes through unchanged.
const geminiInputMime = "audio/pcm;rate=24000"

// DialGemini opens a session against the Gemini Live API (BidiGenerateContent).
func DialGemini(ctx context.Context, o DialOptions, cfg SessionConfig) (Upstream, error) {
	endpoint := o.Endpoint
	if endpoint == "" {
		endpoint = defaultGeminiLiveEndpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("realtime: bad endpoint: %w", err)
	}
	// The key travels in the URL, so every error below is scrubbed of it.
	q := u.Query()
	q.Set("key", o.APIKey)
	u.RawQuery = q.Encode()

	conn, _, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPClient: o.HTTPClient})
	if err != nil {
		return nil, fmt.Errorf("realtime: connecting to gemini: %s", redact(err.Error(), o.APIKey))
	}
	conn.SetReadLimit(16 << 20)

	up := &geminiUpstream{conn: conn, key: o.APIKey, pending: map[string]string{}}
	if err := up.send(ctx, geminiSetup(cfg)); err != nil {
		conn.Close(websocket.StatusInternalError, "setup failed")
		return nil, fmt.Errorf("realtime: configuring gemini session: %s", redact(err.Error(), o.APIKey))
	}

	// The session is not usable until Gemini says so; a bad model name or
	// tool schema shows up here as a close, which belongs to Connect, not to
	// the first audio frame later.
	_, raw, err := conn.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("realtime: gemini rejected the session: %s", redact(err.Error(), o.APIKey))
	}
	var first struct {
		SetupComplete *json.RawMessage `json:"setupComplete"`
	}
	if json.Unmarshal(raw, &first) != nil || first.SetupComplete == nil {
		conn.Close(websocket.StatusProtocolError, "no setupComplete")
		return nil, fmt.Errorf("realtime: gemini did not confirm the session setup")
	}
	return up, nil
}

func redact(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, "***")
}

func geminiSetup(cfg SessionConfig) map[string]interface{} {
	model := cfg.Model
	if !strings.HasPrefix(model, "models/") {
		model = "models/" + model
	}
	gen := map[string]interface{}{"responseModalities": []string{"AUDIO"}}
	if cfg.Voice != "" {
		gen["speechConfig"] = map[string]interface{}{
			"voiceConfig": map[string]interface{}{
				"prebuiltVoiceConfig": map[string]interface{}{"voiceName": cfg.Voice},
			},
		}
	}
	setup := map[string]interface{}{
		"model":                    model,
		"generationConfig":         gen,
		"inputAudioTranscription":  map[string]interface{}{},
		"outputAudioTranscription": map[string]interface{}{},
	}
	if cfg.Instructions != "" {
		setup["systemInstruction"] = map[string]interface{}{"parts": []map[string]interface{}{{"text": cfg.Instructions}}}
	}
	if len(cfg.Tools) > 0 {
		decls := make([]map[string]interface{}, 0, len(cfg.Tools))
		for _, t := range cfg.Tools {
			d := map[string]interface{}{"name": t.Name, "description": t.Description}
			if params := geminiSchema(t.Parameters); params != nil {
				d["parameters"] = params
			}
			decls = append(decls, d)
		}
		setup["tools"] = []map[string]interface{}{{"functionDeclarations": decls}}
	}
	return map[string]interface{}{"setup": setup}
}

// geminiSchema reduces a JSON Schema to the subset Gemini accepts. Keys it
// does not know (additionalProperties, $schema, default, ...) are rejected
// by the API, and an object schema with no properties is too, so a tool that
// takes no arguments is sent with no parameters at all.
func geminiSchema(schema map[string]interface{}) map[string]interface{} {
	if len(schema) == 0 {
		return nil
	}
	out := cleanSchema(schema)
	if props, _ := out["properties"].(map[string]interface{}); out["type"] == "object" && len(props) == 0 {
		return nil
	}
	return out
}

func cleanSchema(in map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for _, k := range []string{"type", "description", "enum", "format", "nullable", "required"} {
		if v, ok := in[k]; ok {
			out[k] = v
		}
	}
	if props, ok := in["properties"].(map[string]interface{}); ok {
		cleaned := map[string]interface{}{}
		for name, p := range props {
			if pm, ok := p.(map[string]interface{}); ok {
				cleaned[name] = cleanSchema(pm)
			}
		}
		out["properties"] = cleaned
	}
	if items, ok := in["items"].(map[string]interface{}); ok {
		out["items"] = cleanSchema(items)
	}
	return out
}

type geminiUpstream struct {
	conn *websocket.Conn
	key  string

	mu      sync.Mutex
	pending map[string]string // tool call id → function name, for the response

	// Used only by the single goroutine that calls Recv.
	queue   []UpstreamEvent
	userBuf strings.Builder
	asstBuf strings.Builder
}

func (u *geminiUpstream) send(ctx context.Context, v interface{}) error {
	return u.conn.Write(ctx, websocket.MessageText, mustJSON(v))
}

func (u *geminiUpstream) Send(ctx context.Context, ev ClientEvent) error {
	switch ev.Type {
	case ClientAudio:
		return u.send(ctx, map[string]interface{}{"realtimeInput": map[string]interface{}{
			"audio": map[string]interface{}{"data": ev.Audio, "mimeType": geminiInputMime},
		}})
	case ClientCommit:
		// With automatic voice detection on, ending the stream is how a
		// client says "I am done talking".
		return u.send(ctx, map[string]interface{}{"realtimeInput": map[string]interface{}{"audioStreamEnd": true}})
	case ClientText:
		return u.send(ctx, map[string]interface{}{"clientContent": map[string]interface{}{
			"turns":        []map[string]interface{}{{"role": "user", "parts": []map[string]interface{}{{"text": ev.Text}}}},
			"turnComplete": true,
		}})
	case ClientInterrupt:
		return fmt.Errorf("%w: Gemini Live has no cancel call; it stops speaking by itself when the caller talks over it", ErrNotSupported)
	}
	return fmt.Errorf("realtime: unsupported client event %q", ev.Type)
}

func (u *geminiUpstream) SendToolResult(ctx context.Context, callID, output string) error {
	u.mu.Lock()
	name := u.pending[callID]
	delete(u.pending, callID)
	u.mu.Unlock()
	return u.send(ctx, map[string]interface{}{"toolResponse": map[string]interface{}{
		"functionResponses": []map[string]interface{}{{
			"id": callID, "name": name, "response": map[string]interface{}{"output": output},
		}},
	}})
}

func (u *geminiUpstream) Close() error {
	return u.conn.Close(websocket.StatusNormalClosure, "")
}

func (u *geminiUpstream) Recv(ctx context.Context) (UpstreamEvent, error) {
	for len(u.queue) == 0 {
		_, raw, err := u.conn.Read(ctx)
		if err != nil {
			if s := websocket.CloseStatus(err); s == websocket.StatusNormalClosure || s == websocket.StatusGoingAway {
				return UpstreamEvent{}, ErrClosed
			}
			if websocket.CloseStatus(err) != -1 {
				// An abnormal close from Gemini carries its reason, which is the
				// only explanation the caller will get.
				return UpstreamEvent{}, fmt.Errorf("gemini closed the session: %s", redact(err.Error(), u.key))
			}
			return UpstreamEvent{}, err
		}
		u.queue = append(u.queue, u.translate(raw)...)
	}
	ev := u.queue[0]
	u.queue = u.queue[1:]
	return ev, nil
}

type geminiFrame struct {
	ServerContent *struct {
		ModelTurn *struct {
			Parts []struct {
				InlineData *struct {
					MimeType string `json:"mimeType"`
					Data     string `json:"data"`
				} `json:"inlineData"`
			} `json:"parts"`
		} `json:"modelTurn"`
		InputTranscription *struct {
			Text string `json:"text"`
		} `json:"inputTranscription"`
		OutputTranscription *struct {
			Text string `json:"text"`
		} `json:"outputTranscription"`
		Interrupted  bool `json:"interrupted"`
		TurnComplete bool `json:"turnComplete"`
	} `json:"serverContent"`
	ToolCall *struct {
		FunctionCalls []struct {
			ID   string          `json:"id"`
			Name string          `json:"name"`
			Args json.RawMessage `json:"args"`
		} `json:"functionCalls"`
	} `json:"toolCall"`
}

// translate turns one Gemini frame into harness events. Transcripts arrive as
// fragments with no "final" marker, so they are accumulated and flushed as a
// final event when the turn ends, is interrupted, or the other speaker starts.
func (u *geminiUpstream) translate(raw []byte) []UpstreamEvent {
	var f geminiFrame
	if json.Unmarshal(raw, &f) != nil {
		return nil
	}
	var out []UpstreamEvent

	flushUser := func() {
		if u.userBuf.Len() > 0 {
			out = append(out, UpstreamEvent{Type: ServerTranscript, Role: "user", Text: u.userBuf.String(), Final: true})
			u.userBuf.Reset()
		}
	}
	flushAssistant := func() {
		if u.asstBuf.Len() > 0 {
			out = append(out, UpstreamEvent{Type: ServerTranscript, Role: "assistant", Text: u.asstBuf.String(), Final: true})
			u.asstBuf.Reset()
		}
	}

	if sc := f.ServerContent; sc != nil {
		if sc.InputTranscription != nil && sc.InputTranscription.Text != "" {
			u.userBuf.WriteString(sc.InputTranscription.Text)
			out = append(out, UpstreamEvent{Type: ServerTranscript, Role: "user", Text: sc.InputTranscription.Text})
		}
		if sc.ModelTurn != nil {
			for _, p := range sc.ModelTurn.Parts {
				if p.InlineData != nil && p.InlineData.Data != "" {
					flushUser()
					out = append(out, UpstreamEvent{Type: ServerAudio, Audio: p.InlineData.Data})
				}
			}
		}
		if sc.OutputTranscription != nil && sc.OutputTranscription.Text != "" {
			flushUser()
			u.asstBuf.WriteString(sc.OutputTranscription.Text)
			out = append(out, UpstreamEvent{Type: ServerTranscript, Role: "assistant", Text: sc.OutputTranscription.Text})
		}
		if sc.Interrupted {
			flushAssistant()
			out = append(out, UpstreamEvent{Type: ServerInterrupted})
		}
		if sc.TurnComplete {
			flushUser()
			flushAssistant()
			out = append(out, UpstreamEvent{Type: ServerTurnDone})
		}
	}

	if f.ToolCall != nil {
		for _, c := range f.ToolCall.FunctionCalls {
			args := string(c.Args)
			if args == "" || args == "null" {
				args = "{}"
			}
			u.mu.Lock()
			u.pending[c.ID] = c.Name
			u.mu.Unlock()
			out = append(out, UpstreamEvent{Type: UpstreamToolCall, Call: &ToolCall{CallID: c.ID, Name: c.Name, Arguments: args}})
		}
	}
	return out
}
