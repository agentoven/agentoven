package realtime_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/realtime"
	"github.com/coder/websocket"
)

// fakeOpenAIRealtime accepts one WebSocket, records every client frame, and
// lets the test push server frames back.
type fakeOpenAIRealtime struct {
	srv      *httptest.Server
	mu       sync.Mutex
	frames   []map[string]interface{}
	auth     string
	query    string
	conn     chan *websocket.Conn
	received chan struct{}
}

func newFakeOpenAIRealtime(t *testing.T) *fakeOpenAIRealtime {
	t.Helper()
	f := &fakeOpenAIRealtime{conn: make(chan *websocket.Conn, 1), received: make(chan struct{}, 64)}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.auth = r.Header.Get("Authorization")
		f.query = r.URL.RawQuery
		f.mu.Unlock()
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		f.conn <- c
		for {
			_, raw, err := c.Read(context.Background())
			if err != nil {
				return
			}
			var m map[string]interface{}
			_ = json.Unmarshal(raw, &m)
			f.mu.Lock()
			f.frames = append(f.frames, m)
			f.mu.Unlock()
			f.received <- struct{}{}
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOpenAIRealtime) wsURL() string { return "ws" + strings.TrimPrefix(f.srv.URL, "http") }

func (f *fakeOpenAIRealtime) push(t *testing.T, c *websocket.Conn, v interface{}) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := c.Write(context.Background(), websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeOpenAIRealtime) waitFrames(t *testing.T, n int) []map[string]interface{} {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		f.mu.Lock()
		got := len(f.frames)
		out := append([]map[string]interface{}(nil), f.frames...)
		f.mu.Unlock()
		if got >= n {
			return out
		}
		select {
		case <-f.received:
		case <-deadline:
			t.Fatalf("timed out waiting for %d frames, have %d", n, got)
		}
	}
}

func connect(t *testing.T, f *fakeOpenAIRealtime, cfg realtime.SessionConfig) (realtime.Upstream, *websocket.Conn) {
	t.Helper()
	up, err := realtime.DialOpenAI(context.Background(), realtime.DialOptions{Endpoint: f.wsURL(), APIKey: "sk-test"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { up.Close() })
	select {
	case c := <-f.conn:
		return up, c
	case <-time.After(2 * time.Second):
		t.Fatal("provider never connected")
		return nil, nil
	}
}

func TestConnectAuthenticatesAndConfiguresTheSession(t *testing.T) {
	f := newFakeOpenAIRealtime(t)
	_, _ = connect(t, f, realtime.SessionConfig{
		Model: "gpt-realtime", Voice: "marin", Instructions: "You are a booking agent.",
		Tools: []realtime.ToolDef{{Name: "lookup_order", Description: "find an order", Parameters: map[string]interface{}{"type": "object"}}},
	})

	frames := f.waitFrames(t, 1)
	if f.auth != "Bearer sk-test" || f.query != "model=gpt-realtime" {
		t.Fatalf("auth/model not sent: auth=%q query=%q", f.auth, f.query)
	}
	update := frames[0]
	if update["type"] != "session.update" {
		t.Fatalf("first frame must configure the session, got %v", update["type"])
	}
	session := update["session"].(map[string]interface{})
	if session["instructions"] != "You are a booking agent." {
		t.Fatalf("instructions missing: %v", session["instructions"])
	}
	tools := session["tools"].([]interface{})
	if len(tools) != 1 || tools[0].(map[string]interface{})["name"] != "lookup_order" {
		t.Fatalf("tools missing: %v", tools)
	}
	voice := session["audio"].(map[string]interface{})["output"].(map[string]interface{})["voice"]
	if voice != "marin" {
		t.Fatalf("voice missing: %v", voice)
	}
}

func TestClientEventsBecomeProviderFrames(t *testing.T) {
	f := newFakeOpenAIRealtime(t)
	up, _ := connect(t, f, realtime.SessionConfig{})
	ctx := context.Background()

	_ = up.Send(ctx, realtime.ClientEvent{Type: realtime.ClientAudio, Audio: "AAAA"})
	_ = up.Send(ctx, realtime.ClientEvent{Type: realtime.ClientCommit})
	_ = up.Send(ctx, realtime.ClientEvent{Type: realtime.ClientText, Text: "hi"})
	_ = up.Send(ctx, realtime.ClientEvent{Type: realtime.ClientInterrupt})

	frames := f.waitFrames(t, 1+1+2+2+2) // session.update + audio + commit/create + text/create + cancel/clear
	var types []string
	for _, fr := range frames[1:] {
		types = append(types, fr["type"].(string))
	}
	want := []string{
		"input_audio_buffer.append",
		"input_audio_buffer.commit", "response.create",
		"conversation.item.create", "response.create",
		"response.cancel", "input_audio_buffer.clear",
	}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("frame sequence mismatch\n got: %v\nwant: %v", types, want)
	}
	if frames[1]["audio"] != "AAAA" {
		t.Fatalf("audio chunk must be forwarded verbatim, got %v", frames[1]["audio"])
	}
}

