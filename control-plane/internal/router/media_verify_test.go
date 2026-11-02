package router

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// The older image_url shape must work on every provider, not only OpenAI.
func TestImageURLPartIsAcceptedByAnthropicAndGemini(t *testing.T) {
	mr := newMapperRouter(t)
	dataURI := models.ContentPart{Type: "image_url", ImageURL: &models.ImageURL{URL: "data:image/png;base64,aGk="}}
	httpURL := models.ContentPart{Type: "image_url", ImageURL: &models.ImageURL{URL: "https://example.com/cat.png?x=1"}}
	msg := models.ChatMessage{Role: "user", Content: "look", ContentParts: []models.ContentPart{dataURI, httpURL}}

	got, err := mr.anthropicContent(context.Background(), testProv("anthropic"), "claude-sonnet-4", msg)
	if err != nil {
		t.Fatalf("anthropic image_url: %v", err)
	}
	goldenJSON(t, got, `[
		{"type":"text","text":"look"},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGk="}},
		{"type":"image","source":{"type":"url","url":"https://example.com/cat.png?x=1"}}
	]`)

	parts, err := mr.geminiUserParts(context.Background(), &models.ModelProvider{Kind: "gemini"}, "gemini-2.5-flash", msg)
	if err != nil {
		t.Fatalf("gemini image_url: %v", err)
	}
	goldenJSON(t, parts, `[
		{"text":"look"},
		{"inlineData":{"mimeType":"image/png","data":"aGk="}},
		{"fileData":{"mimeType":"image/png","fileUri":"https://example.com/cat.png?x=1"}}
	]`)
}

// A refused modality is a typed error, so an HTTP layer can answer 4xx.
func TestRefusedMediaIsATypedError(t *testing.T) {
	mr := newMapperRouter(t)
	img := models.ChatMessage{Role: "user", ContentParts: []models.ContentPart{
		{Type: "image", Media: &models.MediaRef{MimeType: "image/png", Data: "aGk="}},
	}}
	_, err := mr.openAIContent(context.Background(), testProv("openai"), "gpt-3.5-turbo", img)
	if !IsMediaError(err) {
		t.Fatalf("image to a text-only model should be a MediaError, got %T %v", err, err)
	}
	if !strings.Contains(err.Error(), "does not accept image input") {
		t.Fatalf("the error should say what was refused: %v", err)
	}
	// The error survives the router's "all providers failed" wrapping.
	if !IsMediaError(wrapAllFailed(err)) {
		t.Fatal("IsMediaError must see through wrapping")
	}
	// A malformed part is the caller's problem too.
	_, err = mr.resolveMedia(context.Background(), &models.MediaRef{Data: "aGk="})
	if !IsMediaError(err) {
		t.Fatalf("missing mime type should be a MediaError, got %v", err)
	}
	// An upstream failure is not.
	if IsMediaError(http.ErrHandlerTimeout) {
		t.Fatal("an ordinary error is not a MediaError")
	}
}

func wrapAllFailed(err error) error {
	return &wrapped{err}
}

type wrapped struct{ err error }

func (w *wrapped) Error() string { return "all providers failed, last error: " + w.err.Error() }
func (w *wrapped) Unwrap() error { return w.err }

// A scanned PDF has no text layer: refuse it rather than send an empty document.
func TestScannedPDFWithoutNativeSupportIsRefused(t *testing.T) {
	mr := newMapperRouter(t)
	raw := pdfWithText("") // a page with no text
	msg := models.ChatMessage{Role: "user", ContentParts: []models.ContentPart{
		{Type: "file", Media: &models.MediaRef{MimeType: "application/pdf", Data: base64.StdEncoding.EncodeToString(raw), Name: "scan.pdf"}},
	}}
	_, err := mr.openAIContent(context.Background(), testProv("openai"), "gpt-3.5-turbo", msg)
	if err == nil || !strings.Contains(err.Error(), "no extractable text") {
		t.Fatalf("expected a clear refusal for a PDF with no text, got %v", err)
	}
	if !IsMediaError(err) {
		t.Fatal("it should be a MediaError")
	}
}

// ExternalizeMedia moves inline bytes into the blob store; the message the caller
// holds is untouched, and the reference still resolves to the same bytes.
func TestExternalizeMediaReplacesInlineBytesWithABlobRef(t *testing.T) {
	mr := newMapperRouter(t)
	b64 := base64.StdEncoding.EncodeToString([]byte("%PDF-1.7 pretend"))
	orig := models.ChatMessage{Role: "user", Content: "read", ContentParts: []models.ContentPart{
		{Type: "text", Text: "also this"},
		{Type: "file", Media: &models.MediaRef{MimeType: "application/pdf", Data: b64, Name: "a.pdf"}},
		{Type: "image_url", ImageURL: &models.ImageURL{URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("png"))}},
		{Type: "image", Media: &models.MediaRef{MimeType: "image/png", URL: "https://example.com/x.png"}},
	}}

	out := mr.ExternalizeMedia(context.Background(), "k", orig)

	if orig.ContentParts[1].Media.Data != b64 || orig.ContentParts[1].Media.BlobRef != "" {
		t.Fatal("the caller's message must not be modified")
	}
	pdf := out.ContentParts[1].Media
	if pdf.Data != "" || pdf.BlobRef == "" || pdf.MimeType != "application/pdf" || pdf.Name != "a.pdf" {
		t.Fatalf("pdf should now be a blob ref with its metadata, got %+v", pdf)
	}
	img := out.ContentParts[2]
	if img.Media == nil || img.Media.BlobRef == "" || img.ImageURL != nil || img.Media.MimeType != "image/png" {
		t.Fatalf("a data: URI image should become a blob-backed image, got %+v", img)
	}
	if u := out.ContentParts[3].Media; u.URL == "" || u.BlobRef != "" {
		t.Fatalf("a URL image has no bytes to store and must stay a URL, got %+v", u)
	}
	if out.ContentParts[0].Text != "also this" {
		t.Fatal("text parts must be preserved")
	}

	// The reference is what the provider mapper loads again at send time.
	got, err := mr.openAIContent(context.Background(), testProv("openai"), "gpt-4o", out)
	if err != nil {
		t.Fatal(err)
	}
	s := marshal(t, got)
	if !strings.Contains(s, "data:application/pdf;base64,"+b64) {
		t.Fatalf("the blob should be read back into the request, got %s", s)
	}
	if strings.Contains(marshal(t, out), b64) {
		t.Fatal("the stored message must not contain the PDF bytes")
	}
}

