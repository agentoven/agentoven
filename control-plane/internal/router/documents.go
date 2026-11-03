package router

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/agentoven/agentoven/control-plane/pkg/documents"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// maxDocumentTextPerRequest bounds the text every attached document of one request adds to the
// prompt. Each document is already cut at documents.Limits.MaxTextBytes; this stops sixteen
// attachments from adding sixteen times that.
const maxDocumentTextPerRequest = 600_000

// withDocuments returns the request with every attached document in a format no provider takes
// natively (Word, Excel, HTML, CSV, RTF, Markdown, plain text) replaced by a text part holding what
// the document says, read in-process (pkg/documents). The caller's request is not modified. It is
// the capability gate for those formats: no model accepts them as files, so they always become text,
// whichever driver the call goes to. PDFs and images are not touched here; the driver sends them
// natively or, when the model cannot take a PDF, falls back to text (documentFallbackText).
//
// A document that cannot be read is a MediaError (the caller's file, not a provider fault): the
// call is refused before anything is sent, with the reason.
func (mr *ModelRouter) withDocuments(ctx context.Context, prov *models.ModelProvider, req *models.RouteRequest) (*models.RouteRequest, error) {
	if !hasDocumentParts(req.Messages) {
		return req, nil
	}
	ctx = withBlobKitchen(ctx, prov)
	out := *req
	out.Messages = make([]models.ChatMessage, len(req.Messages))
	budget := maxDocumentTextPerRequest
	for i, m := range req.Messages {
		out.Messages[i] = m
		if !hasDocumentParts(req.Messages[i : i+1]) {
			continue
		}
		parts := make([]models.ContentPart, len(m.ContentParts))
		copy(parts, m.ContentParts)
		for j, p := range parts {
			if !isDocumentPart(p) {
				continue
			}
			text, err := mr.documentText(ctx, p.Media, &budget)
			if err != nil {
				return nil, err
			}
			parts[j] = models.ContentPart{Type: "text", Text: text, Extra: map[string]interface{}{"document": nonEmpty(p.Media.Name, "document"), "mime_type": p.Media.MimeType}}
		}
		out.Messages[i].ContentParts = parts
	}
	return &out, nil
}

func hasDocumentParts(msgs []models.ChatMessage) bool {
	for _, m := range msgs {
		for _, p := range m.ContentParts {
			if isDocumentPart(p) {
				return true
			}
		}
	}
	return false
}

func isDocumentPart(p models.ContentPart) bool {
	return (p.Type == "file" || p.Type == "document") && p.Media != nil && documents.IsOfficeOrText(p.Media.MimeType, p.Media.Name)
}

// documentText reads one attached document into the text a model is shown, charging what it adds
// against the request's budget.
func (mr *ModelRouter) documentText(ctx context.Context, ref *models.MediaRef, budget *int) (string, error) {
	if *budget <= 0 {
		return "", mediaErrorf("the attached documents together are longer than %d bytes of text; attach fewer or smaller files", maxDocumentTextPerRequest)
	}
	raw, err := mr.mediaBytes(ctx, ref)
	if err != nil {
		return "", err
	}
	res, err := documents.ExtractFile(ctx, ref.MimeType, ref.Name, raw, documents.Limits{MaxTextBytes: min(*budget, documents.DefaultLimits().MaxTextBytes)})
	if err != nil {
		return "", &MediaError{msg: fmt.Sprintf("%s: %v", nonEmpty(ref.Name, "the attached file"), err)}
	}
	text := res.Render(ref.Name, 0)
	*budget -= len(text)
	return text, nil
}

// mediaBytes returns the bytes a media reference names: the blob (owned by the kitchen set on ctx)
// or the inline data. A URL is not fetched here: only the provider or the caller can say whether it
// may be.
func (mr *ModelRouter) mediaBytes(ctx context.Context, ref *models.MediaRef) ([]byte, error) {
	switch {
	case ref == nil:
		return nil, mediaErrorf("media part has no media reference")
	case ref.BlobRef != "":
		if mr.blobs == nil {
			return nil, fmt.Errorf("media blob %q referenced but no blob store is configured", ref.BlobRef)
		}
		blob, err := mr.blobs.Get(ctx, blobKitchen(ctx), ref.BlobRef)
		if err != nil {
			return nil, fmt.Errorf("loading media blob %q: %w", ref.BlobRef, err)
		}
		return blob.Data, nil
	case ref.Data != "":
		raw, err := base64.StdEncoding.DecodeString(ref.Data)
		if err != nil {
			return nil, mediaErrorf("%s data is not valid base64: %v", nonEmpty(ref.Name, "file"), err)
		}
		return raw, nil
	}
	return nil, mediaErrorf("%s is only available by URL; attach it inline or by blob_ref so its text can be read", nonEmpty(ref.Name, "the file"))
}
