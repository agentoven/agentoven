package router

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/audio"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/agentoven/agentoven/control-plane/pkg/realtime"
	"github.com/coder/websocket"
)

func voiceRouter(t *testing.T) *ModelRouter {
	t.Helper()
	os.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	t.Cleanup(func() { s.Close() })
	return NewModelRouter(s)
}

// wsSeen is what a fake provider socket observed on the upgrade request and
// its first client frame.
type wsSeen struct {
	mu    sync.Mutex
	path  string
	query string
	auth  string
	first map[string]interface{}
}

func (w *wsSeen) get() (path, query, auth string, first map[string]interface{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.path, w.query, w.auth, w.first
}

// fakeSocket accepts one WebSocket, records the upgrade and the first frame,
// and (for Gemini) confirms setup. tls selects an https server.
func fakeSocket(t *testing.T, geminiSetup, tls bool) (*httptest.Server, *wsSeen) {
	t.Helper()
	seen := &wsSeen{}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.mu.Lock()
		seen.path, seen.query, seen.auth = r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		seen.mu.Unlock()
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		_, raw, err := c.Read(context.Background())
		if err != nil {
			return
		}
		seen.mu.Lock()
		_ = json.Unmarshal(raw, &seen.first)
		seen.mu.Unlock()
		if geminiSetup {
			_ = c.Write(context.Background(), websocket.MessageText, []byte(`{"setupComplete":{}}`))
		}
		_, _, _ = c.Read(context.Background()) // hold the socket until the client closes
	})
	var srv *httptest.Server
	if tls {
		srv = httptest.NewTLSServer(h)
	} else {
		srv = httptest.NewServer(h)
	}
	t.Cleanup(srv.Close)
	return srv, seen
}

func provider(kind, endpoint string, cfg map[string]interface{}) *models.ModelProvider {
	return &models.ModelProvider{Name: kind + "-p", Kind: kind, Endpoint: endpoint, Config: cfg}
}

func TestCapabilitiesAreDiscoveredFromTheProvidersDriver(t *testing.T) {
	mr := voiceRouter(t)
	for kind, want := range map[string]bool{"openai": true, "gemini": true, "litellm": true,
		"anthropic": false, "openrouter": false, "ollama": false, "azure-openai": false, "nope": false} {
		if got := mr.RealtimeFor(provider(kind, "", nil)) != nil; got != want {
			t.Errorf("RealtimeFor(%q) capable=%v, want %v", kind, got, want)
		}
	}
	for kind, want := range map[string]bool{"openai": true, "openrouter": true, "litellm": true,
		"anthropic": false, "gemini": false, "ollama": false, "nope": false} {
		if got := mr.SpeechFor(provider(kind, "", nil)) != nil; got != want {
			t.Errorf("SpeechFor(%q) capable=%v, want %v", kind, got, want)
		}
	}
}

func modalities(m map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{"api_key": "k", "modalities": m}
}

func TestAProviderCanSwitchAModalityOffButNotOnBeyondItsDriver(t *testing.T) {
	mr := voiceRouter(t)
	off := map[string]interface{}{"enabled": false}

	p := provider("openai", "", modalities(map[string]interface{}{"realtime": off, "audio": off}))
	if mr.RealtimeFor(p) != nil || mr.SpeechFor(p) != nil {
		t.Fatal("an OpenAI provider with realtime and audio switched off must offer neither")
	}
	if got := strings.Join(mr.Modalities(p, "gpt-4o"), ","); got != "text,image,pdf" {
		t.Fatalf("modalities = %q, want text,image,pdf", got)
	}

	on := map[string]interface{}{"enabled": true}
	anth := provider("anthropic", "", modalities(map[string]interface{}{"realtime": on}))
	if mr.RealtimeFor(anth) != nil {
		t.Fatal("enabling realtime cannot give Anthropic a realtime driver")
	}
	if err := mr.CheckModalities(anth); err == nil || !strings.Contains(err.Error(), "no realtime API") {
		t.Fatalf("enabling realtime on Anthropic must be refused when the provider is saved, got %v", err)
	}
	if err := mr.CheckModalities(provider("anthropic", "", modalities(map[string]interface{}{"realtime": off}))); err != nil {
		t.Fatalf("switching an unsupported modality off is harmless: %v", err)
	}
}

