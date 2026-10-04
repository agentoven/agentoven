package skills_test

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/skills"
)

func zipOf(t *testing.T, files map[string]string) *zip.Reader {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

const validSkillMD = "---\nname: x\ndescription: y\n---\nbody\n"

func TestExtractZipLoadsAValidBundle(t *testing.T) {
	r := zipOf(t, map[string]string{
		"SKILL.md":          validSkillMD,
		"scripts/run.py":    "print('hi')",
		"references/doc.md": "reference material",
	})
	b, err := skills.ExtractZip(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 3 {
		t.Fatalf("expected 3 files, got %d: %v", len(b), b)
	}
	m, err := b.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "x" {
		t.Fatalf("unexpected manifest: %+v", m)
	}
}

func TestExtractZipRejectsMissingManifest(t *testing.T) {
	r := zipOf(t, map[string]string{"notes.txt": "no manifest here"})
	if _, err := skills.ExtractZip(r); err == nil {
		t.Fatal("expected an error for a bundle with no root-level SKILL.md")
	}
}

func TestExtractZipRejectsPathTraversal(t *testing.T) {
	r := zipOf(t, map[string]string{
		"SKILL.md":         validSkillMD,
		"../../etc/passwd": "escape attempt",
	})
	if _, err := skills.ExtractZip(r); err == nil {
		t.Fatal("expected an error for a zip entry that escapes the bundle root")
	}
}

func TestExtractZipRejectsOversizedFile(t *testing.T) {
	big := make([]byte, skills.MaxSkillFileSize+1)
	r := zipOf(t, map[string]string{
		"SKILL.md": validSkillMD,
		"big.bin":  string(big),
	})
	if _, err := skills.ExtractZip(r); err == nil {
		t.Fatal("expected an error for a file exceeding the per-file size limit")
	}
}

func TestExtractZipRejectsTooManyFiles(t *testing.T) {
	files := map[string]string{"SKILL.md": validSkillMD}
	for i := 0; i < skills.MaxSkillFiles; i++ {
		files[filepath.Join("f", strconv.Itoa(i)+".txt")] = "x"
	}
	r := zipOf(t, files)
	if _, err := skills.ExtractZip(r); err == nil {
		t.Fatal("expected an error for a bundle exceeding the file-count limit")
	}
}

func TestLoadDirReadsASkillDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(validSkillMD), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "run.sh"), []byte("echo hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	b, err := skills.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 2 {
		t.Fatalf("expected 2 files, got %d: %v", len(b), b)
	}
}

// TestSaveDirRoundTripsWithLoadDir pins down the exact mechanism durable
// skill storage depends on (see Pro's skillstorage package): a bundle
// written with SaveDir reads back byte-for-byte with LoadDir, on a plain
// POSIX directory — which is all a mounted Azure Files share, an S3/GCS
// FUSE mount, or local disk ever look like to this code.
func TestSaveDirRoundTripsWithLoadDir(t *testing.T) {
	original := skills.Bundle{
		"SKILL.md":          []byte(validSkillMD),
		"scripts/run.sh":    []byte("echo hi"),
		"references/doc.md": []byte("reference material"),
	}

	root := t.TempDir()
	storeDir := filepath.Join(root, "kitchen-a", "web_search") // nested, not yet existing
	if err := skills.SaveDir(original, storeDir); err != nil {
		t.Fatal(err)
	}

	loaded, err := skills.LoadDir(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != len(original) {
		t.Fatalf("expected %d files back, got %d: %v", len(original), len(loaded), loaded)
	}
	for path, want := range original {
		got, ok := loaded[path]
		if !ok {
			t.Fatalf("expected %q to round-trip, got only: %v", path, loaded)
		}
		if string(got) != string(want) {
			t.Fatalf("file %q: got %q, want %q", path, got, want)
		}
	}
}

func TestSaveDirCreatesIntermediateDirectories(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "does", "not", "exist", "yet")
	bundle := skills.Bundle{"SKILL.md": []byte(validSkillMD), "a/b/c/deep.txt": []byte("deep")}

	if err := skills.SaveDir(bundle, deep); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(deep, "a", "b", "c", "deep.txt")); err != nil {
		t.Fatalf("expected nested directories to be created: %v", err)
	}
}

func TestLoadDirRejectsMissingManifest(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("no manifest"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := skills.LoadDir(dir); err == nil {
		t.Fatal("expected an error for a directory with no root-level SKILL.md")
	}
}
