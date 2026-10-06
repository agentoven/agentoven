package blobstore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/blobstore"
)

func newStore(t *testing.T) *blobstore.LocalStore {
	t.Helper()
	s, err := blobstore.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPutGetRoundTrip(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	ref, err := s.Put(ctx, "application/pdf", "report.pdf", []byte("%PDF-1.7 hello"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Data) != "%PDF-1.7 hello" || got.MimeType != "application/pdf" || got.Name != "report.pdf" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestIdenticalBytesAreStoredOnce(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a, err := s.Put(ctx, "audio/wav", "first.wav", []byte("same bytes"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Put(ctx, "audio/wav", "second.wav", []byte("same bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("content-addressed refs should match for identical bytes: %s vs %s", a, b)
	}
}

func TestProviderFileIDIsRecordedPerProvider(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	ref, err := s.Put(ctx, "application/pdf", "doc.pdf", []byte("pdf body"))
	if err != nil {
		t.Fatal(err)
	}

	if _, ok, _ := s.ProviderFileID(ctx, ref, "openai"); ok {
		t.Fatal("no provider ID should exist before upload")
	}
	if err := s.SetProviderFileID(ctx, ref, "openai", "file-abc123"); err != nil {
		t.Fatal(err)
	}
	id, ok, err := s.ProviderFileID(ctx, ref, "openai")
	if err != nil || !ok || id != "file-abc123" {
		t.Fatalf("expected recorded openai id, got %q ok=%v err=%v", id, ok, err)
	}
	if _, ok, _ := s.ProviderFileID(ctx, ref, "anthropic"); ok {
		t.Fatal("a provider ID for one provider must not satisfy another")
	}
}

func TestDeleteRemovesTheBlob(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	ref, _ := s.Put(ctx, "image/png", "x.png", []byte("png"))
	if err := s.Delete(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, ref); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestRejectsPathTraversalRefs(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for _, bad := range []string{"../../etc/passwd", "abc", "", "../x"} {
		if _, err := s.Get(ctx, bad); err == nil {
			t.Fatalf("expected invalid ref %q to be rejected", bad)
		}
	}
}

func TestPutRequiresMimeType(t *testing.T) {
	s := newStore(t)
	if _, err := s.Put(context.Background(), "", "x", []byte("x")); err == nil {
		t.Fatal("expected Put without a mime type to fail")
	}
}
