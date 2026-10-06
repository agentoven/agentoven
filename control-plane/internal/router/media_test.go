package router

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/blobstore"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// goldenJSON compares marshalled output to an expected JSON document, so the
// wire shape is pinned exactly, key for key.
func goldenJSON(t *testing.T, got interface{}, want string) {
	t.Helper()
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var g, w interface{}
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Fatalf("wire shape mismatch\n got: %s\nwant: %s", gb, wb)
	}
}

func newMapperRouter(t *testing.T) *ModelRouter {
	t.Helper()
	mr := &ModelRouter{client: &http.Client{}}
	bs, err := blobstore.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mr.SetBlobStore(bs)
	return mr
}

func TestOpenAITextOnlyStaysAPlainString(t *testing.T) {
	mr := newMapperRouter(t)
	got, err := mr.openAIContent(context.Background(), testProv("openai"), "gpt-4o", models.ChatMessage{Role: "user", Content: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello" {
		t.Fatalf("text-only content must stay a string for existing providers, got %#v", got)
	}
}

func TestOpenAIImageInlineBecomesADataURI(t *testing.T) {
	mr := newMapperRouter(t)
	msg := models.ChatMessage{Role: "user", Content: "what is this?", ContentParts: []models.ContentPart{
		{Type: "image", Media: &models.MediaRef{MimeType: "image/png", Data: "aGk="}},
	}}
	got, err := mr.openAIContent(context.Background(), testProv("openai"), "gpt-4o", msg)
	if err != nil {
		t.Fatal(err)
	}
	goldenJSON(t, got, `[
		{"type":"text","text":"what is this?"},
		{"type":"image_url","image_url":{"url":"data:image/png;base64,aGk="}}
	]`)
}

func TestOpenAIPDFBecomesAFilePart(t *testing.T) {
	mr := newMapperRouter(t)
	msg := models.ChatMessage{Role: "user", Content: "summarise", ContentParts: []models.ContentPart{
		{Type: "file", Media: &models.MediaRef{MimeType: "application/pdf", Data: "JVBER", Name: "report.pdf"}},
	}}
	got, err := mr.openAIContent(context.Background(), testProv("openai"), "gpt-4o", msg)
	if err != nil {
		t.Fatal(err)
	}
	goldenJSON(t, got, `[
		{"type":"text","text":"summarise"},
		{"type":"file","file":{"filename":"report.pdf","file_data":"data:application/pdf;base64,JVBER"}}
	]`)
}

func TestOpenAIWavAudioBecomesInputAudio(t *testing.T) {
	mr := newMapperRouter(t)
	msg := models.ChatMessage{Role: "user", ContentParts: []models.ContentPart{
		{Type: "audio", Media: &models.MediaRef{MimeType: "audio/wav", Data: "UklGRg=="}},
	}}
	got, err := mr.openAIContent(context.Background(), testProv("openai"), "gpt-4o-audio-preview", msg)
	if err != nil {
		t.Fatal(err)
	}
	goldenJSON(t, got, `[{"type":"input_audio","input_audio":{"data":"UklGRg==","format":"wav"}}]`)
}

func TestOpenAIRefusesImageOnNonVisionModel(t *testing.T) {
	mr := newMapperRouter(t)
	msg := models.ChatMessage{Role: "user", ContentParts: []models.ContentPart{
		{Type: "image", Media: &models.MediaRef{MimeType: "image/png", Data: "aGk="}},
	}}
	_, err := mr.openAIContent(context.Background(), testProv("openai"), "gpt-3.5-turbo", msg)
	if err == nil || !strings.Contains(err.Error(), "does not accept image input") {
		t.Fatalf("expected a clear capability error, got %v", err)
	}
}

func TestOpenAIRefusesVideo(t *testing.T) {
	mr := newMapperRouter(t)
	msg := models.ChatMessage{Role: "user", ContentParts: []models.ContentPart{
		{Type: "video", Media: &models.MediaRef{MimeType: "video/mp4", Data: "AAAA"}},
	}}
	if _, err := mr.openAIContent(context.Background(), testProv("openai"), "gpt-4o", msg); err == nil {
		t.Fatal("OpenAI chat completions must refuse video input rather than drop it")
	}
}

func TestOpenAIPDFByURLIsRefused(t *testing.T) {
	mr := newMapperRouter(t)
	msg := models.ChatMessage{Role: "user", ContentParts: []models.ContentPart{
		{Type: "file", Media: &models.MediaRef{MimeType: "application/pdf", URL: "https://example.com/a.pdf"}},
	}}
	if _, err := mr.openAIContent(context.Background(), testProv("openai"), "gpt-4o", msg); err == nil {
		t.Fatal("OpenAI PDF input needs inline data or a blob_ref, not a bare URL")
	}
}

func TestBlobRefIsLoadedFromTheBlobStore(t *testing.T) {
	mr := newMapperRouter(t)
	ref, err := mr.blobs.Put(context.Background(), "image/jpeg", "p.jpg", []byte("jpegbytes"))
	if err != nil {
		t.Fatal(err)
	}
	msg := models.ChatMessage{Role: "user", ContentParts: []models.ContentPart{
		{Type: "image", Media: &models.MediaRef{MimeType: "image/jpeg", BlobRef: ref}},
	}}
	got, err := mr.openAIContent(context.Background(), testProv("openai"), "gpt-4o", msg)
	if err != nil {
		t.Fatal(err)
	}
	goldenJSON(t, got, `[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,anBlZ2J5dGVz"}}]`)
}

func TestAnthropicImageAndPDFBlocks(t *testing.T) {
	mr := newMapperRouter(t)
	msg := models.ChatMessage{Role: "user", Content: "read these", ContentParts: []models.ContentPart{
		{Type: "image", Media: &models.MediaRef{MimeType: "image/png", Data: "aGk="}},
		{Type: "file", Media: &models.MediaRef{MimeType: "application/pdf", Data: "JVBER"}},
	}}
	got, err := mr.anthropicContent(context.Background(), testProv("anthropic"), "claude-sonnet-4", msg)
	if err != nil {
		t.Fatal(err)
	}
	goldenJSON(t, got, `[
		{"type":"text","text":"read these"},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGk="}},
		{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBER"}}
	]`)
}

func TestAnthropicRefusesAudio(t *testing.T) {
	mr := newMapperRouter(t)
	msg := models.ChatMessage{Role: "user", ContentParts: []models.ContentPart{
		{Type: "audio", Media: &models.MediaRef{MimeType: "audio/wav", Data: "AAAA"}},
	}}
	_, err := mr.anthropicContent(context.Background(), testProv("anthropic"), "claude-sonnet-4", msg)
	if err == nil || !strings.Contains(err.Error(), "transcribe") {
		t.Fatalf("expected Anthropic to refuse audio with a pointer to STT, got %v", err)
	}
}

func TestMediaWithoutMimeTypeIsRejected(t *testing.T) {
	mr := newMapperRouter(t)
	if _, err := mr.resolveMedia(context.Background(), &models.MediaRef{Data: "aGk="}); err == nil {
		t.Fatal("a media part without a mime type must be rejected")
	}
}

func TestGeminiInlineImageAndVideoParts(t *testing.T) {
	mr := newMapperRouter(t)
	msg := models.ChatMessage{Role: "user", Content: "describe", ContentParts: []models.ContentPart{
		{Type: "image", Media: &models.MediaRef{MimeType: "image/jpeg", Data: "aGk="}},
		{Type: "video", Media: &models.MediaRef{MimeType: "video/mp4", URL: "https://example.com/v.mp4"}},
	}}
	parts, err := mr.geminiUserParts(context.Background(), &models.ModelProvider{Kind: "gemini"}, "gemini-1.5-pro", msg)
	if err != nil {
		t.Fatal(err)
	}
	goldenJSON(t, parts, `[
		{"text":"describe"},
		{"inlineData":{"mimeType":"image/jpeg","data":"aGk="}},
		{"fileData":{"mimeType":"video/mp4","fileUri":"https://example.com/v.mp4"}}
	]`)
}

func TestGeminiTextOnlyUserTurnIsUnchanged(t *testing.T) {
	mr := newMapperRouter(t)
	parts, err := mr.geminiUserParts(context.Background(), &models.ModelProvider{Kind: "gemini"}, "gemini-1.5-pro", models.ChatMessage{Role: "user", Content: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	goldenJSON(t, parts, `[{"text":"hi"}]`)
}

func TestVideoRefusedForAModelWithoutVideo(t *testing.T) {
	mr := newMapperRouter(t)
	msg := models.ChatMessage{Role: "user", ContentParts: []models.ContentPart{
		{Type: "video", Media: &models.MediaRef{MimeType: "video/mp4", Data: "AAAA"}},
	}}
	if _, err := mr.openAIContent(context.Background(), testProv("ollama"), "llama3", msg); err == nil {
		t.Fatal("a provider without video support must refuse video, not drop it")
	}
}

func testProv(kind string) *models.ModelProvider {
	return &models.ModelProvider{Name: kind + "-p", Kind: kind}
}

func TestOpenAIPDFIsUploadedOnceAndReferencedByFileID(t *testing.T) {
	var uploads int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/files" {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(&uploads, 1)
		_, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"id":"file-abc123"}`))
	}))
	defer srv.Close()

	mr := newMapperRouter(t)
	ref, err := mr.blobs.Put(context.Background(), "application/pdf", "report.pdf", []byte("%PDF-1.7 body"))
	if err != nil {
		t.Fatal(err)
	}
	prov := &models.ModelProvider{Name: "oai", Kind: "openai", Endpoint: srv.URL, Config: map[string]interface{}{"api_key": "sk"}}
	msg := models.ChatMessage{Role: "user", ContentParts: []models.ContentPart{
		{Type: "file", Media: &models.MediaRef{MimeType: "application/pdf", BlobRef: ref, Name: "report.pdf"}},
	}}

	for i := 0; i < 2; i++ {
		got, err := mr.openAIContent(context.Background(), prov, "gpt-4o", msg)
		if err != nil {
			t.Fatal(err)
		}
		goldenJSON(t, got, `[{"type":"file","file":{"file_id":"file-abc123"}}]`)
	}
	if n := atomic.LoadInt32(&uploads); n != 1 {
		t.Fatalf("the PDF must be uploaded once and reused by file id, got %d uploads", n)
	}
}

// pdfWithText builds a minimal one-page PDF showing text, for fallback tests.
func pdfWithText(text string) []byte {
	stream := "BT /F1 24 Tf 72 720 Td (" + text + ") Tj ET"
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		"<< /Length " + strconv.Itoa(len(stream)) + " >>\nstream\n" + stream + "\nendstream",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	offs := make([]int, len(objs))
	for i, o := range objs {
		offs[i] = b.Len()
		b.WriteString(strconv.Itoa(i+1) + " 0 obj\n" + o + "\nendobj\n")
	}
	xref := b.Len()
	b.WriteString("xref\n0 " + strconv.Itoa(len(objs)+1) + "\n0000000000 65535 f \n")
	for _, off := range offs {
		b.WriteString(fmt.Sprintf("%010d 00000 n \n", off))
	}
	b.WriteString("trailer\n<< /Size " + strconv.Itoa(len(objs)+1) + " /Root 1 0 R >>\nstartxref\n" + strconv.Itoa(xref) + "\n%%EOF\n")
	return []byte(b.String())
}

func TestPDFFallsBackToExtractedTextForModelsWithoutPDFSupport(t *testing.T) {
	mr := newMapperRouter(t)
	raw := pdfWithText("Refund policy thirty days")
	msg := models.ChatMessage{Role: "user", Content: "summarise", ContentParts: []models.ContentPart{
		{Type: "file", Media: &models.MediaRef{MimeType: "application/pdf", Data: base64.StdEncoding.EncodeToString(raw), Name: "policy.pdf"}},
	}}
	got, err := mr.openAIContent(context.Background(), testProv("openai"), "gpt-3.5-turbo", msg)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got)
	if !strings.Contains(string(b), "Refund policy thirty days") || !strings.Contains(string(b), "[Document: policy.pdf]") {
		t.Fatalf("expected the PDF text as a labelled text part, got %s", b)
	}
	if strings.Contains(string(b), `"type":"file"`) {
		t.Fatal("a model without PDF support must not receive a file part")
	}
}

func TestAnthropicPDFIsUploadedOnceAndReferencedByFileID(t *testing.T) {
	var uploads int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/files" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("anthropic-beta") != anthropicFilesBeta {
			t.Errorf("upload must carry the files beta header")
		}
		atomic.AddInt32(&uploads, 1)
		_, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"id":"file_011abc"}`))
	}))
	defer srv.Close()

	mr := newMapperRouter(t)
	ref, err := mr.blobs.Put(context.Background(), "application/pdf", "contract.pdf", []byte("%PDF-1.7 contract"))
	if err != nil {
		t.Fatal(err)
	}
	prov := &models.ModelProvider{Name: "claude", Kind: "anthropic", Endpoint: srv.URL, Config: map[string]interface{}{"api_key": "k"}}
	msg := models.ChatMessage{Role: "user", ContentParts: []models.ContentPart{
		{Type: "file", Media: &models.MediaRef{MimeType: "application/pdf", BlobRef: ref, Name: "contract.pdf"}},
	}}
	for i := 0; i < 2; i++ {
		got, err := mr.anthropicContent(context.Background(), prov, "claude-sonnet-4", msg)
		if err != nil {
			t.Fatal(err)
		}
		goldenJSON(t, got, `[{"type":"document","source":{"type":"file","file_id":"file_011abc"}}]`)
	}
	if n := atomic.LoadInt32(&uploads); n != 1 {
		t.Fatalf("expected one upload reused by id, got %d", n)
	}
}

