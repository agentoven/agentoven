package documents

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"path"
	"strings"
)

// Format is a document format this package can turn into text.
type Format string

const (
	FormatText     Format = "text"
	FormatMarkdown Format = "markdown"
	FormatCSV      Format = "csv"
	FormatTSV      Format = "tsv"
	FormatHTML     Format = "html"
	FormatRTF      Format = "rtf"
	FormatDOCX     Format = "docx"
	FormatXLSX     Format = "xlsx"
	FormatPDF      Format = "pdf"
	// FormatImage has no text layer to read: it is recognised so that a caller can say "this needs
	// OCR" instead of "unsupported", and is only turned into text by OCR (see OCR).
	FormatImage Format = "image"
)

const (
	mimeDOCX = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	mimeXLSX = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
)

// ErrUnsupported is returned for a format this package does not read (a legacy .doc or .xls, an
// archive, an executable). The error text says what the caller can do instead.
var ErrUnsupported = errors.New("documents: unsupported format")

// ErrTooLarge is returned when the input or something inside it exceeds a limit that is an error
// rather than a truncation (an archive that expands too far, an entry count, XML nested too deep).
var ErrTooLarge = errors.New("documents: document exceeds a safety limit")

// Detect decides a document's format from its declared MIME type, falling back to its file name's
// extension when the type says nothing (octet-stream, empty). It never reads the bytes: the readers
// validate their own input, so a wrong declaration produces a parse error, not a misread.
func Detect(mimeType, name string) (Format, bool) {
	m := normalizeMime(mimeType)
	if f, ok := formatOfMime(m); ok {
		return f, true
	}
	if m == "" || m == "application/octet-stream" || m == "application/zip" || m == "binary/octet-stream" {
		return formatOfExt(name)
	}
	return "", false
}

// IsTextExtractable reports whether Detect finds a format that Extract turns into text without a
// model: everything but images (OCR) and the formats it does not read. PDFs are included; a
// scanned one yields no text and says so (Result.NeedsOCR).
func IsTextExtractable(mimeType, name string) bool {
	f, ok := Detect(mimeType, name)
	return ok && f != FormatImage
}

// IsOfficeOrText reports whether the document is one of the formats no model provider accepts as a
// native attachment (and so must be converted to text before the call): everything Detect knows but
// PDFs and images, which providers take themselves.
func IsOfficeOrText(mimeType, name string) bool {
	f, ok := Detect(mimeType, name)
	return ok && f != FormatPDF && f != FormatImage
}

func normalizeMime(m string) string {
	m = strings.TrimSpace(m)
	if m == "" {
		return ""
	}
	if mt, _, err := mime.ParseMediaType(m); err == nil {
		return strings.ToLower(mt)
	}
	mt, _, _ := strings.Cut(m, ";")
	return strings.ToLower(strings.TrimSpace(mt))
}

func formatOfMime(m string) (Format, bool) {
	switch m {
	case mimeDOCX:
		return FormatDOCX, true
	case mimeXLSX:
		return FormatXLSX, true
	case "text/html", "application/xhtml+xml":
		return FormatHTML, true
	case "text/csv", "application/csv":
		return FormatCSV, true
	case "text/tab-separated-values", "text/tsv":
		return FormatTSV, true
	case "text/markdown", "text/x-markdown":
		return FormatMarkdown, true
	case "application/rtf", "text/rtf":
		return FormatRTF, true
	case "application/pdf":
		return FormatPDF, true
	case "application/json", "application/xml", "text/xml", "application/x-ndjson", "application/yaml", "text/yaml":
		return FormatText, true
	}
	switch {
	case strings.HasPrefix(m, "text/"):
		return FormatText, true
	case strings.HasPrefix(m, "image/"):
		return FormatImage, true
	}
	return "", false
}

func formatOfExt(name string) (Format, bool) {
	switch strings.ToLower(path.Ext(name)) {
	case ".docx":
		return FormatDOCX, true
	case ".xlsx":
		return FormatXLSX, true
	case ".html", ".htm", ".xhtml":
		return FormatHTML, true
	case ".csv":
		return FormatCSV, true
	case ".tsv", ".tab":
		return FormatTSV, true
	case ".md", ".markdown":
		return FormatMarkdown, true
	case ".rtf":
		return FormatRTF, true
	case ".pdf":
		return FormatPDF, true
	case ".txt", ".text", ".log", ".json", ".xml", ".yaml", ".yml", ".ndjson":
		return FormatText, true
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".tif", ".tiff", ".bmp":
		return FormatImage, true
	}
	return "", false
}

// Limits bound what one extraction may cost. A zero field takes its default (see DefaultLimits), so
// a caller sets only what it wants tighter.
type Limits struct {
	// MaxInputBytes is the largest document accepted. It is the blob store's per-file limit by default.
	MaxInputBytes int64
	// MaxTextBytes caps the text one document contributes, so a single file cannot fill a model's
	// context. Text past it is dropped and Result.Truncated is set; it is never an error.
	MaxTextBytes int
	// MaxZipEntries, MaxPartBytes and MaxRatio bound an Office file (a zip): the number of entries,
	// the bytes any one part may expand to, and how many times its compressed size that may be.
	MaxZipEntries int
	MaxPartBytes  int64
	MaxRatio      int
	// MaxXMLDepth bounds element nesting in the XML parts and in HTML.
	MaxXMLDepth int
	// MaxSheets, MaxCells, MaxCols bound a workbook: sheets read, cells read in all, columns kept per row.
	MaxSheets int
	MaxCells  int
	MaxCols   int
	// MaxPages bounds the pages of a PDF read (and, for OCR, transcribed).
	MaxPages int
}

