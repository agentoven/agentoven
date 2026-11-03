package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/api/middleware"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/blobstore"
	"github.com/agentoven/agentoven/control-plane/pkg/documents"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// Document endpoints (ADR-0039): read what a file says, with its structure, and turn scans into text.
//
//	POST /api/v1/documents/extract  read a blob (blob_ref) or an uploaded file, in-process, no model
//	POST /api/v1/documents/ocr      transcribe an image or a scanned PDF through the kitchen's own provider
//	POST /api/v1/documents/fields   read the fields of a JSON Schema out of a document, each with its quote
//
// Everything is scoped to the caller's kitchen: a blob ref is resolved with the kitchen's ownership
// (another kitchen's ref is a 404, the same as a ref that never existed) and a provider is the
// kitchen's own. extract never calls a model. ocr and fields do, through the model router, so budgets,
// rate limits, the response cache, circuit breakers and traces apply as to any other call; they are
// pinned to the provider the caller names so a document goes nowhere it was not sent on purpose.

// documentSlots bounds how many documents are being read at once: parsing is CPU work done on the
// request goroutine, and an upload endpoint must not be a way to starve the rest of the server.
var documentSlots = make(chan struct{}, max(2, runtime.NumCPU()))

// documentReadTimeout bounds one extraction.
const documentReadTimeout = 30 * time.Second

// maxDocumentJSONBytes bounds the JSON body of the document routes (they carry references, a schema
// and options, not files).
const maxDocumentJSONBytes = 1 << 20

// takeDocumentSlot reserves one reading slot without waiting.
func takeDocumentSlot() (release func(), ok bool) {
	select {
	case documentSlots <- struct{}{}:
		return func() { <-documentSlots }, true
	default:
		return nil, false
	}
}

// documentResponse is what extract returns.
type documentResponse struct {
	BlobRef    string            `json:"blob_ref,omitempty"`
	Name       string            `json:"name,omitempty"`
	MimeType   string            `json:"mime_type"`
	Size       int               `json:"size"`
	Format     documents.Format  `json:"format"`
	Text       string            `json:"text"`
	Blocks     []documents.Block `json:"blocks"`
	Pages      int               `json:"pages,omitempty"`
	Sheets     []string          `json:"sheets,omitempty"`
	Warnings   []string          `json:"warnings,omitempty"`
	Truncated  bool              `json:"truncated,omitempty"`
	NeedsOCR   bool              `json:"needs_ocr,omitempty"`
	EmptyPages []int             `json:"empty_pages,omitempty"`
}

// loadedDocument is a file about to be read, from a blob or an upload.
type loadedDocument struct {
	ref, name, mime string
	data            []byte
}

// extractRequest is the JSON form of the extract request.
type extractRequest struct {
	BlobRef      string `json:"blob_ref"`
	MaxTextBytes int    `json:"max_text_bytes,omitempty"`
}

// ExtractDocument handles POST /api/v1/documents/extract.
func (h *Handlers) ExtractDocument(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	// The slot is taken before the file is read: an upload held in memory is what a flood of requests
	// would otherwise pile up, not only the parsing.
	release, ok := takeDocumentSlot()
	if !ok {
		respondDocumentError(w, errTooBusy)
		return
	}
	defer release()
	var (
		doc          loadedDocument
		maxTextBytes int
	)
	if ct := r.Header.Get("Content-Type"); strings.HasPrefix(ct, "application/json") {
		var req extractRequest
		if !decodeDocumentJSON(w, r, &req) {
			return
		}
		var ok bool
		if doc, ok = h.loadBlob(w, r, kitchen, req.BlobRef); !ok {
			return
		}
		maxTextBytes = req.MaxTextBytes
	} else {
		data, mimeType, name, ok := h.readUpload(w, r, blobstore.MaxBytesFromEnv())
		if !ok {
			return
		}
		doc = loadedDocument{name: name, mime: mimeType, data: data}
	}
	lim := documents.Limits{MaxInputBytes: blobstore.MaxBytesFromEnv()}
	if maxTextBytes > 0 && maxTextBytes < documents.DefaultLimits().MaxTextBytes {
		lim.MaxTextBytes = maxTextBytes
	}
	ctx, cancel := context.WithTimeout(r.Context(), documentReadTimeout)
	defer cancel()
	res, err := documents.ExtractFile(ctx, doc.mime, doc.name, doc.data, lim)
	if err != nil {
		respondDocumentError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, &documentResponse{
		BlobRef: doc.ref, Name: doc.name, MimeType: doc.mime, Size: len(doc.data), Format: res.Format,
		Text: res.Text(0), Blocks: res.Blocks, Pages: res.Pages, Sheets: res.Sheets, Warnings: res.Warnings,
		Truncated: res.Truncated, NeedsOCR: res.NeedsOCR, EmptyPages: res.EmptyPages,
	})
}

