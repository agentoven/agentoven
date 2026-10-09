package skills

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// Size limits mirror what OpenClaw enforces for a managed skill bundle —
// generous enough for real instructions/scripts/references, small enough
// that a bundle can't be used to smuggle in an unbounded payload.
const (
	MaxSkillFiles       = 256
	MaxSkillFileSize    = 1 << 20 // 1 MiB
	MaxSkillBundleBytes = 8 << 20 // 8 MiB
)

// ManifestFilename is the one file every skill bundle must contain, at its
// root — matching every Agent Skills-compatible client.
const ManifestFilename = "SKILL.md"

// Bundle is a skill's files, read into memory: relative path → content.
// Bundles are capped (MaxSkillFiles/MaxSkillFileSize/MaxSkillBundleBytes) so
// nothing downstream — the verification prompt, the registered record — ever
// has to handle an unbounded payload.
type Bundle map[string][]byte

// Manifest parses this bundle's SKILL.md. Every bundle-producing function in
// this package already guarantees one exists before returning, so this only
// fails if ParseManifest itself rejects the content.
func (b Bundle) Manifest() (*models.SkillManifest, error) {
	return ParseManifest(b[ManifestFilename])
}

// ExtractZip reads a ZIP archive into a Bundle, enforcing the size limits and
// requiring a root-level SKILL.md. Path traversal (a ZIP entry naming itself
// outside the bundle root, e.g. "../../etc/passwd") is rejected outright —
// archive/zip does not sanitize entry names for you.
func ExtractZip(r *zip.Reader) (Bundle, error) {
	if len(r.File) > MaxSkillFiles {
		return nil, fmt.Errorf("skill bundle has %d files, exceeding the limit of %d", len(r.File), MaxSkillFiles)
	}

	bundle := make(Bundle, len(r.File))
	var total int64
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		clean := filepath.Clean(f.Name)
		if clean == ".." || strings.HasPrefix(clean, "../") || filepath.IsAbs(clean) {
			return nil, fmt.Errorf("skill bundle entry %q escapes the bundle root", f.Name)
		}
		if f.UncompressedSize64 > MaxSkillFileSize {
			return nil, fmt.Errorf("skill bundle file %q is %d bytes, exceeding the per-file limit of %d", f.Name, f.UncompressedSize64, MaxSkillFileSize)
		}
		total += int64(f.UncompressedSize64)
		if total > MaxSkillBundleBytes {
			return nil, fmt.Errorf("skill bundle exceeds the total size limit of %d bytes", MaxSkillBundleBytes)
		}

		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("opening %q in skill bundle: %w", f.Name, err)
		}
		data, err := io.ReadAll(io.LimitReader(rc, MaxSkillFileSize+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("reading %q in skill bundle: %w", f.Name, err)
		}
		bundle[clean] = data
	}

	if _, ok := bundle[ManifestFilename]; !ok {
		return nil, fmt.Errorf("skill bundle has no root-level %s", ManifestFilename)
	}
	return bundle, nil
}

// LoadDir reads a skill bundle from a directory already present on the
// server's own filesystem — the SkillSourcePath install path, for an admin
// who deploys skills alongside the control plane rather than uploading them.
func LoadDir(root string) (Bundle, error) {
	bundle := make(Bundle)
	var total int64
	var count int
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// A symlink in a cloned repo could point anywhere on this machine
		// (/etc/passwd, a credentials file); never follow one into a bundle.
		if d.IsDir() || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		count++
		if count > MaxSkillFiles {
			return fmt.Errorf("skill directory has more than %d files", MaxSkillFiles)
		}
		info, statErr := d.Info()
		if statErr != nil {
			return statErr
		}
		if info.Size() > MaxSkillFileSize {
			return fmt.Errorf("skill file %q is %d bytes, exceeding the per-file limit of %d", rel, info.Size(), MaxSkillFileSize)
		}
		total += info.Size()
		if total > MaxSkillBundleBytes {
			return fmt.Errorf("skill directory exceeds the total size limit of %d bytes", MaxSkillBundleBytes)
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		bundle[rel] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, ok := bundle[ManifestFilename]; !ok {
		return nil, fmt.Errorf("skill directory %s has no root-level %s", root, ManifestFilename)
	}
	return bundle, nil
}

// SaveDir writes a bundle's files to root, creating it and any needed
// subdirectories. This is LoadDir's inverse — the two together are what let
// a bundle round-trip through any plain POSIX filesystem, which is the
// entire point: a durable skill store backed by a mounted Azure Files
// share, an S3/GCS FUSE mount, or just local disk all look identical to this
// function — it never needs to know which one it's writing to.
func SaveDir(bundle Bundle, root string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("creating skill storage directory %s: %w", root, err)
	}
	for rel, data := range bundle {
		dest := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("creating directory for %s: %w", rel, err)
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", rel, err)
		}
	}
	return nil
}