func TestAProviderCanWidenMediaForAGatewayTheCatalogDoesNotKnow(t *testing.T) {
	mr := voiceRouter(t)
	plain := provider("litellm", "http://gw/v1", map[string]interface{}{"api_key": "k"})
	if mr.Supports(plain, "my-vision-alias", "image") {
		t.Fatal("an unknown LiteLLM alias is conservatively text-only")
	}
	widened := provider("litellm", "http://gw/v1", modalities(map[string]interface{}{"image": map[string]interface{}{"enabled": true}}))
	if !mr.Supports(widened, "my-vision-alias", "image") {
		t.Fatal("image: enabled must switch image input on for the gateway")
	}
	narrowed := provider("openai", "", modalities(map[string]interface{}{"image": map[string]interface{}{"enabled": false}}))
	if mr.Supports(narrowed, "gpt-4o", "image") {
		t.Fatal("image: enabled=false must switch image input off even for a vision model")
	}
}

func TestModalitiesConfigIsValidated(t *testing.T) {
	mr := voiceRouter(t)
	for name, tc := range map[string]struct {
		m    map[string]interface{}
		want string
	}{
		"unknown modality":  {map[string]interface{}{"smell": map[string]interface{}{}}, "unknown modality"},
		"text has no entry": {map[string]interface{}{"text": map[string]interface{}{}}, "always on"},
		"unknown setting":   {map[string]interface{}{"audio": map[string]interface{}{"stt": "x"}}, "unknown setting"},
		"enabled not bool":  {map[string]interface{}{"image": map[string]interface{}{"enabled": "yes"}}, "true or false"},
		"model not string":  {map[string]interface{}{"realtime": map[string]interface{}{"model": 3}}, "must be a string"},
	} {
		err := mr.CheckModalities(provider("openai", "", modalities(tc.m)))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want error containing %q, got %v", name, tc.want, err)
		}
	}
	good := provider("openai", "", modalities(map[string]interface{}{
		"audio": map[string]interface{}{"stt_model": "whisper-1"}, "realtime": map[string]interface{}{"model": "gpt-realtime"}}))
	if err := mr.CheckModalities(good); err != nil {
		t.Fatalf("a valid config must pass: %v", err)
	}
}

func TestOpenAIRealtimeUsesTheProvidersEndpointKeyAndDefaultModel(t *testing.T) {
	mr := voiceRouter(t)
	srv, seen := fakeSocket(t, false, false)
	p := provider("openai", srv.URL+"/v1", map[string]interface{}{"api_key": "sk-1"})

	up, err := mr.RealtimeFor(p).OpenRealtime(context.Background(), p, realtime.SessionConfig{Instructions: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()

	waitFirst(t, seen)
	path, query, auth, _ := seen.get()
	if path != "/v1/realtime" || query != "model=gpt-realtime" || auth != "Bearer sk-1" {
		t.Fatalf("path=%q query=%q auth=%q", path, query, auth)
	}
}

func waitFirst(t *testing.T, seen *wsSeen) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if _, _, _, f := seen.get(); f != nil {
			return
		}
		sleepMs(10)
	}
	t.Fatal("the provider socket never received a frame")
}

