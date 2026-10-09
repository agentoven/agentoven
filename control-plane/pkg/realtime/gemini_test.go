package realtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/realtime"
	"github.com/coder/websocket"
)

type fakeGeminiLive struct {
	srv    *httptest.Server
	mu     sync.Mutex
	frames []map[string]interface{}
	query  string
	conn   chan *websocket.Conn
	got    chan struct{}
	// refuse closes the socket instead of confirming setup.
	refuse bool
}

func newFakeGeminiLive(t *testing.T, refuse bool) *fakeGeminiLive {
	t.Helper()
	f := &fakeGeminiLive{conn: make(chan *websocket.Conn, 1), got: make(chan struct{}, 64), refuse: refuse}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.query = r.URL.RawQuery
		f.mu.Unlock()
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		f.conn <- c
		first := true
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
			f.got <- struct{}{}
			if first {
				first = false
				if f.refuse {
					c.Close(websocket.StatusPolicyViolation, "model not found")
					return
				}
				_ = c.Write(context.Background(), websocket.MessageText, []byte(`{"setupComplete":{}}`))
			}
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGeminiLive) wsURL() string { return "ws" + strings.TrimPrefix(f.srv.URL, "http") }

func (f *fakeGeminiLive) push(t *testing.T, c *websocket.Conn, v interface{}) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := c.Write(context.Background(), websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeGeminiLive) waitFrames(t *testing.T, n int) []map[string]interface{} {
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
		case <-f.got:
		case <-deadline:
			t.Fatalf("timed out waiting for %d frames, have %d", n, got)
		}
	}
}

func connectGemini(t *testing.T, f *fakeGeminiLive, cfg realtime.SessionConfig) (realtime.Upstream, *websocket.Conn) {
	t.Helper()
	up, err := realtime.DialGemini(context.Background(), realtime.DialOptions{Endpoint: f.wsURL(), APIKey: "AIza-secret"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { up.Close() })
	return up, <-f.conn
}

func recvN(t *testing.T, up realtime.Upstream, n int) []realtime.UpstreamEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var out []realtime.UpstreamEvent
	for len(out) < n {
		ev, err := up.Recv(ctx)
		if err != nil {
			t.Fatalf("after %d events: %v", len(out), err)
		}
		out = append(out, ev)
	}
	return out
}

func TestGeminiSetupCarriesModelVoicePromptAndCleanedTools(t *testing.T) {
	f := newFakeGeminiLive(t, false)
	connectGemini(t, f, realtime.SessionConfig{
		Model: "gemini-live-x", Voice: "Kore", Instructions: "You are a booking agent.",
		Tools: []realtime.ToolDef{
			{Name: "lookup_order", Description: "find an order", Parameters: map[string]interface{}{
				"type": "object", "additionalProperties": false, "$schema": "x",
				"properties": map[string]interface{}{"id": map[string]interface{}{"type": "string", "default": "z"}},
				"required":   []interface{}{"id"},
			}},
			{Name: "ping", Description: "no args", Parameters: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}},
		},
	})

	if f.query != "key=AIza-secret" {
		t.Fatalf("the API key must be sent, got %q", f.query)
	}
	setup := f.waitFrames(t, 1)[0]["setup"].(map[string]interface{})
	if setup["model"] != "models/gemini-live-x" {
		t.Fatalf("model must be prefixed, got %v", setup["model"])
	}
	gen := setup["generationConfig"].(map[string]interface{})
	if gen["responseModalities"].([]interface{})[0] != "AUDIO" {
		t.Fatalf("audio output expected: %v", gen["responseModalities"])
	}
	voice := gen["speechConfig"].(map[string]interface{})["voiceConfig"].(map[string]interface{})["prebuiltVoiceConfig"].(map[string]interface{})["voiceName"]
	if voice != "Kore" {
		t.Fatalf("voice missing: %v", voice)
	}
	if setup["systemInstruction"].(map[string]interface{})["parts"].([]interface{})[0].(map[string]interface{})["text"] != "You are a booking agent." {
		t.Fatalf("instructions missing: %v", setup["systemInstruction"])
	}
	if setup["inputAudioTranscription"] == nil || setup["outputAudioTranscription"] == nil {
		t.Fatal("transcription must be enabled so clients get transcripts")
	}

	decls := setup["tools"].([]interface{})[0].(map[string]interface{})["functionDeclarations"].([]interface{})
	lookup := decls[0].(map[string]interface{})
	params := lookup["parameters"].(map[string]interface{})
	if _, bad := params["additionalProperties"]; bad {
		t.Fatal("keys Gemini rejects must be stripped from the schema")
	}
	if _, bad := params["$schema"]; bad {
		t.Fatal("$schema must be stripped")
	}
	if _, bad := params["properties"].(map[string]interface{})["id"].(map[string]interface{})["default"]; bad {
		t.Fatal("nested unsupported keys must be stripped too")
	}
	if _, has := decls[1].(map[string]interface{})["parameters"]; has {
		t.Fatal("a tool with no arguments must be sent without parameters (Gemini rejects empty objects)")
	}
}

