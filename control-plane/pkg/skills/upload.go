package skills

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
)

// uploadTTL bounds how long a staged upload can sit unfinished before it is
// treated as abandoned and swept on the next access — there is no background
// goroutine for this; cleanup happens lazily, the same way MemoryStore's
// snapshot save is request-driven rather than ticker-driven elsewhere in OSS.
const uploadTTL = 30 * time.Minute

// UploadStore stages a skill ZIP bundle across begin/chunk/commit calls — the
// same three-step shape OpenClaw's upload API uses — so a client never has to
// hold an entire bundle in memory or in one HTTP request body. It is
// in-process, ephemeral state: an unfinished upload is lost on restart, which
// is fine, since nothing has been registered yet at that point.
type UploadStore struct {
	mu     sync.Mutex
	staged map[string]*stagedUpload
}

type stagedUpload struct {
	file         *os.File
	expectedSize int64
	written      int64
	createdAt    time.Time
	committed    bool
	path         string
}

// NewUploadStore creates an empty UploadStore.
func NewUploadStore() *UploadStore {
	return &UploadStore{staged: make(map[string]*stagedUpload)}
}

// Begin stages a new upload of the given declared size, returning an ID the
// caller passes to WriteChunk and Commit. size is checked against
// MaxSkillBundleBytes up front so a caller can't stage a bundle that could
// never pass ExtractZip anyway.
func (u *UploadStore) Begin(size int64) (uploadID string, err error) {
	if size <= 0 {
		return "", fmt.Errorf("upload size must be positive")
	}
	if size > MaxSkillBundleBytes {
		return "", fmt.Errorf("upload size %d exceeds the bundle size limit of %d", size, MaxSkillBundleBytes)
	}

	f, err := os.CreateTemp("", "agentoven-skill-upload-*.zip")
	if err != nil {
		return "", fmt.Errorf("staging upload: %w", err)
	}

	id := uuid.New().String()
	u.mu.Lock()
	u.sweepLocked()
	u.staged[id] = &stagedUpload{file: f, expectedSize: size, createdAt: time.Now()}
	u.mu.Unlock()
	return id, nil
}

// WriteChunk appends data to the staged upload. Writing past the declared
// size is rejected rather than silently truncated or accepted — a client
// that doesn't know its own upload's size shouldn't be trusted to know when
// to stop.
func (u *UploadStore) WriteChunk(uploadID string, data []byte) error {
	u.mu.Lock()
	up, ok := u.staged[uploadID]
	u.mu.Unlock()
	if !ok {
		return fmt.Errorf("no staged upload %q (expired or never began)", uploadID)
	}
	if up.written+int64(len(data)) > up.expectedSize {
		return fmt.Errorf("upload %q exceeds its declared size of %d bytes", uploadID, up.expectedSize)
	}
	n, err := up.file.Write(data)
	up.written += int64(n)
	return err
}

// Commit finalizes a staged upload and returns the local file path holding
// the complete ZIP bytes, ready for ExtractZip. It is idempotent — the HTTP
// commit endpoint calls it once to confirm completeness, and RegisterSkill's
// source=upload path calls it again to get the file back; the second call
// must not try to close an already-closed file. The caller is responsible
// for calling Discard once it's done with that file.
func (u *UploadStore) Commit(uploadID string) (path string, err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	up, ok := u.staged[uploadID]
	if !ok {
		return "", fmt.Errorf("no staged upload %q (expired or never began)", uploadID)
	}
	if up.committed {
		return up.path, nil
	}
	if up.written != up.expectedSize {
		return "", fmt.Errorf("upload %q incomplete: received %d of %d declared bytes", uploadID, up.written, up.expectedSize)
	}
	if err := up.file.Close(); err != nil {
		return "", err
	}
	up.committed = true
	up.path = up.file.Name()
	return up.path, nil
}

// Discard removes a staged upload's temp file and bookkeeping — call it once
// a committed upload has been consumed, or to abandon one early.
func (u *UploadStore) Discard(uploadID string) error {
	u.mu.Lock()
	up, ok := u.staged[uploadID]
	delete(u.staged, uploadID)
	u.mu.Unlock()
	if !ok {
		return nil
	}
	up.file.Close()
	return os.Remove(up.file.Name())
}

// sweepLocked removes staged uploads older than uploadTTL. Called with mu
// already held, from Begin — an abandoned upload is only ever cleaned up the
// next time someone starts a new one, which is enough to keep temp files
// from accumulating under normal use without needing a background goroutine.
func (u *UploadStore) sweepLocked() {
	cutoff := time.Now().Add(-uploadTTL)
	for id, up := range u.staged {
		if up.createdAt.Before(cutoff) {
			up.file.Close()
			os.Remove(up.file.Name())
			delete(u.staged, id)
		}
	}
}
