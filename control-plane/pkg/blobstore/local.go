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
	"strings"
	"sync"
	"time"
)

// refPattern is the only shape a ref can take. Refs come from clients, so
// anything else is rejected before it can be joined into a filesystem path.
var refPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// LocalStore keeps blobs on local disk under a root directory:
//
//	<root>/<sha256>.bin        the bytes
//	<root>/<sha256>.json       mime type, name, and provider file IDs
type LocalStore struct {
	root     string
	maxBlob  int64
	touchAge time.Duration
	mu       sync.Mutex
}

// Option configures a LocalStore.
type Option func(*LocalStore)

// WithMaxBlobBytes makes Put refuse blobs larger than n bytes (ErrTooLarge). Zero is no limit.
func WithMaxBlobBytes(n int64) Option { return func(s *LocalStore) { s.maxBlob = n } }

// NewLocalStore opens (creating if needed) a local blob store at dir.
func NewLocalStore(dir string, opts ...Option) (*LocalStore, error) {
	if dir == "" {
		return nil, fmt.Errorf("blobstore: directory must not be empty")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("blobstore: %w", err)
	}
	s := &LocalStore{root: dir, touchAge: time.Hour}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// meta is the sidecar of one blob. Owners are the kitchens allowed to read it; a
// blob with no owners (written before ownership existed) is readable by nobody.
type meta struct {
	MimeType    string            `json:"mime_type"`
	Name        string            `json:"name,omitempty"`
	Size        int64             `json:"size,omitempty"`
	Owners      []string          `json:"owners,omitempty"`
	AccessedAt  time.Time         `json:"accessed_at,omitempty"`
	ProviderIDs map[string]string `json:"provider_file_ids,omitempty"`
}

func (m *meta) ownedBy(kitchen string) bool {
	for _, o := range m.Owners {
		if o == kitchen {
			return true
		}
	}
	return false
}

func (s *LocalStore) dataPath(ref string) string { return filepath.Join(s.root, ref+".bin") }
func (s *LocalStore) metaPath(ref string) string { return filepath.Join(s.root, ref+".json") }

func checkRef(ref string) error {
	if !refPattern.MatchString(ref) {
		return fmt.Errorf("blobstore: invalid ref %q", ref)
	}
	return nil
}

func (s *LocalStore) Put(_ context.Context, kitchen, mimeType, name string, data []byte) (string, error) {
	if mimeType == "" {
		return "", fmt.Errorf("blobstore: mime type is required")
	}
	if kitchen == "" {
		return "", fmt.Errorf("blobstore: kitchen is required")
	}
	if s.maxBlob > 0 && int64(len(data)) > s.maxBlob {
		return "", fmt.Errorf("%w (%d bytes, limit %d)", ErrTooLarge, len(data), s.maxBlob)
	}
	sum := sha256.Sum256(data)
	ref := hex.EncodeToString(sum[:])

	s.mu.Lock()
	defer s.mu.Unlock()

	if m, err := s.readMeta(ref); err == nil {
		// Identical bytes are already stored: add this kitchen as an owner.
		if !m.ownedBy(kitchen) {
			m.Owners = append(m.Owners, kitchen)
		}
		m.AccessedAt = time.Now().UTC()
		if _, derr := os.Stat(s.dataPath(ref)); derr == nil {
			return ref, s.writeMeta(ref, *m)
		}
		// meta without data (interrupted write): fall through and rewrite both.
	}
	if err := writeFileAtomic(s.dataPath(ref), data); err != nil {
		return "", fmt.Errorf("blobstore: write data: %w", err)
	}
	if err := s.writeMeta(ref, meta{MimeType: mimeType, Name: name, Size: int64(len(data)),
		Owners: []string{kitchen}, AccessedAt: time.Now().UTC()}); err != nil {
		return "", err
	}
	return ref, nil
}

func (s *LocalStore) Get(_ context.Context, kitchen, ref string) (*Blob, error) {
	if err := checkRef(ref); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	m, err := s.readMeta(ref)
	if err != nil {
		return nil, err
	}
	if !m.ownedBy(kitchen) {
		return nil, ErrNotFound // another kitchen's blob looks exactly like a missing one
	}
	data, err := os.ReadFile(s.dataPath(ref))
	if err != nil {
		return nil, fmt.Errorf("blobstore: read data: %w", err)
	}
	if time.Since(m.AccessedAt) > s.touchAge {
		m.AccessedAt = time.Now().UTC() // last access, kept coarse so reads do not rewrite the sidecar every time
		_ = s.writeMeta(ref, *m)
	}
	return &Blob{Ref: ref, MimeType: m.MimeType, Name: m.Name, Data: data}, nil
}

func (s *LocalStore) Stat(_ context.Context, kitchen, ref string) (*Info, error) {
	if err := checkRef(ref); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readMeta(ref)
	if err != nil {
		return nil, err
	}
	if !m.ownedBy(kitchen) {
		return nil, ErrNotFound
	}
	size := m.Size
	if size == 0 {
		if fi, err := os.Stat(s.dataPath(ref)); err == nil {
			size = fi.Size()
		}
	}
	return &Info{MimeType: m.MimeType, Name: m.Name, Size: size}, nil
}

func (s *LocalStore) Delete(_ context.Context, kitchen, ref string) error {
	if err := checkRef(ref); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readMeta(ref)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !m.ownedBy(kitchen) {
		return nil
	}
	rest := m.Owners[:0:0]
	for _, o := range m.Owners {
		if o != kitchen {
			rest = append(rest, o)
		}
	}
	if len(rest) > 0 {
		m.Owners = rest
		return s.writeMeta(ref, *m)
	}
	return s.remove(ref)
}

func (s *LocalStore) remove(ref string) error {
	for _, p := range []string{s.dataPath(ref), s.metaPath(ref)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("blobstore: delete: %w", err)
		}
	}
	return nil
}

// Sweep removes blobs not read or written since olderThan (see Janitor).
func (s *LocalStore) Sweep(_ context.Context, olderThan time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return 0, fmt.Errorf("blobstore: sweep: %w", err)
	}
	removed := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		ref := strings.TrimSuffix(name, ".json")
		if checkRef(ref) != nil {
			continue
		}
		m, err := s.readMeta(ref)
		if err != nil {
			continue
		}
		last := m.AccessedAt
		if last.IsZero() {
			if info, ierr := e.Info(); ierr == nil {
				last = info.ModTime()
			}
		}
		if last.Before(olderThan) {
			if err := s.remove(ref); err != nil {
				return removed, err
			}
			removed++
		}
	}
	return removed, nil
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
	if err := writeFileAtomic(s.metaPath(ref), raw); err != nil {
		return fmt.Errorf("blobstore: write meta: %w", err)
	}
	return nil
}

// writeFileAtomic writes through a temp file and a rename, so a crash never leaves a half-written file.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}
