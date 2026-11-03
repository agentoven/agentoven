package router_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// countingDriver answers every call and counts them (usageDriver does not count when it always answers).
type countingDriver struct {
	usageDriver
	calls int32
}

func (d *countingDriver) Call(ctx context.Context, p *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, error) {
	atomic.AddInt32(&d.calls, 1)
	return d.usageDriver.Call(ctx, p, req)
}

// refusingGate refuses with the error it is given, for the providers it is told to, and otherwise admits.
type refusingGate struct {
	mu     sync.Mutex
	err    func(provider string) error
	asked  []string
	settle int32
}

func (g *refusingGate) Allow(context.Context, models.SpendQuery) error { return nil }
func (g *refusingGate) Record(context.Context, models.SpendRecord)     {}
func (g *refusingGate) Reserve(_ context.Context, q models.SpendQuery) (router.SpendReservation, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.asked = append(g.asked, q.Provider)
	if g.err != nil {
		if err := g.err(q.Provider); err != nil {
			return nil, err
		}
	}
	return okReservation{g}, nil
}

type okReservation struct{ g *refusingGate }

func (r okReservation) Settle(context.Context, models.SpendRecord) { atomic.AddInt32(&r.g.settle, 1) }
func (r okReservation) Cancel(context.Context)                     {}

// twoProviders is a kitchen with p1 (the default) and p2, each with its own driver.
func twoProviders(t *testing.T) (*router.ModelRouter, *countingDriver, *countingDriver, context.Context, store.Store) {
	t.Helper()
	os.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	t.Cleanup(func() { s.Close() })
	d1 := &countingDriver{usageDriver: usageDriver{flakyDriver: flakyDriver{kind: "tc-1", minCallsOK: true}}}
	d2 := &countingDriver{usageDriver: usageDriver{flakyDriver: flakyDriver{kind: "tc-2", minCallsOK: true}}}
	mr := router.NewModelRouter(s)
	mr.RegisterDriver(d1)
	mr.RegisterDriver(d2)
	ctx := context.Background()
	for i, d := range []*countingDriver{d1, d2} {
		if err := s.CreateProvider(ctx, &models.ModelProvider{Name: []string{"p1", "p2"}[i], Kitchen: "default", Kind: d.Kind(),
			Models: []string{"test-model"}, IsDefault: i == 0, Config: map[string]interface{}{"api_key": "test"}}); err != nil {
			t.Fatal(err)
		}
	}
	return mr, d1, d2, ctx, s
}

func TestARefusalNeverCountsAgainstTheProvidersCircuit(t *testing.T) {
	for name, refusal := range map[string]error{
		"budget":     &models.SpendRefusedError{Scope: "kitchen", Reason: "used up"},
		"rate limit": &models.RateLimitedError{Scope: "kitchen", Limit: "requests_per_minute", RetryAfter: time.Second, Reason: "slow down"},
	} {
		d := &usageDriver{flakyDriver: flakyDriver{kind: "tc-circuit-" + name, minCallsOK: true}}
		mr, ctx := routerWithProvider(t, "p1", d)
		g := &refusingGate{err: func(string) error { return refusal }}
		mr.SetSpendGate(g)
		// Far more refusals than the circuit's threshold of consecutive failures.
		for i := 0; i < 12; i++ {
			if _, err := mr.Route(ctx, &models.RouteRequest{Kitchen: "default", Model: "test-model"}); !errors.Is(err, refusal) && !isKind(err, refusal) {
				t.Fatalf("%s: a refusal stays recognisable: %v", name, err)
			}
		}
		g.mu.Lock()
		g.err = nil // the budget is raised, or the minute passes
		g.mu.Unlock()
		if _, err := mr.Route(ctx, &models.RouteRequest{Kitchen: "default", Model: "test-model"}); err != nil {
			t.Fatalf("%s: a healthy provider that was only refused is not out of rotation: %v", name, err)
		}
	}
}

