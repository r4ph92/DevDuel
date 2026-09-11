package api_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// tree is the file list a player sees.
type tree struct {
	Files []struct {
		Path      string    `json:"path"`
		Size      int       `json:"size"`
		UpdatedAt time.Time `json:"updated_at"`
	} `json:"files"`
}

// startedMatch is two signed-in players in a match with the clock running.
func startedMatch(t *testing.T, h *harness) (host, guest *http.Cookie, match string) {
	t.Helper()

	host, guest, match = twoPlayerLobby(t, h)
	for _, session := range []*http.Cookie{host, guest} {
		if res := h.post("/matches/"+match+"/ready", ``, session); res.status != http.StatusOK {
			t.Fatalf("ready: status %d, body %s", res.status, res.body)
		}
	}
	return host, guest, match
}

func (h *harness) files(t *testing.T, match string, session *http.Cookie) tree {
	t.Helper()

	res := h.get("/matches/"+match+"/files", session)
	if res.status != http.StatusOK {
		t.Fatalf("list files: status %d, body %s", res.status, res.body)
	}
	var out tree
	res.decode(t, &out)
	return out
}

// Both players get the challenge's starting files the moment the clock
// starts, and they get the same ones.
func TestBothPlayersStartFromTheSameFiles(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)
	host, guest, match := startedMatch(t, h)

	hostTree, guestTree := h.files(t, match, host), h.files(t, match, guest)
	if len(hostTree.Files) == 0 {
		t.Fatal("the match started with an empty workspace")
	}
	if len(hostTree.Files) != len(guestTree.Files) {
		t.Fatalf("host sees %d files, guest sees %d", len(hostTree.Files), len(guestTree.Files))
	}
	for i, f := range hostTree.Files {
		if g := guestTree.Files[i]; g.Path != f.Path || g.Size != f.Size {
			t.Errorf("file %d is %s (%d bytes) for the host and %s (%d bytes) for the guest",
				i, f.Path, f.Size, g.Path, g.Size)
		}
		if f.Size == 0 {
			t.Errorf("%s is empty", f.Path)
		}
	}
}

func TestWritingAndReadingAFileBack(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)
	host, _, match := startedMatch(t, h)

	const content = "export function add(a, b) { return a + b }\n"
	res := h.request(http.MethodPut, "/matches/"+match+"/files/src/add.js", content, host)
	if res.status != http.StatusNoContent {
		t.Fatalf("write: status %d, body %s", res.status, res.body)
	}

	back := h.get("/matches/"+match+"/files/src/add.js", host)
	if back.status != http.StatusOK {
		t.Fatalf("read: status %d, body %s", back.status, back.body)
	}
	if string(back.body) != content {
		t.Errorf("read back %q, want %q", back.body, content)
	}

	// And it shows up in the tree, at its own path.
	var found bool
	for _, f := range h.files(t, match, host).Files {
		if f.Path == "src/add.js" {
			found = true
			if f.Size != len(content) {
				t.Errorf("tree says %d bytes, want %d", f.Size, len(content))
			}
		}
	}
	if !found {
		t.Error("the new file is missing from the tree")
	}
}

// The done-when for this issue: one player cannot reach the other's files,
// and a test says so.
func TestAPlayerCannotReachTheOpponentsWorkspace(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)
	host, guest, match := startedMatch(t, h)

	if res := h.request(http.MethodPut, "/matches/"+match+"/files/secret.js", "host only", host); res.status != http.StatusNoContent {
		t.Fatalf("host writes: status %d, body %s", res.status, res.body)
	}

	// The guest asking for that exact path gets nothing: the path names a
	// file in the guest's own workspace, and there is none.
	if res := h.get("/matches/"+match+"/files/secret.js", guest); res.status != http.StatusNotFound {
		t.Errorf("guest reading the host's file = %d, want 404", res.status)
	}
	for _, f := range h.files(t, match, guest).Files {
		if f.Path == "secret.js" {
			t.Error("the host's file appeared in the guest's tree")
		}
	}

	// The guest writing to the same path writes their own file, and leaves
	// the host's alone.
	if res := h.request(http.MethodPut, "/matches/"+match+"/files/secret.js", "guest only", guest); res.status != http.StatusNoContent {
		t.Fatalf("guest writes: status %d, body %s", res.status, res.body)
	}
	hostCopy := h.get("/matches/"+match+"/files/secret.js", host)
	if string(hostCopy.body) != "host only" {
		t.Errorf("the host's file now reads %q", hostCopy.body)
	}

	// Somebody not in the match is told the match does not exist.
	stranger := h.signIn("stranger")
	if res := h.get("/matches/"+match+"/files", stranger); res.status != http.StatusNotFound {
		t.Errorf("stranger listing = %d, want 404", res.status)
	}
	if res := h.request(http.MethodPut, "/matches/"+match+"/files/x.js", "x", stranger); res.status != http.StatusNotFound {
		t.Errorf("stranger writing = %d, want 404", res.status)
	}
}

