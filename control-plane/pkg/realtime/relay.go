package realtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ClientConn is the client side of a session, independent of transport. The
// WebSocket handler adapts a connection to it; tests use an in-memory one.
type ClientConn interface {
	ReadEvent(ctx context.Context) (ClientEvent, error)
	WriteEvent(ctx context.Context, ev ServerEvent) error
}

// Relay connects a client to a provider session for the life of a call.
//
// Audio flows both ways without the harness inspecting it. Tool calls are the
// exception: the provider stops and waits for the output, so the relay runs
// the tool in its own goroutine (audio keeps moving meanwhile), tells the
// client what happened, and hands the result back.
type Relay struct {
	Tools     ToolRunner
	SessionID string

	// Observe, if set, sees every event sent to the client except audio, from
	// the relay's goroutines (so it must be safe for concurrent use and quick).
	// It is how a caller records transcripts and tool activity, and how it
	// enforces policy: returning an error that wraps ErrPolicy tells the client
	// why (an error event), stops the session, and makes Run return that error.
	// Any other error is ignored.
	Observe func(ServerEvent) error
}

// ErrPolicy is wrapped by an Observe error to end the session on purpose, for
// example because what the caller said broke a guardrail.
var ErrPolicy = errors.New("realtime: session ended by policy")

func (r *Relay) observe(ev ServerEvent) error {
	if r.Observe == nil || ev.Type == ServerAudio {
		return nil
	}
	if err := r.Observe(ev); errors.Is(err, ErrPolicy) {
		return err
	}
	return nil
}

// Run blocks until either side ends the session or ctx is cancelled. A normal
// end (client hung up, provider closed) returns nil.
func (r *Relay) Run(ctx context.Context, client ClientConn, up Upstream) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if err := client.WriteEvent(ctx, ServerEvent{Type: ServerReady, SessionID: r.SessionID, Format: AudioFormat}); err != nil {
		return err
	}

	errc := make(chan error, 2)
	var tools sync.WaitGroup

	go func() { errc <- r.clientToUpstream(ctx, client, up) }()
	go func() { errc <- r.upstreamToClient(ctx, client, up, &tools) }()

	err := <-errc
	cancel()
	_ = up.Close()
	tools.Wait()
	if errors.Is(err, ErrClosed) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func (r *Relay) clientToUpstream(ctx context.Context, client ClientConn, up Upstream) error {
	for {
		ev, err := client.ReadEvent(ctx)
		if err != nil {
			return err
		}
		switch ev.Type {
		case ClientAudio, ClientCommit, ClientText, ClientInterrupt:
			if err := up.Send(ctx, ev); err != nil {
				if errors.Is(err, ErrNotSupported) {
					if werr := client.WriteEvent(ctx, ServerEvent{Type: ServerError, Error: err.Error()}); werr != nil {
						return werr
					}
					continue
				}
				return err
			}
		default:
			// An unknown frame is a client bug worth surfacing, not a reason to
			// drop the call.
			if werr := client.WriteEvent(ctx, ServerEvent{Type: ServerError, Error: fmt.Sprintf("unknown client event %q", ev.Type)}); werr != nil {
				return werr
			}
		}
	}
}

func (r *Relay) upstreamToClient(ctx context.Context, client ClientConn, up Upstream, tools *sync.WaitGroup) error {
	for {
		ev, err := up.Recv(ctx)
		if err != nil {
			return err
		}
		if ev.Type == UpstreamToolCall {
			if ev.Call == nil {
				continue
			}
			call := *ev.Call
			tools.Add(1)
			go func() {
				defer tools.Done()
				r.runTool(ctx, client, up, call)
			}()
			continue
		}
		out := ServerEvent{Type: ev.Type, Audio: ev.Audio, Role: ev.Role, Text: ev.Text, Final: ev.Final, Error: ev.Error}
		if err := client.WriteEvent(ctx, out); err != nil {
			return err
		}
		if err := r.observe(out); err != nil {
			// Stop the model talking before telling the caller why the call ends.
			_ = up.Send(ctx, ClientEvent{Type: ClientInterrupt})
			_ = client.WriteEvent(ctx, ServerEvent{Type: ServerError, Error: err.Error()})
			return err
		}
	}
}

func (r *Relay) runTool(ctx context.Context, client ClientConn, up Upstream, call ToolCall) {
	callEv := ServerEvent{Type: ServerToolCall, ToolName: call.Name, ToolArgs: call.Arguments}
	_ = client.WriteEvent(ctx, callEv)
	_ = r.observe(callEv)

	output, isErr := "", false
	if r.Tools == nil {
		output, isErr = "tools are not available in this session", true
	} else {
		o, e, err := r.Tools(ctx, call)
		switch {
		case err != nil:
			output, isErr = "tool failed: "+err.Error(), true
		default:
			output, isErr = o, e
		}
	}

	resultEv := ServerEvent{Type: ServerToolResult, ToolName: call.Name, ToolOut: output, IsError: isErr}
	_ = client.WriteEvent(ctx, resultEv)
	_ = r.observe(resultEv)
	// The model must always get an answer, or it waits forever.
	_ = up.SendToolResult(ctx, call.CallID, output)
}
