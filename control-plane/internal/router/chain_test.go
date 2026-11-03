package router_test

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/pkg/audio"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/agentoven/agentoven/control-plane/pkg/realtime"
)

// chainDriver is a provider that can do everything a provider can do, and counts how often anything reached it.
type chainDriver struct{ hits int32 }

func (d *chainDriver) Kind() string                                             { return "chain" }
func (d *chainDriver) HealthCheck(context.Context, *models.ModelProvider) error { return nil }
func (d *chainDriver) Call(_ context.Context, p *models.ModelProvider, req *models.RouteRequest) (*models.RouteResponse, error) {
	atomic.AddInt32(&d.hits, 1)
	return &models.RouteResponse{Provider: p.Name, Model: req.Model, Content: "card 4111 1111 1111 1111",
		Usage: models.TokenUsage{InputTokens: 3, OutputTokens: 4, TotalTokens: 7}}, nil
}
func (d *chainDriver) StreamCall(_ context.Context, _ *models.ModelProvider, _ *models.RouteRequest, cb func(*models.StreamChunk) error) error {
	atomic.AddInt32(&d.hits, 1)
	if err := cb(&models.StreamChunk{Content: "card 4111 "}); err != nil {
		return err
	}
	return cb(&models.StreamChunk{Content: "1111 1111 1111", Done: true, Usage: &models.TokenUsage{InputTokens: 3, OutputTokens: 4}})
}
func (d *chainDriver) EmbeddingModels() []router.EmbeddingModelInfo {
	return []router.EmbeddingModelInfo{{Model: "emb", Dimensions: 2, MaxBatch: 8}}
}
func (d *chainDriver) Embed(context.Context, *models.ModelProvider, string, []string) ([][]float64, error) {
	atomic.AddInt32(&d.hits, 1)
	return [][]float64{{1, 2}}, nil
}
func (d *chainDriver) SpeechEngine(*models.ModelProvider) (audio.Engine, error) {
	return chainEngine{d}, nil
}
func (d *chainDriver) OpenRealtime(context.Context, *models.ModelProvider, realtime.SessionConfig) (realtime.Upstream, error) {
	atomic.AddInt32(&d.hits, 1)
	return chainUpstream{}, nil
}

type chainEngine struct{ d *chainDriver }

func (e chainEngine) Transcribe(context.Context, []byte, string) (string, error) {
	atomic.AddInt32(&e.d.hits, 1)
	return "hello", nil
}
func (e chainEngine) Speak(context.Context, string, string) ([]byte, string, error) {
	atomic.AddInt32(&e.d.hits, 1)
	return []byte("audio"), "audio/mpeg", nil
}

type chainUpstream struct{}

func (chainUpstream) Send(context.Context, realtime.ClientEvent) error     { return nil }
func (chainUpstream) SendToolResult(context.Context, string, string) error { return nil }
func (chainUpstream) Recv(context.Context) (realtime.UpstreamEvent, error) {
	return realtime.UpstreamEvent{}, realtime.ErrClosed
}
func (chainUpstream) Close() error { return nil }

// chainGuard blocks or redacts what it is told to and counts what it was asked.
type chainGuard struct {
	blockInput, blockOutput bool
	inputs, outputs         int32
}

func (g *chainGuard) Input(_ context.Context, req *models.RouteRequest) (*models.RouteRequest, error) {
	atomic.AddInt32(&g.inputs, 1)
	if g.blockInput {
		return nil, &models.GuardrailBlockedError{Stage: "input"}
	}
	return req, nil
}
func (g *chainGuard) Output(_ context.Context, _ *models.RouteRequest, resp *models.RouteResponse) (*models.RouteResponse, error) {
	atomic.AddInt32(&g.outputs, 1)
	if g.blockOutput {
		return nil, &models.GuardrailBlockedError{Stage: "output"}
	}
	out := *resp
	out.Content = strings.ReplaceAll(resp.Content, "4111 1111 1111 1111", "[CARD]")
	return &out, nil
}
func (g *chainGuard) HoldsOutput(context.Context, *models.RouteRequest) (bool, error) {
	return true, nil
}

