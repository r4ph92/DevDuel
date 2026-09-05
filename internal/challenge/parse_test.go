package challenge_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/r4ph92/DevDuel/internal/challenge"
)

// validSpec is the smallest spec that loads: every required field, nothing
// that has a default. Tests mutate a copy of it to isolate one failure.
const validSpec = `
id: todo-api
version: 1
category: debugging
difficulty: medium
duration: 45m
image:
  tag: devduel/todo-api:1
app:
  command: ["node", "server.js"]
  port: 3000
tester:
  command: ["pytest", "-q"]
requirements:
  - key: list-todos
    title: GET /todos returns every todo
    description: The list endpoint currently drops completed todos.
    broken: true
`

func parseOK(t *testing.T, src string) *challenge.Spec {
	t.Helper()
	spec, err := challenge.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	return spec
}

// withLine replaces the first line starting with prefix. It keeps the test
// specs readable: each case says only what it changes.
func withLine(src, prefix, replacement string) string {
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, prefix) {
			lines[i] = replacement
			return strings.Join(lines, "\n")
		}
	}
	panic("withLine: no line starting with " + prefix)
}

// afterLine inserts a line just below the first line starting with prefix.
// The spec has one mapping per top-level key, so a case that adds a field
// has to reach into the existing block rather than append a second one.
func afterLine(src, prefix, added string) string {
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, prefix) {
			rest := append([]string{added}, lines[i+1:]...)
			return strings.Join(append(lines[:i+1:i+1], rest...), "\n")
		}
	}
	panic("afterLine: no line starting with " + prefix)
}

func TestParseReadsEveryField(t *testing.T) {
	spec := parseOK(t, validSpec)

	if spec.ID != "todo-api" {
		t.Errorf("ID = %q, want %q", spec.ID, "todo-api")
	}
	if spec.Version != 1 {
		t.Errorf("Version = %d, want 1", spec.Version)
	}
	if spec.Category != challenge.CategoryDebugging {
		t.Errorf("Category = %q, want %q", spec.Category, challenge.CategoryDebugging)
	}
	if spec.Difficulty != challenge.DifficultyMedium {
		t.Errorf("Difficulty = %q, want %q", spec.Difficulty, challenge.DifficultyMedium)
	}
	if spec.Duration != 45*time.Minute {
		t.Errorf("Duration = %v, want 45m", spec.Duration)
	}
	if spec.Image.Tag != "devduel/todo-api:1" {
		t.Errorf("Image.Tag = %q", spec.Image.Tag)
	}
	if got, want := strings.Join(spec.App.Command, " "), "node server.js"; got != want {
		t.Errorf("App.Command = %q, want %q", got, want)
	}
	if spec.App.Port != 3000 {
		t.Errorf("App.Port = %d, want 3000", spec.App.Port)
	}
	if len(spec.Requirements) != 1 {
		t.Fatalf("len(Requirements) = %d, want 1", len(spec.Requirements))
	}
	req := spec.Requirements[0]
	if req.Key != "list-todos" {
		t.Errorf("Requirements[0].Key = %q", req.Key)
	}
	if !req.Broken {
		t.Error("Requirements[0].Broken = false, want true")
	}
}

func TestParseAppliesDefaults(t *testing.T) {
	spec := parseOK(t, validSpec)

	checks := []struct {
		field string
		got   any
		want  any
	}{
		{"image.context", spec.Image.Context, "image"},
		{"app.workdir", spec.App.Workdir, "/app"},
		{"app.health_path", spec.App.HealthPath, "/health"},
		{"tester.timeout", spec.Tester.Timeout, 60 * time.Second},
		{"limits.cpus", spec.Limits.CPUs, 1.0},
		{"limits.memory_mb", spec.Limits.MemoryMB, 512},
		{"limits.pids", spec.Limits.PIDs, 128},
		{"requirements[0].weight", spec.Requirements[0].Weight, 1},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.field, c.got, c.want)
		}
	}
}

func TestParseKeepsExplicitValuesOverDefaults(t *testing.T) {
	src := validSpec + `
limits:
  cpus: 2
  memory_mb: 1024
  pids: 64
`
	spec := parseOK(t, src)

	if spec.Limits.CPUs != 2 {
		t.Errorf("limits.cpus = %v, want 2", spec.Limits.CPUs)
	}
	if spec.Limits.MemoryMB != 1024 {
		t.Errorf("limits.memory_mb = %d, want 1024", spec.Limits.MemoryMB)
	}
	if spec.Limits.PIDs != 64 {
		t.Errorf("limits.pids = %d, want 64", spec.Limits.PIDs)
	}
}

// assertFieldErrors checks that parsing src fails and reports exactly the
// given field paths. Naming the field is the point: an author should not have
// to guess which line of challenge.yaml is wrong.
// It returns the full error message so a caller can assert on its wording.
func assertFieldErrors(t *testing.T, src string, wantFields ...string) string {
	t.Helper()

	_, err := challenge.Parse([]byte(src))
	if err == nil {
		t.Fatalf("Parse: expected an error naming %v, got nil", wantFields)
	}

	var invalid *challenge.InvalidSpecError
	if !errors.As(err, &invalid) {
		t.Fatalf("Parse: error is %T (%v), want *challenge.InvalidSpecError", err, err)
	}

	var got []string
	for _, f := range invalid.Fields {
		got = append(got, f.Field)
	}
	if strings.Join(got, ",") != strings.Join(wantFields, ",") {
		t.Fatalf("Parse reported fields %v, want %v\nfull error:\n%v", got, wantFields, err)
	}
	return invalid.Error()
}

