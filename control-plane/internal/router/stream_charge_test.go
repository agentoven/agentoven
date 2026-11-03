package router_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// scriptedStream sends its chunks, then ends with err (nil: a complete stream).
type scriptedStream struct {
	streamingDriver
	chunks []*models.StreamChunk
	err    error
}

func (d *scriptedStream) StreamCall(_ context.Context, _ *models.ModelProvider, _ *models.RouteRequest, cb func(*models.StreamChunk) error) error {
	for _, c := range d.chunks {
		if err := cb(c); err != nil {
			return err
		}
	}
	return d.err
}

func streamWith(t *testing.T, kind string, chunks []*models.StreamChunk, endErr error, cb func(*models.StreamChunk) error) (*reservingGate, error) {
	t.Helper()
	d := &scriptedStream{streamingDriver: streamingDriver{usageDriver{flakyDriver: flakyDriver{kind: kind, minCallsOK: true}}}, chunks: chunks, err: endErr}
	mr, ctx := routerWithProvider(t, "p1", d)
	g := &reservingGate{}
	mr.SetSpendGate(g)
	if cb == nil {
		cb = func(*models.StreamChunk) error { return nil }
	}
	err := mr.RouteStream(ctx, &models.RouteRequest{Kitchen: "default", Model: "test-model",
		Messages: []models.ChatMessage{{Role: "user", Content: strings.Repeat("question ", 40)}}}, cb)
	return g, err
}

// Review finding 9: a stream that errors, or that the client drops, was cancelled and never charged.
func TestAStreamThatFailsMidwayIsChargedForWhatWasGenerated(t *testing.T) {
	answer := strings.Repeat("generated words ", 25) // 400 characters
	g, err := streamWith(t, "sc-err", []*models.StreamChunk{{Content: answer}}, errors.New("upstream reset"), nil)
	if err == nil {
		t.Fatal("the only provider failed")
	}
	if len(g.settled) != 1 || g.cancelled != 0 {
		t.Fatalf("a stream that produced text is charged, not released: settled=%d cancelled=%d", len(g.settled), g.cancelled)
	}
	u := g.settled[0].Usage
	if u.OutputTokens < 90 || u.OutputTokens > 110 || u.InputTokens == 0 || u.TotalTokens != u.InputTokens+u.OutputTokens {
		t.Fatalf("usage is estimated from the 400 characters received (about 100 tokens) and the prompt: %+v", u)
	}
	if u.EstimatedCost < 0 || u.PriceSource == "" {
		t.Fatalf("the estimate is priced like a reported one: %+v", u)
	}
}

func TestAClientThatDropsTheStreamStillPays(t *testing.T) {
	gone := errors.New("client went away")
	calls := 0
	g, err := streamWith(t, "sc-drop", []*models.StreamChunk{{Content: "first part of the answer "}, {Content: "second part"}, {Done: true, Usage: &models.TokenUsage{InputTokens: 9, OutputTokens: 40, TotalTokens: 49}}}, nil,
		func(*models.StreamChunk) error {
			if calls++; calls == 2 {
				return gone // the caller read the first chunk and the connection closed on the second
			}
			return nil
		})
	if !errors.Is(err, gone) {
		t.Fatalf("the callback's error comes back: %v", err)
	}
	if len(g.settled) != 1 || g.cancelled != 0 || g.settled[0].Usage.OutputTokens == 0 {
		t.Fatalf("what was generated before the drop is charged: %+v cancelled=%d", g.settled, g.cancelled)
	}
}

func TestAStreamThatNeverStartedIsReleased(t *testing.T) {
	g, err := streamWith(t, "sc-none", nil, errors.New("connection refused"), nil)
	if err == nil {
		t.Fatal("the provider is down")
	}
	if len(g.settled) != 0 || g.cancelled != 1 {
		t.Fatalf("nothing was generated, so nothing is charged: settled=%d cancelled=%d", len(g.settled), g.cancelled)
	}
}

func TestAStreamThatCompletesWithoutUsageIsChargedAnEstimate(t *testing.T) {
	g, err := streamWith(t, "sc-nousage", []*models.StreamChunk{{Content: strings.Repeat("abcd", 50)}, {Done: true}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.settled) != 1 || g.settled[0].Usage.OutputTokens < 45 {
		t.Fatalf("a provider that sends no usage must not make streaming free: %+v", g.settled)
	}
}

// Usage the provider did report is never replaced by an estimate.
func TestReportedStreamUsageIsKept(t *testing.T) {
	g, err := streamWith(t, "sc-reported", []*models.StreamChunk{{Content: strings.Repeat("x", 4000)}, {Done: true, Usage: &models.TokenUsage{InputTokens: 7, OutputTokens: 3, TotalTokens: 10}}}, nil, nil)
	if err != nil || len(g.settled) != 1 || g.settled[0].Usage.OutputTokens != 3 || g.settled[0].Usage.InputTokens != 7 {
		t.Fatalf("%v %+v", err, g.settled)
	}
}
