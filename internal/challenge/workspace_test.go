package challenge_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r4ph92/DevDuel/internal/challenge"
)

// write puts a file in dir, creating the parents it needs.
func write(t *testing.T, dir, path string, content []byte) {
	t.Helper()

	full := filepath.Join(dir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

// The repository's own challenge, which is what registration will read.
func TestWorkspaceReadsTheChallengesStartingFiles(t *testing.T) {
	t.Parallel()

	spec, err := challenge.Load("../../challenges/todo-api")
	if err != nil {
		t.Fatalf("load challenge: %v", err)
	}

	files, err := spec.Workspace()
	if err != nil {
		t.Fatalf("read workspace: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("the challenge's workspace came back empty")
	}

	for _, f := range files {
		if strings.HasPrefix(f.Path, "/") || strings.Contains(f.Path, "\\") {
			t.Errorf("path %q is not a relative slash path", f.Path)
		}
		if len(f.Content) == 0 {
			t.Errorf("%s came back with no content", f.Path)
		}
		if f.Digest() == "" {
			t.Errorf("%s has no digest", f.Path)
		}
	}
}

func TestLoadWorkspaceSortsAndKeepsNestedPaths(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "src/b.js", []byte("b"))
	write(t, dir, "a.js", []byte("a"))
	write(t, dir, "src/nested/deep/c.js", []byte("c"))

	files, err := challenge.LoadWorkspace(dir)
	if err != nil {
		t.Fatalf("read workspace: %v", err)
	}

	want := []string{"a.js", "src/b.js", "src/nested/deep/c.js"}
	if len(files) != len(want) {
		t.Fatalf("read %d files, want %d", len(files), len(want))
	}
	for i, path := range want {
		if files[i].Path != path {
			t.Errorf("file %d is %q, want %q", i, files[i].Path, path)
		}
	}
}

// A link is how a starting workspace would otherwise hand a player something
// from outside itself, or hand the judge a path that does not exist in a
// container.
func TestLoadWorkspaceRefusesASymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "real.js", []byte("real"))

	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "sneaky.js")); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}

	if _, err := challenge.LoadWorkspace(dir); err == nil {
		t.Error("a workspace with a symlink loaded, want a refusal")
	}
}

func TestLoadWorkspaceRefusesWhatItCannotStore(t *testing.T) {
	t.Parallel()

	t.Run("a file over the size limit", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		write(t, dir, "huge.bin", make([]byte, challenge.MaxFileBytes+1))

		if _, err := challenge.LoadWorkspace(dir); err == nil {
			t.Error("an oversized file loaded, want a refusal")
		}
	})

	t.Run("more files than the limit", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		for i := range challenge.MaxWorkspaceFiles + 1 {
			write(t, dir, filepath.ToSlash(filepath.Join("src", string(rune('a'+i%26))+strings.Repeat("x", i/26)+".js")), []byte("x"))
		}

		if _, err := challenge.LoadWorkspace(dir); err == nil {
			t.Error("a workspace over the file count loaded, want a refusal")
		}
	})

	t.Run("more bytes than the limit", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		big := make([]byte, challenge.MaxFileBytes)
		for i := range (challenge.MaxWorkspaceBytes / challenge.MaxFileBytes) + 1 {
			write(t, dir, filepath.ToSlash(filepath.Join("big", string(rune('a'+i))+".bin")), big)
		}

		if _, err := challenge.LoadWorkspace(dir); err == nil {
			t.Error("a workspace over the byte limit loaded, want a refusal")
		}
	})

	t.Run("no files at all", func(t *testing.T) {
		t.Parallel()

		if _, err := challenge.LoadWorkspace(t.TempDir()); err == nil {
			t.Error("an empty workspace loaded, want a refusal")
		}
	})
}

// The digest is what registration compares, so it has to follow the bytes and
// nothing else.
func TestDigestFollowsTheContent(t *testing.T) {
	t.Parallel()

	same := challenge.File{Path: "a.js", Content: []byte("const x = 1")}
	renamed := challenge.File{Path: "b.js", Content: []byte("const x = 1")}
	edited := challenge.File{Path: "a.js", Content: []byte("const x = 2")}

	if same.Digest() != renamed.Digest() {
		t.Error("the same bytes under a different name gave a different digest")
	}
	if same.Digest() == edited.Digest() {
		t.Error("edited bytes gave the same digest")
	}
}