// entryPoints is every way a caller of the router can make the provider do something that costs money or
// sends data out. Each is run against a gate that refuses: the provider must not be reached and the refusal
// must come back as one.
func entryPoints(t *testing.T, mr *router.ModelRouter, p *models.ModelProvider) map[string]func(ctx context.Context) error {
	t.Helper()
	req := func() *models.RouteRequest {
		return &models.RouteRequest{Kitchen: "default", Model: "m", Messages: []models.ChatMessage{{Role: "user", Content: "hi"}}}
	}
	return map[string]func(context.Context) error{
		"Route": func(ctx context.Context) error { _, err := mr.Route(ctx, req()); return err },
		"RouteWithBackup": func(ctx context.Context) error {
			_, err := mr.RouteWithBackup(ctx, req(), "", "")
			return err
		},
		"RouteStream": func(ctx context.Context) error {
			return mr.RouteStream(ctx, req(), func(*models.StreamChunk) error { return nil })
		},
		"RouteVia": func(ctx context.Context) error {
			_, err := mr.RouteVia(ctx, p, req())
			return err
		},
		"SpeechFor.Transcribe": func(ctx context.Context) error {
			e, err := mr.SpeechFor(p).SpeechEngine(p)
			if err != nil {
				return err
			}
			_, err = e.Transcribe(ctx, []byte("wav"), "audio/wav")
			return err
		},
		"SpeechFor.Speak": func(ctx context.Context) error {
			e, err := mr.SpeechFor(p).SpeechEngine(p)
			if err != nil {
				return err
			}
			_, _, err = e.Speak(ctx, "hello", "")
			return err
		},
		"RealtimeFor.OpenRealtime": func(ctx context.Context) error {
			up, err := mr.RealtimeFor(p).OpenRealtime(ctx, p, realtime.SessionConfig{Model: "rt"})
			if err == nil {
				err = up.Close() // a session is settled when it ends
			}
			return err
		},
		"ListEmbeddingCapableDrivers.Embed": func(ctx context.Context) error {
			_, err := mr.ListEmbeddingCapableDrivers()["chain"].Embed(ctx, p, "emb", []string{"x"})
			return err
		},
		"DiscoverEmbeddingsForProvider.Embed": func(ctx context.Context) error {
			d, _ := mr.DiscoverEmbeddingsForProvider(p)
			_, err := d.Embed(ctx, p, "emb", []string{"x"})
			return err
		},
	}
}

func TestEveryProviderCallPassesTheSpendGate(t *testing.T) {
	d := &chainDriver{}
	mr, ctx := routerWithProvider(t, "p1", d)
	gate := &reservingGate{refuse: true}
	mr.SetSpendGate(gate)
	p := &models.ModelProvider{Name: "p1", Kitchen: "default", Kind: "chain", Models: []string{"m"}, Config: map[string]interface{}{"api_key": "k"}}
	for name, call := range entryPoints(t, mr, p) {
		err := call(ctx)
		if !errors.Is(err, models.ErrSpendRefused) {
			t.Errorf("%s with a budget that is used up: %v, want a spend refusal", name, err)
		}
	}
	if hits := atomic.LoadInt32(&d.hits); hits != 0 {
		t.Errorf("the provider was reached %d times through entry points the gate refused", hits)
	}

	// Admitted calls reserve and settle exactly once each; one that fails gives its reservation back.
	gate = &reservingGate{}
	mr.SetSpendGate(gate)
	n := 0
	for name, call := range entryPoints(t, mr, p) {
		if err := call(ctx); err != nil {
			t.Errorf("%s with a budget with room: %v", name, err)
		}
		n++
	}
	if gate.reserved != n || len(gate.settled) != n || gate.cancelled != 0 {
		t.Errorf("%d admitted calls: reserved %d, settled %d, cancelled %d", n, gate.reserved, len(gate.settled), gate.cancelled)
	}

	// A refused provider test says so, and sends nothing.
	sent := int32(0)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { atomic.AddInt32(&sent, 1) }))
	defer srv.Close()
	oai := &models.ModelProvider{Name: "o", Kitchen: "default", Kind: "openai", Endpoint: srv.URL, Models: []string{"gpt-4o"}, Config: map[string]interface{}{"api_key": "k"}}
	mr.SetSpendGate(&reservingGate{refuse: true})
	if res := mr.TestProvider(ctx, oai); !errors.Is(res.Refused, models.ErrSpendRefused) || res.Healthy || atomic.LoadInt32(&sent) != 0 {
		t.Errorf("a provider test over a used-up budget: %+v, requests sent %d", res, sent)
	}
}