func isKind(err, want error) bool {
	switch want.(type) {
	case *models.SpendRefusedError:
		return errors.Is(err, models.ErrSpendRefused)
	case *models.RateLimitedError:
		return errors.Is(err, models.ErrRateLimited)
	}
	return false
}

func TestAProviderScopedRefusalFallsBackAndAWiderOneDoesNot(t *testing.T) {
	mr, d1, d2, ctx, _ := twoProviders(t)
	g := &refusingGate{err: func(p string) error {
		if p == "p1" {
			return &models.RateLimitedError{Scope: "provider", Limit: "requests_per_minute", RetryAfter: time.Second, Reason: "p1 is busy"}
		}
		return nil
	}}
	mr.SetSpendGate(g)
	resp, err := mr.Route(ctx, &models.RouteRequest{Kitchen: "default", Model: "test-model"})
	if err != nil || resp.Provider != "p2" {
		t.Fatalf("a limit on one provider is exactly what fallback is for: %v %+v", err, resp)
	}
	if d1.calls != 0 || d2.calls != 1 {
		t.Fatalf("p1 was refused before the call: %d %d", d1.calls, d2.calls)
	}

	// The kitchen's own limit would be met again at p2: stop at once.
	g.mu.Lock()
	g.asked = nil
	g.err = func(string) error {
		return &models.SpendRefusedError{Scope: "kitchen", Reason: "used up"}
	}
	g.mu.Unlock()
	_, err = mr.Route(ctx, &models.RouteRequest{Kitchen: "default", Model: "test-model"})
	if !errors.Is(err, models.ErrSpendRefused) {
		t.Fatalf("%v", err)
	}
	if len(g.asked) != 1 {
		t.Fatalf("the kitchen's budget is not asked again for every other provider: %v", g.asked)
	}
	// Streaming behaves the same.
	g.mu.Lock()
	g.asked = nil
	g.mu.Unlock()
	if err := mr.RouteStream(ctx, &models.RouteRequest{Kitchen: "default", Model: "test-model"}, func(*models.StreamChunk) error { return nil }); !errors.Is(err, models.ErrSpendRefused) {
		t.Fatalf("%v", err)
	}
	if len(g.asked) != 1 {
		t.Fatalf("streaming stops too: %v", g.asked)
	}
}

// fakeCache is a ResponseCache that answers one request and records what it is offered.
type fakeCache struct {
	mu      sync.Mutex
	entries map[string]*models.RouteResponse
	lookups int
	stores  int
}

func (c *fakeCache) key(p *models.ModelProvider, r *models.RouteRequest) string {
	return p.Name + "|" + r.Model + "|" + r.Messages[len(r.Messages)-1].Content
}

func (c *fakeCache) Lookup(_ context.Context, p *models.ModelProvider, r *models.RouteRequest) (*models.RouteResponse, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lookups++
	if e, ok := c.entries[c.key(p, r)]; ok {
		cp := *e
		return &cp, true
	}
	return nil, false
}

func (c *fakeCache) Store(_ context.Context, p *models.ModelProvider, r *models.RouteRequest, resp *models.RouteResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stores++
	cp := *resp
	if c.entries == nil {
		c.entries = map[string]*models.RouteResponse{}
	}
	c.entries[c.key(p, r)] = &cp
}

