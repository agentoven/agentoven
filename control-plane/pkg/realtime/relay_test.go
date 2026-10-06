package realtime_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/realtime"
)

// memClient is an in-memory client: tests push ClientEvents in and read the
// ServerEvents the relay wrote.
type memClient struct {
	in  chan realtime.ClientEvent
	mu  sync.Mutex
	out []realtime.ServerEvent
}

func newMemClient() *memClient { return &memClient{in: make(chan realtime.ClientEvent, 16)} }

func (c *memClient) ReadEvent(ctx context.Context) (realtime.ClientEvent, error) {
	select {
	case ev, ok := <-c.in:
		if !ok {
			return realtime.ClientEvent{}, realtime.ErrClosed
		}
		return ev, nil
	case <-ctx.Done():
		return realtime.ClientEvent{}, ctx.Err()
	}
}

func (c *memClient) WriteEvent(_ context.Context, ev realtime.ServerEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.out = append(c.out, ev)
	return nil
}

func (c *memClient) events() []realtime.ServerEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]realtime.ServerEvent(nil), c.out...)
}

// memUpstream is a scripted provider session.
type memUpstream struct {
	recv    chan realtime.UpstreamEvent
	mu      sync.Mutex
	sent    []realtime.ClientEvent
	results map[string]string
	closed  chan struct{}
	once    sync.Once
}

func newMemUpstream() *memUpstream {
	return &memUpstream{recv: make(chan realtime.UpstreamEvent, 16), results: map[string]string{}, closed: make(chan struct{})}
}

func (u *memUpstream) Send(_ context.Context, ev realtime.ClientEvent) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.sent = append(u.sent, ev)
	return nil
}
func (u *memUpstream) SendToolResult(_ context.Context, callID, output string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.results[callID] = output
	return nil
}
func (u *memUpstream) Recv(ctx context.Context) (realtime.UpstreamEvent, error) {
	select {
	case ev, ok := <-u.recv:
		if !ok {
			return realtime.UpstreamEvent{}, realtime.ErrClosed
		}
		return ev, nil
	case <-u.closed:
		return realtime.UpstreamEvent{}, realtime.ErrClosed
	case <-ctx.Done():
		return realtime.UpstreamEvent{}, ctx.Err()
	}
}
func (u *memUpstream) Close() error { u.once.Do(func() { close(u.closed) }); return nil }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestRelaySendsReadyThenMovesAudioBothWays(t *testing.T) {
	client, up := newMemClient(), newMemUpstream()
	r := &realtime.Relay{SessionID: "s1"}
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background(), client, up) }()

	client.in <- realtime.ClientEvent{Type: realtime.ClientAudio, Audio: "AAAA"}
	up.recv <- realtime.UpstreamEvent{Type: realtime.ServerAudio, Audio: "BBBB"}
	up.recv <- realtime.UpstreamEvent{Type: realtime.ServerTranscript, Role: "assistant", Text: "hello", Final: true}

	waitFor(t, "audio to reach the provider", func() bool { up.mu.Lock(); defer up.mu.Unlock(); return len(up.sent) == 1 })
	waitFor(t, "audio and transcript to reach the client", func() bool { return len(client.events()) == 3 })

	evs := client.events()
	if evs[0].Type != realtime.ServerReady || evs[0].SessionID != "s1" || evs[0].Format != realtime.AudioFormat {
		t.Fatalf("first frame must be ready with session and format, got %+v", evs[0])
	}
	if evs[1].Type != realtime.ServerAudio || evs[1].Audio != "BBBB" {
		t.Fatalf("model audio must reach the client, got %+v", evs[1])
	}
	if evs[2].Text != "hello" || !evs[2].Final {
		t.Fatalf("transcript must reach the client, got %+v", evs[2])
	}

	close(client.in) // client hangs up
	if err := <-done; err != nil {
		t.Fatalf("a client hang-up is a normal end, got %v", err)
	}
}