func TestDeletingAFile(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)
	host, _, match := startedMatch(t, h)

	if res := h.request(http.MethodPut, "/matches/"+match+"/files/tmp.js", "x", host); res.status != http.StatusNoContent {
		t.Fatalf("write: status %d", res.status)
	}

	// Twice, because a retried request must not become an error.
	for range 2 {
		if res := h.request(http.MethodDelete, "/matches/"+match+"/files/tmp.js", "", host); res.status != http.StatusNoContent {
			t.Fatalf("delete: status %d, body %s", res.status, res.body)
		}
	}
	if res := h.get("/matches/"+match+"/files/tmp.js", host); res.status != http.StatusNotFound {
		t.Errorf("reading a deleted file = %d, want 404", res.status)
	}
}

// The tree the judge will read is the one that existed at the buzzer.
func TestTheWorkspaceIsClosedOutsideTheMatch(t *testing.T) {
	t.Parallel()

	// A harness each: both cases sign in the same usernames, and they would
	// otherwise collide in one database.
	t.Run("before the clock starts", func(t *testing.T) {
		h := newHarness(t, false)
		host, _, match := twoPlayerLobby(t, h)

		res := h.request(http.MethodPut, "/matches/"+match+"/files/early.js", "x", host)
		if res.status != http.StatusConflict {
			t.Errorf("writing in a lobby = %d, want 409", res.status)
		}
		if code := res.failure(t).Code; code != "not_active" {
			t.Errorf("error code = %q, want not_active", code)
		}
	})

	t.Run("after both submit", func(t *testing.T) {
		h := newHarness(t, false)
		host, guest, match := startedMatch(t, h)
		for _, session := range []*http.Cookie{host, guest} {
			if res := h.post("/matches/"+match+"/submit", ``, session); res.status != http.StatusOK {
				t.Fatalf("submit: status %d, body %s", res.status, res.body)
			}
		}

		res := h.request(http.MethodPut, "/matches/"+match+"/files/late.js", "x", host)
		if res.status != http.StatusConflict {
			t.Errorf("writing after the match ended = %d, want 409", res.status)
		}

		// Reading still works: the match is over, not hidden.
		if read := h.get("/matches/"+match+"/files", host); read.status != http.StatusOK {
			t.Errorf("listing after the match ended = %d, want 200", read.status)
		}
	})
}

func TestWhatCannotBeStoredIsRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)
	host, _, match := startedMatch(t, h)

	t.Run("a path that climbs out", func(t *testing.T) {
		res := h.request(http.MethodPut, "/matches/"+match+"/files/../../etc/passwd", "x", host)
		// Either the router never matches it or the store refuses it; what
		// matters is that it is not a write.
		if res.status == http.StatusNoContent {
			t.Fatal("a path climbing out of the workspace was written")
		}
		if res.status != http.StatusBadRequest && res.status != http.StatusNotFound &&
			res.status != http.StatusMovedPermanently {
			t.Errorf("status = %d, want a refusal", res.status)
		}
	})

	t.Run("a file over the limit", func(t *testing.T) {
		res := h.request(http.MethodPut, "/matches/"+match+"/files/huge.bin",
			strings.Repeat("x", (1<<20)+1), host)
		if res.status != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d, want 413", res.status)
		}
		if code := res.failure(t).Code; code != "file_too_large" {
			t.Errorf("error code = %q, want file_too_large", code)
		}
	})
}

func TestWorkspaceRoutesNeedASession(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false)
	match := "01912d3f-0000-7000-8000-000000000000"

	for name, call := range map[string]func() response{
		"list":   func() response { return h.get("/matches/" + match + "/files") },
		"read":   func() response { return h.get("/matches/" + match + "/files/a.js") },
		"write":  func() response { return h.request(http.MethodPut, "/matches/"+match+"/files/a.js", "x") },
		"delete": func() response { return h.request(http.MethodDelete, "/matches/"+match+"/files/a.js", "") },
	} {
		t.Run(name, func(t *testing.T) {
			if res := call(); res.status != http.StatusUnauthorized {
				t.Errorf("%s without a session = %d, want 401", name, res.status)
			}
		})
	}
}