// providerChoice names the provider (and model) that reads a document.
type providerChoice struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// ocrOptions is the OCR part of a request.
type ocrOptions struct {
	providerChoice
	MaxPages int `json:"max_pages,omitempty"`
	// Verify asks for a second transcription by another provider or model; see documents.OCRVerify.
	Verify *struct {
		providerChoice
		MinAgreement float64 `json:"min_agreement,omitempty"`
	} `json:"verify,omitempty"`
}

type ocrRequest struct {
	BlobRef string `json:"blob_ref"`
	ocrOptions
}

// OCRDocument handles POST /api/v1/documents/ocr.
func (h *Handlers) OCRDocument(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	var req ocrRequest
	if !decodeDocumentJSON(w, r, &req) {
		return
	}
	doc, ok := h.loadBlob(w, r, kitchen, req.BlobRef)
	if !ok {
		return
	}
	if !h.providerExists(w, r, kitchen, req.Provider) {
		return
	}
	res, err := h.runOCR(r.Context(), kitchen, doc, req.ocrOptions)
	if err != nil {
		respondDocumentError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"blob_ref": doc.ref, "name": doc.name, "format": res.Format, "pages": res.Pages, "text": res.Text(),
		"confidence": res.Confidence, "incomplete": res.Incomplete, "warnings": res.Warnings,
		"provider": res.Provider, "model": res.Model, "usage": res.Usage,
		"confidence_note": "confidence describes what was checked, not a probability: " + documents.ConfidenceUnverified +
			" is one model's reading with no second pass",
	})
}

func (h *Handlers) runOCR(ctx context.Context, kitchen string, doc loadedDocument, o ocrOptions) (*documents.OCRResult, error) {
	opts := documents.OCROptions{
		Kitchen: kitchen, Provider: o.Provider, Model: o.Model, MaxPages: o.MaxPages,
		Externalize: h.Router.ExternalizeMedia, Limits: documents.Limits{MaxInputBytes: blobstore.MaxBytesFromEnv()},
	}
	if o.Verify != nil {
		opts.Verify = &documents.OCRVerify{Provider: o.Verify.Provider, Model: o.Verify.Model, MinAgreement: o.Verify.MinAgreement}
	}
	// One provider call per page can take a while; the caller's own deadline and cancellation apply.
	return documents.OCR(ctx, h.Router, doc.name, doc.mime, doc.data, opts)
}

type fieldsRequest struct {
	BlobRef      string                 `json:"blob_ref"`
	Text         string                 `json:"text"`
	Schema       map[string]interface{} `json:"schema"`
	Instructions string                 `json:"instructions"`
	providerChoice
	// OCR, when set, reads the pages of a scanned document first (with its own provider and model,
	// which default to the ones that read the fields).
	OCR *ocrOptions `json:"ocr,omitempty"`
}

// ExtractDocumentFields handles POST /api/v1/documents/fields.
func (h *Handlers) ExtractDocumentFields(w http.ResponseWriter, r *http.Request) {
	kitchen := middleware.GetKitchen(r.Context())
	var req fieldsRequest
	if !decodeDocumentJSON(w, r, &req) {
		return
	}
	if (req.BlobRef == "") == (req.Text == "") {
		respondError(w, http.StatusBadRequest, "give exactly one of blob_ref and text")
		return
	}
	if !h.providerExists(w, r, kitchen, req.Provider) {
		return
	}
	var pages []documents.PageText
	var warnings []string
	if req.Text != "" {
		pages = []documents.PageText{{Text: req.Text}}
	} else {
		doc, ok := h.loadBlob(w, r, kitchen, req.BlobRef)
		if !ok {
			return
		}
		var err error
		if pages, warnings, err = h.documentPages(r, kitchen, doc, req.OCR, req.providerChoice); err != nil {
			respondDocumentError(w, err)
			return
		}
	}
	res, err := documents.ExtractFields(r.Context(), h.Router, documents.FieldsOptions{
		Kitchen: kitchen, Provider: req.Provider, Model: req.Model, Schema: req.Schema, Instructions: req.Instructions, Pages: pages,
	})
	if err != nil {
		respondDocumentError(w, err)
		return
	}
	res.Warnings = append(warnings, res.Warnings...)
	respondJSON(w, http.StatusOK, res)
}

