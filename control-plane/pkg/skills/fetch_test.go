package skills_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/skills"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Skipf("git unavailable or failed (%v): %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestCheckoutGitPinsToASha(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	file := filepath.Join(repo, "SKILL.md")
	if err := os.WriteFile(file, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "one")
	first := git(t, repo, "rev-parse", "HEAD")
	if err := os.WriteFile(file, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "commit", "-q", "-am", "two")
	// A shallow clone of a local path ignores --depth unless told it is a URL.
	git(t, repo, "config", "uploadpack.allowAnySHA1InWant", "true")
	url := "file://" + repo

	read := func(sha string) string {
		dir, cleanup, err := skills.CheckoutGit(context.Background(), url, "", sha)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		b, _ := os.ReadFile(filepath.Join(dir, "SKILL.md"))
		return string(b)
	}
	if got := read(""); got != "v2" {
		t.Errorf("default checkout = %q", got)
	}
	if got := read(first); got != "v1" {
		t.Errorf("pinned checkout = %q, want the first commit", got)
	}
}

func TestCheckoutGitFailureReturnsAnErrorAndLeavesNothingBehind(t *testing.T) {
	before, _ := filepath.Glob(filepath.Join(os.TempDir(), "agentoven-skill-clone-*"))
	dir, cleanup, err := skills.CheckoutGit(context.Background(), "file:///definitely/not/a/repo", "", "")
	if err == nil || dir != "" || cleanup != nil {
		t.Fatalf("dir=%q cleanup=%v err=%v", dir, cleanup != nil, err)
	}
	after, _ := filepath.Glob(filepath.Join(os.TempDir(), "agentoven-skill-clone-*"))
	if len(after) > len(before) {
		t.Errorf("a failed clone left its directory behind")
	}
}
