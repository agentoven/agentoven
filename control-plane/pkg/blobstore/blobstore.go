// Package blobstore holds the binary media (audio, video, PDF, images) that
// agent messages refer to, so large bytes never sit in trace or session JSON.
//
// Blobs are content-addressed: the ref is the SHA-256 of the bytes, so the same
// PDF uploaded twice is stored once. A blob belongs to the kitchens that stored
// it: a ref only resolves for an owner, and another kitchen is told it does not
// exist (ErrNotFound), so a ref is never a credential. The store also records, per blob and per
// provider, the provider's own file reference (OpenAI file_id, Anthropic file
// id, Gemini file URI). Uploading once and reusing that ID is what keeps
// repeated media out of request bodies and lets provider-side caching apply.
package blobstore

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned when a blob ref is unknown, or belongs to another kitchen.
var ErrNotFound = errors.New("blobstore: blob not found")

// ErrTooLarge is returned by Put when the bytes exceed the store's per-blob limit.
var ErrTooLarge = errors.New("blobstore: blob exceeds the size limit")

// Blob is a stored binary with its metadata.
type Blob struct {
	Ref      string
	MimeType string
	Name     string
	Data     []byte
}

// Store is the blob store contract. The OSS implementation is local disk;
// Pro can back the same interface with object storage or a mounted share.
type Store interface {
	// Put stores data for a kitchen and returns its content-addressed ref. Storing
	// identical bytes again returns the same ref and does not duplicate the object;
	// the kitchen is added to the blob's owners.
	Put(ctx context.Context, kitchen, mimeType, name string, data []byte) (ref string, err error)
	// Get returns the blob if the kitchen owns it; otherwise ErrNotFound.
	Get(ctx context.Context, kitchen, ref string) (*Blob, error)
	// Delete drops the kitchen's ownership, and the bytes when it was the last owner.
	Delete(ctx context.Context, kitchen, ref string) error

	// ProviderFileID returns the provider's own file reference for this blob,
	// if one was recorded. ok is false when the blob has not been uploaded to
	// that provider yet.
	ProviderFileID(ctx context.Context, ref, provider string) (fileID string, ok bool, err error)
	// SetProviderFileID records the provider's reference after a successful upload.
	SetProviderFileID(ctx context.Context, ref, provider, fileID string) error
}

// Sweeper is implemented, optionally, by a store that can expire blobs on its
// own (the local store does; an object store would use bucket lifecycle rules
// instead). Janitor drives it.
type Sweeper interface {
	// Sweep removes blobs not read or written since olderThan and returns how many.
	Sweep(ctx context.Context, olderThan time.Time) (removed int, err error)
}

// Info describes a blob without its bytes.
type Info struct {
	MimeType string
	Name     string
	Size     int64
}

// Stater is implemented, optionally, by a store that can answer "does this kitchen own this
// blob, and what is it" without reading the bytes. Stat reports ErrNotFound like Get.
type Stater interface {
	Stat(ctx context.Context, kitchen, ref string) (*Info, error)
}

// Stat returns what is known about a blob the kitchen owns, using the store's own Stat when it
// has one and reading the blob otherwise.
func Stat(ctx context.Context, s Store, kitchen, ref string) (*Info, error) {
	if st, ok := s.(Stater); ok {
		return st.Stat(ctx, kitchen, ref)
	}
	b, err := s.Get(ctx, kitchen, ref)
	if err != nil {
		return nil, err
	}
	return &Info{MimeType: b.MimeType, Name: b.Name, Size: int64(len(b.Data))}, nil
}
