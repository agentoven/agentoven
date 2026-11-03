// Package documents turns document bytes into text, in-process and without
// external tools, so documents work on every deployment target (including
// serverless functions with no sidecar). See ADR-0039 for what it reads, what
// it does not, and how it relates to the knowledge engine's richer parsers.
package documents

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/ledongthuc/pdf"
)

// maxExtractedBytes bounds how much text PDFText returns for one document, so
// one large PDF cannot blow a model's context window on its own.
const maxExtractedBytes = 200_000

// PDFText extracts the plain text of every page, in order, without page labels
// (the form retrieval indexing wants; Extract keeps the pages).
func PDFText(data []byte) (string, error) {
	pages, _, err := readPDFPages(context.Background(), data, DefaultLimits().MaxPages)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(strings.Join(pages, "\n"))
	if len(text) > maxExtractedBytes {
		text = cutRunes(text, maxExtractedBytes) + "\n[truncated]"
	}
	return text, nil
}

// readPDFPages returns the text of up to maxPages pages and the page count of the file. The PDF
// library panics on some damaged files, so any panic is returned as an error: a hostile upload
// must not take the server down.
func readPDFPages(ctx context.Context, data []byte, maxPages int) (pages []string, total int, err error) {
	defer func() {
		if r := recover(); r != nil {
			pages, total, err = nil, 0, fmt.Errorf("documents: not a readable PDF: %v", r)
		}
	}()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, 0, fmt.Errorf("documents: not a readable PDF: %w", err)
	}
	total = r.NumPage()
	for i := 1; i <= total && i <= maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		text, err := r.Page(i).GetPlainText(nil)
		if err != nil {
			return nil, 0, fmt.Errorf("documents: extracting PDF text from page %d: %w", i, err)
		}
		pages = append(pages, text)
	}
	return pages, total, nil
}

// extractPDF reads a PDF's text layer page by page. A page without any text is probably a scan:
// it is listed (Result.EmptyPages) and Result.NeedsOCR is set instead of returning nothing as if
// the page were blank. Tables are not recovered from a PDF (the text comes out in reading order).
func extractPDF(ctx context.Context, data []byte, lim Limits) (*Result, error) {
	pages, total, err := readPDFPages(ctx, data, lim.MaxPages)
	if err != nil {
		return nil, err
	}
	res := &Result{Pages: total}
	c := newCollector(res, lim)
	for i, text := range pages {
		text = sanitize(text)
		if strings.TrimSpace(text) == "" {
			res.EmptyPages = append(res.EmptyPages, i+1)
			continue
		}
		c.text(Block{Kind: "paragraph", Page: i + 1, Text: text})
	}
	if total > lim.MaxPages {
		res.Truncated = true
		res.warn("the PDF has %d pages; only the first %d were read", total, lim.MaxPages)
	}
	if len(res.EmptyPages) > 0 {
		res.NeedsOCR = true
		res.warn("%d page(s) have no text layer (scanned?): %s", len(res.EmptyPages), pageList(res.EmptyPages))
	}
	return res, nil
}

func pageList(pages []int) string {
	const max = 20
	var b strings.Builder
	for i, p := range pages {
		if i == max {
			fmt.Fprintf(&b, " and %d more", len(pages)-max)
			break
		}
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%d", p)
	}
	return b.String()
}
