package store_test

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/r4ph92/DevDuel/internal/challenge"
	"github.com/r4ph92/DevDuel/internal/id"
	"github.com/r4ph92/DevDuel/internal/store"
)

// paths is the tree as a list of paths, which is what most of these tests
// actually want to compare.
func paths(t *testing.T, db *store.Store, match, user id.ID) []string {
	t.Helper()

	entries, err := db.ListWorkspace(t.Context(), match, user)
	if err != nil {
		t.Fatalf("list workspace: %v", err)
	}
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Path
	}
	return out
}

// Both players start from the same code. That is the premise of the whole
// product, so it is seeded from one set of rows in the transaction that
// starts the clock rather than copied twice and hoped over.
func TestStartingAMatchSeedsBothWorkspaces(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	match, host, guest := l.started(t, "SEEDED23")

	hostTree, guestTree := paths(t, l.db, match, host), paths(t, l.db, match, guest)
	if len(hostTree) == 0 {
		t.Fatal("the host started with an empty workspace")
	}
	if fmt.Sprint(hostTree) != fmt.Sprint(guestTree) {
		t.Errorf("host has %v and guest has %v, want identical trees", hostTree, guestTree)
	}

	// And byte for byte, not just the same names.
	for _, path := range hostTree {
		hostFile, err := l.db.ReadWorkspaceFile(t.Context(), match, host, path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		guestFile, err := l.db.ReadWorkspaceFile(t.Context(), match, guest, path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !bytes.Equal(hostFile.Content, guestFile.Content) {
			t.Errorf("%s differs between the two players", path)
		}
	}
}

// A lobby has no workspace: there is nothing to edit until the clock starts.
func TestALobbyHasNoWorkspaceYet(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	match, host, _ := l.filled(t, "NOTYET23")

	if tree := paths(t, l.db, match, host); len(tree) != 0 {
		t.Errorf("a lobby already holds %v, want nothing", tree)
	}
}

// The issue's done-when, at the layer that can actually enforce it.
func TestOnePlayerCannotReachTheOthersWorkspace(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	match, host, guest := l.started(t, "MINEON23")
	stranger := seedUser(t, l.db)

	if err := l.db.WriteWorkspaceFile(t.Context(), match, host, "mine.js", []byte("host only")); err != nil {
		t.Fatalf("host writes: %v", err)
	}

	// The guest's tree is untouched by it.
	for _, path := range paths(t, l.db, match, guest) {
		if path == "mine.js" {
			t.Error("the host's new file appeared in the guest's workspace")
		}
	}
	if _, err := l.db.ReadWorkspaceFile(t.Context(), match, guest, "mine.js"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("guest reading the host's file = %v, want ErrNotFound", err)
	}

	// And somebody who is not in the match gets the same answer everywhere,
	// rather than learning that the match exists.
	if _, err := l.db.ReadWorkspaceFile(t.Context(), match, stranger, "mine.js"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("stranger reading = %v, want ErrNotFound", err)
	}
	if err := l.db.WriteWorkspaceFile(t.Context(), match, stranger, "theirs.js", []byte("x")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("stranger writing = %v, want ErrNotFound", err)
	}
	if err := l.db.DeleteWorkspaceFile(t.Context(), match, stranger, "mine.js"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("stranger deleting = %v, want ErrNotFound", err)
	}
}

func TestWritingAndReadingBackAFile(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	match, host, _ := l.started(t, "WRITE234")

	// A NUL byte, because the column is bytea precisely so that a file the
	// player uploads cannot be rejected for holding one.
	content := []byte("first\x00second")
	if err := l.db.WriteWorkspaceFile(t.Context(), match, host, "src/app.js", content); err != nil {
		t.Fatalf("write: %v", err)
	}

	file, err := l.db.ReadWorkspaceFile(t.Context(), match, host, "src/app.js")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(file.Content, content) {
		t.Errorf("read back %q, want %q", file.Content, content)
	}
	if file.UpdatedAt.IsZero() {
		t.Error("the file came back with no update time")
	}

	// Writing again replaces rather than failing on the primary key.
	if err := l.db.WriteWorkspaceFile(t.Context(), match, host, "src/app.js", []byte("second version")); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	file, err = l.db.ReadWorkspaceFile(t.Context(), match, host, "src/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if string(file.Content) != "second version" {
		t.Errorf("after rewriting, content is %q", file.Content)
	}
}

func TestDeletingAFileIsIdempotent(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	match, host, _ := l.started(t, "DELETE23")

	if err := l.db.WriteWorkspaceFile(t.Context(), match, host, "gone.js", []byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}
	for range 2 {
		if err := l.db.DeleteWorkspaceFile(t.Context(), match, host, "gone.js"); err != nil {
			t.Fatalf("delete: %v", err)
		}
	}
	if _, err := l.db.ReadWorkspaceFile(t.Context(), match, host, "gone.js"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("reading a deleted file = %v, want ErrNotFound", err)
	}
}

// The tree the judge reads is the tree that existed at the buzzer, so the
// window closes with the clock rather than whenever the HTTP layer remembers.
func TestWritesAreRefusedOutsideTheMatch(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)

	t.Run("before it starts", func(t *testing.T) {
		match, host, _ := l.filled(t, "BEFORE23")

		err := l.db.WriteWorkspaceFile(t.Context(), match, host, "early.js", []byte("x"))
		if !errors.Is(err, store.ErrMatchNotActive) {
			t.Errorf("writing in a lobby = %v, want ErrMatchNotActive", err)
		}
	})

	t.Run("after both submit", func(t *testing.T) {
		match, host, guest := l.started(t, "AFTER234")
		for _, player := range []id.ID{host, guest} {
			if _, _, err := l.db.Submit(t.Context(), match, player); err != nil {
				t.Fatalf("submit: %v", err)
			}
		}

		err := l.db.WriteWorkspaceFile(t.Context(), match, host, "late.js", []byte("x"))
		if !errors.Is(err, store.ErrMatchNotActive) {
			t.Errorf("writing after judging began = %v, want ErrMatchNotActive", err)
		}
		if err := l.db.DeleteWorkspaceFile(t.Context(), match, host, "server.js"); !errors.Is(err, store.ErrMatchNotActive) {
			t.Errorf("deleting after judging began = %v, want ErrMatchNotActive", err)
		}
	})
}

func TestPathsThatCouldNotBeFilesAreRefused(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	match, host, _ := l.started(t, "PATHS234")

	for name, path := range map[string]string{
		"empty":          "",
		"absolute":       "/etc/passwd",
		"parent":         "../secrets",
		"parent within":  "src/../../secrets",
		"current":        "./app.js",
		"trailing slash": "src/",
		"doubled slash":  "src//app.js",
		"a NUL byte":     "src/app\x00.js",
	} {
		t.Run(name, func(t *testing.T) {
			if err := l.db.WriteWorkspaceFile(t.Context(), match, host, path, []byte("x")); !errors.Is(err, store.ErrInvalidPath) {
				t.Errorf("writing to %q = %v, want ErrInvalidPath", path, err)
			}
			if _, err := l.db.ReadWorkspaceFile(t.Context(), match, host, path); !errors.Is(err, store.ErrInvalidPath) {
				t.Errorf("reading %q = %v, want ErrInvalidPath", path, err)
			}
		})
	}
}

func TestAWorkspaceHasACeiling(t *testing.T) {
	t.Parallel()
	l := newLobbyFixture(t)
	match, host, _ := l.started(t, "FULLUP23")

	oversized := make([]byte, challenge.MaxFileBytes+1)
	if err := l.db.WriteWorkspaceFile(t.Context(), match, host, "huge.bin", oversized); !errors.Is(err, store.ErrFileTooLarge) {
		t.Errorf("writing an oversized file = %v, want ErrFileTooLarge", err)
	}

	// Fill the tree to its byte ceiling one megabyte at a time, then ask for
	// one more.
	big := make([]byte, challenge.MaxFileBytes)
	var full bool
	for i := range (store.MaxWorkspaceBytes / challenge.MaxFileBytes) + 1 {
		err := l.db.WriteWorkspaceFile(t.Context(), match, host, fmt.Sprintf("big/%d.bin", i), big)
		if errors.Is(err, store.ErrWorkspaceFull) {
			full = true
			break
		}
		if err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if !full {
		t.Error("a workspace grew past its byte ceiling, want ErrWorkspaceFull")
	}
}
