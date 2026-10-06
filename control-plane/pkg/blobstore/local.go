package blobstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

// refPattern is the only shape a ref can take. Refs come from clients, so
// anything else is rejected before it can be joined into a filesystem path.
var refPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// LocalStore keeps blobs on local disk under a root directory:
//
//	<root>/<sha256>.bin        the bytes
//	<root>/<sha256>.json       mime type, name, and provider file IDs
type LocalStore struct {
	root string
	mu   sync.Mutex
}

// NewLocalStore opens (creating if needed) a local blob store at dir.
func NewLocalStore(dir string) (*LocalStore, error) {
	if dir == "" {
		return nil, fmt.Errorf("blobstore: directory must not be empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("blobstore: %w", err)
	}
	return &LocalStore{root: dir}, nil
}

type meta struct {
	MimeType    string            `json:"mime_type"`
	Name        string            `json:"name,omitempty"`
	ProviderIDs map[string]string `json:"provider_file_ids,omitempty"`
}

func (s *LocalStore) dataPath(ref string) string { return filepath.Join(s.root, ref+".bin") }
func (s *LocalStore) metaPath(ref string) string { return filepath.Join(s.root, ref+".json") }

func checkRef(ref string) error {
	if !refPattern.MatchString(ref) {
		return fmt.Errorf("blobstore: invalid ref %q", ref)
	}
	return nil
}

func (s *LocalStore) Put(_ context.Context, mimeType, name string, data []byte) (string, error) {
	if mimeType == "" {
		return "", fmt.Errorf("blobstore: mime type is required")
	}
	sum := sha256.Sum256(data)
	ref := hex.EncodeToString(sum[:])

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := os.Stat(s.metaPath(ref)); err == nil {
		return ref, nil // identical bytes already stored
	}
	if err := os.WriteFile(s.dataPath(ref), data, 0o644); err != nil {
		return "", fmt.Errorf("blobstore: write data: %w", err)
	}
	if err := s.writeMeta(ref, meta{MimeType: mimeType, Name: name}); err != nil {
		return "", err
	}
	return ref, nil
}

func (s *LocalStore) Get(_ context.Context, ref string) (*Blob, error) {
	if err := checkRef(ref); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	m, err := s.readMeta(ref)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.dataPath(ref))
	if err != nil {
		return nil, fmt.Errorf("blobstore: read data: %w", err)
	}
	return &Blob{Ref: ref, MimeType: m.MimeType, Name: m.Name, Data: data}, nil
}

func (s *LocalStore) Delete(_ context.Context, ref string) error {
	if err := checkRef(ref); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range []string{s.dataPath(ref), s.metaPath(ref)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("blobstore: delete: %w", err)
		}
	}
	return nil
}

func (s *LocalStore) ProviderFileID(_ context.Context, ref, provider string) (string, bool, error) {
	if err := checkRef(ref); err != nil {
		return "", false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readMeta(ref)
	if err != nil {
		return "", false, err
	}
	id, ok := m.ProviderIDs[provider]
	return id, ok, nil
}

func (s *LocalStore) SetProviderFileID(_ context.Context, ref, provider, fileID string) error {
	if err := checkRef(ref); err != nil {
		return err
	}
	if provider == "" || fileID == "" {
		return fmt.Errorf("blobstore: provider and file id are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readMeta(ref)
	if err != nil {
		return err
	}
	if m.ProviderIDs == nil {
		m.ProviderIDs = map[string]string{}
	}
	m.ProviderIDs[provider] = fileID
	return s.writeMeta(ref, *m)
}

func (s *LocalStore) readMeta(ref string) (*meta, error) {
	raw, err := os.ReadFile(s.metaPath(ref))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("blobstore: read meta: %w", err)
	}
	var m meta
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("blobstore: corrupt meta for %s: %w", ref, err)
	}
	return &m, nil
}

func (s *LocalStore) writeMeta(ref string, m meta) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.metaPath(ref), raw, 0o644); err != nil {
		return fmt.Errorf("blobstore: write meta: %w", err)
	}
	return nil
}