func TestRelayRunsToolCallsAndAnswersTheModel(t *testing.T) {
	client, up := newMemClient(), newMemUpstream()
	r := &realtime.Relay{Tools: func(_ context.Context, call realtime.ToolCall) (string, bool, error) {
		if call.Name != "lookup_order" || call.Arguments != `{"id":"A-1"}` {
			t.Errorf("unexpected call %+v", call)
		}
		return "shipped", false, nil
	}}
	go r.Run(context.Background(), client, up)

	up.recv <- realtime.UpstreamEvent{Type: realtime.UpstreamToolCall, Call: &realtime.ToolCall{CallID: "c1", Name: "lookup_order", Arguments: `{"id":"A-1"}`}}

	waitFor(t, "the tool result to reach the model", func() bool { up.mu.Lock(); defer up.mu.Unlock(); return up.results["c1"] == "shipped" })

	var sawCall, sawResult bool
	for _, ev := range client.events() {
		sawCall = sawCall || (ev.Type == realtime.ServerToolCall && ev.ToolName == "lookup_order")
		sawResult = sawResult || (ev.Type == realtime.ServerToolResult && ev.ToolOut == "shipped" && !ev.IsError)
	}
	if !sawCall || !sawResult {
		t.Fatalf("client should be told about the tool call and result, got %+v", client.events())
	}
}

func TestRelayAnswersTheModelEvenWhenTheToolFails(t *testing.T) {
	client, up := newMemClient(), newMemUpstream()
	r := &realtime.Relay{Tools: func(context.Context, realtime.ToolCall) (string, bool, error) {
		return "", false, errors.New("gateway down")
	}}
	go r.Run(context.Background(), client, up)

	up.recv <- realtime.UpstreamEvent{Type: realtime.UpstreamToolCall, Call: &realtime.ToolCall{CallID: "c2", Name: "x", Arguments: "{}"}}
	waitFor(t, "an error result for the model", func() bool { up.mu.Lock(); defer up.mu.Unlock(); return up.results["c2"] != "" })
	if got := up.results["c2"]; got != "tool failed: gateway down" {
		t.Fatalf("the model must be told the tool failed, got %q", got)
	}
}

func TestRelayWithoutAToolRunnerStillAnswersToolCalls(t *testing.T) {
	client, up := newMemClient(), newMemUpstream()
	go (&realtime.Relay{}).Run(context.Background(), client, up)
	up.recv <- realtime.UpstreamEvent{Type: realtime.UpstreamToolCall, Call: &realtime.ToolCall{CallID: "c3", Name: "x", Arguments: "{}"}}
	waitFor(t, "an answer", func() bool { up.mu.Lock(); defer up.mu.Unlock(); return up.results["c3"] != "" })
}

func TestRelayReportsUnknownClientEventsWithoutEndingTheCall(t *testing.T) {
	client, up := newMemClient(), newMemUpstream()
	go (&realtime.Relay{}).Run(context.Background(), client, up)
	client.in <- realtime.ClientEvent{Type: "bogus"}
	waitFor(t, "an error frame", func() bool {
		for _, ev := range client.events() {
			if ev.Type == realtime.ServerError {
				return true
			}
		}
		return false
	})
	// still alive: audio afterwards must still be forwarded
	client.in <- realtime.ClientEvent{Type: realtime.ClientAudio, Audio: "AAAA"}
	waitFor(t, "audio forwarded after the bad frame", func() bool { up.mu.Lock(); defer up.mu.Unlock(); return len(up.sent) == 1 })
}

func TestRelayEndsCleanlyWhenTheProviderCloses(t *testing.T) {
	client, up := newMemClient(), newMemUpstream()
	done := make(chan error, 1)
	go func() { done <- (&realtime.Relay{}).Run(context.Background(), client, up) }()
	up.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("provider close is a normal end, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not stop after the provider closed")
	}
}

