package router

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"strings"

	"github.com/agentoven/agentoven/control-plane/pkg/blobstore"
	"github.com/agentoven/agentoven/control-plane/pkg/documents"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/rs/zerolog/log"
)

// MediaError is a media part the router refuses before anything is sent to a
// provider: the model cannot take that kind of input, or the part itself is
// malformed. It is the caller's problem, not the provider's, so an HTTP layer
// should answer 4xx, not 5xx (see IsMediaError).
type MediaError struct{ msg string }

func (e *MediaError) Error() string { return e.msg }

func mediaErrorf(format string, args ...interface{}) error {
	return &MediaError{msg: fmt.Sprintf(format, args...)}
}

type blobKitchenKey struct{}

// withBlobKitchen records which kitchen's blobs a request may read: the kitchen of the
// provider it is sent through (the agent's own), or the default kitchen when the provider has none.
// A blob ref that kitchen does not own is "not found", whatever the ref says.
func withBlobKitchen(ctx context.Context, prov *models.ModelProvider) context.Context {
	k := prov.Kitchen
	if k == "" {
		k = "default"
	}
	return context.WithValue(ctx, blobKitchenKey{}, k)
}

// blobKitchen is the kitchen set by withBlobKitchen; empty (which owns nothing) when unset,
// so a code path that forgot to set it fails closed.
func blobKitchen(ctx context.Context) string {
	k, _ := ctx.Value(blobKitchenKey{}).(string)
	return k
}

// Blobs returns the blob store media parts are resolved from, or nil.
func (mr *ModelRouter) Blobs() blobstore.Store { return mr.blobs }

// IsMediaError reports whether err is (or wraps) a refused media part.
func IsMediaError(err error) bool {
	var me *MediaError
	return errors.As(err, &me)
}

// mediaCaps is what a specific provider+model can accept as non-text input.
// Gating happens before any bytes go out: a model that cannot take a modality
// gets a precise error instead of a request the provider silently mishandles.
type mediaCaps struct {
	Vision   bool
	Document bool // accepted natively; without it PDFs are sent as extracted text
	PDFOff   bool // the provider's pdf modality is switched off: PDFs are refused
	Audio    bool
	Video    bool
}

// mediaCapsFor decides capabilities from the catalog (vision) and from the
// provider/model family (document and audio). Conservative: anything not
// clearly known is refused. The provider's modalities config then has the last
// word, so an operator can switch a modality off, or on for a gateway whose
// models the catalog does not know.
func (mr *ModelRouter) mediaCapsFor(prov *models.ModelProvider, model string) mediaCaps {
	caps := mr.inferredMediaCaps(prov.Kind, model)
	if on, set := prov.ModalityEnabled(models.ModalityImage); set {
		caps.Vision = on
	}
	if on, set := prov.ModalityEnabled(models.ModalityVideo); set {
		caps.Video = on
	}
	if on, set := prov.ModalityEnabled(models.ModalityPDF); set {
		caps.Document, caps.PDFOff = on, !on
	}
	return caps
}

func (mr *ModelRouter) inferredMediaCaps(kind, model string) mediaCaps {
	m := strings.ToLower(model)
	var caps mediaCaps

	known := false
	if mr.catalog != nil {
		if c := mr.catalog.Lookup(kind, model); c != nil {
			known = true
			caps.Vision = c.SupportsVision
		}
	}
	// A gateway or a local runner serves whatever model its operator pointed it at, which the
	// catalog cannot know. Trust the operator: pass images through unless the model is known to
	// be text-only or the provider's image modality is switched off. A model the catalog does
	// know to be text-only keeps its clear refusal.
	switch kind {
	case "litellm", "openrouter", "ollama":
		if !known {
			caps.Vision = true
		}
	}

	switch kind {
	case "openai", "azure-openai", "litellm", "openrouter":
		if strings.HasPrefix(m, "gpt-4o") || strings.HasPrefix(m, "gpt-4.1") || strings.HasPrefix(m, "gpt-5") {
			caps.Vision = true
			caps.Document = true
		}
		if strings.Contains(m, "audio") {
			caps.Audio = true
		}
	case "anthropic":
		if strings.HasPrefix(m, "claude-") {
			caps.Vision = true
			caps.Document = true
		}
	case "gemini":
		caps.Vision = true
		caps.Document = true
		caps.Audio = true
		caps.Video = true
	}
	return caps
}

