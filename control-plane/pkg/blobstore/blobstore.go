// Package blobstore holds the binary media (audio, video, PDF, images) that
// agent messages refer to, so large bytes never sit in trace or session JSON.
//
// Blobs are content-addressed: the ref is the SHA-256 of the bytes, so the same
// PDF uploaded twice is stored once. The store also records, per blob and per
// provider, the provider's own file reference (OpenAI file_id, Anthropic file
// id, Gemini file URI). Uploading once and reusing that ID is what keeps
// repeated media out of request bodies and lets provider-side caching apply.
package blobstore

import (
	"context"
	"errors"
)

// ErrNotFound is returned when a blob ref is unknown.
var ErrNotFound = errors.New("blobstore: blob not found")

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
	// Put stores data and returns its content-addressed ref. Storing identical
	// bytes again returns the same ref and does not duplicate the object.
	Put(ctx context.Context, mimeType, name string, data []byte) (ref string, err error)
	Get(ctx context.Context, ref string) (*Blob, error)
	Delete(ctx context.Context, ref string) error

	// ProviderFileID returns the provider's own file reference for this blob,
	// if one was recorded. ok is false when the blob has not been uploaded to
	// that provider yet.
	ProviderFileID(ctx context.Context, ref, provider string) (fileID string, ok bool, err error)
	// SetProviderFileID records the provider's reference after a successful upload.
	SetProviderFileID(ctx context.Context, ref, provider, fileID string) error
}
