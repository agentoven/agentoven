package handlers_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/sessions"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/blobstore"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// recordingProvider stands in for a model provider that speaks the OpenAI chat
// shape and the Gemini generateContent shape. It keeps every request body, so a
// test can prove what bytes reached the provider.
type recordingProvider struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []string
}

func newRecordingProvider(t *testing.T) *recordingProvider {
	t.Helper()
	p := &recordingProvider{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.bodies = append(p.bodies, string(body))
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"seen"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`))
		case strings.HasSuffix(r.URL.Path, "/audio/speech"):
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write([]byte("SPOKEN"))
		case strings.HasSuffix(r.URL.Path, "/audio/transcriptions"):
			_, _ = w.Write([]byte(`{"text":"heard it"}`))
		case strings.Contains(r.URL.Path, ":generateContent"):
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"heard"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1,"totalTokenCount":4}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *recordingProvider) all() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.bodies, "\n")
}

func (p *recordingProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.bodies)
}

type mmEnv struct {
	h     *handlers.Handlers
	s     store.Store
	sess  *sessions.MemorySessionStore
	prov  *recordingProvider
	blobs *blobstore.LocalStore
}

func newMMEnv(t *testing.T, kind, model string) *mmEnv {
	t.Helper()
	t.Setenv("AGENTOVEN_JOURNAL_DISABLE", "true")
	prov := newRecordingProvider(t)

	os.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	s := store.NewMemoryStore()
	os.Unsetenv("AGENTOVEN_DATA_DIR")
	t.Cleanup(func() { s.Close() })

	mr := router.NewModelRouter(s)
	bs, err := blobstore.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mr.SetBlobStore(bs)

	ctx := context.Background()
	if err := s.CreateProvider(ctx, &models.ModelProvider{
		Name: "p", Kitchen: "default", Kind: kind, Endpoint: prov.URL,
		Models: []string{model}, IsDefault: true, Config: map[string]interface{}{"api_key": "k"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAgent(ctx, &models.Agent{
		Name: "ag", Kitchen: "default", Mode: models.AgentModeManaged, Status: models.AgentStatusReady,
		ModelProvider: "p", ModelName: model, MaxTurns: 3,
		ResolvedConfig: &models.ResolvedIngredients{Model: &models.ResolvedModel{Provider: "p", Kind: kind, Model: model, Endpoint: prov.URL}},
	}); err != nil {
		t.Fatal(err)
	}
	sess := sessions.NewMemorySessionStore()
	return &mmEnv{h: handlers.New(s, mr, nil, nil, nil, sess, nil), s: s, sess: sess, prov: prov, blobs: bs}
}

const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

func imagePart() map[string]interface{} {
	return map[string]interface{}{"type": "image", "media": map[string]string{"mime_type": "image/png", "data": tinyPNG, "name": "t.png"}}
}

func (e *mmEnv) sendSession(t *testing.T, sid string, body map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/m", strings.NewReader(string(raw)))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("agentName", "ag")
	rctx.URLParams.Add("sessionID", sid)
	req = withKitchen(req, "default")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	e.h.SendSessionMessage(rec, req)
	return rec
}

func (e *mmEnv) newSession(t *testing.T) string {
	t.Helper()
	sess := &models.Session{ID: "s1", AgentName: "ag", Kitchen: "default", Status: models.SessionActive}
	if err := e.sess.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	return sess.ID
}

// A model without vision answers a session image with a clear 4xx, and the
// refused message does not stay in the history to fail every later turn.
func TestSessionImageOnTextOnlyModelIsRefusedAndNotRemembered(t *testing.T) {
	e := newMMEnv(t, "openai", "gpt-3.5-turbo")
	sid := e.newSession(t)

	rec := e.sendSession(t, sid, map[string]interface{}{"content": "what is this", "content_parts": []interface{}{imagePart()}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a capability error is the caller's, expected 422, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "does not accept image input") {
		t.Fatalf("the error must say what was refused: %s", rec.Body.String())
	}
	if e.prov.count() != 0 {
		t.Fatal("nothing should have been sent to the provider")
	}
	got, _ := e.sess.GetSession(context.Background(), sid)
	if len(got.Messages) != 0 {
		t.Fatalf("the refused message must not stay in the session, history has %d messages", len(got.Messages))
	}

	rec = e.sendSession(t, sid, map[string]interface{}{"content": "never mind, just hello"})
	if rec.Code != http.StatusOK {
		t.Fatalf("the session must still work after a refused turn, got %d: %s", rec.Code, rec.Body.String())
	}
}

// The provider gets the image, and the session stores a blob reference, not the bytes.
func TestSessionImageReachesTheProviderAndTheSessionKeepsOnlyAReference(t *testing.T) {
	e := newMMEnv(t, "openai", "gpt-4o")
	sid := e.newSession(t)

	rec := e.sendSession(t, sid, map[string]interface{}{"content": "what is this", "content_parts": []interface{}{imagePart()}})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(e.prov.all(), "data:image/png;base64,"+tinyPNG) {
		t.Fatalf("the provider must receive the image, got %s", e.prov.all())
	}
	got, _ := e.sess.GetSession(context.Background(), sid)
	stored, _ := json.Marshal(got.Messages)
	if strings.Contains(string(stored), tinyPNG) {
		t.Fatalf("the session must not hold the image bytes: %s", stored)
	}
	if !strings.Contains(string(stored), `"blob_ref"`) {
		t.Fatalf("the session should hold a blob_ref: %s", stored)
	}

	// The next turn still sends the image from the blob store.
	e.sendSession(t, sid, map[string]interface{}{"content": "and now?"})
	if strings.Count(e.prov.all(), "data:image/png;base64,"+tinyPNG) < 2 {
		t.Fatal("the later turn must resend the image from the blob store")
	}
}

// /invoke takes images and PDFs, not only audio.
func TestInvokeAcceptsContentParts(t *testing.T) {
	e := newMMEnv(t, "openai", "gpt-4o")
	body, _ := json.Marshal(map[string]interface{}{"message": "what is this", "content_parts": []interface{}{imagePart()}})
	req := withChiParam(withKitchen(httptest.NewRequest(http.MethodPost, "/invoke", strings.NewReader(string(body))), "default"), "agentName", "ag")
	rec := httptest.NewRecorder()
	e.h.InvokeAgent(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(e.prov.all(), "data:image/png;base64,"+tinyPNG) {
		t.Fatalf("the provider must receive the image, got %s", e.prov.all())
	}
	// The response carries the run's trace; it must hold a reference, not the bytes.
	if strings.Contains(rec.Body.String(), tinyPNG) {
		t.Fatal("the image bytes must not be echoed back in the execution trace")
	}
}

func TestInvokeImageOnTextOnlyModelIsA422(t *testing.T) {
	e := newMMEnv(t, "openai", "gpt-3.5-turbo")
	body, _ := json.Marshal(map[string]interface{}{"message": "what is this", "content_parts": []interface{}{imagePart()}})
	req := withChiParam(withKitchen(httptest.NewRequest(http.MethodPost, "/invoke", strings.NewReader(string(body))), "default"), "agentName", "ag")
	rec := httptest.NewRecorder()
	e.h.InvokeAgent(rec, req)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "does not accept image input") {
		t.Fatalf("expected a 422 naming the refused modality, got %d: %s", rec.Code, rec.Body.String())
	}
}

// A running pod reads text only; a message with media must not be handed to it.
func TestInvokeWithMediaBypassesTheTextOnlyPod(t *testing.T) {
	e := newMMEnv(t, "openai", "gpt-4o")
	pod := newFakeProcess(t, "pod answer")
	ag, _ := e.s.GetAgent(context.Background(), "default", "ag")
	ag.Process = &models.ProcessInfo{Status: models.ProcessRunning, Endpoint: pod.srv.URL}
	if err := e.s.UpdateAgent(context.Background(), ag); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]interface{}{"message": "what is this", "content_parts": []interface{}{imagePart()}})
	req := withChiParam(withKitchen(httptest.NewRequest(http.MethodPost, "/invoke", strings.NewReader(string(body))), "default"), "agentName", "ag")
	rec := httptest.NewRecorder()
	e.h.InvokeAgent(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if pod.calls() != 0 {
		t.Fatal("the pod would have dropped the image; the in-process executor must answer instead")
	}
	if !strings.Contains(e.prov.all(), tinyPNG) {
		t.Fatal("the provider must receive the image")
	}
}

func a2aSend(t *testing.T, e *mmEnv, parts []map[string]interface{}) map[string]interface{} {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": "tasks/send", "params": map[string]interface{}{
		"message": map[string]interface{}{"role": "user", "parts": parts}, "metadata": map[string]string{"agent_name": "ag"},
	}})
	rec := httptest.NewRecorder()
	req := withKitchen(httptest.NewRequest(http.MethodPost, "/a2a", strings.NewReader(string(body))), "default")
	e.h.A2AEndpoint(rec, req)
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("not JSON: %s", rec.Body.String())
	}
	return out
}

func TestA2AFileAndDataPartsReachTheModel(t *testing.T) {
	e := newMMEnv(t, "openai", "gpt-4o")
	out := a2aSend(t, e, []map[string]interface{}{
		{"kind": "text", "text": "describe"}, // later A2A revisions say "kind"
		{"type": "file", "file": map[string]string{"name": "t.png", "mimeType": "image/png", "bytes": tinyPNG}},
		{"type": "data", "data": map[string]interface{}{"order_id": 7}},
	})
	if out["error"] != nil {
		t.Fatalf("unexpected error %v", out["error"])
	}
	sent := e.prov.all()
	if !strings.Contains(sent, "data:image/png;base64,"+tinyPNG) {
		t.Fatalf("the FilePart must reach the provider as an image: %s", sent)
	}
	if !strings.Contains(sent, `order_id`) {
		t.Fatalf("the DataPart must reach the model as JSON text: %s", sent)
	}
}

// A refused file is an invalid request with the reason, never a bare "failed".
func TestA2AFileOnTextOnlyModelIsRefusedWithAReason(t *testing.T) {
	e := newMMEnv(t, "openai", "gpt-3.5-turbo")
	out := a2aSend(t, e, []map[string]interface{}{
		{"type": "text", "text": "describe"},
		{"type": "file", "file": map[string]string{"name": "t.png", "mimeType": "image/png", "bytes": tinyPNG}},
	})
	errObj, _ := out["error"].(map[string]interface{})
	if errObj == nil || !strings.Contains(errObj["message"].(string), "does not accept image input") {
		t.Fatalf("expected a JSON-RPC error naming the refused modality, got %v", out)
	}
	if e.prov.count() != 0 {
		t.Fatal("nothing should reach the provider")
	}
}

func TestA2AUnknownPartTypeIsRefusedNotDropped(t *testing.T) {
	e := newMMEnv(t, "openai", "gpt-4o")
	out := a2aSend(t, e, []map[string]interface{}{
		{"type": "text", "text": "hi"},
		{"type": "hologram", "text": "?"},
	})
	errObj, _ := out["error"].(map[string]interface{})
	if errObj == nil || !strings.Contains(errObj["message"].(string), "hologram") {
		t.Fatalf("expected the unknown part type to be named, got %v", out)
	}
	if e.prov.count() != 0 {
		t.Fatal("the agent must not run on a partly understood message")
	}
	out = a2aSend(t, e, []map[string]interface{}{{"type": "file", "file": map[string]string{"mimeType": "image/png"}}})
	if out["error"] == nil {
		t.Fatal("a file part with neither bytes nor uri must be refused")
	}
}

// A provider without speech endpoints whose model takes audio itself (Gemini)
// gets the audio as audio, instead of the request failing for lack of a transcriber.
func TestAudioPartGoesToAnAudioCapableModelWithoutSpeechEndpoints(t *testing.T) {
	e := newMMEnv(t, "gemini", "gemini-2.5-flash")
	wav := base64.StdEncoding.EncodeToString([]byte("RIFF....WAVE"))
	out := a2aSend(t, e, []map[string]interface{}{
		{"type": "text", "text": "what was said"},
		{"type": "file", "file": map[string]string{"name": "q.wav", "mimeType": "audio/wav", "bytes": wav}},
	})
	if out["error"] != nil {
		t.Fatalf("unexpected error %v", out["error"])
	}
	sent := e.prov.all()
	if !strings.Contains(sent, `"mimeType":"audio/wav"`) {
		t.Fatalf("the audio must reach Gemini as inline audio: %s", sent)
	}
}

// The per-agent A2A endpoint proxies to the agent's pod, which reads only the
// text of a message. Files for an agent on AgentOven's own loop go to the
// in-process executor instead of being dropped by the pod.
func TestPerAgentA2AFilesAreNotHandedToTheTextOnlyPod(t *testing.T) {
	e := newMMEnv(t, "openai", "gpt-4o")
	pod := newFakeProcess(t, "pod answer")
	ag, _ := e.s.GetAgent(context.Background(), "default", "ag")
	ag.Process = &models.ProcessInfo{Status: models.ProcessRunning, Endpoint: pod.srv.URL}
	if err := e.s.UpdateAgent(context.Background(), ag); err != nil {
		t.Fatal(err)
	}

	send := func(method string, meta map[string]string) map[string]interface{} {
		body, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": method, "params": map[string]interface{}{
			"message": map[string]interface{}{"role": "user", "parts": []map[string]interface{}{
				{"type": "text", "text": "describe"},
				{"type": "file", "file": map[string]string{"mimeType": "image/png", "bytes": tinyPNG}},
			}},
			"metadata": meta,
		}})
		req := withChiParam(withKitchen(httptest.NewRequest(http.MethodPost, "/agents/ag/a2a", strings.NewReader(string(body))), "default"), "agentName", "ag")
		rec := httptest.NewRecorder()
		e.h.A2AAgentEndpoint(rec, req)
		var out map[string]interface{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}

	// metadata.agent_name names someone else: the route decides which agent runs.
	out := send("tasks/send", map[string]string{"agent_name": "someone-else"})
	if out["error"] != nil {
		t.Fatalf("unexpected error %v", out["error"])
	}
	if pod.calls() != 0 {
		t.Fatal("the pod would have dropped the file; it must not be called")
	}
	if !strings.Contains(e.prov.all(), tinyPNG) {
		t.Fatalf("the model must receive the file: %s", e.prov.all())
	}

	// A method the gateway cannot answer in-process is refused rather than forwarded to drop the file.
	out = send("message/send", nil)
	errObj, _ := out["error"].(map[string]interface{})
	if errObj == nil || !strings.Contains(errObj["message"].(string), "tasks/send") {
		t.Fatalf("expected a refusal naming tasks/send, got %v", out)
	}
	if pod.calls() != 0 {
		t.Fatal("the pod must not be called with a file")
	}
}