// resolvedMedia is a MediaRef with its bytes loaded (for base64 wire formats)
// or its URL kept as-is.
type resolvedMedia struct {
	mime string
	name string
	url  string
	b64  string
}

// resolveMedia turns a MediaRef into something a wire format can carry. A
// blob ref is loaded from the blob store; inline data is used as given.
func (mr *ModelRouter) resolveMedia(ctx context.Context, ref *models.MediaRef) (resolvedMedia, error) {
	if ref == nil {
		return resolvedMedia{}, mediaErrorf("media part has no media reference")
	}
	if ref.MimeType == "" {
		return resolvedMedia{}, mediaErrorf("media part is missing mime_type")
	}
	out := resolvedMedia{mime: ref.MimeType, name: ref.Name, url: ref.URL}
	switch {
	case ref.BlobRef != "":
		if mr.blobs == nil {
			return resolvedMedia{}, fmt.Errorf("media blob %q referenced but no blob store is configured", ref.BlobRef)
		}
		blob, err := mr.blobs.Get(ctx, blobKitchen(ctx), ref.BlobRef)
		if err != nil {
			return resolvedMedia{}, fmt.Errorf("loading media blob %q: %w", ref.BlobRef, err)
		}
		out.b64 = base64.StdEncoding.EncodeToString(blob.Data)
	case ref.Data != "":
		out.b64 = ref.Data
	case ref.URL != "":
		// URL kept as-is; the caller decides whether this provider accepts URLs.
	default:
		return resolvedMedia{}, mediaErrorf("media part has no url, data, or blob_ref")
	}
	return out, nil
}

// normalizeImagePart turns the older image_url shape ({"type":"image_url",
// "image_url":{"url":...}}) into a Media image, so every provider mapper sees
// one shape: a data: URI becomes inline data, any other URL stays a URL.
func normalizeImagePart(p models.ContentPart) models.ContentPart {
	if p.Media != nil || p.ImageURL == nil || p.ImageURL.URL == "" {
		return p
	}
	u := p.ImageURL.URL
	if strings.HasPrefix(u, "data:") {
		header, data, ok := strings.Cut(strings.TrimPrefix(u, "data:"), ",")
		if ok {
			mime, _, _ := strings.Cut(header, ";")
			if strings.HasSuffix(header, ";base64") && mime != "" {
				p.Media = &models.MediaRef{MimeType: mime, Data: data}
				p.Type = "image"
				return p
			}
		}
		return p // not a base64 data URI: leave it for the mapper to refuse
	}
	p.Media = &models.MediaRef{MimeType: guessImageMime(u), URL: u}
	p.Type = "image"
	return p
}

func guessImageMime(u string) string {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	switch strings.ToLower(path.Ext(u)) {
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	}
	return "image/jpeg"
}

// partKind classifies a ContentPart for capability checks.
func partKind(p models.ContentPart) string {
	switch p.Type {
	case "text", "":
		return "text"
	case "image_url":
		return "image"
	case "image", "file", "audio", "video":
		if p.Media == nil {
			return p.Type
		}
		mime := strings.ToLower(p.Media.MimeType)
		switch {
		case strings.HasPrefix(mime, "image/"):
			return "image"
		case mime == "application/pdf":
			return "document"
		case strings.HasPrefix(mime, "audio/"):
			return "audio"
		case strings.HasPrefix(mime, "video/"):
			return "video"
		}
		return p.Type
	}
	return p.Type
}

// checkCaps refuses a part the model cannot accept.
func checkCaps(caps mediaCaps, kind, model, partKind string) error {
	ok := true
	switch partKind {
	case "image":
		ok = caps.Vision
	case "document":
		ok = !caps.PDFOff
	case "audio":
		ok = caps.Audio
	case "video":
		ok = caps.Video
	}
	if !ok {
		return mediaErrorf("model %q on provider %q does not accept %s input", model, kind, partKind)
	}
	return nil
}

