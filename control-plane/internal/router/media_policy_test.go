package router

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/blobstore"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// A blob ref from one kitchen must not resolve through another kitchen's provider.
func TestBlobRefFromAnotherKitchenIsNotFound(t *testing.T) {
	mr := newMapperRouter(t)
	ref, _ := mr.blobs.Put(context.Background(), "team-a", "image/png", "x.png", []byte("png bytes"))
	msg := models.ChatMessage{Role: "user", ContentParts: []models.ContentPart{
		{Type: "image", Media: &models.MediaRef{MimeType: "image/png", BlobRef: ref}},
	}}
	a := &models.ModelProvider{Name: "p", Kitchen: "team-a", Kind: "openai"}
	b := &models.ModelProvider{Name: "p", Kitchen: "team-b", Kind: "openai"}

	if _, err := mr.openAIContent(context.Background(), a, "gpt-4o", msg); err != nil {
		t.Fatalf("the owner can use its blob: %v", err)
	}
	_, err := mr.openAIContent(context.Background(), b, "gpt-4o", msg)
	if err == nil || !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("another kitchen must get not-found, got %v", err)
	}
	if _, err := mr.anthropicContent(context.Background(), &models.ModelProvider{Kitchen: "team-b", Kind: "anthropic"}, "claude-sonnet-4", msg); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("anthropic path must scope too, got %v", err)
	}
	if _, err := mr.geminiUserParts(context.Background(), &models.ModelProvider{Kitchen: "team-b", Kind: "gemini"}, "gemini-2.5-flash", msg); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("gemini path must scope too, got %v", err)
	}
}

// Gateways and local runners serve models the catalog cannot know: images pass through unless
// the operator switched the modality off.
func TestGatewayAndLocalProvidersPassImagesThroughForUnknownModels(t *testing.T) {
	mr := newMapperRouter(t)
	img := models.ChatMessage{Role: "user", ContentParts: []models.ContentPart{
		{Type: "image", Media: &models.MediaRef{MimeType: "image/png", Data: "aGk="}},
	}}
	for _, kind := range []string{"litellm", "openrouter", "ollama"} {
		prov := &models.ModelProvider{Name: kind, Kitchen: "k", Kind: kind}
		if _, err := mr.openAIContent(context.Background(), prov, "my-private-llava-finetune", img); err != nil {
			t.Fatalf("%s: an unknown model should get the image, got %v", kind, err)
		}
		off := &models.ModelProvider{Name: kind, Kitchen: "k", Kind: kind,
			Config: map[string]interface{}{"modalities": map[string]interface{}{"image": map[string]interface{}{"enabled": false}}}}
		if _, err := mr.openAIContent(context.Background(), off, "my-private-llava-finetune", img); !IsMediaError(err) {
			t.Fatalf("%s: an explicitly disabled image modality is a clear refusal, got %v", kind, err)
		}
	}
	// Providers with a known model list keep refusing models that cannot see.
	if _, err := mr.openAIContent(context.Background(), testProv("openai"), "gpt-3.5-turbo", img); !IsMediaError(err) {
		t.Fatalf("a known text-only model on openai stays refused, got %v", err)
	}
}

type fakeCatalog struct{ vision map[string]bool }

func (f fakeCatalog) Lookup(kind, model string) *models.ModelCapability {
	v, ok := f.vision[model]
	if !ok {
		return nil
	}
	return &models.ModelCapability{ModelName: model, SupportsVision: v}
}

func TestKnownTextOnlyModelOnAGatewayKeepsItsRefusal(t *testing.T) {
	mr := newMapperRouter(t)
	mr.catalog = fakeCatalog{map[string]bool{"llama3-text": false, "llava": true}}
	img := models.ChatMessage{Role: "user", ContentParts: []models.ContentPart{
		{Type: "image", Media: &models.MediaRef{MimeType: "image/png", Data: "aGk="}},
	}}
	prov := &models.ModelProvider{Name: "o", Kitchen: "k", Kind: "ollama"}
	if _, err := mr.openAIContent(context.Background(), prov, "llama3-text", img); !IsMediaError(err) {
		t.Fatalf("the catalog knows this model is text-only: %v", err)
	}
	if _, err := mr.openAIContent(context.Background(), prov, "llava", img); err != nil {
		t.Fatalf("a known vision model passes: %v", err)
	}
}

// ── Gemini Files API ─────────────────────────────────────────

type geminiFilesServer struct {
	*httptest.Server
	uploads  int32
	gets     int32
	state    string // state returned by the upload: "ACTIVE" or "PROCESSING"
	failWith int
}

