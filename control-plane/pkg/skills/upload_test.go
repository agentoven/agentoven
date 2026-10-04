package skills_test

import (
	"os"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/skills"
)

func TestUploadStoreRoundTrips(t *testing.T) {
	u := skills.NewUploadStore()
	data := []byte("some zip bytes")

	id, err := u.Begin(int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if err := u.WriteChunk(id, data[:5]); err != nil {
		t.Fatal(err)
	}
	if err := u.WriteChunk(id, data[5:]); err != nil {
		t.Fatal(err)
	}
	path, err := u.Commit(id)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Fatalf("got %q, want %q", got, data)
	}
	if err := u.Discard(id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("expected the staged file to be removed after Discard")
	}
}

func TestUploadStoreRejectsOversizedDeclaration(t *testing.T) {
	u := skills.NewUploadStore()
	if _, err := u.Begin(skills.MaxSkillBundleBytes + 1); err == nil {
		t.Fatal("expected Begin to reject a declared size over the bundle limit")
	}
}

func TestUploadStoreRejectsWritingPastDeclaredSize(t *testing.T) {
	u := skills.NewUploadStore()
	id, err := u.Begin(3)
	if err != nil {
		t.Fatal(err)
	}
	if err := u.WriteChunk(id, []byte("too much data")); err == nil {
		t.Fatal("expected WriteChunk to reject data exceeding the declared size")
	}
}

func TestUploadStoreRejectsCommitBeforeComplete(t *testing.T) {
	u := skills.NewUploadStore()
	id, err := u.Begin(10)
	if err != nil {
		t.Fatal(err)
	}
	if err := u.WriteChunk(id, []byte("short")); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Commit(id); err == nil {
		t.Fatal("expected Commit to reject an incomplete upload")
	}
}

func TestUploadStoreRejectsUnknownID(t *testing.T) {
	u := skills.NewUploadStore()
	if err := u.WriteChunk("nonexistent", []byte("x")); err == nil {
		t.Fatal("expected an error writing to an unknown upload ID")
	}
	if _, err := u.Commit("nonexistent"); err == nil {
		t.Fatal("expected an error committing an unknown upload ID")
	}
}