// openAIContent builds an OpenAI chat "content" value: a plain string when the
// message is text only (unchanged from before this change), or a parts array
// when it carries media.
func (mr *ModelRouter) openAIContent(ctx context.Context, prov *models.ModelProvider, model string, msg models.ChatMessage) (interface{}, error) {
	ctx = withBlobKitchen(ctx, prov)
	kind := prov.Kind
	hasMedia := false
	for _, p := range msg.ContentParts {
		if k := partKind(p); k != "text" && k != "" {
			hasMedia = true
			break
		}
	}
	if !hasMedia {
		if len(msg.ContentParts) == 0 {
			return msg.Content, nil
		}
		// Text-only parts collapse back to a string so existing text-only
		// providers and tests see exactly what they always did.
		var b strings.Builder
		b.WriteString(msg.Content)
		for _, p := range msg.ContentParts {
			b.WriteString(p.Text)
		}
		return b.String(), nil
	}

	caps := mr.mediaCapsFor(prov, model)
	var parts []map[string]interface{}
	if msg.Content != "" {
		parts = append(parts, map[string]interface{}{"type": "text", "text": msg.Content})
	}
	for _, p := range msg.ContentParts {
		switch partKind(p) {
		case "text", "":
			if p.Text != "" {
				parts = append(parts, map[string]interface{}{"type": "text", "text": p.Text})
			}
		case "image":
			if err := checkCaps(caps, kind, model, "image"); err != nil {
				return nil, err
			}
			url, err := mr.openAIImageURL(ctx, p)
			if err != nil {
				return nil, err
			}
			parts = append(parts, map[string]interface{}{"type": "image_url", "image_url": map[string]interface{}{"url": url}})
		case "document":
			if err := checkCaps(caps, kind, model, "document"); err != nil {
				return nil, err
			}
			if !caps.Document {
				txt, err := mr.documentFallbackText(ctx, p)
				if err != nil {
					return nil, err
				}
				parts = append(parts, map[string]interface{}{"type": "text", "text": txt})
				continue
			}
			if fileID, err := mr.openAIFileID(ctx, prov, p.Media); err != nil {
				return nil, err
			} else if fileID != "" {
				parts = append(parts, map[string]interface{}{"type": "file", "file": map[string]interface{}{"file_id": fileID}})
				continue
			}
			r, err := mr.resolveMedia(ctx, p.Media)
			if err != nil {
				return nil, err
			}
			if r.b64 == "" {
				return nil, mediaErrorf("openai PDF input needs inline data or a blob_ref; URLs are not accepted")
			}
			parts = append(parts, map[string]interface{}{"type": "file", "file": map[string]interface{}{
				"filename":  nonEmpty(r.name, "document.pdf"),
				"file_data": "data:application/pdf;base64," + r.b64,
			}})
		case "audio":
			if err := checkCaps(caps, kind, model, "audio"); err != nil {
				return nil, err
			}
			r, err := mr.resolveMedia(ctx, p.Media)
			if err != nil {
				return nil, err
			}
			format, err := openAIAudioFormat(r.mime)
			if err != nil {
				return nil, err
			}
			if r.b64 == "" {
				return nil, mediaErrorf("openai audio input needs inline data or a blob_ref")
			}
			parts = append(parts, map[string]interface{}{"type": "input_audio", "input_audio": map[string]interface{}{
				"data": r.b64, "format": format,
			}})
		default:
			return nil, mediaErrorf("openai does not accept %q content parts", p.Type)
		}
	}
	return parts, nil
}

// openAIImageURL returns a URL or data URI for an image part. Both the
// explicit ImageURL field (the older shape) and a Media image are accepted.
func (mr *ModelRouter) openAIImageURL(ctx context.Context, p models.ContentPart) (string, error) {
	if p.ImageURL != nil && p.ImageURL.URL != "" {
		return p.ImageURL.URL, nil
	}
	r, err := mr.resolveMedia(ctx, p.Media)
	if err != nil {
		return "", err
	}
	if r.url != "" {
		return r.url, nil
	}
	return "data:" + r.mime + ";base64," + r.b64, nil
}

func openAIAudioFormat(mime string) (string, error) {
	switch strings.ToLower(mime) {
	case "audio/wav", "audio/x-wav", "audio/wave":
		return "wav", nil
	case "audio/mpeg", "audio/mp3":
		return "mp3", nil
	}
	return "", mediaErrorf("openai audio input supports wav and mp3, got %q", mime)
}

