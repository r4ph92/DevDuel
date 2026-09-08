package challenge_test

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/r4ph92/DevDuel/internal/challenge"
)

const goldenDir = "testdata/challenges/todo-api"

// copyChallenge copies the golden challenge into a temp directory so a test
// can break one thing about it without disturbing the fixture.
func copyChallenge(t *testing.T) string {
	t.Helper()

	dst := filepath.Join(t.TempDir(), "todo-api")
	if err := os.CopyFS(dst, os.DirFS(goldenDir)); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	return dst
}

func TestLoadReadsAChallengeDirectory(t *testing.T) {
	spec, err := challenge.Load(goldenDir)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}

	if got, want := spec.Key().String(), "todo-api@v1"; got != want {
		t.Errorf("Key() = %q, want %q", got, want)
	}
	if got, want := spec.Tester.Timeout, 90*time.Second; got != want {
		t.Errorf("Tester.Timeout = %v, want %v", got, want)
	}
	if got, want := spec.TotalWeight(), 3; got != want {
		t.Errorf("TotalWeight() = %d, want %d", got, want)
	}

	dirs := []struct {
		name string
		got  string
		want string
	}{
		{"Dir", spec.Dir(), goldenDir},
		{"ImageContextDir", spec.ImageContextDir(), filepath.Join(goldenDir, "image")},
		{"WorkspaceDir", spec.WorkspaceDir(), filepath.Join(goldenDir, "workspace")},
		{"TestsDir", spec.TestsDir(), filepath.Join(goldenDir, "tests")},
		{"SolutionDir", spec.SolutionDir(), filepath.Join(goldenDir, "solution")},
	}
	for _, d := range dirs {
		if d.got != d.want {
			t.Errorf("%s() = %q, want %q", d.name, d.got, d.want)
		}
	}
}

func TestLoadRequiresEveryPartOfTheChallengeDirectory(t *testing.T) {
	cases := []struct {
		remove string
		field  string
	}{
		{"workspace", "workspace/"},
		{"tests", "tests/"},
		{"solution", "solution/"},
		{"image", "image.context"},
	}

	for _, c := range cases {
		t.Run("missing "+c.remove, func(t *testing.T) {
			dir := copyChallenge(t)
			if err := os.RemoveAll(filepath.Join(dir, c.remove)); err != nil {
				t.Fatalf("remove %s: %v", c.remove, err)
			}

			_, err := challenge.Load(dir)
			var invalid *challenge.InvalidSpecError
			if !errors.As(err, &invalid) {
				t.Fatalf("Load: error is %T (%v), want *challenge.InvalidSpecError", err, err)
			}
			if len(invalid.Fields) != 1 || invalid.Fields[0].Field != c.field {
				t.Fatalf("Load reported %v, want a single %s problem", invalid.Fields, c.field)
			}
			if !strings.Contains(invalid.Error(), filepath.Join(dir, "challenge.yaml")) {
				t.Errorf("error should name the spec file, got:\n%v", invalid)
			}
		})
	}
}

func TestLoadRejectsAFileWhereADirectoryBelongs(t *testing.T) {
	dir := copyChallenge(t)
	tests := filepath.Join(dir, "tests")
	if err := os.RemoveAll(tests); err != nil {
		t.Fatalf("remove tests: %v", err)
	}
	if err := os.WriteFile(tests, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write tests: %v", err)
	}

	_, err := challenge.Load(dir)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("Load: got %v, want a \"not a directory\" error", err)
	}
}

func TestLoadReportsEveryMissingDirectoryAtOnce(t *testing.T) {
	dir := copyChallenge(t)
	for _, name := range []string{"workspace", "tests", "solution"} {
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			t.Fatalf("remove %s: %v", name, err)
		}
	}

	_, err := challenge.Load(dir)
	var invalid *challenge.InvalidSpecError
	if !errors.As(err, &invalid) {
		t.Fatalf("Load: error is %T (%v), want *challenge.InvalidSpecError", err, err)
	}
	if len(invalid.Fields) != 3 {
		t.Fatalf("Load reported %v, want all three missing directories", invalid.Fields)
	}
}

