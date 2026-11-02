package blobstore

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"
)

// Policy knobs, shared by the OSS server and by Pro (which mounts the same
// store behind its own routes).
const (
	// EnvMaxBytes caps one blob and one upload request, in bytes.
	EnvMaxBytes = "AGENTOVEN_BLOB_MAX_BYTES"
	// EnvTTLDays is how long a blob survives without being read or written. 0 keeps blobs forever.
	EnvTTLDays = "AGENTOVEN_BLOB_TTL_DAYS"

	DefaultMaxBytes int64 = 25 << 20
	DefaultTTLDays        = 7
)

// MaxBytesFromEnv returns the per-blob and per-upload size limit.
func MaxBytesFromEnv() int64 {
	if v := os.Getenv(EnvMaxBytes); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return DefaultMaxBytes
}

// TTLFromEnv returns the blob retention; zero means keep forever.
func TTLFromEnv() time.Duration {
	days := DefaultTTLDays
	if v := os.Getenv(EnvTTLDays); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			days = n
		}
	}
	return time.Duration(days) * 24 * time.Hour
}

// Janitor expires old blobs on a timer.
//
// Retention is by last access, not by reference: the control plane cannot cheaply
// list which sessions or journal entries still name a blob, so a blob is removed
// once nothing has read or written it for the TTL. A session or a paused run that
// comes back after that gets a clear "blob not found" for the file, not stale
// bytes. The default TTL matches the run journal's retention (7 days), so a run
// that can still be resumed still has its files.
type Janitor struct {
	Store    Sweeper
	TTL      time.Duration
	Interval time.Duration
}

// Sweep runs one pass.
func (j *Janitor) Sweep(ctx context.Context) (int, error) {
	return j.Store.Sweep(ctx, time.Now().Add(-j.TTL))
}

// Start runs the janitor until ctx is cancelled. It does nothing when the TTL is zero.
func (j *Janitor) Start(ctx context.Context) {
	if j.TTL <= 0 || j.Store == nil {
		return
	}
	interval := j.Interval
	if interval <= 0 {
		interval = time.Hour
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if n, err := j.Sweep(ctx); err != nil {
					log.Warn().Err(err).Msg("Blob retention sweep failed")
				} else if n > 0 {
					log.Info().Int("removed", n).Msg("Blob retention removed expired blobs")
				}
			}
		}
	}()
}

// StartJanitorFromEnv starts a janitor for s using AGENTOVEN_BLOB_TTL_DAYS, when
// the store can expire blobs itself. Pro calls this for any store it plugs in.
func StartJanitorFromEnv(ctx context.Context, s Store) {
	sw, ok := s.(Sweeper)
	if !ok {
		return
	}
	(&Janitor{Store: sw, TTL: TTLFromEnv()}).Start(ctx)
}