// anthropicContent builds the content blocks for one user or assistant message
// that carries media. Text-only messages keep the existing string form.
func (mr *ModelRouter) anthropicContent(ctx context.Context, prov *models.ModelProvider, model string, msg models.ChatMessage) (interface{}, error) {
	ctx = withBlobKitchen(ctx, prov)
	kind := prov.Kind
	caps := mr.mediaCapsFor(prov, model)
	var blocks []interface{}
	if msg.Content != "" {
		blocks = append(blocks, map[string]interface{}{"type": "text", "text": msg.Content})
	}
	for _, p := range msg.ContentParts {
		p = normalizeImagePart(p)
		switch partKind(p) {
		case "text", "":
			if p.Text != "" {
				blocks = append(blocks, map[string]interface{}{"type": "text", "text": p.Text})
			}
		case "image":
			if err := checkCaps(caps, kind, model, "image"); err != nil {
				return nil, err
			}
			src, err := mr.anthropicSource(ctx, p)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, map[string]interface{}{"type": "image", "source": src})
		case "document":
			if err := checkCaps(caps, kind, model, "document"); err != nil {
				return nil, err
			}
			if !caps.Document {
				txt, err := mr.documentFallbackText(ctx, p)
				if err != nil {
					return nil, err
				}
				blocks = append(blocks, map[string]interface{}{"type": "text", "text": txt})
				continue
			}
			if fileID, err := mr.anthropicFileID(ctx, prov, p.Media); err != nil {
				return nil, err
			} else if fileID != "" {
				blocks = append(blocks, map[string]interface{}{"type": "document", "source": map[string]interface{}{"type": "file", "file_id": fileID}})
				continue
			}
			src, err := mr.anthropicSource(ctx, p)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, map[string]interface{}{"type": "document", "source": src})
		case "audio", "video":
			return nil, mediaErrorf("anthropic does not accept %s input; transcribe it first", partKind(p))
		default:
			return nil, mediaErrorf("anthropic does not accept %q content parts", p.Type)
		}
	}
	return blocks, nil
}