func TestExternalizeMediaWithoutABlobStoreKeepsBytesInline(t *testing.T) {
	mr := &ModelRouter{client: &http.Client{}}
	msg := models.ChatMessage{Role: "user", ContentParts: []models.ContentPart{
		{Type: "image", Media: &models.MediaRef{MimeType: "image/png", Data: "aGk="}},
	}}
	out := mr.ExternalizeMedia(context.Background(), "k", msg)
	if out.ContentParts[0].Media.Data != "aGk=" {
		t.Fatal("with no blob store the bytes must stay inline so the turn still works")
	}
}

// The Files API is an optimisation: when it is unavailable the PDF goes in the
// request body instead of failing the turn.
func TestPDFUploadFailureFallsBackToInline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no files api here", http.StatusNotFound)
	}))
	defer srv.Close()

	mr := newMapperRouter(t)
	raw := []byte("%PDF-1.7 body")
	ref, _ := mr.blobs.Put(context.Background(), "k", "application/pdf", "r.pdf", raw)
	prov := &models.ModelProvider{Name: "oai", Kitchen: "k", Kind: "openai", Endpoint: srv.URL, Config: map[string]interface{}{"api_key": "sk"}}
	msg := models.ChatMessage{Role: "user", ContentParts: []models.ContentPart{
		{Type: "file", Media: &models.MediaRef{MimeType: "application/pdf", BlobRef: ref, Name: "r.pdf"}},
	}}
	got, err := mr.openAIContent(context.Background(), prov, "gpt-4o", msg)
	if err != nil {
		t.Fatalf("a failed upload must not fail the turn: %v", err)
	}
	if !strings.Contains(marshal(t, got), "file_data") {
		t.Fatalf("expected the PDF inline, got %s", marshal(t, got))
	}
}

// Providers whose gateways have no OpenAI Files API never see an upload attempt.
func TestOpenRouterPDFIsSentInlineWithoutAnUploadAttempt(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer srv.Close()
	mr := newMapperRouter(t)
	ref, _ := mr.blobs.Put(context.Background(), "k", "application/pdf", "r.pdf", []byte("%PDF-1.7 body"))
	prov := &models.ModelProvider{Name: "or", Kitchen: "k", Kind: "openrouter", Endpoint: srv.URL, Config: map[string]interface{}{"api_key": "sk"}}
	msg := models.ChatMessage{Role: "user", ContentParts: []models.ContentPart{
		{Type: "file", Media: &models.MediaRef{MimeType: "application/pdf", BlobRef: ref}},
	}}
	if _, err := mr.openAIContent(context.Background(), prov, "gpt-4o", msg); err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Fatal("OpenRouter has no Files API; no upload should be attempted")
	}
}

// A file ID is only valid in the account that uploaded it, so two kitchens with a
// provider of the same name must not share one.
func TestProviderFileIDsAreScopedToTheKitchen(t *testing.T) {
	var uploads int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads++
		_, _ = w.Write([]byte(`{"id":"file-` + r.Header.Get("Authorization")[7:] + `"}`))
	}))
	defer srv.Close()

	mr := newMapperRouter(t)
	ref, _ := mr.blobs.Put(context.Background(), "team-a", "application/pdf", "r.pdf", []byte("%PDF-1.7 body"))
	_, _ = mr.blobs.Put(context.Background(), "team-b", "application/pdf", "r.pdf", []byte("%PDF-1.7 body"))
	msg := models.ChatMessage{Role: "user", ContentParts: []models.ContentPart{
		{Type: "file", Media: &models.MediaRef{MimeType: "application/pdf", BlobRef: ref}},
	}}
	a := &models.ModelProvider{Name: "openai", Kitchen: "team-a", Kind: "openai", Endpoint: srv.URL, Config: map[string]interface{}{"api_key": "keyA"}}
	b := &models.ModelProvider{Name: "openai", Kitchen: "team-b", Kind: "openai", Endpoint: srv.URL, Config: map[string]interface{}{"api_key": "keyB"}}

	ga, err := mr.openAIContent(context.Background(), a, "gpt-4o", msg)
	if err != nil {
		t.Fatal(err)
	}
	gb, err := mr.openAIContent(context.Background(), b, "gpt-4o", msg)
	if err != nil {
		t.Fatal(err)
	}
	goldenJSON(t, ga, `[{"type":"file","file":{"file_id":"file-keyA"}}]`)
	goldenJSON(t, gb, `[{"type":"file","file":{"file_id":"file-keyB"}}]`)
	if uploads != 2 {
		t.Fatalf("each kitchen's account needs its own upload, got %d", uploads)
	}
}

func marshal(t *testing.T, v interface{}) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