// documentPages reads a document into pages of text: in-process when it has a text layer, and by
// OCR for the pages that do not when the request asks for OCR.
func (h *Handlers) documentPages(r *http.Request, kitchen string, doc loadedDocument, ocr *ocrOptions, def providerChoice) ([]documents.PageText, []string, error) {
	release, ok := takeDocumentSlot()
	if !ok {
		return nil, nil, errTooBusy
	}
	ctx, cancel := context.WithTimeout(r.Context(), documentReadTimeout)
	defer cancel()
	res, err := documents.ExtractFile(ctx, doc.mime, doc.name, doc.data, documents.Limits{MaxInputBytes: blobstore.MaxBytesFromEnv()})
	release()
	if err != nil {
		return nil, nil, err
	}
	if !res.NeedsOCR {
		return res.PageTexts(), res.Warnings, nil
	}
	if ocr == nil {
		if len(res.Blocks) == 0 {
			return nil, nil, errNeedsOCR
		}
		return res.PageTexts(), append(res.Warnings, "some pages have no text and were not read: add an \"ocr\" object to read them"), nil
	}
	if ocr.Provider == "" {
		ocr.providerChoice = def
	}
	if !h.providerKnown(r, kitchen, ocr.Provider) {
		return nil, nil, &documentRequestError{status: http.StatusNotFound, msg: "provider " + ocr.Provider + " not found"}
	}
	read, err := h.runOCR(r.Context(), kitchen, doc, *ocr)
	if err != nil {
		return nil, nil, err
	}
	pages := make([]documents.PageText, 0, len(read.Pages))
	for _, p := range read.Pages {
		if strings.TrimSpace(p.Text) != "" {
			pages = append(pages, documents.PageText{Page: p.Page, Text: p.Text})
		}
	}
	return pages, append(read.Warnings, "scanned pages were transcribed by "+read.Provider+" with confidence "+read.Confidence), nil
}

// loadBlob reads a blob the caller's kitchen owns. A missing, malformed or foreign ref is one 404.
func (h *Handlers) loadBlob(w http.ResponseWriter, r *http.Request, kitchen, ref string) (loadedDocument, bool) {
	store := h.blobStore(w)
	if store == nil {
		return loadedDocument{}, false
	}
	if ref == "" {
		respondError(w, http.StatusBadRequest, "blob_ref is required (upload the file with POST /api/v1/blobs first, or send the file itself to extract)")
		return loadedDocument{}, false
	}
	blob, err := store.Get(r.Context(), kitchen, ref)
	if err != nil {
		respondError(w, http.StatusNotFound, "blob not found")
		return loadedDocument{}, false
	}
	return loadedDocument{ref: ref, name: blob.Name, mime: blob.MimeType, data: blob.Data}, true
}

func (h *Handlers) providerKnown(r *http.Request, kitchen, name string) bool {
	if name == "" {
		return false
	}
	_, err := h.Store.GetProvider(r.Context(), kitchen, name)
	return err == nil
}

func (h *Handlers) providerExists(w http.ResponseWriter, r *http.Request, kitchen, name string) bool {
	if name == "" {
		respondError(w, http.StatusBadRequest, "provider is required: name the provider (and model) that reads the document; it is the only one the document is sent to")
		return false
	}
	if _, err := h.Store.GetProvider(r.Context(), kitchen, name); err != nil {
		var nf *store.ErrNotFound
		if errors.As(err, &nf) {
			respondError(w, http.StatusNotFound, "provider "+name+" not found")
		} else {
			respondError(w, http.StatusInternalServerError, "could not look up the provider")
		}
		return false
	}
	return true
}

func decodeDocumentJSON(w http.ResponseWriter, r *http.Request, into interface{}) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDocumentJSONBytes))
	if err := dec.Decode(into); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			respondError(w, http.StatusRequestEntityTooLarge, "the request body is larger than 1 MB")
		} else {
			respondError(w, http.StatusBadRequest, "invalid request body")
		}
		return false
	}
	return true
}

var (
	errTooBusy  = &documentRequestError{status: http.StatusTooManyRequests, msg: "too many documents are being read; retry shortly"}
	errNeedsOCR = &documentRequestError{status: http.StatusUnprocessableEntity, msg: "the document has no text layer (a scan or an image): add an \"ocr\" object to read it with a vision model"}
)

// documentRequestError is a refusal that carries its own status.
type documentRequestError struct {
	status int
	msg    string
}

func (e *documentRequestError) Error() string { return e.msg }

// respondDocumentError answers an error from reading a document. A problem with the file or the
// request is a 4xx that says what; a failure of a model call keeps its meaning (a spent budget is
// 402, a rate limit 429) and any other provider failure is a 502.
func respondDocumentError(w http.ResponseWriter, err error) {
	var (
		re *documentRequestError
		pe *documents.ProviderError
	)
	switch {
	case errors.As(err, &re):
		if re.status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "2")
		}
		respondError(w, re.status, re.msg)
	case errors.Is(err, context.DeadlineExceeded):
		respondError(w, http.StatusGatewayTimeout, "reading the document took too long")
	case errors.Is(err, context.Canceled):
		respondError(w, 499, "the request was cancelled")
	case errors.Is(err, models.ErrSpendRefused):
		respondError(w, http.StatusPaymentRequired, err.Error())
	case errors.Is(err, models.ErrRateLimited):
		w.Header().Set("Retry-After", "2")
		respondError(w, http.StatusTooManyRequests, err.Error())
	case errors.As(err, &pe):
		respondError(w, http.StatusBadGateway, err.Error())
	case errors.Is(err, documents.ErrUnsupported):
		respondError(w, http.StatusUnsupportedMediaType, err.Error())
	case errors.Is(err, documents.ErrTooLarge):
		respondError(w, http.StatusRequestEntityTooLarge, err.Error())
	default:
		respondError(w, http.StatusUnprocessableEntity, err.Error())
	}
}
