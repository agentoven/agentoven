package router

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/blobstore"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

const (
	testDOCXMime = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	testCSVMime  = "text/csv"
)

func testDOCX(t *testing.T, text string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range [][2]string{
		{"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`},
		{"_rels/.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="r" Type="x/officeDocument" Target="word/document.xml"/></Relationships>`},
		{"word/document.xml", `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>` + text + `</w:t></w:r></w:p></w:body></w:document>`},
	} {
		w, _ := zw.Create(f[0])
		_, _ = w.Write([]byte(f[1]))
	}
	_ = zw.Close()
	return buf.Bytes()
}

func docRequest(parts ...models.ContentPart) *models.RouteRequest {
	return &models.RouteRequest{Kitchen: "team-a", Messages: []models.ChatMessage{{Role: "user", Content: "summarise", ContentParts: parts}}}
}

func TestAttachedDocumentsBecomeTextBeforeTheCall(t *testing.T) {
	mr := newMapperRouter(t)
	ref, err := mr.blobs.Put(context.Background(), "team-a", testDOCXMime, "claim.docx", testDOCX(t, "Garage invoice 420.00"))
	if err != nil {
		t.Fatal(err)
	}
	prov := &models.ModelProvider{Name: "p", Kitchen: "team-a", Kind: "openai"}
	req := docRequest(
		models.ContentPart{Type: "file", Media: &models.MediaRef{MimeType: testDOCXMime, BlobRef: ref, Name: "claim.docx"}},
		models.ContentPart{Type: "file", Media: &models.MediaRef{MimeType: testCSVMime, Name: "lines.csv", Data: base64.StdEncoding.EncodeToString([]byte("item,qty\nbolt,4\n"))}},
		models.ContentPart{Type: "file", Media: &models.MediaRef{MimeType: "application/pdf", Data: "JVBERg=="}},
	)
	got, err := mr.withDocuments(context.Background(), prov, req)
	if err != nil {
		t.Fatal(err)
	}
	parts := got.Messages[0].ContentParts
	if parts[0].Type != "text" || !strings.Contains(parts[0].Text, "Garage invoice 420.00") || !strings.Contains(parts[0].Text, "[Attached file: claim.docx (docx)") {
		t.Errorf("the Word file must become labelled text: %+v", parts[0])
	}
	if parts[1].Type != "text" || !strings.Contains(parts[1].Text, "| bolt | 4 |") {
		t.Errorf("the CSV must become a table: %+v", parts[1])
	}
	if parts[2].Type != "file" {
		t.Errorf("a PDF is left for the driver: %+v", parts[2])
	}
	if req.Messages[0].ContentParts[0].Type != "file" {
		t.Error("the caller's request must not be modified")
	}
	if partKind(parts[0]) != "text" {
		t.Error("every driver now sees a plain text part")
	}

	plain := &models.RouteRequest{Messages: []models.ChatMessage{{Role: "user", Content: "hi"}}}
	if same, _ := mr.withDocuments(context.Background(), prov, plain); same != plain {
		t.Error("a request without documents is passed on as it is")
	}
}

func TestAttachedDocumentFailuresAreTheCallersNotTheProviders(t *testing.T) {
	mr := newMapperRouter(t)
	ref, _ := mr.blobs.Put(context.Background(), "team-a", testDOCXMime, "claim.docx", testDOCX(t, "secret of team a"))
	badRef, _ := mr.blobs.Put(context.Background(), "team-a", testDOCXMime, "broken.docx", []byte("PK not really a zip"))
	a := &models.ModelProvider{Name: "p", Kitchen: "team-a", Kind: "openai"}
	b := &models.ModelProvider{Name: "p", Kitchen: "team-b", Kind: "openai"}
	media := func(m models.MediaRef) *models.RouteRequest {
		return docRequest(models.ContentPart{Type: "file", Media: &m})
	}

	if _, err := mr.withDocuments(context.Background(), b, media(models.MediaRef{MimeType: testDOCXMime, BlobRef: ref})); !errors.Is(err, blobstore.ErrNotFound) {
		t.Errorf("another kitchen's blob is not found, got %v", err)
	}
	if _, err := mr.withDocuments(context.Background(), a, media(models.MediaRef{MimeType: testDOCXMime, BlobRef: badRef, Name: "broken.docx"})); !IsMediaError(err) || !strings.Contains(err.Error(), "broken.docx") {
		t.Errorf("a damaged file is refused with its name, got %v", err)
	}
	if _, err := mr.withDocuments(context.Background(), a, media(models.MediaRef{MimeType: testCSVMime, URL: "https://example.com/x.csv"})); !IsMediaError(err) {
		t.Errorf("a URL is never fetched here, got %v", err)
	}
	if _, err := mr.withDocuments(context.Background(), a, media(models.MediaRef{MimeType: testCSVMime, Data: "!!not base64"})); !IsMediaError(err) {
		t.Errorf("bad base64 is refused, got %v", err)
	}

	// Sixteen attachments cannot add sixteen documents' worth of text.
	big := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("word ", 40_000)))
	var parts []models.ContentPart
	for i := 0; i < 8; i++ {
		parts = append(parts, models.ContentPart{Type: "file", Media: &models.MediaRef{MimeType: "text/plain", Data: big, Name: "big.txt"}})
	}
	if _, err := mr.withDocuments(context.Background(), a, docRequest(parts...)); !IsMediaError(err) || !strings.Contains(err.Error(), "together") {
		t.Errorf("the request-wide text budget must hold, got %v", err)
	}
}
