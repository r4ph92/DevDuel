package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/r4ph92/DevDuel/internal/container"
	"github.com/r4ph92/DevDuel/internal/judge"
	"github.com/r4ph92/DevDuel/internal/tester"
)

type stubBuilder struct{ err error }

func (s stubBuilder) BuildImage(context.Context, container.ImageSpec) error { return s.err }

// stubRunner answers every job with the same statuses, except that the
// solution directory may be scripted separately.
type stubRunner struct {
	workspace string
	solution  string
	err       error
}

func (s stubRunner) Run(_ context.Context, job judge.Job) (judge.Report, error) {
	if s.err != nil {
		return judge.Report{}, s.err
	}

	answer := s.solution
	if strings.HasSuffix(job.Workspace, "workspace") {
		answer = s.workspace
	}

	var out []tester.Result
	for _, pair := range strings.Fields(answer) {
		key, status, _ := strings.Cut(pair, "=")
		out = append(out, tester.Result{Key: key, Status: tester.Status(status)})
	}
	return judge.Report{Results: out}, nil
}

const (
	asDeclared = "list-todos=fail create-todo=pass"
	allPass    = "list-todos=pass create-todo=pass"
)

func TestVerifyChallengeReportsAValidChallenge(t *testing.T) {
	var out bytes.Buffer

	err := verifyChallenge(t.Context(), "testdata/todo-api",
		stubBuilder{}, stubRunner{workspace: asDeclared, solution: allPass}, &out)
	if err != nil {
		t.Fatalf("verifyChallenge: %v", err)
	}

	got := out.String()
	for _, want := range []string{"todo-api@v1", "is valid", "workspace", "solution", "repeat"} {
		if !strings.Contains(got, want) {
			t.Errorf("output should mention %q, got:\n%s", want, got)
		}
	}
}

func TestVerifyChallengeNamesWhatDiverged(t *testing.T) {
	var out bytes.Buffer

	// list-todos is declared broken but passes in the starting workspace.
	err := verifyChallenge(t.Context(), "testdata/todo-api",
		stubBuilder{}, stubRunner{workspace: allPass, solution: allPass}, &out)

	if !errors.Is(err, errReported) {
		t.Fatalf("verifyChallenge returned %v, want the reported-failure sentinel", err)
	}

	got := out.String()
	for _, want := range []string{"is not valid", "1 problem", "list-todos", "declared broken"} {
		if !strings.Contains(got, want) {
			t.Errorf("output should mention %q, got:\n%s", want, got)
		}
	}
}

func TestVerifyChallengeFailsOnAnUnloadableChallenge(t *testing.T) {
	var out bytes.Buffer

	err := verifyChallenge(t.Context(), t.TempDir(), stubBuilder{}, stubRunner{}, &out)

	if err == nil {
		t.Fatal("verifyChallenge: expected an error for a directory with no challenge.yaml")
	}
	if errors.Is(err, errReported) {
		t.Error("a load failure has not been reported yet; main must print it")
	}
}

func TestVerifyChallengeFailsWhenTheImageWillNotBuild(t *testing.T) {
	var out bytes.Buffer

	err := verifyChallenge(t.Context(), "testdata/todo-api",
		stubBuilder{err: errors.New("npm ERR! 404")}, stubRunner{}, &out)

	if err == nil || !strings.Contains(err.Error(), "npm ERR!") {
		t.Fatalf("verifyChallenge returned %v, want the build failure", err)
	}
}

func TestRunRejectsCommandsItDoesNotHave(t *testing.T) {
	cases := [][]string{
		{},
		{"challenge"},
		{"challenge", "publish", "dir"},
		{"nonsense"},
		{"challenge", "verify"},
		{"challenge", "verify", "one", "two"},
		{"database", "migrate"},
		{"db", "reset"},
	}

	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer

			if err := run(t.Context(), args, &out, &errOut); err == nil {
				t.Fatal("run: expected an error")
			}
			if !strings.Contains(errOut.String(), "devduelctl") {
				t.Errorf("usage should be printed to stderr, got:\n%s", errOut.String())
			}
		})
	}
}

func TestMigrateNeedsADatabase(t *testing.T) {
	t.Setenv("DATABASE_URL", "")

	var out, errOut bytes.Buffer
	err := run(t.Context(), []string{"db", "migrate"}, &out, &errOut)
	if err == nil {
		t.Fatal("db migrate succeeded without a database, want an error")
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("error = %q, want it to name DATABASE_URL", err)
	}
}

func TestMigrateTakesNoArguments(t *testing.T) {
	var out, errOut bytes.Buffer

	err := run(t.Context(), []string{"db", "migrate", "somewhere"}, &out, &errOut)
	if err == nil {
		t.Fatal("db migrate succeeded with an argument, want an error")
	}
}
