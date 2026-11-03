package handlers

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// An agent is one row that several requests write: an edit (PUT), a bake or re-cook, a cool or rewarm.
// Each reads the agent, changes it and writes it back, so two at once used to lose one of them (a PUT
// racing a bake: the PUT's copy, read before the bake wrote, put the old configuration back, or the
// bake's write dropped the edit). The stores now refuse a write made from a copy that is no longer
// current (store.ErrStale, models.Agent.Revision). These handlers read the agent again and redo their
// change on it, which is safe because each one derives its write from the agent it has just read, never
// from a copy it kept; only when the agent keeps changing under it does the caller get a 409.

// staleAttempts is how often a handler reads the agent again before giving up with 409. A write that
// loses to another one loses to something that took a few milliseconds; five in a row is a storm.
const staleAttempts = 5

// staleRecorder holds back a handler's answer while retryOnStale decides whether to run it again. A
// handler tells it the write was stale through respondStoreError (or respondStale), which sets stale
// and writes nothing.
type staleRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
	stale  *store.ErrStale
}

func (s *staleRecorder) Header() http.Header { return s.header }
func (s *staleRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
}
func (s *staleRecorder) Write(b []byte) (int, error) {
	s.WriteHeader(http.StatusOK)
	return s.body.Write(b)
}

// maxRetriedBody bounds the body held for a repeat. An agent with its ingredients is far below it.
const maxRetriedBody = 16 << 20

// retryOnStale serves fn, and serves it again (on a fresh read of the agent) while its write was refused
// as stale, up to staleAttempts times. The request body is read once and given to each attempt.
func retryOnStale(w http.ResponseWriter, r *http.Request, fn func(http.ResponseWriter, *http.Request)) {
	var body []byte
	if r.Body != nil {
		var err error
		if body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, maxRetriedBody)); err != nil {
			respondError(w, http.StatusRequestEntityTooLarge, "request body is too large")
			return
		}
	}
	var last *store.ErrStale
	for i := 0; i < staleAttempts; i++ {
		rec := &staleRecorder{header: http.Header{}}
		req := r.Clone(r.Context())
		req.Body = io.NopCloser(bytes.NewReader(body))
		fn(rec, req)
		if rec.stale == nil {
			for k, v := range rec.header {
				w.Header()[k] = v
			}
			if rec.status != 0 {
				w.WriteHeader(rec.status)
			}
			_, _ = w.Write(rec.body.Bytes())
			return
		}
		last = rec.stale
	}
	respondStale(w, last)
}

// respondStale answers a write that lost to another one: 409 with the revision and version the agent has
// now. Inside retryOnStale it only records the loss, so the handler runs again.
func respondStale(w http.ResponseWriter, stale *store.ErrStale) {
	if rec, ok := w.(*staleRecorder); ok {
		rec.stale = stale
		return
	}
	respondJSON(w, http.StatusConflict, map[string]interface{}{
		"error":            "the agent was changed by another request while this one was being applied: read it again and retry",
		"current_revision": stale.Revision,
		"current_version":  stale.Version,
	})
}

// asStale returns the *store.ErrStale in err, if there is one.
func asStale(err error) (*store.ErrStale, bool) {
	var s *store.ErrStale
	return s, errors.As(err, &s)
}

// ifMatchRevision reads the revision a client says its edit is based on: "revision" in the body, or an
// If-Match header holding it (quoted or not). 0 means the client did not say.
func ifMatchRevision(r *http.Request, bodyRevision int64) int64 {
	if bodyRevision > 0 {
		return bodyRevision
	}
	v := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`)
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// respondStaleRevision answers an edit that named a revision other than the stored one. Unlike a write
// that lost a race it is not retried: the client asked for exactly that revision.
func respondStaleRevision(w http.ResponseWriter, current *models.Agent) {
	respondJSON(w, http.StatusConflict, map[string]interface{}{
		"error":            "the agent has changed since the revision this edit is based on: read it again and retry",
		"current_revision": current.Revision,
		"current_version":  current.Version,
	})
}
