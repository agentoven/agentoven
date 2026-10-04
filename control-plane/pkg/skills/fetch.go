package skills

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ExtractZipFile opens a ZIP file already on local disk (e.g. a committed
// chunked upload) and loads it the same way ExtractZip does.
func ExtractZipFile(path string) (Bundle, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("opening skill bundle: %w", err)
	}
	defer r.Close()
	return ExtractZip(&r.Reader)
}

// GitCloneTimeout bounds CloneGit. It runs inline in an admin-facing request
// (register, stage, refresh), not a background job, so it needs to fail
// fast rather than hang indefinitely on an unreachable or slow host.
const GitCloneTimeout = 60 * time.Second

// CloneGit shallow-clones gitURL at ref (a branch or tag; empty means the
// repo's default branch) into a throwaway temp directory, loads it as a
// bundle, and removes the clone — the bundle's in-memory file contents are
// what outlives this call, not the checkout itself.
func CloneGit(ctx context.Context, gitURL, ref string) (Bundle, error) {
	dir, err := os.MkdirTemp("", "agentoven-skill-clone-*")
	if err != nil {
		return nil, fmt.Errorf("staging git clone: %w", err)
	}
	defer os.RemoveAll(dir)

	cloneCtx, cancel := context.WithTimeout(ctx, GitCloneTimeout)
	defer cancel()

	args := []string{"clone", "--depth", "1"}
	if ref != "" {
		args = append(args, "--branch", ref, "--single-branch")
	}
	args = append(args, gitURL, dir)

	cmd := exec.CommandContext(cloneCtx, "git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git clone failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return LoadDir(dir)
}
