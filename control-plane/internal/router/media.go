package router

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/agentoven/agentoven/control-plane/pkg/documents"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

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

	if mr.catalog != nil {
		if c := mr.catalog.Lookup(kind, model); c != nil && c.SupportsVision {
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
		return resolvedMedia{}, fmt.Errorf("media part has no media reference")
	}
	if ref.MimeType == "" {
		return resolvedMedia{}, fmt.Errorf("media part is missing mime_type")
	}
	out := resolvedMedia{mime: ref.MimeType, name: ref.Name, url: ref.URL}
	switch {
	case ref.BlobRef != "":
		if mr.blobs == nil {
			return resolvedMedia{}, fmt.Errorf("media blob %q referenced but no blob store is configured", ref.BlobRef)
		}
		blob, err := mr.blobs.Get(ctx, ref.BlobRef)
		if err != nil {
			return resolvedMedia{}, fmt.Errorf("loading media blob %q: %w", ref.BlobRef, err)
		}
		out.b64 = base64.StdEncoding.EncodeToString(blob.Data)
	case ref.Data != "":
		out.b64 = ref.Data
	case ref.URL != "":
		// URL kept as-is; the caller decides whether this provider accepts URLs.
	default:
		return resolvedMedia{}, fmt.Errorf("media part has no url, data, or blob_ref")
	}
	return out, nil
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
		return fmt.Errorf("model %q on provider %q does not accept %s input", model, kind, partKind)
	}
	return nil
}

// openAIContent builds an OpenAI chat "content" value: a plain string when the
// message is text only (unchanged from before this change), or a parts array
// when it carries media.
func (mr *ModelRouter) openAIContent(ctx context.Context, prov *models.ModelProvider, model string, msg models.ChatMessage) (interface{}, error) {
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
				return nil, fmt.Errorf("openai PDF input needs inline data or a blob_ref; URLs are not accepted")
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
				return nil, fmt.Errorf("openai audio input needs inline data or a blob_ref")
			}
			parts = append(parts, map[string]interface{}{"type": "input_audio", "input_audio": map[string]interface{}{
				"data": r.b64, "format": format,
			}})
		default:
			return nil, fmt.Errorf("openai does not accept %q content parts", p.Type)
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
	return "", fmt.Errorf("openai audio input supports wav and mp3, got %q", mime)
}

// anthropicContent builds the content blocks for one user or assistant message
// that carries media. Text-only messages keep the existing string form.
func (mr *ModelRouter) anthropicContent(ctx context.Context, prov *models.ModelProvider, model string, msg models.ChatMessage) (interface{}, error) {
	kind := prov.Kind
	caps := mr.mediaCapsFor(prov, model)
	var blocks []interface{}
	if msg.Content != "" {
		blocks = append(blocks, map[string]interface{}{"type": "text", "text": msg.Content})
	}
	for _, p := range msg.ContentParts {
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
			return nil, fmt.Errorf("anthropic does not accept %s input; transcribe it first", partKind(p))
		default:
			return nil, fmt.Errorf("anthropic does not accept %q content parts", p.Type)
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
	var parts []geminiPart
	if msg.Content != "" {
		parts = append(parts, geminiPart{Text: msg.Content})
	}
	caps := mr.mediaCapsFor(prov, model)
	for _, p := range msg.ContentParts {
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
		if kind == "image" && p.ImageURL != nil && p.Media == nil {
			return nil, fmt.Errorf("gemini image parts need a media reference, not image_url")
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

// openAIFileID returns the OpenAI file_id for a blob-backed PDF, uploading it
// the first time and reusing the recorded ID after that. Inline or URL media
// returns "" and is sent in the request body instead.
func (mr *ModelRouter) openAIFileID(ctx context.Context, prov *models.ModelProvider, ref *models.MediaRef) (string, error) {
	if ref == nil || ref.BlobRef == "" || mr.blobs == nil {
		return "", nil
	}
	if id, ok, err := mr.blobs.ProviderFileID(ctx, ref.BlobRef, prov.Name); err != nil {
		return "", err
	} else if ok {
		return id, nil
	}

	blob, err := mr.blobs.Get(ctx, ref.BlobRef)
	if err != nil {
		return "", fmt.Errorf("loading media blob %q: %w", ref.BlobRef, err)
	}
	endpoint := prov.Endpoint
	if endpoint == "" {
		endpoint = "https://api.openai.com/v1"
	}
	id, err := uploadOpenAIFile(ctx, mr.client, strings.TrimRight(endpoint, "/"), mr.SelectAPIKey(prov), nonEmpty(ref.Name, "document.pdf"), blob.Data)
	if err != nil {
		return "", fmt.Errorf("uploading %s to openai: %w", ref.BlobRef, err)
	}
	if err := mr.blobs.SetProviderFileID(ctx, ref.BlobRef, prov.Name, id); err != nil {
		return "", err
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
		return "", fmt.Errorf("this model cannot read PDFs natively and the PDF is only available by URL; attach it inline or by blob_ref so its text can be extracted")
	}
	raw, err := base64.StdEncoding.DecodeString(r.b64)
	if err != nil {
		return "", fmt.Errorf("pdf data is not valid base64: %w", err)
	}
	text, err := documents.PDFText(raw)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("[Document: %s]\n%s", nonEmpty(r.name, "document.pdf"), text), nil
}

// anthropicFileID uploads a blob-backed PDF to the Anthropic Files API once and
// returns its file id on later requests. Inline and URL media return "".
func (mr *ModelRouter) anthropicFileID(ctx context.Context, prov *models.ModelProvider, ref *models.MediaRef) (string, error) {
	if ref == nil || ref.BlobRef == "" || mr.blobs == nil {
		return "", nil
	}
	if id, ok, err := mr.blobs.ProviderFileID(ctx, ref.BlobRef, prov.Name); err != nil {
		return "", err
	} else if ok {
		return id, nil
	}
	blob, err := mr.blobs.Get(ctx, ref.BlobRef)
	if err != nil {
		return "", fmt.Errorf("loading media blob %q: %w", ref.BlobRef, err)
	}
	endpoint := prov.Endpoint
	if endpoint == "" {
		endpoint = "https://api.anthropic.com"
	}
	id, err := uploadAnthropicFile(ctx, mr.client, strings.TrimRight(endpoint, "/"), mr.SelectAPIKey(prov), nonEmpty(ref.Name, "document.pdf"), blob.Data)
	if err != nil {
		return "", fmt.Errorf("uploading %s to anthropic: %w", ref.BlobRef, err)
	}
	if err := mr.blobs.SetProviderFileID(ctx, ref.BlobRef, prov.Name, id); err != nil {
		return "", err
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