func (mr *ModelRouter) anthropicSource(ctx context.Context, p models.ContentPart) (map[string]interface{}, error) {
	if p.Media != nil && p.Media.URL != "" && p.Media.BlobRef == "" && p.Media.Data == "" {
		return map[string]interface{}{"type": "url", "url": p.Media.URL}, nil
	}
	r, err := mr.resolveMedia(ctx, p.Media)
	if err != nil {
		return nil, err
	}
	if r.url != "" {
		return map[string]interface{}{"type": "url", "url": r.url}, nil
	}
	return map[string]interface{}{"type": "base64", "media_type": r.mime, "data": r.b64}, nil
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// geminiUserParts maps one user message to Gemini parts: text, inline media for
// bytes we hold, and file URIs for URLs.
func (mr *ModelRouter) geminiUserParts(ctx context.Context, prov *models.ModelProvider, model string, msg models.ChatMessage) ([]geminiPart, error) {
	ctx = withBlobKitchen(ctx, prov)
	var parts []geminiPart
	if msg.Content != "" {
		parts = append(parts, geminiPart{Text: msg.Content})
	}
	caps := mr.mediaCapsFor(prov, model)
	for _, p := range msg.ContentParts {
		p = normalizeImagePart(p)
		kind := partKind(p)
		if kind == "text" || kind == "" {
			if p.Text != "" {
				parts = append(parts, geminiPart{Text: p.Text})
			}
			continue
		}
		if kind == "document" {
			if err := checkCaps(caps, "gemini", model, kind); err != nil {
				return nil, err
			}
		}
		if kind == "document" && !caps.Document {
			txt, err := mr.documentFallbackText(ctx, p)
			if err != nil {
				return nil, err
			}
			parts = append(parts, geminiPart{Text: txt})
			continue
		}
		if err := checkCaps(caps, "gemini", model, kind); err != nil {
			return nil, err
		}
		if p.Media != nil && p.Media.MimeType != "" {
			if uri := mr.geminiFileURI(ctx, prov, p.Media); uri != "" {
				parts = append(parts, geminiPart{FileData: &geminiFileData{MimeType: p.Media.MimeType, FileURI: uri}})
				continue
			}
		}
		r, err := mr.resolveMedia(ctx, p.Media)
		if err != nil {
			return nil, err
		}
		if r.url != "" {
			parts = append(parts, geminiPart{FileData: &geminiFileData{MimeType: r.mime, FileURI: r.url}})
			continue
		}
		parts = append(parts, geminiPart{InlineData: &geminiBlob{MimeType: r.mime, Data: r.b64}})
	}
	if len(parts) == 0 {
		parts = []geminiPart{{Text: msg.Content}}
	}
	return parts, nil
}

// fileKey names the provider account a recorded file ID belongs to. Provider
// names are only unique within a kitchen, and a file ID is only valid in the
// account that uploaded it, so the kitchen is part of the key.
func fileKey(prov *models.ModelProvider) string {
	return prov.Kitchen + "/" + prov.Name
}

// openAIFilesAPI reports whether the provider kind is known to serve the
// OpenAI Files API at <endpoint>/files. OpenRouter has none and Azure serves it
// under a different path, so those send the PDF in the request body.
func openAIFilesAPI(kind string) bool {
	return kind == "openai" || kind == "litellm"
}

// openAIFileID returns the OpenAI file_id for a blob-backed PDF, uploading it
// the first time and reusing the recorded ID after that. Inline or URL media
// returns "" and is sent in the request body instead, and so does a PDF whose
// upload failed: the Files API is an optimisation, never a reason to fail a turn.
func (mr *ModelRouter) openAIFileID(ctx context.Context, prov *models.ModelProvider, ref *models.MediaRef) (string, error) {
	if ref == nil || ref.BlobRef == "" || mr.blobs == nil || !openAIFilesAPI(prov.Kind) {
		return "", nil
	}
	if id, ok, err := mr.blobs.ProviderFileID(ctx, ref.BlobRef, fileKey(prov)); err != nil && !errors.Is(err, blobstore.ErrNotFound) {
		return "", err
	} else if ok {
		return id, nil
	}

	blob, err := mr.blobs.Get(ctx, blobKitchen(ctx), ref.BlobRef)
	if err != nil {
		return "", fmt.Errorf("loading media blob %q: %w", ref.BlobRef, err)
	}
	endpoint := prov.Endpoint
	if endpoint == "" {
		endpoint = "https://api.openai.com/v1"
	}
	id, err := uploadOpenAIFile(ctx, mr.client, strings.TrimRight(endpoint, "/"), mr.SelectAPIKey(prov), nonEmpty(ref.Name, "document.pdf"), blob.Data)
	if err != nil {
		log.Warn().Err(err).Str("provider", prov.Name).Msg("Could not upload PDF to the provider's Files API; sending it in the request instead")
		return "", nil
	}
	if err := mr.blobs.SetProviderFileID(ctx, ref.BlobRef, fileKey(prov), id); err != nil {
		log.Warn().Err(err).Msg("Could not record the provider file ID")
	}
	return id, nil
}

// uploadOpenAIFile posts bytes to the OpenAI Files API (purpose user_data) and
// returns the file id.
func uploadOpenAIFile(ctx context.Context, client *http.Client, endpoint, apiKey, filename string, data []byte) (string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("purpose", "user_data"); err != nil {
		return "", err
	}
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(data); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/files", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("files API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.ID == "" {
		return "", fmt.Errorf("files API response had no id")
	}
	return out.ID, nil
}

// documentFallbackText is used when a model cannot take a PDF natively: the
// document's text is extracted in-process and sent as an ordinary text part,
// labelled so the model knows where it came from.
func (mr *ModelRouter) documentFallbackText(ctx context.Context, p models.ContentPart) (string, error) {
	r, err := mr.resolveMedia(ctx, p.Media)
	if err != nil {
		return "", err
	}
	if r.url != "" && r.b64 == "" {
		return "", mediaErrorf("this model cannot read PDFs natively and the PDF is only available by URL; attach it inline or by blob_ref so its text can be extracted")
	}
	raw, err := base64.StdEncoding.DecodeString(r.b64)
	if err != nil {
		return "", mediaErrorf("pdf data is not valid base64: %v", err)
	}
	text, err := documents.PDFText(raw)
	if err != nil {
		return "", &MediaError{msg: err.Error()}
	}
	if strings.TrimSpace(text) == "" {
		// A scanned PDF has no text layer. Sending an empty document would make the
		// model answer as if the file were blank, so say so instead.
		return "", mediaErrorf("%s has no extractable text (it may be a scan) and this model cannot read PDFs natively; use a model with PDF input", nonEmpty(r.name, "the PDF"))
	}
	return fmt.Sprintf("[Document: %s]\n%s", nonEmpty(r.name, "document.pdf"), text), nil
}

// anthropicFileID uploads a blob-backed PDF to the Anthropic Files API once and
// returns its file id on later requests. Inline and URL media return "".
func (mr *ModelRouter) anthropicFileID(ctx context.Context, prov *models.ModelProvider, ref *models.MediaRef) (string, error) {
	if ref == nil || ref.BlobRef == "" || mr.blobs == nil {
		return "", nil
	}
	if id, ok, err := mr.blobs.ProviderFileID(ctx, ref.BlobRef, fileKey(prov)); err != nil && !errors.Is(err, blobstore.ErrNotFound) {
		return "", err
	} else if ok {
		return id, nil
	}
	blob, err := mr.blobs.Get(ctx, blobKitchen(ctx), ref.BlobRef)
	if err != nil {
		return "", fmt.Errorf("loading media blob %q: %w", ref.BlobRef, err)
	}
	endpoint := prov.Endpoint
	if endpoint == "" {
		endpoint = "https://api.anthropic.com"
	}
	id, err := uploadAnthropicFile(ctx, mr.client, strings.TrimRight(endpoint, "/"), mr.SelectAPIKey(prov), nonEmpty(ref.Name, "document.pdf"), blob.Data)
	if err != nil {
		log.Warn().Err(err).Str("provider", prov.Name).Msg("Could not upload PDF to Anthropic's Files API; sending it in the request instead")
		return "", nil
	}
	if err := mr.blobs.SetProviderFileID(ctx, ref.BlobRef, fileKey(prov), id); err != nil {
		log.Warn().Err(err).Msg("Could not record the provider file ID")
	}
	return id, nil
}

// anthropicFilesBeta is the Files API beta header; the endpoint is gated on it.
const anthropicFilesBeta = "files-api-2025-04-14"

func uploadAnthropicFile(ctx context.Context, client *http.Client, endpoint, apiKey, filename string, data []byte) (string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(data); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/v1/files", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", anthropicFilesBeta)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("files API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.ID == "" {
		return "", fmt.Errorf("files API response had no id")
	}
	return out.ID, nil
}