func TestEveryModelCallIsJudgedByTheGuard(t *testing.T) {
	d := &chainDriver{}
	mr, ctx := routerWithProvider(t, "p1", d)
	req := func() *models.RouteRequest {
		return &models.RouteRequest{Kitchen: "default", Model: "test-model", Messages: []models.ChatMessage{{Role: "user", Content: "hi"}}}
	}
	call := map[string]func(ctx context.Context) (string, error){
		"Route": func(ctx context.Context) (string, error) {
			r, err := mr.Route(ctx, req())
			if err != nil {
				return "", err
			}
			return r.Content, nil
		},
		"RouteVia": func(ctx context.Context) (string, error) {
			p := &models.ModelProvider{Name: "adhoc", Kitchen: "default", Kind: "chain", Models: []string{"m"}, Config: map[string]interface{}{"api_key": "k"}}
			r, err := mr.RouteVia(ctx, p, req())
			if err != nil {
				return "", err
			}
			return r.Content, nil
		},
		"RouteWithBackup": func(ctx context.Context) (string, error) {
			r, err := mr.RouteWithBackup(ctx, req(), "", "")
			if err != nil {
				return "", err
			}
			return r.Content, nil
		},
		"RouteStream": func(ctx context.Context) (string, error) {
			var out strings.Builder
			err := mr.RouteStream(ctx, req(), func(c *models.StreamChunk) error { out.WriteString(c.Content); return nil })
			return out.String(), err
		},
	}
	for name, f := range call {
		// The prompt is refused before any provider is reached.
		g := &chainGuard{blockInput: true}
		mr.SetContentGuard(g)
		before := atomic.LoadInt32(&d.hits)
		if _, err := f(ctx); !errors.Is(err, models.ErrGuardrailBlocked) || atomic.LoadInt32(&d.hits) != before {
			t.Errorf("%s: a blocked prompt must be refused before the provider: %v, provider hits %d", name, err, atomic.LoadInt32(&d.hits)-before)
		}
		// The reply is refused after the provider answered, and nothing of it is returned.
		mr.SetContentGuard(&chainGuard{blockOutput: true})
		if out, err := f(ctx); !errors.Is(err, models.ErrGuardrailBlocked) || out != "" {
			t.Errorf("%s: a blocked reply must come back as a refusal with no text: %q, %v", name, out, err)
		}
		// A redacted reply is the redacted text, whole, for a stream as well.
		mr.SetContentGuard(&chainGuard{})
		if out, err := f(ctx); err != nil || strings.Contains(out, "4111") || !strings.Contains(out, "[CARD]") {
			t.Errorf("%s: the reply handed on must be the redacted one: %q, %v", name, out, err)
		}
		// A caller that has judged the exchange itself is not judged twice.
		g = &chainGuard{blockInput: true}
		mr.SetContentGuard(g)
		if _, err := f(router.ContextGuardrailsApplied(ctx)); err != nil || atomic.LoadInt32(&g.inputs) != 0 {
			t.Errorf("%s: a call marked as already judged must not meet the guard again: %v", name, err)
		}
	}
	mr.SetContentGuard(nil)
}

// A held stream is released only after the whole reply was judged, so a blocked reply sends nothing at all.
func TestAHeldStreamReleasesNothingWhenTheReplyIsRefused(t *testing.T) {
	d := &chainDriver{}
	mr, ctx := routerWithProvider(t, "p1", d)
	mr.SetContentGuard(&chainGuard{blockOutput: true})
	chunks := 0
	err := mr.RouteStream(ctx, &models.RouteRequest{Kitchen: "default", Model: "test-model", Messages: []models.ChatMessage{{Role: "user", Content: "hi"}}},
		func(*models.StreamChunk) error { chunks++; return nil })
	if !errors.Is(err, models.ErrGuardrailBlocked) || chunks != 0 {
		t.Fatalf("a refused stream sent %d chunks (%v)", chunks, err)
	}
}