func TestProviderCanSwitchPDFOffOrForceItNative(t *testing.T) {
	mr := newMapperRouter(t)
	msg := models.ChatMessage{Role: "user", Content: "summarise", ContentParts: []models.ContentPart{
		{Type: "file", Media: &models.MediaRef{MimeType: "application/pdf", Data: base64.StdEncoding.EncodeToString(pdfWithText("hello")), Name: "a.pdf"}},
	}}

	off := testProv("openai")
	off.Config = map[string]interface{}{"modalities": map[string]interface{}{"pdf": map[string]interface{}{"enabled": false}}}
	if _, err := mr.openAIContent(context.Background(), off, "gpt-4o", msg); err == nil || !strings.Contains(err.Error(), "does not accept document input") {
		t.Fatalf("a provider with pdf switched off must refuse PDFs (not fall back to text), got %v", err)
	}
	if _, err := mr.openAIContent(context.Background(), off, "gpt-3.5-turbo", msg); err == nil {
		t.Fatal("pdf off applies to models that would otherwise use the text fallback too")
	}

	native := testProv("openai")
	native.Config = map[string]interface{}{"modalities": map[string]interface{}{"pdf": map[string]interface{}{"enabled": true}}}
	got, err := mr.openAIContent(context.Background(), native, "gpt-3.5-turbo", msg)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(got); !strings.Contains(string(b), `"type":"file"`) {
		t.Fatalf("pdf enabled on a gateway model the catalog does not know must send the file natively, got %s", b)
	}
}