// noInterruptUpstream is a provider that cannot cancel a response.
type noInterruptUpstream struct{ *memUpstream }

func (u noInterruptUpstream) Send(ctx context.Context, ev realtime.ClientEvent) error {
	if ev.Type == realtime.ClientInterrupt {
		return realtime.ErrNotSupported
	}
	return u.memUpstream.Send(ctx, ev)
}

func TestRelayReportsUnsupportedEventsAndKeepsTheCallAlive(t *testing.T) {
	client := newMemClient()
	up := noInterruptUpstream{newMemUpstream()}
	go (&realtime.Relay{}).Run(context.Background(), client, up)

	client.in <- realtime.ClientEvent{Type: realtime.ClientInterrupt}
	waitFor(t, "an error frame for the unsupported interrupt", func() bool {
		for _, ev := range client.events() {
			if ev.Type == realtime.ServerError && ev.Error != "" {
				return true
			}
		}
		return false
	})

	client.in <- realtime.ClientEvent{Type: realtime.ClientAudio, Audio: "AAAA"}
	waitFor(t, "audio still flowing after the unsupported event", func() bool {
		up.mu.Lock()
		defer up.mu.Unlock()
		return len(up.sent) == 1
	})
}

func TestRelayObserverSeesEventsButNotAudio(t *testing.T) {
	client, up := newMemClient(), newMemUpstream()
	var mu sync.Mutex
	var seen []string
	r := &realtime.Relay{Observe: func(ev realtime.ServerEvent) error {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, ev.Type)
		return nil
	}}
	go r.Run(context.Background(), client, up)

	up.recv <- realtime.UpstreamEvent{Type: realtime.ServerAudio, Audio: "AAAA"}
	up.recv <- realtime.UpstreamEvent{Type: realtime.ServerTranscript, Role: "user", Text: "hi", Final: true}
	up.recv <- realtime.UpstreamEvent{Type: realtime.UpstreamToolCall, Call: &realtime.ToolCall{CallID: "c", Name: "t", Arguments: "{}"}}
	waitFor(t, "observed transcript and tool events", func() bool { mu.Lock(); defer mu.Unlock(); return len(seen) >= 3 })

	mu.Lock()
	defer mu.Unlock()
	got := map[string]bool{}
	for _, s := range seen {
		got[s] = true
	}
	if got[realtime.ServerAudio] || !got[realtime.ServerTranscript] || !got[realtime.ServerToolCall] || !got[realtime.ServerToolResult] {
		t.Fatalf("observer must see transcripts and tool events, never audio; saw %v", seen)
	}
}

func TestRelayEndsTheCallWhenTheObserverReportsAPolicyViolation(t *testing.T) {
	client, up := newMemClient(), newMemUpstream()
	r := &realtime.Relay{Observe: func(ev realtime.ServerEvent) error {
		if ev.Type == realtime.ServerTranscript && ev.Text == "forbidden" {
			return fmt.Errorf("%w: input blocked by guardrails", realtime.ErrPolicy)
		}
		return nil
	}}
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background(), client, up) }()

	up.recv <- realtime.UpstreamEvent{Type: realtime.ServerTranscript, Role: "user", Text: "forbidden", Final: true}

	select {
	case err := <-done:
		if !errors.Is(err, realtime.ErrPolicy) {
			t.Fatalf("Run must return the policy error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the call must end on a policy violation")
	}
	var told bool
	for _, ev := range client.events() {
		told = told || (ev.Type == realtime.ServerError && strings.Contains(ev.Error, "blocked by guardrails"))
	}
	if !told {
		t.Fatalf("the client must be told why, got %+v", client.events())
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	var interrupted bool
	for _, ev := range up.sent {
		interrupted = interrupted || ev.Type == realtime.ClientInterrupt
	}
	if !interrupted {
		t.Fatal("the model must be told to stop speaking")
	}
}
