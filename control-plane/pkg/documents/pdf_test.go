package documents_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/documents"
)

// minimalPDF builds a one-page PDF whose content stream shows the given text.
func minimalPDF(text string) []byte {
	stream := fmt.Sprintf("BT /F1 24 Tf 72 720 Td (%s) Tj ET", text)
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return []byte(b.String())
}

func TestPDFTextExtractsThePageText(t *testing.T) {
	text, err := documents.PDFText(minimalPDF("Refund policy thirty days"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Refund policy thirty days") {
		t.Fatalf("expected the page text, got %q", text)
	}
}

func TestPDFTextRejectsGarbage(t *testing.T) {
	if _, err := documents.PDFText([]byte("not a pdf at all")); err == nil {
		t.Fatal("expected a non-PDF to be rejected")
	}
}