func TestACacheHitSkipsTheGateAndTheProviderAndCostsNothing(t *testing.T) {
	d := &countingDriver{usageDriver: usageDriver{flakyDriver: flakyDriver{kind: "tc-cache", minCallsOK: true}}}
	mr, ctx := routerWithProvider(t, "p1", d)
	g := &reservingGate{}
	mr.SetSpendGate(g)
	c := &fakeCache{}
	mr.SetResponseCache(c)
	req := func() *models.RouteRequest {
		return &models.RouteRequest{Kitchen: "default", Model: "test-model", Messages: []models.ChatMessage{{Role: "user", Content: "hi"}}}
	}

	first, err := mr.Route(ctx, req())
	if err != nil || first.Cached || first.Usage.TotalTokens != 150 {
		t.Fatalf("the first call goes to the provider: %v %+v", err, first)
	}
	if c.stores != 1 {
		t.Fatalf("and its answer is offered to the cache: %d", c.stores)
	}
	second, err := mr.Route(ctx, req())
	if err != nil || !second.Cached || second.Content != "ok" {
		t.Fatalf("%v %+v", err, second)
	}
	if second.Usage.EstimatedCost != 0 || second.Usage.TotalTokens != 0 || second.Usage.InputTokens != 0 {
		t.Fatalf("a hit is free: %+v", second.Usage)
	}
	if d.calls != 1 || g.reserved != 1 || len(g.settled) != 1 {
		t.Fatalf("a hit reaches neither the provider nor the budget: calls=%d reserved=%d", d.calls, g.reserved)
	}
	if c.stores != 1 {
		t.Fatal("a hit is not stored again")
	}
	if cs := mr.GetCostSummary("default"); cs.TotalTokens != 150 {
		t.Fatalf("the cost summary counts the one real call: %+v", cs)
	}

	// The trace tells the two apart.
	mr.SetResponseCache(nil)
	if _, err := mr.Route(ctx, req()); err != nil || d.calls != 2 {
		t.Fatalf("with the cache removed it goes to the provider again: %v %d", err, d.calls)
	}
}

func TestATraceShowsACacheHit(t *testing.T) {
	d := &usageDriver{flakyDriver: flakyDriver{kind: "tc-trace", minCallsOK: true}}
	os.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	defer s.Close()
	mr := router.NewModelRouter(s)
	mr.RegisterDriver(d)
	ctx := context.Background()
	if err := s.CreateProvider(ctx, &models.ModelProvider{Name: "p1", Kitchen: "default", Kind: d.Kind(), Models: []string{"test-model"}, IsDefault: true,
		Config: map[string]interface{}{"api_key": "test"}}); err != nil {
		t.Fatal(err)
	}
	mr.SetResponseCache(&fakeCache{})
	req := func() *models.RouteRequest {
		return &models.RouteRequest{Kitchen: "default", Model: "test-model", Messages: []models.ChatMessage{{Role: "user", Content: "hi"}}}
	}
	for i := 0; i < 2; i++ {
		if _, err := mr.Route(ctx, req()); err != nil {
			t.Fatal(err)
		}
	}
	trs, err := s.ListTraces(ctx, "default", 10)
	if err != nil || len(trs) != 2 {
		t.Fatalf("%v %d", err, len(trs))
	}
	hits := 0
	for _, tr := range trs {
		spans, _ := s.ListSpansByTrace(ctx, tr.ID)
		for _, sp := range spans {
			if sp.Kind == models.SpanKindLLM && sp.Metadata["cache_hit"] == true {
				hits++
				if tr.CostUSD != 0 || sp.Usage.TotalTokens != 0 || tr.Metadata["cache_hit"] != true {
					t.Fatalf("a hit's trace costs nothing: %+v %+v", tr, sp)
				}
			}
		}
	}
	if hits != 1 {
		t.Fatalf("exactly one of the two traces is a cache hit: %d", hits)
	}
}

func TestAStreamedCallIsNeitherLookedUpNorStored(t *testing.T) {
	d := &streamingDriver{usageDriver{flakyDriver: flakyDriver{kind: "tc-stream", minCallsOK: true}}}
	mr, ctx := routerWithProvider(t, "p1", d)
	c := &fakeCache{}
	mr.SetResponseCache(c)
	if err := mr.RouteStream(ctx, &models.RouteRequest{Kitchen: "default", Model: "test-model", Messages: []models.ChatMessage{{Role: "user", Content: "hi"}}},
		func(*models.StreamChunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if c.lookups != 0 || c.stores != 0 {
		t.Fatalf("streams bypass the cache: %+v", c)
	}
}