func TestGeminiOmitsVoiceWhenNoneIsChosen(t *testing.T) {
	f := newFakeGeminiLive(t, false)
	connectGemini(t, f, realtime.SessionConfig{Model: "m"})
	gen := f.waitFrames(t, 1)[0]["setup"].(map[string]interface{})["generationConfig"].(map[string]interface{})
	if _, has := gen["speechConfig"]; has {
		t.Fatal("with no voice chosen the provider default must apply")
	}
}

func TestGeminiRefusedSetupFailsConnectAndNeverLeaksTheKey(t *testing.T) {
	f := newFakeGeminiLive(t, true)
	_, err := realtime.DialGemini(context.Background(), realtime.DialOptions{Endpoint: f.wsURL(), APIKey: "AIza-secret"}, realtime.SessionConfig{Model: "bad"})
	if err == nil {
		t.Fatal("a refused setup must fail Connect, not the first audio frame")
	}
	if strings.Contains(err.Error(), "AIza-secret") {
		t.Fatalf("the API key leaked into the error: %v", err)
	}
	if !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("Gemini's own reason should reach the caller: %v", err)
	}
}

func TestGeminiConnectErrorsDoNotLeakTheKey(t *testing.T) {
	_, err := realtime.DialGemini(context.Background(), realtime.DialOptions{Endpoint: "ws://127.0.0.1:1", APIKey: "AIza-secret"}, realtime.SessionConfig{Model: "m"})
	if err == nil || strings.Contains(err.Error(), "AIza-secret") {
		t.Fatalf("expected a connection error without the key, got %v", err)
	}
}

func TestGeminiClientEventsBecomeFrames(t *testing.T) {
	f := newFakeGeminiLive(t, false)
	up, _ := connectGemini(t, f, realtime.SessionConfig{Model: "m"})
	ctx := context.Background()
	_ = up.Send(ctx, realtime.ClientEvent{Type: realtime.ClientAudio, Audio: "AAAA"})
	_ = up.Send(ctx, realtime.ClientEvent{Type: realtime.ClientCommit})
	_ = up.Send(ctx, realtime.ClientEvent{Type: realtime.ClientText, Text: "hi"})

	frames := f.waitFrames(t, 4)
	audio := frames[1]["realtimeInput"].(map[string]interface{})["audio"].(map[string]interface{})
	if audio["data"] != "AAAA" || audio["mimeType"] != "audio/pcm;rate=24000" {
		t.Fatalf("audio frame: %v", audio)
	}
	if frames[2]["realtimeInput"].(map[string]interface{})["audioStreamEnd"] != true {
		t.Fatalf("commit must end the audio stream: %v", frames[2])
	}
	cc := frames[3]["clientContent"].(map[string]interface{})
	turn := cc["turns"].([]interface{})[0].(map[string]interface{})
	if cc["turnComplete"] != true || turn["role"] != "user" || turn["parts"].([]interface{})[0].(map[string]interface{})["text"] != "hi" {
		t.Fatalf("text turn: %v", cc)
	}
}

func TestGeminiInterruptIsReportedAsNotSupported(t *testing.T) {
	f := newFakeGeminiLive(t, false)
	up, _ := connectGemini(t, f, realtime.SessionConfig{Model: "m"})
	err := up.Send(context.Background(), realtime.ClientEvent{Type: realtime.ClientInterrupt})
	if !errors.Is(err, realtime.ErrNotSupported) {
		t.Fatalf("Gemini has no cancel call; expected ErrNotSupported, got %v", err)
	}
}

func TestGeminiAudioAndTranscriptsAreTranslatedAndAccumulated(t *testing.T) {
	f := newFakeGeminiLive(t, false)
	up, conn := connectGemini(t, f, realtime.SessionConfig{Model: "m"})

	// the caller speaks (fragments), then the model answers (audio + fragments), then the turn ends
	f.push(t, conn, map[string]interface{}{"serverContent": map[string]interface{}{"inputTranscription": map[string]string{"text": "where is "}}})
	f.push(t, conn, map[string]interface{}{"serverContent": map[string]interface{}{"inputTranscription": map[string]string{"text": "my order"}}})
	f.push(t, conn, map[string]interface{}{"serverContent": map[string]interface{}{
		"modelTurn":           map[string]interface{}{"parts": []map[string]interface{}{{"inlineData": map[string]string{"mimeType": "audio/pcm;rate=24000", "data": "QUJD"}}}},
		"outputTranscription": map[string]string{"text": "It shipped "},
	}})
	f.push(t, conn, map[string]interface{}{"serverContent": map[string]interface{}{"outputTranscription": map[string]string{"text": "today."}, "turnComplete": true}})

	evs := recvN(t, up, 8)
	var got []string
	for _, e := range evs {
		tag := e.Type
		if e.Type == realtime.ServerTranscript {
			tag += ":" + e.Role + ":" + e.Text
			if e.Final {
				tag += ":final"
			}
		}
		got = append(got, tag)
	}
	want := []string{
		"transcript:user:where is ",
		"transcript:user:my order",
		"transcript:user:where is my order:final", // flushed when the model starts answering
		"audio",
		"transcript:assistant:It shipped ",
		"transcript:assistant:today.",
		"transcript:assistant:It shipped today.:final",
		"turn_done",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("event sequence mismatch\n got: %v\nwant: %v", got, want)
	}
}

