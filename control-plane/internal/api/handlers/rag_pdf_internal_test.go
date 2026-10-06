package handlers

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func onePagePDF(text string) []byte {
	stream := "BT /F1 24 Tf 72 720 Td (" + text + ") Tj ET"
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	offs := make([]int, len(objs))
	for i, o := range objs {
		offs[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return []byte(b.String())
}

func TestNormalizeDocumentsExtractsPDFText(t *testing.T) {
	docs, err := normalizeDocuments([]models.RawDocument{
		{ID: "p", Content: base64.StdEncoding.EncodeToString(onePagePDF("Warranty covers two years")), MIMEType: "application/pdf"},
		{ID: "t", Content: "plain notes", MIMEType: "text/plain"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(docs[0].Content, "Warranty covers two years") || docs[0].MIMEType != "text/plain" {
		t.Fatalf("pdf should become indexable text, got %+v", docs[0])
	}
	if docs[1].Content != "plain notes" {
		t.Fatalf("text documents must pass through unchanged, got %+v", docs[1])
	}
}

func TestNormalizeDocumentsRejectsBadPDF(t *testing.T) {
	_, err := normalizeDocuments([]models.RawDocument{{Content: "%%%not-base64", MIMEType: "application/pdf"}})
	if err == nil {
		t.Fatal("expected a non-base64 PDF to be rejected")
	}
	_, err = normalizeDocuments([]models.RawDocument{{Content: base64.StdEncoding.EncodeToString([]byte("junk")), MIMEType: "application/pdf"}})
	if err == nil {
		t.Fatal("expected a non-PDF payload to be rejected")
	}
}