func TestRealtimeModelPrecedenceIsExplicitThenProviderConfigThenDefault(t *testing.T) {
	mr := voiceRouter(t)
	for _, tc := range []struct {
		name, explicit string
		cfg            map[string]interface{}
		want           string
	}{
		{"default", "", map[string]interface{}{"api_key": "k"}, "gpt-realtime"},
		{"provider config", "", modalities(map[string]interface{}{"realtime": map[string]interface{}{"model": "gpt-realtime-mini"}}), "gpt-realtime-mini"},
		{"explicit wins", "gpt-x", modalities(map[string]interface{}{"realtime": map[string]interface{}{"model": "gpt-realtime-mini"}}), "gpt-x"},
	} {
		srv, seen := fakeSocket(t, false, false)
		op := provider("openai", srv.URL+"/v1", tc.cfg)
		up, err := mr.RealtimeFor(op).OpenRealtime(context.Background(), op, realtime.SessionConfig{Model: tc.explicit})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		waitFirst(t, seen)
		_, query, _, _ := seen.get()
		up.Close()
		if query != "model="+tc.want {
			t.Errorf("%s: query %q, want model=%s", tc.name, query, tc.want)
		}
	}
}

func TestLiteLLMRealtimeNeedsAnEndpointAndAModelAlias(t *testing.T) {
	mr := voiceRouter(t)

	noEndpoint := provider("litellm", "", nil)
	if _, err := mr.RealtimeFor(noEndpoint).OpenRealtime(context.Background(), noEndpoint, realtime.SessionConfig{Model: "x"}); err == nil || !strings.Contains(err.Error(), "no endpoint") {
		t.Fatalf("a LiteLLM provider with no endpoint must be refused, got %v", err)
	}

	srv, seen := fakeSocket(t, false, false)
	p := provider("litellm", srv.URL+"/v1", map[string]interface{}{"api_key": "virtual-key"})
	if _, err := mr.RealtimeFor(p).OpenRealtime(context.Background(), p, realtime.SessionConfig{}); err == nil || !strings.Contains(err.Error(), "modalities.realtime.model") {
		t.Fatalf("with no model alias anywhere the error must say how to set one, got %v", err)
	}

	up, err := mr.RealtimeFor(p).OpenRealtime(context.Background(), p, realtime.SessionConfig{Model: "my-alias"})
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	waitFirst(t, seen)
	path, query, auth, _ := seen.get()
	if path != "/v1/realtime" || query != "model=my-alias" || auth != "Bearer virtual-key" {
		t.Fatalf("path=%q query=%q auth=%q", path, query, auth)
	}
}

