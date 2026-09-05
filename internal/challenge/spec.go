// Package challenge defines the on-disk format of a DevDuel challenge and
// loads it into a validated Spec.
//
// A challenge lives in one directory:
//
//	challenge.yaml   the spec this package parses
//	image/           Docker build context for the runner image
//	workspace/       the starting repository handed to both players
//	tests/           hidden tests, baked into the tester image
//	solution/        reference solution, expected to pass every requirement
//
// Challenges are immutable. A change to any of it means a new version: the
// pair (id, version) is the identity every other component keys on, so an
// already-played match can always be replayed against the exact spec it ran
// under.
package challenge

import (
	"path/filepath"
	"strconv"
	"time"
)

// SpecFile is the name of the spec document inside a challenge directory.
const SpecFile = "challenge.yaml"

// Directories every challenge must provide, alongside the image build context.
const (
	workspaceDir = "workspace"
	testsDir     = "tests"
	solutionDir  = "solution"
)

// Category is the kind of work a challenge asks for.
type Category string

const (
	// CategoryDebugging starts the player from a repository with planted bugs.
	CategoryDebugging Category = "debugging"
	// CategoryBuild starts the player from an empty or skeleton repository.
	CategoryBuild Category = "build"
)

// Difficulty is the coarse effort band a challenge is authored for.
type Difficulty string

// The difficulty bands, in increasing order of effort.
const (
	DifficultyEasy   Difficulty = "easy"
	DifficultyMedium Difficulty = "medium"
	DifficultyHard   Difficulty = "hard"
)

// Spec is a fully loaded, validated and defaulted challenge definition.
//
// Construct one only through [Load], [LoadAll] or [Parse]; a zero Spec or one
// assembled by hand has not been through validation and no other package
// should trust it.
type Spec struct {
	// ID is the stable slug identifying the challenge across versions.
	ID string
	// Version increments on every change. (ID, Version) is the identity.
	Version int

	Category   Category
	Difficulty Difficulty
	// Duration is how long players get on the clock.
	Duration time.Duration

	Image        Image
	App          App
	Tester       Tester
	Limits       Limits
	Requirements []Requirement

	// dir is the challenge directory, empty for a Spec built by Parse.
	dir string
}

// Image describes the runner image carrying the player's code.
//
// The judge network has no egress, so the image must already contain every
// dependency the app and the tests need.
type Image struct {
	// Tag is the fully qualified image reference. It must pin an explicit,
	// non-floating tag so an old match can be rebuilt exactly.
	Tag string `yaml:"tag"`
	// Context is the build context, relative to the challenge directory.
	Context string `yaml:"context"`
}

// App describes how the runner container serves the player's code.
type App struct {
	Command []string `yaml:"command"`
	Workdir string   `yaml:"workdir"`
	// Port is the port the app listens on inside the runner container.
	Port int `yaml:"port"`
	// HealthPath is polled until it answers, before the tester starts.
	HealthPath string `yaml:"health_path"`
}

// Tester describes the hidden test suite, which runs in its own container and
// reaches the app only over HTTP.
type Tester struct {
	Command []string
	Timeout time.Duration
}

// Limits are the resource ceilings applied to both judge containers.
type Limits struct {
	CPUs     float64
	MemoryMB int
	PIDs     int
}

// Requirement is one named, independently scored item on the player checklist.
type Requirement struct {
	// Key identifies the requirement in tester output and in scoring.
	Key string
	// Title is the one-line statement players see on the checklist.
	Title string
	// Description is the full statement of the requirement.
	Description string
	// Weight is this requirement's share of the score. Higher counts more.
	Weight int
	// Broken marks a requirement that must fail against the starting
	// workspace. `devduelctl challenge verify` asserts that exactly the
	// broken set fails, and that the reference solution passes everything.
	Broken bool
}

// Key is the immutable identity of a challenge version.
type Key struct {
	ID      string
	Version int
}

// Key returns the challenge's identity.
func (s *Spec) Key() Key { return Key{ID: s.ID, Version: s.Version} }

// String renders the key as it appears in logs and CLI output, e.g. "todo-api@v1".
func (k Key) String() string { return k.ID + "@v" + strconv.Itoa(k.Version) }

// sub resolves a path inside the challenge directory, or "" when the Spec was
// parsed from bytes and has no directory.
func (s *Spec) sub(name string) string {
	if s.dir == "" {
		return ""
	}
	return filepath.Join(s.dir, name)
}

// Dir is the challenge directory, or "" for a Spec built by [Parse].
func (s *Spec) Dir() string { return s.dir }

// ImageContextDir is the Docker build context for the runner image.
func (s *Spec) ImageContextDir() string { return s.sub(s.Image.Context) }

// WorkspaceDir holds the starting repository handed to both players.
func (s *Spec) WorkspaceDir() string { return s.sub(workspaceDir) }

// TestsDir holds the hidden tests. It is never copied into a player workspace.
func (s *Spec) TestsDir() string { return s.sub(testsDir) }

// SolutionDir holds the reference solution used to verify the challenge.
func (s *Spec) SolutionDir() string { return s.sub(solutionDir) }

// TotalWeight is the sum of every requirement's weight, the denominator of a
// match score. Validation guarantees it is positive.
func (s *Spec) TotalWeight() int {
	total := 0
	for _, r := range s.Requirements {
		total += r.Weight
	}
	return total
}