// DefaultLimits are the limits applied when a field is left zero.
func DefaultLimits() Limits {
	return Limits{
		MaxInputBytes: 25 << 20,
		MaxTextBytes:  200_000,
		MaxZipEntries: 2000,
		MaxPartBytes:  64 << 20,
		MaxRatio:      200,
		MaxXMLDepth:   128,
		MaxSheets:     64,
		MaxCells:      500_000,
		MaxCols:       256,
		MaxPages:      200,
	}
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxInputBytes <= 0 {
		l.MaxInputBytes = d.MaxInputBytes
	}
	if l.MaxTextBytes <= 0 {
		l.MaxTextBytes = d.MaxTextBytes
	}
	if l.MaxZipEntries <= 0 {
		l.MaxZipEntries = d.MaxZipEntries
	}
	if l.MaxPartBytes <= 0 {
		l.MaxPartBytes = d.MaxPartBytes
	}
	if l.MaxRatio <= 0 {
		l.MaxRatio = d.MaxRatio
	}
	if l.MaxXMLDepth <= 0 {
		l.MaxXMLDepth = d.MaxXMLDepth
	}
	if l.MaxSheets <= 0 {
		l.MaxSheets = d.MaxSheets
	}
	if l.MaxCells <= 0 {
		l.MaxCells = d.MaxCells
	}
	if l.MaxCols <= 0 {
		l.MaxCols = d.MaxCols
	}
	if l.MaxPages <= 0 {
		l.MaxPages = d.MaxPages
	}
	return l
}

// Block is one piece of a document, in reading order: a heading, a paragraph, a list item or a
// table, with where it came from (page of a PDF, sheet of a workbook).
type Block struct {
	Kind  string `json:"kind"` // heading, paragraph, list_item, table
	Text  string `json:"text,omitempty"`
	Level int    `json:"level,omitempty"` // heading level, 1..6
	Page  int    `json:"page,omitempty"`
	Sheet string `json:"sheet,omitempty"`
	Table *Table `json:"table,omitempty"`
}

// Table is a block of cells. For a worksheet, Columns names the used columns (A, B, ...) and
// RowNumbers the worksheet row each entry of Rows came from, so a reader can cite "C7"; rows that
// are entirely empty are left out, which is why the numbers are listed rather than implied.
type Table struct {
	Range      string     `json:"range,omitempty"` // A1:D12 for a worksheet
	Columns    []string   `json:"columns,omitempty"`
	RowNumbers []int      `json:"row_numbers,omitempty"`
	Rows       [][]string `json:"rows"`
	Truncated  bool       `json:"truncated,omitempty"`
}

// Result is what a document said, with structure.
type Result struct {
	Format Format   `json:"format"`
	Blocks []Block  `json:"blocks"`
	Pages  int      `json:"pages,omitempty"` // PDF: the number of pages the file has
	Sheets []string `json:"sheets,omitempty"`
	// Warnings are things the reader skipped or could not do (hidden sheets, a page without a
	// text layer, truncation). A caller that shows the text should show these with it.
	Warnings []string `json:"warnings,omitempty"`
	// Truncated is set when a limit cut the document short.
	Truncated bool `json:"truncated,omitempty"`
	// NeedsOCR is set when part of the document is an image of text the reader cannot read: a
	// scanned PDF, or an image file. EmptyPages lists the PDF pages concerned.
	NeedsOCR   bool  `json:"needs_ocr,omitempty"`
	EmptyPages []int `json:"empty_pages,omitempty"`
}

func (r *Result) warn(format string, args ...interface{}) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// Extract reads a document of the given format into blocks. The bytes are never executed or
// written anywhere; every reader is bounded by lim (see Limits). A document that is damaged or
// larger than a hard limit is an error; one that is merely long is cut at MaxTextBytes.
func Extract(ctx context.Context, f Format, data []byte, lim Limits) (*Result, error) {
	lim = lim.withDefaults()
	if int64(len(data)) > lim.MaxInputBytes {
		return nil, fmt.Errorf("%w: the file is %d bytes, the limit is %d", ErrTooLarge, len(data), lim.MaxInputBytes)
	}
	if len(data) == 0 {
		return nil, errors.New("documents: the file is empty")
	}
	var (
		res *Result
		err error
	)
	switch f {
	case FormatText, FormatMarkdown:
		res, err = extractPlain(data, lim)
	case FormatCSV, FormatTSV:
		res, err = extractDelimited(f, data, lim)
	case FormatHTML:
		res, err = extractHTML(ctx, data, lim)
	case FormatRTF:
		res, err = extractRTF(ctx, data, lim)
	case FormatDOCX:
		res, err = extractDOCX(ctx, data, lim)
	case FormatXLSX:
		res, err = extractXLSX(ctx, data, lim)
	case FormatPDF:
		res, err = extractPDF(ctx, data, lim)
	case FormatImage:
		res = &Result{Format: FormatImage, NeedsOCR: true}
		res.warn("an image has no text layer; run OCR to read it")
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupported, f)
	}
	if err != nil {
		return nil, err
	}
	res.Format = f
	return res, nil
}

// ExtractFile is Extract with the format taken from the MIME type and file name. A format that is
// not recognised is ErrUnsupported with the reason a caller can show.
func ExtractFile(ctx context.Context, mimeType, name string, data []byte, lim Limits) (*Result, error) {
	f, ok := Detect(mimeType, name)
	if !ok {
		return nil, fmt.Errorf("%w: %s %q cannot be read as text (supported: pdf, docx, xlsx, html, csv, tsv, markdown, rtf, plain text; legacy .doc and .xls must be saved as docx/xlsx first)", ErrUnsupported, nonEmpty(normalizeMime(mimeType), "unknown type"), name)
	}
	return Extract(ctx, f, data, lim)
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