func TestLoadFailsWhenTheSpecIsMissing(t *testing.T) {
	_, err := challenge.Load(t.TempDir())
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load: got %v, want a not-exist error", err)
	}
}

func TestLoadNamesTheSpecFileOnInvalidContent(t *testing.T) {
	dir := copyChallenge(t)
	path := filepath.Join(dir, "challenge.yaml")
	if err := os.WriteFile(path, []byte("id: todo-api\nversion: 0\n"), 0o600); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	_, err := challenge.Load(dir)
	if err == nil {
		t.Fatal("Load: expected an error, got nil")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error should name %s, got:\n%v", path, err)
	}
}

func TestLoadAllReturnsChallengesInKeyOrder(t *testing.T) {
	root := t.TempDir()
	writeVariant(t, root, "b-v1", "beta", 1)
	writeVariant(t, root, "a", "alpha", 1)
	writeVariant(t, root, "b-v2", "beta", 2)

	specs, err := challenge.LoadAll(root)
	if err != nil {
		t.Fatalf("LoadAll: unexpected error: %v", err)
	}

	var got []string
	for _, s := range specs {
		got = append(got, s.Key().String())
	}
	want := []string{"alpha@v1", "beta@v1", "beta@v2"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("LoadAll returned %v, want %v", got, want)
	}
}

func TestLoadAllRejectsTwoChallengesWithTheSameKey(t *testing.T) {
	root := t.TempDir()
	writeVariant(t, root, "original", "alpha", 1)
	writeVariant(t, root, "edited-in-place", "alpha", 1)

	_, err := challenge.LoadAll(root)
	var dup *challenge.DuplicateKeyError
	if !errors.As(err, &dup) {
		t.Fatalf("LoadAll: error is %T (%v), want *challenge.DuplicateKeyError", err, err)
	}
	if dup.Key != (challenge.Key{ID: "alpha", Version: 1}) {
		t.Errorf("DuplicateKeyError.Key = %v", dup.Key)
	}
}

func TestLoadAllRejectsADirectoryWithoutASpec(t *testing.T) {
	root := t.TempDir()
	writeVariant(t, root, "alpha", "alpha", 1)
	if err := os.Mkdir(filepath.Join(root, "notes"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if _, err := challenge.LoadAll(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("LoadAll: got %v, want a not-exist error", err)
	}
}

// writeVariant copies the golden challenge into root/name under a different
// (id, version).
func writeVariant(t *testing.T, root, name, id string, version int) {
	t.Helper()

	dir := filepath.Join(root, name)
	if err := os.CopyFS(dir, os.DirFS(goldenDir)); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}

	path := filepath.Join(dir, "challenge.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	spec := strings.Replace(string(data), "id: todo-api", "id: "+id, 1)
	spec = strings.Replace(spec, "version: 1", "version: "+strconv.Itoa(version), 1)
	if err := os.WriteFile(path, []byte(spec), 0o600); err != nil {
		t.Fatalf("write spec: %v", err)
	}
}

func TestEveryShippedChallengeLoads(t *testing.T) {
	// The challenges directory is what players are served. A spec that does
	// not load, or a challenge missing a directory the judge needs, should
	// fail here rather than in somebody's match.
	specs, err := challenge.LoadAll("../../challenges")
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(specs) == 0 {
		t.Fatal("no challenges found; this guards a directory that should not be empty")
	}

	for _, spec := range specs {
		broken := 0
		for _, req := range spec.Requirements {
			if req.Broken {
				broken++
			}
		}
		t.Logf("%s: %d requirements, %d declared broken, %d total weight",
			spec.Key(), len(spec.Requirements), broken, spec.TotalWeight())
	}
}