func TestGeminiRealtimeThroughAProxyMapsToTheLiveServiceAndConfirmsSetup(t *testing.T) {
	mr := voiceRouter(t)
	srv, seen := fakeSocket(t, true, false)
	p := provider("gemini", srv.URL+"/v1beta", map[string]interface{}{"api_key": "AIza-1"})

	up, err := mr.RealtimeFor(p).OpenRealtime(context.Background(), p, realtime.SessionConfig{Instructions: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()

	path, query, _, first := seen.get()
	if path != geminiLivePath || query != "key=AIza-1" {
		t.Fatalf("path=%q query=%q", path, query)
	}
	if model := first["setup"].(map[string]interface{})["model"]; model != "models/gemini-3.8-live" {
		t.Fatalf("default Gemini model expected, got %v", model)
	}
}

func TestGeminiLiveEndpointMapping(t *testing.T) {
	for in, want := range map[string]string{
		"": "",
		"https://generativelanguage.googleapis.com/v1beta": "",
		"https://gw.example.com/v1beta":                    "wss://gw.example.com" + geminiLivePath,
		"http://localhost:8080":                            "ws://localhost:8080" + geminiLivePath,
	} {
		if got := geminiLiveEndpoint(in); got != want {
			t.Errorf("geminiLiveEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}

// A provider behind a private CA works for chat because every call goes through
// clientFor. Realtime must honour the same settings, or voice would fail for
// exactly the providers whose chat works.
func TestRealtimeHonoursTheProvidersCustomCA(t *testing.T) {
	mr := voiceRouter(t)
	srv, _ := fakeSocket(t, false, true)
	pemCert := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	cfg := map[string]interface{}{"api_key": "k"}

	withCA := provider("openai", srv.URL+"/v1", cfg)
	withCA.Name, withCA.CABundle = "with-ca", pemCert
	up, err := mr.RealtimeFor(withCA).OpenRealtime(context.Background(), withCA, realtime.SessionConfig{})
	if err != nil {
		t.Fatalf("a provider with its CA configured must connect: %v", err)
	}
	up.Close()

	noCA := provider("openai", srv.URL+"/v1", cfg)
	noCA.Name = "no-ca"
	if _, err := mr.RealtimeFor(noCA).OpenRealtime(context.Background(), noCA, realtime.SessionConfig{}); err == nil {
		t.Fatal("without the CA the untrusted certificate must be rejected")
	}
}

func TestSpeechEnginesCarryEachProvidersDefaultsAndOverrides(t *testing.T) {
	mr := voiceRouter(t)
	get := func(p *models.ModelProvider) (string, string, string) {
		e, err := mr.SpeechFor(p).SpeechEngine(p)
		if err != nil {
			t.Fatalf("%s: %v", p.Kind, err)
		}
		oe := e.(*audioEngine)
		return oe.Endpoint, oe.STTModel, oe.TTSModel
	}

	ep, stt, tts := get(provider("openai", "", map[string]interface{}{"api_key": "k"}))
	if ep != "https://api.openai.com/v1" || stt != "whisper-1" || tts != "tts-1" {
		t.Fatalf("openai defaults: %s %s %s", ep, stt, tts)
	}
	ep, stt, tts = get(provider("openrouter", "", map[string]interface{}{"api_key": "k"}))
	if ep != "https://openrouter.ai/api/v1" || stt != "openai/whisper-1" || !strings.HasPrefix(tts, "openai/gpt-4o-mini-tts") {
		t.Fatalf("openrouter defaults: %s %s %s", ep, stt, tts)
	}
	ep, stt, tts = get(provider("litellm", "http://litellm:4000/v1", modalities(map[string]interface{}{"audio": map[string]interface{}{"stt_model": "groq-whisper", "tts_model": "eleven"}})))
	if ep != "http://litellm:4000/v1" || stt != "groq-whisper" || tts != "eleven" {
		t.Fatalf("litellm overrides: %s %s %s", ep, stt, tts)
	}

	if _, err := mr.SpeechFor(provider("litellm", "", nil)).SpeechEngine(provider("litellm", "", nil)); err == nil {
		t.Fatal("a LiteLLM provider with no endpoint cannot serve speech")
	}
}

func sleepMs(n int) { time.Sleep(time.Duration(n) * time.Millisecond) }

type audioEngine = audio.OpenAIEngine

// A provider with its own wire format is health-checked by its driver. It used
// to be sent an OpenAI-style call (to api.openai.com when it had no endpoint),
// so a valid Gemini key failed the test — and a failed test burns the agent at bake.
func TestProviderTestUsesTheDriversHealthCheckForNonOpenAIKinds(t *testing.T) {
	mr := voiceRouter(t)
	var gotPath, gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey = r.URL.Path, r.URL.Query().Get("key")
		if gotKey != "good-key" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"API key not valid","status":"INVALID_ARGUMENT"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer srv.Close()

	ok := mr.TestProvider(context.Background(), provider("gemini", srv.URL+"/v1beta", map[string]interface{}{"api_key": "good-key"}))
	if !ok.Healthy || gotPath != "/v1beta/models" {
		t.Fatalf("a valid Gemini key must pass through the Gemini driver's own check, got %+v (path %q)", ok, gotPath)
	}
	bad := mr.TestProvider(context.Background(), provider("gemini", srv.URL+"/v1beta", map[string]interface{}{"api_key": "bad-key"}))
	if bad.Healthy || !strings.Contains(bad.Error, "API key not valid") {
		t.Fatalf("a bad key must fail with Google's message, not an OpenAI one: %+v", bad)
	}
}