func TestProviderEventsAreTranslated(t *testing.T) {
	f := newFakeOpenAIRealtime(t)
	up, conn := connect(t, f, realtime.SessionConfig{})

	f.push(t, conn, map[string]interface{}{"type": "session.created"}) // ignored
	f.push(t, conn, map[string]interface{}{"type": "response.output_audio.delta", "delta": "QUJD"})
	f.push(t, conn, map[string]interface{}{"type": "response.output_audio_transcript.done", "transcript": "hello there"})
	f.push(t, conn, map[string]interface{}{"type": "conversation.item.input_audio_transcription.completed", "transcript": "hi"})
	f.push(t, conn, map[string]interface{}{"type": "response.function_call_arguments.done", "call_id": "c1", "name": "lookup_order", "arguments": `{"id":"A-1"}`})
	f.push(t, conn, map[string]interface{}{"type": "input_audio_buffer.speech_started"})
	f.push(t, conn, map[string]interface{}{"type": "response.done"})
	f.push(t, conn, map[string]interface{}{"type": "error", "error": map[string]string{"message": "rate limited"}})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	recv := func() realtime.UpstreamEvent {
		ev, err := up.Recv(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return ev
	}

	if ev := recv(); ev.Type != realtime.ServerAudio || ev.Audio != "QUJD" {
		t.Fatalf("audio delta (session.created must be skipped): %+v", ev)
	}
	if ev := recv(); ev.Type != realtime.ServerTranscript || ev.Role != "assistant" || ev.Text != "hello there" || !ev.Final {
		t.Fatalf("assistant transcript: %+v", ev)
	}
	if ev := recv(); ev.Role != "user" || ev.Text != "hi" {
		t.Fatalf("user transcript: %+v", ev)
	}
	if ev := recv(); ev.Type != realtime.UpstreamToolCall || ev.Call.CallID != "c1" || ev.Call.Name != "lookup_order" || ev.Call.Arguments != `{"id":"A-1"}` {
		t.Fatalf("tool call: %+v", ev)
	}
	if ev := recv(); ev.Type != realtime.ServerInterrupted {
		t.Fatalf("speech started should surface as interrupted: %+v", ev)
	}
	if ev := recv(); ev.Type != realtime.ServerTurnDone {
		t.Fatalf("turn done: %+v", ev)
	}
	if ev := recv(); ev.Type != realtime.ServerError || ev.Error != "rate limited" {
		t.Fatalf("error: %+v", ev)
	}
}

func TestBetaEventNamesAreAcceptedToo(t *testing.T) {
	f := newFakeOpenAIRealtime(t)
	up, conn := connect(t, f, realtime.SessionConfig{})
	f.push(t, conn, map[string]interface{}{"type": "response.audio.delta", "delta": "QUJD"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ev, err := up.Recv(ctx)
	if err != nil || ev.Type != realtime.ServerAudio {
		t.Fatalf("beta audio delta should translate, got %+v err=%v", ev, err)
	}
}

func TestToolResultIsSentAndTheModelAskedToContinue(t *testing.T) {
	f := newFakeOpenAIRealtime(t)
	up, _ := connect(t, f, realtime.SessionConfig{})
	if err := up.SendToolResult(context.Background(), "c1", "shipped"); err != nil {
		t.Fatal(err)
	}
	frames := f.waitFrames(t, 3)
	item := frames[1]["item"].(map[string]interface{})
	if frames[1]["type"] != "conversation.item.create" || item["type"] != "function_call_output" || item["call_id"] != "c1" || item["output"] != "shipped" {
		t.Fatalf("tool output frame: %v", frames[1])
	}
	if frames[2]["type"] != "response.create" {
		t.Fatalf("the model must be asked to continue after a tool result, got %v", frames[2]["type"])
	}
}

func TestNormalCloseSurfacesAsErrClosed(t *testing.T) {
	f := newFakeOpenAIRealtime(t)
	up, conn := connect(t, f, realtime.SessionConfig{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	errc := make(chan error, 1)
	go func() { _, err := up.Recv(ctx); errc <- err }()
	// The reader must already be running: Close waits for the peer to read the
	// close frame, as a real client would.
	go conn.Close(websocket.StatusNormalClosure, "bye")
	if err := <-errc; err != realtime.ErrClosed {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
}

func TestConnectFailsClearlyWhenTheProviderIsUnreachable(t *testing.T) {
	if _, err := realtime.DialOpenAI(context.Background(), realtime.DialOptions{Endpoint: "ws://127.0.0.1:1", APIKey: "k"}, realtime.SessionConfig{}); err == nil {
		t.Fatal("expected a connection error")
	}
}
