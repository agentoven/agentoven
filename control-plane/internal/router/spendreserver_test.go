package router_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// reservingGate is a SpendGate that also reserves; it records what the router did with each call.
type reservingGate struct {
	mu        sync.Mutex
	refuse    bool
	queries   []models.SpendQuery
	reserved  int
	settled   []models.SpendRecord
	cancelled int
	allowed   int32 // Allow must not be used when Reserve is
	recorded  int32 // Record must not be used when Settle is
}

func (g *reservingGate) Allow(context.Context, models.SpendQuery) error {
	atomic.AddInt32(&g.allowed, 1)
	return nil
}
func (g *reservingGate) Record(context.Context, models.SpendRecord) { atomic.AddInt32(&g.recorded, 1) }
func (g *reservingGate) Reserve(_ context.Context, q models.SpendQuery) (router.SpendReservation, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.queries = append(g.queries, q)
	if g.refuse {
		return nil, &models.SpendRefusedError{Scope: "kitchen", Reason: "used up", Spent: 10, Cap: 10}
	}
	g.reserved++
	return &reservation{g: g}, nil
}

type reservation struct{ g *reservingGate }

func (r *reservation) Settle(_ context.Context, rec models.SpendRecord) {
	r.g.mu.Lock()
	r.g.settled = append(r.g.settled, rec)
	r.g.mu.Unlock()
}
func (r *reservation) Cancel(context.Context) {
	r.g.mu.Lock()
	r.g.cancelled++
	r.g.mu.Unlock()
}

type usageDriver struct {
	flakyDriver
	stream bool
}

func (d *usageDriver) Call(ctx context.Context, p *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, error) {
	if d.minCallsOK || atomic.AddInt32(&d.callCount, 1) > d.failUntil {
		return &models.RouteResponse{Provider: p.Name, Model: req.Model, Content: "ok", Usage: models.TokenUsage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150}}, nil
	}
	return nil, d.failWith
}

type streamingDriver struct{ usageDriver }

func (d *streamingDriver) StreamCall(_ context.Context, _ *models.ModelProvider, _ *models.RouteRequest, cb func(*models.StreamChunk) error) error {
	if err := cb(&models.StreamChunk{Content: "he"}); err != nil {
		return err
	}
	return cb(&models.StreamChunk{Content: "llo", Done: true, Usage: &models.TokenUsage{InputTokens: 7, OutputTokens: 3, TotalTokens: 10}})
}

func TestAReservingGateIsAskedToReserveAndTheCallIsSettledWithItsUsage(t *testing.T) {
	d := &usageDriver{flakyDriver: flakyDriver{kind: "usage-a", minCallsOK: true}}
	mr, ctx := routerWithProvider(t, "p1", d)
	g := &reservingGate{}
	mr.SetSpendGate(g)
	max := 77
	_, err := mr.Route(ctx, &models.RouteRequest{Kitchen: "default", Model: "test-model", MaxTokens: &max,
		Messages: []models.ChatMessage{{Role: "user", Content: "0123456789012345678901234567890123456789"}}})
	if err != nil {
		t.Fatal(err)
	}
	if g.reserved != 1 || len(g.settled) != 1 || g.cancelled != 0 {
		t.Fatalf("one reservation, settled once, never cancelled: %+v", g)
	}
	if g.allowed != 0 || g.recorded != 0 {
		t.Fatal("a gate that reserves is not also asked Allow and Record for the same call")
	}
	q := g.queries[0]
	if q.MaxOutputTokens != 77 || q.EstimatedInputTokens < 10 || q.Kitchen != "default" || q.Provider != "p1" {
		t.Fatalf("the reservation is sized from the call: %+v", q)
	}
	if g.settled[0].Usage.TotalTokens != 150 {
		t.Fatalf("settled with the call's usage: %+v", g.settled[0])
	}
}

func TestAReservationIsCancelledWhenTheCallFails(t *testing.T) {
	d := &usageDriver{flakyDriver: flakyDriver{kind: "usage-b", failUntil: 1000, failWith: errors.New("status 401: nope")}}
	mr, ctx := routerWithProvider(t, "p1", d)
	g := &reservingGate{}
	mr.SetSpendGate(g)
	if _, err := mr.Route(ctx, &models.RouteRequest{Kitchen: "default", Model: "test-model"}); err == nil {
		t.Fatal("the call fails")
	}
	if g.reserved != 1 || g.cancelled != 1 || len(g.settled) != 0 {
		t.Fatalf("a failed call releases what it held: reserved=%d cancelled=%d settled=%d", g.reserved, g.cancelled, len(g.settled))
	}
}

func TestARefusedReservationNeverReachesTheProvider(t *testing.T) {
	d := &usageDriver{flakyDriver: flakyDriver{kind: "usage-c", minCallsOK: true}}
	mr, ctx := routerWithProvider(t, "p1", d)
	g := &reservingGate{refuse: true}
	mr.SetSpendGate(g)
	_, err := mr.Route(ctx, &models.RouteRequest{Kitchen: "default", Model: "test-model"})
	var refused *models.SpendRefusedError
	if !errors.As(err, &refused) || refused.Scope != "kitchen" {
		t.Fatalf("a refusal stays recognisable as a budget decision: %v", err)
	}
	if atomic.LoadInt32(&d.callCount) != 0 {
		t.Fatal("a refused call must not be sent")
	}
}

func TestAStreamedCallIsGatedAndChargedWithItsFinalUsage(t *testing.T) {
	d := &streamingDriver{usageDriver{flakyDriver: flakyDriver{kind: "usage-d", minCallsOK: true}}}
	mr, ctx := routerWithProvider(t, "p1", d)
	g := &reservingGate{}
	mr.SetSpendGate(g)
	var got string
	err := mr.RouteStream(ctx, &models.RouteRequest{Kitchen: "default", Model: "test-model"}, func(c *models.StreamChunk) error {
		got += c.Content
		return nil
	})
	if err != nil || got != "hello" {
		t.Fatalf("stream: %q %v", got, err)
	}
	if g.reserved != 1 || len(g.settled) != 1 || g.settled[0].Usage.TotalTokens != 10 {
		t.Fatalf("a streamed call is reserved and settled with the usage on its last chunk: %+v", g.settled)
	}

	g.mu.Lock()
	g.refuse = true
	g.mu.Unlock()
	got = ""
	err = mr.RouteStream(ctx, &models.RouteRequest{Kitchen: "default", Model: "test-model"}, func(c *models.StreamChunk) error { got += c.Content; return nil })
	if !errors.Is(err, models.ErrSpendRefused) || got != "" {
		t.Fatalf("a refused stream sends nothing: %q %v", got, err)
	}
}
