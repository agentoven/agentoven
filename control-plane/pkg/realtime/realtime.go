// Package realtime is the library behind native speech-to-speech agents:
// a bidirectional audio session between a client and a provider's realtime
// model (OpenAI Realtime today), with the agent's tools executed by the
// harness in the middle.
//
// It defines three things:
//
//   - the wire protocol a client speaks to the harness (ClientEvent and
//     ServerEvent, JSON text frames over one WebSocket);
//   - Provider and Upstream, the seam a provider driver implements;
//   - Relay, which connects the two and runs tool calls through a ToolRunner.
//
// Audio is PCM16, 24 kHz, mono, base64 inside JSON frames. Carrying it as
// text frames keeps the protocol identical for browsers, the SDKs, and a
// telephony bridge, none of which need a binary-frame code path.
package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// Client → harness event types.
const (
	ClientAudio     = "audio"     // Audio: a base64 PCM16 chunk of the user's speech
	ClientCommit    = "commit"    // end of the user's turn (manual turn detection)
	ClientText      = "text"      // Text: a typed user turn instead of speech
	ClientInterrupt = "interrupt" // stop the model's current response (barge-in)
)

// Harness → client event types.
const (
	ServerReady       = "ready"       // session is up; SessionID and format set
	ServerAudio       = "audio"       // Audio: a base64 PCM16 chunk of the model's speech
	ServerTranscript  = "transcript"  // Role, Text, Final: what was said, by whom
	ServerToolCall    = "tool_call"   // the model is calling a tool; informational
	ServerToolResult  = "tool_result" // the tool finished; informational
	ServerTurnDone    = "turn_done"   // the model finished its response
	ServerInterrupted = "interrupted" // the caller spoke over the model: flush queued playback
	ServerError       = "error"       // Error: something failed
)

// AudioFormat is the one audio format the protocol carries.
const AudioFormat = "pcm16-24khz-mono"

// ClientEvent is one frame from the client.
type ClientEvent struct {
	Type  string `json:"type"`
	Audio string `json:"audio,omitempty"` // base64 PCM16
	Text  string `json:"text,omitempty"`
}

// ServerEvent is one frame to the client.
type ServerEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id,omitempty"`
	Format    string `json:"format,omitempty"`
	Audio     string `json:"audio,omitempty"`
	Role      string `json:"role,omitempty"` // "user" or "assistant"
	Text      string `json:"text,omitempty"`
	Final     bool   `json:"final,omitempty"`
	ToolName  string `json:"tool_name,omitempty"`
	ToolArgs  string `json:"tool_args,omitempty"`
	ToolOut   string `json:"tool_output,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
	Error     string `json:"error,omitempty"`
}

// ToolDef is a tool the model may call during the session.
type ToolDef struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// SessionConfig is everything a provider needs to open a session.
type SessionConfig struct {
	Model        string
	Voice        string
	Instructions string // the agent's system prompt
	Tools        []ToolDef
	// WebSearch asks the provider to ground answers with its own web search, where it has one
	// (Gemini Live: Google Search). Providers cannot combine it with function tools, so it is
	// applied only when Tools is empty.
	WebSearch bool
}

// ToolCall is a function call the model made.
type ToolCall struct {
	CallID    string
	Name      string
	Arguments string // JSON object text
}

// UpstreamEvent is one event from the provider, already translated to the
// harness's vocabulary. Exactly one field group is meaningful per Type.
type UpstreamEvent struct {
	Type string // one of the Server* constants, or "tool_call_request"

	Audio string
	Role  string
	Text  string
	Final bool

	Call  *ToolCall // set for Type == UpstreamToolCall
	Error string
}

// UpstreamToolCall is an UpstreamEvent.Type: the model wants a tool run and is
// waiting for its output before it continues.
const UpstreamToolCall = "tool_call_request"

// Upstream is one open provider session.
type Upstream interface {
	// Send forwards a client event (audio, commit, text, interrupt).
	Send(ctx context.Context, ev ClientEvent) error
	// SendToolResult answers a tool call and asks the model to continue.
	SendToolResult(ctx context.Context, callID, output string) error
	// Recv blocks for the next provider event. It returns io.EOF-like
	// ErrClosed when the session ended normally.
	Recv(ctx context.Context) (UpstreamEvent, error)
	Close() error
}

// ErrClosed means the provider ended the session.
var ErrClosed = errors.New("realtime: session closed")

// ErrNotSupported means the provider cannot do what a client event asked
// (for example Gemini Live has no cancel call). It is reported to the client
// as an error frame and the call continues; it never ends the session.
var ErrNotSupported = errors.New("realtime: not supported by this provider")

// Driver is implemented, optionally, by a provider driver that can run live
// sessions — the same optional-interface pattern the router already uses for
// streaming and embeddings. The driver reads the provider's own settings
// (endpoint, rotated key, TLS, modalities.realtime.model), so nothing here or in the
// caller needs to know which vendor it is talking to.
type Driver interface {
	OpenRealtime(ctx context.Context, p *models.ModelProvider, cfg SessionConfig) (Upstream, error)
}

// DialOptions is what a driver supplies to the wire clients below.
type DialOptions struct {
	// Endpoint is the ws(s) URL, or "" for the vendor's own.
	Endpoint string
	APIKey   string
	// HTTPClient carries the provider's TLS settings (custom CA, skip-verify).
	// Its Timeout is only the handshake deadline; it does not cut a live call.
	HTTPClient *http.Client
}

// ToolRunner executes a tool the model called and returns its output text.
// An error result is returned as output with isError set, not as a Go error,
// so the model sees the failure and can recover; a Go error means the harness
// itself failed.
type ToolRunner func(ctx context.Context, call ToolCall) (output string, isError bool, err error)

func mustJSON(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}
