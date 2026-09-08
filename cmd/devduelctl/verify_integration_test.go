package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r4ph92/DevDuel/internal/container"
	"github.com/r4ph92/DevDuel/internal/judge"
)

// realChallenge is the smallest challenge that is genuinely a challenge: a
// static site with one file missing from the starting workspace.
const realChallenge = "testdata/static-site"

// requireDocker skips unless a daemon answers. Verification is the one thing
// in this repo that cannot be established with a fake: its whole purpose is
// finding out what really happens when the challenge runs.
func requireDocker(t *testing.T) *container.CLI {
	t.Helper()

	if testing.Short() {
		t.Skip("docker integration test skipped in -short mode")
	}

	cli := container.NewCLI(container.WithCommandTimeout(2 * time.Minute))
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	if err := cli.RemoveContainer(ctx, "devduel-probe-that-does-not-exist"); err != nil {
		t.Skipf("docker not available: %v", err)
	}
	return cli
}

// verifyDir runs a real verification of the challenge in dir.
func verifyDir(t *testing.T, dir string) (string, error) {
	t.Helper()

	cli := requireDocker(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()

	var out bytes.Buffer
	err := verifyChallenge(ctx, dir, cli, judge.New(cli), &out)
	t.Log("\n" + out.String())

	// Collapse the report's column padding: these tests are about what it
	// says, not how it lines up.
	return strings.Join(strings.Fields(out.String()), " "), err
}

// copyChallenge copies the fixture so a test can break one thing about it.
func copyChallenge(t *testing.T) string {
	t.Helper()

	dst := filepath.Join(t.TempDir(), "challenge")
	if err := os.CopyFS(dst, os.DirFS(realChallenge)); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	// os.CopyFS drops the executable bit, and the tester is a shell script
	// the image runs through sh, so this only has to be readable.
	return dst
}

func TestVerifyAcceptsAChallengeThatBehavesAsDeclared(t *testing.T) {
	out, err := verifyDir(t, realChallenge)
	if err != nil {
		t.Fatalf("verifyChallenge: %v", err)
	}

	if !strings.Contains(out, "is valid") {
		t.Errorf("expected a valid verdict, got:\n%s", out)
	}
	// The starting workspace fails exactly the one declared broken.
	if !strings.Contains(out, "workspace 1 of 2") {
		t.Errorf("the workspace should fail exactly the broken requirement, got:\n%s", out)
	}
	if !strings.Contains(out, "solution 2 of 2") || !strings.Contains(out, "repeat 2 of 2") {
		t.Errorf("the solution should pass everything, twice, got:\n%s", out)
	}
}

func TestVerifyRejectsAChallengeThatIsAlreadySolved(t *testing.T) {
	// Give the starting workspace the file whose absence is the whole
	// challenge. Nothing is broken any more, so the declaration is a lie.
	dir := copyChallenge(t)
	if err := os.WriteFile(filepath.Join(dir, "workspace", "todos.json"), []byte("[]\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	out, err := verifyDir(t, dir)

	if err == nil {
		t.Fatal("verifyChallenge: an already-solved challenge must not verify")
	}
	if !strings.Contains(out, "workspace: list-todos") {
		t.Errorf("the failure should name the phase and the requirement, got:\n%s", out)
	}
	if !strings.Contains(out, "declared broken") {
		t.Errorf("the failure should say what was expected, got:\n%s", out)
	}
}

func TestVerifyRejectsASolutionThatDoesNotSolveIt(t *testing.T) {
	dir := copyChallenge(t)
	if err := os.Remove(filepath.Join(dir, "solution", "todos.json")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	out, err := verifyDir(t, dir)

	if err == nil {
		t.Fatal("verifyChallenge: a challenge its own solution cannot solve must not verify")
	}
	if !strings.Contains(out, "solution: list-todos") {
		t.Errorf("the failure should name the phase and the requirement, got:\n%s", out)
	}
}
