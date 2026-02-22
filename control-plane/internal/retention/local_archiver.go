package retention

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
	"github.com/rs/zerolog/log"
)

// LocalFileArchiver writes expired data as JSONL files to a local directory.
// This is the default archive driver for OSS / development.
//
// Directory structure:
//
//	{basePath}/{kitchen}/traces/2026-02-20T15-04-05Z.jsonl[.gz]
//	{basePath}/{kitchen}/audit_events/2026-02-20T15-04-05Z.jsonl[.gz]
type LocalFileArchiver struct {
	basePath string
	compress bool
}

// NewLocalFileArchiver creates a file-based archiver. If basePath is empty,
// it defaults to "~/.agentoven/archive".
func NewLocalFileArchiver(basePath string, compress bool) *LocalFileArchiver {
	if basePath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			basePath = "/tmp/agentoven/archive"
		} else {
			basePath = filepath.Join(home, ".agentoven", "archive")
		}
	}
	return &LocalFileArchiver{basePath: basePath, compress: compress}
}

func (a *LocalFileArchiver) Kind() string { return "local" }

func (a *LocalFileArchiver) ArchiveTraces(_ context.Context, kitchen string, traces []models.Trace) (string, error) {
	dir := filepath.Join(a.basePath, kitchen, "traces")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create archive dir: %w", err)
	}

	filename := time.Now().UTC().Format("2006-01-02T15-04-05Z") + ".jsonl"
	if a.compress {
		filename += ".gz"
	}
	fpath := filepath.Join(dir, filename)

	f, err := os.Create(fpath)
	if err != nil {
		return "", fmt.Errorf("create archive file: %w", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	if a.compress {
		gw := gzip.NewWriter(f)
		defer gw.Close()
		enc = json.NewEncoder(gw)
	}

	for _, t := range traces {
		if err := enc.Encode(t); err != nil {
			return "", fmt.Errorf("encode trace %s: %w", t.ID, err)
		}
	}

	log.Debug().
		Str("path", fpath).
		Int("count", len(traces)).
		Str("kitchen", kitchen).
		Msg("Archived traces to local file")

	return fpath, nil
}

func (a *LocalFileArchiver) ArchiveAuditEvents(_ context.Context, kitchen string, events []models.AuditEvent) (string, error) {
	dir := filepath.Join(a.basePath, kitchen, "audit_events")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create archive dir: %w", err)
	}

	filename := time.Now().UTC().Format("2006-01-02T15-04-05Z") + ".jsonl"
	if a.compress {
		filename += ".gz"
	}
	fpath := filepath.Join(dir, filename)

	f, err := os.Create(fpath)
	if err != nil {
		return "", fmt.Errorf("create archive file: %w", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	if a.compress {
		gw := gzip.NewWriter(f)
		defer gw.Close()
		enc = json.NewEncoder(gw)
	}

	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			return "", fmt.Errorf("encode audit event %s: %w", e.ID, err)
		}
	}

	log.Debug().
		Str("path", fpath).
		Int("count", len(events)).
		Str("kitchen", kitchen).
		Msg("Archived audit events to local file")

	return fpath, nil
}

func (a *LocalFileArchiver) HealthCheck(_ context.Context) error {
	// Verify we can write to the base path
	if err := os.MkdirAll(a.basePath, 0o755); err != nil {
		return fmt.Errorf("archive path not writable: %w", err)
	}
	testFile := filepath.Join(a.basePath, ".healthcheck")
	if err := os.WriteFile(testFile, []byte("ok"), 0o644); err != nil {
		return fmt.Errorf("archive path not writable: %w", err)
	}
	os.Remove(testFile)
	return nil
}