func TestParseRejectsInvalidFields(t *testing.T) {
	cases := []struct {
		name  string
		line  string
		with  string
		field string
	}{
		{"empty id", "id:", "id: \"\"", "id"},
		{"id is not a slug", "id:", "id: Todo_API", "id"},
		{"version below one", "version:", "version: 0", "version"},
		{"unknown category", "category:", "category: puzzle", "category"},
		{"unknown difficulty", "difficulty:", "difficulty: brutal", "difficulty"},
		{"unparseable duration", "duration:", "duration: 45x", "duration"},
		{"duration without a unit", "duration:", "duration: 45", "duration"},
		{"duration too short to be a match", "duration:", "duration: 10s", "duration"},
		{"duration beyond the cap", "duration:", "duration: 12h", "duration"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertFieldErrors(t, withLine(validSpec, c.line, c.with), c.field)
		})
	}
}

func TestParseRequiresAnImmutableImageTag(t *testing.T) {
	cases := []struct {
		name string
		tag  string
	}{
		{"missing tag", "tag: \"\""},
		{"untagged reference", "tag: devduel/todo-api"},
		{"floating latest tag", "tag: devduel/todo-api:latest"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertFieldErrors(t, withLine(validSpec, "  tag:", "  "+c.tag), "image.tag")
		})
	}
}

func TestParseRejectsAnImageContextOutsideTheChallenge(t *testing.T) {
	src := afterLine(validSpec, "  tag:", "  context: ../shared")
	assertFieldErrors(t, src, "image.context")
}

func TestParseRejectsInvalidAppAndTester(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		field string
	}{
		{"no app command", strings.Replace(validSpec, `command: ["node", "server.js"]`, "command: []", 1), "app.command"},
		{"port out of range", withLine(validSpec, "  port:", "  port: 70000"), "app.port"},
		{"relative workdir", afterLine(validSpec, "  port:", "  workdir: app"), "app.workdir"},
		{"health path without a slash", afterLine(validSpec, "  port:", "  health_path: health"), "app.health_path"},
		{"no tester command", strings.Replace(validSpec, `command: ["pytest", "-q"]`, "command: []", 1), "tester.command"},
		{"tester timeout beyond the cap", afterLine(validSpec, `  command: ["pytest"`, "  timeout: 30m"), "tester.timeout"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertFieldErrors(t, c.src, c.field)
		})
	}
}

func TestParseRejectsUnsafeLimits(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		field string
	}{
		{"zero cpus", "limits:\n  cpus: 0\n", "limits.cpus"},
		{"cpus beyond the cap", "limits:\n  cpus: 64\n", "limits.cpus"},
		{"memory below the floor", "limits:\n  memory_mb: 8\n", "limits.memory_mb"},
		{"memory beyond the cap", "limits:\n  memory_mb: 65536\n", "limits.memory_mb"},
		{"pids below the floor", "limits:\n  pids: 1\n", "limits.pids"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertFieldErrors(t, validSpec+"\n"+c.src, c.field)
		})
	}
}

func TestParseRequiresUniqueRequirementKeys(t *testing.T) {
	src := validSpec + `  - key: list-todos
    title: A second requirement claiming the same key
    description: Duplicate keys would silently collide during scoring.
`
	msg := assertFieldErrors(t, src, "requirements[1].key")
	if !strings.Contains(msg, "list-todos") {
		t.Errorf("error should quote the duplicated key, got:\n%v", msg)
	}
}

func TestParseRequiresPositiveWeights(t *testing.T) {
	for _, weight := range []string{"0", "-3"} {
		t.Run("weight "+weight, func(t *testing.T) {
			src := validSpec + "    weight: " + weight + "\n"
			assertFieldErrors(t, src, "requirements[0].weight")
		})
	}
}

func TestParseRejectsIncompleteRequirements(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		field string
	}{
		{
			"no requirements at all",
			strings.SplitN(validSpec, "requirements:", 2)[0] + "requirements: []\n",
			"requirements",
		},
		{
			"key is not a slug",
			withLine(validSpec, "  - key:", "  - key: List Todos"),
			"requirements[0].key",
		},
		{
			"no title",
			withLine(validSpec, "    title:", "    title: \"\""),
			"requirements[0].title",
		},
		{
			"no description",
			withLine(validSpec, "    description:", "    description: \"\""),
			"requirements[0].description",
		},
		{
			"nothing is broken, so verify would prove nothing",
			withLine(validSpec, "    broken:", "    broken: false"),
			"requirements",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertFieldErrors(t, c.src, c.field)
		})
	}
}

