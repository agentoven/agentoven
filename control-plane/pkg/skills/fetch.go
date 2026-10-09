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
	dir, cleanup, err := CheckoutGit(ctx, gitURL, ref, "")
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return LoadDir(dir)
}

// CheckoutGit shallow-clones gitURL into a temp directory and returns it with
// a cleanup func. ref is a branch or tag (empty: the default branch); sha, if
// set, pins the checkout to that exact commit, which is what a marketplace
// entry gives so an install cannot drift from what was reviewed.
//
// Git is told never to prompt for credentials, so an unreachable or private
// repo fails fast instead of hanging the request.
func CheckoutGit(ctx context.Context, gitURL, ref, sha string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "agentoven-skill-clone-*")
	if err != nil {
		return "", nil, fmt.Errorf("staging git clone: %w", err)
	}
	cleanup := func() { os.RemoveAll(dir) }

	cloneCtx, cancel := context.WithTimeout(ctx, GitCloneTimeout)
	defer cancel()

	git := func(args ...string) error {
		cmd := exec.CommandContext(cloneCtx, "git", args...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if out, runErr := cmd.CombinedOutput(); runErr != nil {
			return fmt.Errorf("git %s failed: %s: %w", args[0], strings.TrimSpace(string(out)), runErr)
		}
		return nil
	}

	args := []string{"clone", "--depth", "1"}
	if ref != "" {
		args = append(args, "--branch", ref, "--single-branch")
	}
	args = append(args, gitURL, dir)
	steps := [][]string{args}
	if sha != "" {
		steps = append(steps,
			[]string{"-C", dir, "fetch", "--depth", "1", "origin", sha},
			[]string{"-C", dir, "checkout", "--detach", "FETCH_HEAD"})
	}
	for _, step := range steps {
		if err := git(step...); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	return dir, cleanup, nil
}