// ExternalizeMedia moves the bytes of a user message's inline media into the
// blob store and leaves a blob_ref in their place. Sessions, the run journal and
// traces keep the message, so this is what keeps large base64 payloads out of
// them (and lets PDFs ride the provider's Files API across turns). The caller's
// message is not modified. Anything that cannot be stored stays inline: a
// missing blob store never turns into a failed turn.
func (mr *ModelRouter) ExternalizeMedia(ctx context.Context, kitchen string, msg models.ChatMessage) models.ChatMessage {
	if mr == nil || mr.blobs == nil || len(msg.ContentParts) == 0 {
		return msg
	}
	parts := make([]models.ContentPart, len(msg.ContentParts))
	copy(parts, msg.ContentParts)
	for i, p := range parts {
		p = normalizeImagePart(p)
		if p.Media == nil || p.Media.Data == "" || p.Media.MimeType == "" {
			parts[i] = p
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(p.Media.Data)
		if err != nil {
			parts[i] = p // the mapper reports the bad payload with the part's context
			continue
		}
		ref, err := mr.blobs.Put(ctx, kitchen, p.Media.MimeType, p.Media.Name, raw)
		if err != nil {
			log.Warn().Err(err).Msg("Could not store media in the blob store; keeping it inline")
			parts[i] = p
			continue
		}
		m := *p.Media
		m.Data, m.BlobRef = "", ref
		p.Media = &m
		p.ImageURL = nil
		parts[i] = p
	}
	msg.ContentParts = parts
	return msg
}