func TestParseReportsEveryProblemAtOnce(t *testing.T) {
	src := withLine(withLine(validSpec, "version:", "version: 0"), "  port:", "  port: 0")
	msg := assertFieldErrors(t, src, "app.port", "version")
	if !strings.Contains(msg, "2 problems") {
		t.Errorf("error should count the problems, got:\n%v", msg)
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	src := validSpec + "\ntimelimit: 45m\n"

	_, err := challenge.Parse([]byte(src))
	if err == nil {
		t.Fatal("Parse: expected an error for an unknown field, got nil")
	}
	if !strings.Contains(err.Error(), "timelimit") {
		t.Errorf("error should name the unknown field, got: %v", err)
	}
}

func TestParseRejectsMalformedYAML(t *testing.T) {
	for _, src := range []string{"", "   \n", "id: [unterminated", "not a mapping"} {
		if _, err := challenge.Parse([]byte(src)); err == nil {
			t.Errorf("Parse(%q): expected an error, got nil", src)
		}
	}
}

func TestSpecDerivedValues(t *testing.T) {
	src := validSpec + `    weight: 3
  - key: create-todo
    title: POST /todos creates a todo
    description: The create endpoint ignores the completed flag.
    weight: 2
`
	spec := parseOK(t, src)

	if got, want := spec.Key(), (challenge.Key{ID: "todo-api", Version: 1}); got != want {
		t.Errorf("Key() = %v, want %v", got, want)
	}
	if got, want := spec.Key().String(), "todo-api@v1"; got != want {
		t.Errorf("Key().String() = %q, want %q", got, want)
	}
	if got, want := spec.TotalWeight(), 5; got != want {
		t.Errorf("TotalWeight() = %d, want %d", got, want)
	}
	if got := spec.Dir(); got != "" {
		t.Errorf("Dir() = %q, want \"\" for a parsed spec", got)
	}
}

func TestParseRejectsAnEmptySpecFile(t *testing.T) {
	src := "id: todo-api\n"
	assertFieldErrors(t, src,
		"app.command", "app.port", "category", "difficulty", "duration",
		"image.tag", "requirements", "tester.command", "version",
	)
}

func TestParseRejectsANonScalarDuration(t *testing.T) {
	src := withLine(validSpec, "duration:", "duration: [45m]")

	_, err := challenge.Parse([]byte(src))
	if err == nil || !strings.Contains(err.Error(), "scalar") {
		t.Fatalf("Parse: got %v, want a scalar-duration error", err)
	}
}

func TestParseRejectsMoreThanOneDocument(t *testing.T) {
	src := validSpec + "\n---\n" + validSpec

	_, err := challenge.Parse([]byte(src))
	if err == nil || !strings.Contains(err.Error(), "exactly one YAML document") {
		t.Fatalf("Parse: got %v, want a single-document error", err)
	}
}

func TestParseRejectsAnEmptyCommandArgument(t *testing.T) {
	src := strings.Replace(validSpec, `command: ["node", "server.js"]`, `command: ["node", ""]`, 1)
	assertFieldErrors(t, src, "app.command[1]")
}

func TestParseRejectsOversizedText(t *testing.T) {
	t.Run("id", func(t *testing.T) {
		src := withLine(validSpec, "id:", "id: "+strings.Repeat("a", 65))
		assertFieldErrors(t, src, "id")
	})
	t.Run("requirement title", func(t *testing.T) {
		src := withLine(validSpec, "    title:", "    title: "+strings.Repeat("a", 121))
		assertFieldErrors(t, src, "requirements[0].title")
	})
}

func TestParseRejectsOutOfRangeLimitsAndTimeouts(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		field string
	}{
		{"pids beyond the cap", validSpec + "\nlimits:\n  pids: 100000\n", "limits.pids"},
		{"zero tester timeout", afterLine(validSpec, `  command: ["pytest"`, "  timeout: 0s"), "tester.timeout"},
		{"unparseable tester timeout", afterLine(validSpec, `  command: ["pytest"`, "  timeout: soon"), "tester.timeout"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertFieldErrors(t, c.src, c.field)
		})
	}
}

func TestParseAcceptsARegistryPortInTheImageTag(t *testing.T) {
	src := withLine(validSpec, "  tag:", "  tag: localhost:5000/devduel/todo-api:3")
	spec := parseOK(t, src)

	if spec.Image.Tag != "localhost:5000/devduel/todo-api:3" {
		t.Errorf("Image.Tag = %q", spec.Image.Tag)
	}
}

func TestParseRejectsARegistryPortWithNoTag(t *testing.T) {
	src := withLine(validSpec, "  tag:", "  tag: localhost:5000/devduel/todo-api")
	assertFieldErrors(t, src, "image.tag")
}

func TestDuplicateKeyErrorNamesBothChallenges(t *testing.T) {
	err := &challenge.DuplicateKeyError{
		Key:   challenge.Key{ID: "todo-api", Version: 1},
		Paths: [2]string{"challenges/todo-api", "challenges/todo-api-copy"},
	}

	msg := err.Error()
	for _, want := range []string{"todo-api@v1", "challenges/todo-api", "challenges/todo-api-copy"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should mention %q, got: %s", want, msg)
		}
	}
}