func TestGeminiInterruptedFlushesPartialSpeech(t *testing.T) {
	f := newFakeGeminiLive(t, false)
	up, conn := connectGemini(t, f, realtime.SessionConfig{Model: "m"})
	f.push(t, conn, map[string]interface{}{"serverContent": map[string]interface{}{"outputTranscription": map[string]string{"text": "Your order is"}}})
	f.push(t, conn, map[string]interface{}{"serverContent": map[string]interface{}{"interrupted": true}})
	evs := recvN(t, up, 3)
	if evs[1].Type != realtime.ServerTranscript || !evs[1].Final || evs[1].Text != "Your order is" {
		t.Fatalf("what the model managed to say should be kept as final: %+v", evs[1])
	}
	if evs[2].Type != realtime.ServerInterrupted {
		t.Fatalf("expected interrupted so clients can flush playback: %+v", evs[2])
	}
}

func TestGeminiToolCallsRoundTripWithTheFunctionName(t *testing.T) {
	f := newFakeGeminiLive(t, false)
	up, conn := connectGemini(t, f, realtime.SessionConfig{Model: "m"})
	f.push(t, conn, map[string]interface{}{"toolCall": map[string]interface{}{"functionCalls": []map[string]interface{}{
		{"id": "fc1", "name": "lookup_order", "args": map[string]string{"id": "A-1"}},
		{"id": "fc2", "name": "ping"},
	}}})

	evs := recvN(t, up, 2)
	if evs[0].Type != realtime.UpstreamToolCall || evs[0].Call.CallID != "fc1" || evs[0].Call.Name != "lookup_order" || evs[0].Call.Arguments != `{"id":"A-1"}` {
		t.Fatalf("first tool call: %+v", evs[0])
	}
	if evs[1].Call.Arguments != "{}" {
		t.Fatalf("a call with no args must still carry valid JSON, got %q", evs[1].Call.Arguments)
	}

	if err := up.SendToolResult(context.Background(), "fc1", "shipped"); err != nil {
		t.Fatal(err)
	}
	frames := f.waitFrames(t, 2)
	resp := frames[1]["toolResponse"].(map[string]interface{})["functionResponses"].([]interface{})[0].(map[string]interface{})
	if resp["id"] != "fc1" || resp["name"] != "lookup_order" || resp["response"].(map[string]interface{})["output"] != "shipped" {
		t.Fatalf("the response must echo the call id AND function name: %v", resp)
	}
}

func TestGeminiAbnormalCloseCarriesGeminisReason(t *testing.T) {
	f := newFakeGeminiLive(t, false)
	up, conn := connectGemini(t, f, realtime.SessionConfig{Model: "m"})
	errc := make(chan error, 1)
	go func() { _, err := up.Recv(context.Background()); errc <- err }()
	go conn.Close(websocket.StatusInternalError, "quota exceeded")
	err := <-errc
	if err == nil || !strings.Contains(err.Error(), "quota exceeded") || errors.Is(err, realtime.ErrClosed) {
		t.Fatalf("an abnormal close must surface Gemini's reason, got %v", err)
	}
}

func TestGeminiNormalCloseIsErrClosed(t *testing.T) {
	f := newFakeGeminiLive(t, false)
	up, conn := connectGemini(t, f, realtime.SessionConfig{Model: "m"})
	errc := make(chan error, 1)
	go func() { _, err := up.Recv(context.Background()); errc <- err }()
	go conn.Close(websocket.StatusNormalClosure, "bye")
	if err := <-errc; err != realtime.ErrClosed {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
}

func TestGeminiSetupAddsGoogleSearchOnlyWhenAskedAndWhenThereAreNoFunctionTools(t *testing.T) {
	tools := func(t *testing.T, cfg realtime.SessionConfig) []interface{} {
		t.Helper()
		f := newFakeGeminiLive(t, false)
		connectGemini(t, f, cfg)
		setup := f.waitFrames(t, 1)[0]["setup"].(map[string]interface{})
		got, _ := setup["tools"].([]interface{})
		return got
	}

	if got := tools(t, realtime.SessionConfig{Model: "m"}); got != nil {
		t.Fatalf("no web search unless asked, got %v", got)
	}
	got := tools(t, realtime.SessionConfig{Model: "m", WebSearch: true})
	if len(got) != 1 || got[0].(map[string]interface{})["googleSearch"] == nil {
		t.Fatalf("expected googleSearch, got %v", got)
	}
	withFn := tools(t, realtime.SessionConfig{Model: "m", WebSearch: true,
		Tools: []realtime.ToolDef{{Name: "ping", Description: "d", Parameters: map[string]interface{}{"type": "object"}}}})
	if len(withFn) != 1 || withFn[0].(map[string]interface{})["googleSearch"] != nil || withFn[0].(map[string]interface{})["functionDeclarations"] == nil {
		t.Fatalf("function tools win, Gemini cannot combine them with search: %v", withFn)
	}
}