// classified says, for every exported method of the router, why it is or is not a way to make a provider do
// something. A method added later has to be classified here, which is the moment someone decides whether it
// needs the gate: TestEveryProviderCallPassesTheSpendGate runs the "entry" ones.
var classified = map[string]string{
	"Route": "entry", "RouteVia": "entry", "RouteWithBackup": "entry", "RouteStream": "entry", "TestProvider": "entry",
	"SpeechFor": "entry", "RealtimeFor": "entry", "ListEmbeddingCapableDrivers": "entry", "DiscoverEmbeddingsForProvider": "entry",
	"Admit": "the gate itself, for calls that are not completions",
	// Reading what a provider offers is not a model call: no tokens, no prompt, no reply.
	"DiscoverModelsForProvider": "lists the provider's models", "ListDiscoveryCapableDrivers": "lists drivers that can list models",
	"HealthCheck": "probes each provider's liveness endpoint", "ListDrivers": "names", "GetDriver": "see TestNoOneReachesADriverAroundTheRouter",
	"FreshProvider": "reads the store", "ProviderKey": "reads the store", "SelectAPIKey": "picks a key", "PriceFor": "reads prices", "Settle": "prices usage",
	"AcceptsAudio": "capability", "Modalities": "capability", "Supports": "capability", "CheckModalities": "capability", "WebSearchOn": "capability",
	"Blobs": "accessor", "CallCapturer": "accessor", "GetCostSummary": "reads", "ExternalizeMedia": "stores media",
	"InvalidateProviderClient": "cache", "RegisterDriver": "setup", "SetBlobStore": "setup", "SetCallCapturer": "setup", "SetCatalog": "setup",
	"SetContentGuard": "setup", "SetResponseCache": "setup", "SetSecretResolver": "setup", "SetSpendGate": "setup",
}

func TestEveryExportedRouterMethodIsClassified(t *testing.T) {
	typ := reflect.TypeOf(&router.ModelRouter{})
	var unclassified []string
	for i := 0; i < typ.NumMethod(); i++ {
		if _, ok := classified[typ.Method(i).Name]; !ok {
			unclassified = append(unclassified, typ.Method(i).Name)
		}
	}
	if len(unclassified) > 0 {
		t.Fatalf("new exported ModelRouter methods must be classified in chain_test.go (does the new method make a provider do something? then it passes the spend gate and is added to entryPoints): %v", unclassified)
	}
	for name := range classified {
		if _, ok := typ.MethodByName(name); !ok {
			t.Errorf("classified method %s no longer exists", name)
		}
	}
}

// TestNoOneReachesADriverAroundTheRouter: the raw driver (GetDriver) can be called without the gate, the
// guard or the cache, so only the router's own package and the docs generator, which asks a driver a
// question about itself, may hold one. Everyone else gets drivers' capabilities through the router's
// methods, which meter them.
func TestNoOneReachesADriverAroundTheRouter(t *testing.T) {
	root := filepath.Join("..", "..")
	var offenders []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		slash := filepath.ToSlash(path)
		if strings.Contains(slash, "internal/router/") || strings.Contains(slash, "cmd/docgen/") || strings.Contains(slash, "/node_modules/") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if perr != nil {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if s, ok := c.Fun.(*ast.SelectorExpr); ok && s.Sel.Name == "GetDriver" {
					if x, ok := s.X.(*ast.SelectorExpr); ok && strings.Contains(strings.ToLower(x.Sel.Name), "rout") {
						offenders = append(offenders, slash)
					} else if id, ok := s.X.(*ast.Ident); ok && strings.Contains(strings.ToLower(id.Name), "rout") {
						offenders = append(offenders, slash)
					}
				}
			}
			return true
		})
		return nil
	})
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Fatalf("these files take a raw driver from the router and so skip the gate: %v", offenders)
	}
}