func newGeminiFilesServer(t *testing.T) *geminiFilesServer {
	t.Helper()
	g := &geminiFilesServer{state: "ACTIVE"}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/upload/v1beta/files":
			_, _ = io.ReadAll(r.Body)
			if r.Header.Get("X-Goog-Upload-Protocol") != "multipart" || !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/related") {
				http.Error(w, "bad protocol", 400)
				return
			}
			if g.failWith != 0 {
				http.Error(w, "nope", g.failWith)
				return
			}
			n := atomic.AddInt32(&g.uploads, 1)
			_, _ = w.Write([]byte(`{"file":{"name":"files/f` + string(rune('0'+n)) + `","uri":"https://gen.example/files/f` + string(rune('0'+n)) + `","state":"` + g.state + `"}}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1beta/files/"):
			atomic.AddInt32(&g.gets, 1)
			_, _ = w.Write([]byte(`{"name":"files/f1","uri":"https://gen.example/files/f1","state":"ACTIVE"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.Close)
	return g
}

func geminiProv(g *geminiFilesServer) *models.ModelProvider {
	return &models.ModelProvider{Name: "gem", Kitchen: "k", Kind: "gemini", Endpoint: g.URL + "/v1beta",
		Config: map[string]interface{}{"api_key": "gk"}}
}

func videoMsg(ref string) models.ChatMessage {
	return models.ChatMessage{Role: "user", Content: "what happens", ContentParts: []models.ContentPart{
		{Type: "video", Media: &models.MediaRef{MimeType: "video/mp4", BlobRef: ref, Name: "clip.mp4"}},
	}}
}

func TestGeminiLargeMediaIsUploadedOnceAndReferencedByURI(t *testing.T) {
	t.Setenv(EnvGeminiFilesMinBytes, "10")
	g := newGeminiFilesServer(t)
	mr := newMapperRouter(t)
	ref, _ := mr.blobs.Put(context.Background(), "k", "video/mp4", "clip.mp4", []byte("0123456789abcdef"))

	for i := 0; i < 2; i++ {
		parts, err := mr.geminiUserParts(context.Background(), geminiProv(g), "gemini-2.5-flash", videoMsg(ref))
		if err != nil {
			t.Fatal(err)
		}
		goldenJSON(t, parts, `[{"text":"what happens"},{"fileData":{"mimeType":"video/mp4","fileUri":"https://gen.example/files/f1"}}]`)
	}
	if n := atomic.LoadInt32(&g.uploads); n != 1 {
		t.Fatalf("one upload, reused after that; got %d", n)
	}
}

func TestGeminiSmallMediaStaysInline(t *testing.T) {
	t.Setenv(EnvGeminiFilesMinBytes, "1000")
	g := newGeminiFilesServer(t)
	mr := newMapperRouter(t)
	ref, _ := mr.blobs.Put(context.Background(), "k", "video/mp4", "clip.mp4", []byte("tiny"))
	parts, err := mr.geminiUserParts(context.Background(), geminiProv(g), "gemini-2.5-flash", videoMsg(ref))
	if err != nil {
		t.Fatal(err)
	}
	if parts[1].InlineData == nil || atomic.LoadInt32(&g.uploads) != 0 {
		t.Fatalf("a small file goes inline with no upload: %+v", parts)
	}
}

func TestGeminiUploadFailureFallsBackToInline(t *testing.T) {
	t.Setenv(EnvGeminiFilesMinBytes, "10")
	g := newGeminiFilesServer(t)
	g.failWith = 500
	mr := newMapperRouter(t)
	ref, _ := mr.blobs.Put(context.Background(), "k", "video/mp4", "clip.mp4", []byte("0123456789abcdef"))
	parts, err := mr.geminiUserParts(context.Background(), geminiProv(g), "gemini-2.5-flash", videoMsg(ref))
	if err != nil {
		t.Fatalf("a failed upload must not fail the turn: %v", err)
	}
	if parts[1].InlineData == nil {
		t.Fatalf("expected inline bytes, got %+v", parts[1])
	}
}

func TestGeminiWaitsForAVideoToFinishProcessing(t *testing.T) {
	t.Setenv(EnvGeminiFilesMinBytes, "10")
	geminiProcessingPoll = time.Millisecond
	t.Cleanup(func() { geminiProcessingPoll = time.Second })
	g := newGeminiFilesServer(t)
	g.state = "PROCESSING"
	mr := newMapperRouter(t)
	ref, _ := mr.blobs.Put(context.Background(), "k", "video/mp4", "clip.mp4", []byte("0123456789abcdef"))
	if _, err := mr.geminiUserParts(context.Background(), geminiProv(g), "gemini-2.5-flash", videoMsg(ref)); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&g.gets) == 0 {
		t.Fatal("a PROCESSING file must be polled until it is ACTIVE")
	}
}

// Gemini deletes files after 48 hours: an expired record is uploaded again.
func TestGeminiExpiredFileRecordIsUploadedAgain(t *testing.T) {
	t.Setenv(EnvGeminiFilesMinBytes, "10")
	g := newGeminiFilesServer(t)
	mr := newMapperRouter(t)
	ref, _ := mr.blobs.Put(context.Background(), "k", "video/mp4", "clip.mp4", []byte("0123456789abcdef"))
	prov := geminiProv(g)
	_ = mr.blobs.SetProviderFileID(context.Background(), ref, fileKey(prov), formatGeminiRecord("https://gen.example/files/old", time.Now().Add(-time.Minute)))

	parts, err := mr.geminiUserParts(context.Background(), prov, "gemini-2.5-flash", videoMsg(ref))
	if err != nil {
		t.Fatal(err)
	}
	if parts[1].FileData == nil || strings.HasSuffix(parts[1].FileData.FileURI, "/old") || atomic.LoadInt32(&g.uploads) != 1 {
		t.Fatalf("expected a fresh upload, got %+v (%d uploads)", parts[1], g.uploads)
	}
}

func TestGeminiUploadURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://generativelanguage.googleapis.com/v1beta": "https://generativelanguage.googleapis.com/upload/v1beta/files",
		"http://127.0.0.1:9/v1beta/":                       "http://127.0.0.1:9/upload/v1beta/files",
		"http://127.0.0.1:9":                               "http://127.0.0.1:9/upload/files",
	} {
		if got, _ := geminiUploadURL(in); got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
}
