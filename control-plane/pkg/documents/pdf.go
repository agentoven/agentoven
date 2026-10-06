// Package documents turns document bytes into text, in-process and without
// external tools, so PDFs work on every deployment target (including
// serverless functions with no sidecar).
package documents

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/ledongthuc/pdf"
)

// maxExtractedBytes bounds how much text a single document can contribute, so
// one large PDF cannot blow a model's context window on its own.
const maxExtractedBytes = 200_000

// PDFText extracts the plain text of every page, in order.
func PDFText(data []byte) (string, error) {
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("documents: not a readable PDF: %w", err)
	}
	reader, err := r.GetPlainText()
	if err != nil {
		return "", fmt.Errorf("documents: extracting PDF text: %w", err)
	}
	var b bytes.Buffer
	if _, err := b.ReadFrom(reader); err != nil {
		return "", fmt.Errorf("documents: reading PDF text: %w", err)
	}
	text := strings.TrimSpace(b.String())
	if len(text) > maxExtractedBytes {
		text = text[:maxExtractedBytes] + "\n[truncated]"
	}
	return text, nil
}
