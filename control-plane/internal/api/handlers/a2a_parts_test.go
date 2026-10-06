package handlers

import (
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func TestA2AFilePartBecomesImageForImageMime(t *testing.T) {
	p := a2aFileToContentPart(&a2aFilePart{Name: "cat.png", MimeType: "image/png", Bytes: "aGk="})
	if p.Type != "image" || p.Media == nil || p.Media.Data != "aGk=" || p.Media.MimeType != "image/png" {
		t.Fatalf("unexpected part: %+v", p)
	}
}

func TestA2AFilePartKeepsURIAsURL(t *testing.T) {
	p := a2aFileToContentPart(&a2aFilePart{Name: "r.pdf", MimeType: "application/pdf", URI: "https://example.com/r.pdf"})
	if p.Type != "file" || p.Media.URL != "https://example.com/r.pdf" || p.Media.Data != "" {
		t.Fatalf("unexpected part: %+v", p)
	}
}

func TestA2AAudioAndVideoMapToTheirTypes(t *testing.T) {
	if p := a2aFileToContentPart(&a2aFilePart{MimeType: "audio/wav", Bytes: "AAAA"}); p.Type != "audio" {
		t.Fatalf("audio/wav should map to audio, got %q", p.Type)
	}
	if p := a2aFileToContentPart(&a2aFilePart{MimeType: "video/mp4", Bytes: "AAAA"}); p.Type != "video" {
		t.Fatalf("video/mp4 should map to video, got %q", p.Type)
	}
	var _ models.ContentPart = a2aFileToContentPart(&a2aFilePart{MimeType: "application/pdf", Bytes: "x"})
}
