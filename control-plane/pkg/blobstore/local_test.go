package blobstore_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	ref, err := s.Put(ctx, "k1", "application/pdf", "report.pdf", []byte("%PDF-1.7 hello"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "k1", ref)
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
	a, err := s.Put(ctx, "k1", "audio/wav", "first.wav", []byte("same bytes"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Put(ctx, "k1", "audio/wav", "second.wav", []byte("same bytes"))
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
	ref, err := s.Put(ctx, "k1", "application/pdf", "doc.pdf", []byte("pdf body"))
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
	ref, _ := s.Put(ctx, "k1", "image/png", "x.png", []byte("png"))
	if err := s.Delete(ctx, "k1", ref); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "k1", ref); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestRejectsPathTraversalRefs(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for _, bad := range []string{"../../etc/passwd", "abc", "", "../x"} {
		if _, err := s.Get(ctx, "k1", bad); err == nil {
			t.Fatalf("expected invalid ref %q to be rejected", bad)
		}
	}
}

func TestPutRequiresMimeType(t *testing.T) {
	s := newStore(t)
	if _, err := s.Put(context.Background(), "k1", "", "x", []byte("x")); err == nil {
		t.Fatal("expected Put without a mime type to fail")
	}
}

func TestABlobResolvesOnlyForItsOwnerKitchen(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	ref, _ := s.Put(ctx, "team-a", "application/pdf", "a.pdf", []byte("secret plans"))

	if _, err := s.Get(ctx, "team-b", ref); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("another kitchen must see ErrNotFound, got %v", err)
	}
	if _, err := s.Get(ctx, "team-a", ref); err != nil {
		t.Fatal(err)
	}
	// B deleting a blob it does not own changes nothing.
	_ = s.Delete(ctx, "team-b", ref)
	if _, err := s.Get(ctx, "team-a", ref); err != nil {
		t.Fatalf("a non-owner's delete must not remove the blob: %v", err)
	}
}

func TestSharedBytesAreStoredOnceWithBothOwners(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a, _ := s.Put(ctx, "team-a", "image/png", "x.png", []byte("same"))
	b, _ := s.Put(ctx, "team-b", "image/png", "x.png", []byte("same"))
	if a != b {
		t.Fatal("identical bytes share one object")
	}
	for _, k := range []string{"team-a", "team-b"} {
		if _, err := s.Get(ctx, k, a); err != nil {
			t.Fatalf("%s should own it: %v", k, err)
		}
	}
	_ = s.Delete(ctx, "team-a", a)
	if _, err := s.Get(ctx, "team-a", a); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatal("A gave up its ownership")
	}
	if _, err := s.Get(ctx, "team-b", a); err != nil {
		t.Fatalf("B still owns it: %v", err)
	}
	_ = s.Delete(ctx, "team-b", a)
	if _, err := s.Get(ctx, "team-b", a); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatal("the last owner's delete removes the bytes")
	}
}

func TestBlobOverTheLimitIsRefused(t *testing.T) {
	s, _ := blobstore.NewLocalStore(t.TempDir(), blobstore.WithMaxBlobBytes(8))
	if _, err := s.Put(context.Background(), "k", "image/png", "x", []byte("123456789")); !errors.Is(err, blobstore.ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
	if _, err := s.Put(context.Background(), "k", "image/png", "x", []byte("12345678")); err != nil {
		t.Fatalf("a blob at the limit is fine: %v", err)
	}
}

func TestSweepRemovesOnlyBlobsNotAccessedSinceTheCutoff(t *testing.T) {
	dir := t.TempDir()
	s, _ := blobstore.NewLocalStore(dir)
	ctx := context.Background()
	old, _ := s.Put(ctx, "k", "image/png", "old", []byte("old bytes"))
	fresh, _ := s.Put(ctx, "k", "image/png", "fresh", []byte("fresh bytes"))

	n, err := s.Sweep(ctx, time.Now().Add(time.Hour)) // everything is older than "an hour from now"
	if err != nil || n != 2 {
		t.Fatalf("expected both swept, got %d %v", n, err)
	}
	if _, err := s.Get(ctx, "k", old); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatal("old blob should be gone")
	}
	fresh, _ = s.Put(ctx, "k", "image/png", "fresh", []byte("fresh bytes"))
	n, _ = s.Sweep(ctx, time.Now().Add(-time.Hour))
	if n != 0 {
		t.Fatalf("a just-written blob must survive a TTL sweep, removed %d", n)
	}
	if _, err := s.Get(ctx, "k", fresh); err != nil {
		t.Fatal(err)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, ".tmp-*")); len(m) != 0 {
		t.Fatalf("temp files left behind: %v", m)
	}
}

func TestJanitorSweepsWithTheConfiguredTTL(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	ref, _ := s.Put(ctx, "k", "image/png", "x", []byte("bytes"))
	j := &blobstore.Janitor{Store: s, TTL: time.Hour}
	if n, _ := j.Sweep(ctx); n != 0 {
		t.Fatalf("a fresh blob is inside the TTL, removed %d", n)
	}
	j.TTL = -time.Minute // everything written before "a minute from now"
	if n, _ := j.Sweep(ctx); n != 1 {
		t.Fatalf("expected the blob to expire, removed %d", n)
	}
	if _, err := s.Get(ctx, "k", ref); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatal("expired blob should be gone")
	}
}

func TestPolicyFromEnv(t *testing.T) {
	t.Setenv(blobstore.EnvMaxBytes, "")
	t.Setenv(blobstore.EnvTTLDays, "")
	if blobstore.MaxBytesFromEnv() != 25<<20 || blobstore.TTLFromEnv() != 7*24*time.Hour {
		t.Fatal("defaults are 25 MB and 7 days")
	}
	t.Setenv(blobstore.EnvMaxBytes, "1000")
	t.Setenv(blobstore.EnvTTLDays, "0")
	if blobstore.MaxBytesFromEnv() != 1000 || blobstore.TTLFromEnv() != 0 {
		t.Fatal("env overrides, and 0 days keeps blobs forever")
	}
	_ = os.Getenv
}
