package container_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r4ph92/DevDuel/internal/container"
)

// Records for one invocation of the fake docker binary. Args are written with
// unit separators so a value containing spaces or newlines still round-trips.
const (
	argSep  = "\x1f"
	callSep = "\x1e"
)

type fakeDocker struct {
	dir  string
	path string
}

// newFakeDocker writes an executable that stands in for docker. body is a sh
// snippet with the invocation's arguments in "$@" and the fake's scratch
// directory in $FAKE_DIR; it decides what to print and what to exit with.
//
// Standing up a real process rather than stubbing an internal seam means the
// tests cover argument marshalling, stream capture and exit codes the same way
// production does.
func newFakeDocker(t *testing.T, body string) *fakeDocker {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "docker")
	script := "#!/bin/sh\n" +
		"FAKE_DIR=" + shellQuote(dir) + "\n" +
		"{ printf '%s\\037' \"$@\"; printf '\\036'; } >> \"$FAKE_DIR/argv\"\n" +
		body + "\n"

	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	return &fakeDocker{dir: dir, path: path}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// calls returns the argv of every invocation so far.
func (f *fakeDocker) calls(t *testing.T) [][]string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(f.dir, "argv"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read argv: %v", err)
	}

	var calls [][]string
	for _, raw := range strings.Split(strings.TrimSuffix(string(data), callSep), callSep) {
		args := strings.Split(strings.TrimSuffix(raw, argSep), argSep)
		if len(args) == 1 && args[0] == "" {
			args = nil
		}
		calls = append(calls, args)
	}
	return calls
}

// onlyCall asserts that docker was invoked exactly once and returns its argv.
func (f *fakeDocker) onlyCall(t *testing.T) []string {
	t.Helper()

	calls := f.calls(t)
	if len(calls) != 1 {
		t.Fatalf("docker was invoked %d times, want 1: %v", len(calls), calls)
	}
	return calls[0]
}

func newCLI(t *testing.T, body string, opts ...container.Option) (*container.CLI, *fakeDocker) {
	t.Helper()

	fake := newFakeDocker(t, body)
	return container.NewCLI(append([]container.Option{container.WithBinary(fake.path)}, opts...)...), fake
}

func wantArgs(t *testing.T, got, want []string) {
	t.Helper()

	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("docker was called with\n  %v\nwant\n  %v", got, want)
	}
}

func TestCreateNetworkIssuesAnInternalLabelledNetwork(t *testing.T) {
	cli, fake := newCLI(t, "echo netid")

	err := cli.CreateNetwork(t.Context(), container.NetworkSpec{
		Name:     "devduel-job-7",
		Internal: true,
		Labels:   map[string]string{"devduel.job": "7", "devduel.owner": "judge"},
	})
	if err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}

	wantArgs(t, fake.onlyCall(t), []string{
		"network", "create", "--internal",
		"--label", "devduel.job=7", "--label", "devduel.owner=judge",
		"devduel-job-7",
	})
}

func TestCreateContainerBuildsAReproducibleCommand(t *testing.T) {
	cli, fake := newCLI(t, "echo c0ffeeb4be")

	id, err := cli.CreateContainer(t.Context(), container.Spec{
		Name:    "devduel-job-7-runner",
		Image:   "devduel/todo-api:1",
		Command: []string{"node", "server.js"},
		Workdir: "/app",
		Env:     map[string]string{"PORT": "3000", "DEVDUEL_JOB": "7", "NODE_ENV": "production"},
		Network: "devduel-job-7",
		Aliases: []string{"runner"},
		Labels:  map[string]string{"devduel.job": "7"},
	})
	if err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	if id != "c0ffeeb4be" {
		t.Errorf("CreateContainer returned %q, want the id docker printed", id)
	}

	wantArgs(t, fake.onlyCall(t), []string{
		"create",
		"--name", "devduel-job-7-runner",
		"--network", "devduel-job-7",
		"--network-alias", "runner",
		"--workdir", "/app",
		"--env", "DEVDUEL_JOB=7",
		"--env", "NODE_ENV=production",
		"--env", "PORT=3000",
		"--label", "devduel.job=7",
		"devduel/todo-api:1",
		"node", "server.js",
	})
}

func TestCreateContainerOmitsFlagsItWasNotGiven(t *testing.T) {
	cli, fake := newCLI(t, "echo id")

	if _, err := cli.CreateContainer(t.Context(), container.Spec{Image: "alpine:3"}); err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}

	wantArgs(t, fake.onlyCall(t), []string{"create", "alpine:3"})
}

func TestSimpleCommands(t *testing.T) {
	cases := []struct {
		name string
		body string
		call func(*container.CLI) error
		want []string
	}{
		{
			"start",
			"",
			func(c *container.CLI) error { return c.StartContainer(t.Context(), "runner") },
			[]string{"start", "runner"},
		},
		{
			"force-remove",
			"",
			func(c *container.CLI) error { return c.RemoveContainer(t.Context(), "runner") },
			[]string{"rm", "--force", "--volumes", "runner"},
		},
		{
			"remove a network",
			"",
			func(c *container.CLI) error { return c.RemoveNetwork(t.Context(), "devduel-job-7") },
			[]string{"network", "rm", "devduel-job-7"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cli, fake := newCLI(t, c.body)
			if err := c.call(cli); err != nil {
				t.Fatalf("call: %v", err)
			}
			wantArgs(t, fake.onlyCall(t), c.want)
		})
	}
}

func TestWaitContainerReturnsTheExitCode(t *testing.T) {
	cli, fake := newCLI(t, "echo 137")

	code, err := cli.WaitContainer(t.Context(), "runner")
	if err != nil {
		t.Fatalf("WaitContainer: %v", err)
	}
	if code != 137 {
		t.Errorf("WaitContainer returned %d, want 137", code)
	}
	wantArgs(t, fake.onlyCall(t), []string{"wait", "runner"})
}

func TestWaitContainerRejectsOutputThatIsNotAnExitCode(t *testing.T) {
	cli, _ := newCLI(t, "echo 'still running'")

	_, err := cli.WaitContainer(t.Context(), "runner")
	if err == nil {
		t.Fatal("WaitContainer: expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "still running") {
		t.Errorf("error should quote what docker printed, got: %v", err)
	}
}

func TestFailuresCarryTheCommandAndItsOutput(t *testing.T) {
	cli, _ := newCLI(t, "echo 'partial stdout'; echo 'Error response from daemon: pull access denied' >&2; exit 125")

	_, err := cli.CreateContainer(t.Context(), container.Spec{Image: "devduel/nope:1"})

	var cmdErr *container.Error
	if !errors.As(err, &cmdErr) {
		t.Fatalf("error is %T (%v), want *container.Error", err, err)
	}
	if cmdErr.ExitCode != 125 {
		t.Errorf("ExitCode = %d, want 125", cmdErr.ExitCode)
	}
	for _, want := range []string{"partial stdout", "pull access denied"} {
		if !strings.Contains(cmdErr.Output, want) {
			t.Errorf("Output should contain %q, got %q", want, cmdErr.Output)
		}
	}

	// The message has to be enough to re-run the command by hand.
	msg := cmdErr.Error()
	for _, want := range []string{"docker", "create", "devduel/nope:1", "pull access denied"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message should contain %q, got:\n%s", want, msg)
		}
	}
}

func TestFailuresReportAMissingBinaryClearly(t *testing.T) {
	cli := container.NewCLI(container.WithBinary(filepath.Join(t.TempDir(), "no-docker-here")))

	err := cli.StartContainer(t.Context(), "runner")
	if err == nil {
		t.Fatal("StartContainer: expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "no-docker-here") {
		t.Errorf("error should name the binary it could not run, got: %v", err)
	}
}

func TestRemoveIsIdempotent(t *testing.T) {
	// Docker reports a second removal as a failure. Teardown runs on every
	// path, including paths where an earlier teardown already ran, so the
	// runtime absorbs that rather than making every caller special-case it.
	cases := []struct {
		name   string
		stderr string
		call   func(*container.CLI) error
	}{
		{
			"container already gone",
			"Error response from daemon: No such container: runner",
			func(c *container.CLI) error { return c.RemoveContainer(t.Context(), "runner") },
		},
		{
			"network already gone",
			"Error response from daemon: network devduel-job-7 not found",
			func(c *container.CLI) error { return c.RemoveNetwork(t.Context(), "devduel-job-7") },
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cli, fake := newCLI(t, "echo "+shellQuote(c.stderr)+" >&2; exit 1")

			if err := c.call(cli); err != nil {
				t.Fatalf("first removal: %v", err)
			}
			if err := c.call(cli); err != nil {
				t.Fatalf("second removal: %v", err)
			}
			if got := len(fake.calls(t)); got != 2 {
				t.Errorf("docker was invoked %d times, want 2", got)
			}
		})
	}
}

func TestRemoveStillReportsARealFailure(t *testing.T) {
	cli, _ := newCLI(t, "echo 'Error response from daemon: network has active endpoints' >&2; exit 1")

	err := cli.RemoveNetwork(t.Context(), "devduel-job-7")
	if err == nil {
		t.Fatal("RemoveNetwork: expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "active endpoints") {
		t.Errorf("error should explain the failure, got: %v", err)
	}
}

func TestLogsSeparatesTheStreams(t *testing.T) {
	cli, fake := newCLI(t, "echo 'listening on 3000'; echo 'deprecation warning' >&2")

	logs, err := cli.Logs(t.Context(), "runner")
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}

	if got, want := strings.TrimSpace(logs.Stdout), "listening on 3000"; got != want {
		t.Errorf("Stdout = %q, want %q", got, want)
	}
	if got, want := strings.TrimSpace(logs.Stderr), "deprecation warning"; got != want {
		t.Errorf("Stderr = %q, want %q", got, want)
	}
	if logs.Truncated() {
		t.Error("Truncated() = true, want false for output well under the cap")
	}
	wantArgs(t, fake.onlyCall(t), []string{"logs", "runner"})
}

func TestLogsCapsAFloodingContainer(t *testing.T) {
	const limit = 4096
	// 4MB of stdout, with a recognisable first and last line. The result
	// marker the tester protocol depends on arrives last, so the tail has to
	// survive the cap.
	body := `echo FIRST-LINE
head -c 4000000 /dev/zero | tr '\0' x
echo
echo '##DEVDUEL_RESULTS##'`

	cli, _ := newCLI(t, body, container.WithLogLimit(limit))

	logs, err := cli.Logs(t.Context(), "runner")
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}

	if !logs.Truncated() {
		t.Fatal("Truncated() = false, want the flood to be capped")
	}
	if got := len(logs.Stdout); got > limit+64 {
		t.Errorf("kept %d bytes of stdout, want no more than %d", got, limit+64)
	}
	if !strings.HasPrefix(logs.Stdout, "FIRST-LINE") {
		t.Error("the head of the output should survive: startup failures are there")
	}
	if !strings.Contains(logs.Stdout, "##DEVDUEL_RESULTS##") {
		t.Error("the tail of the output should survive: the result marker is there")
	}
}

func TestControlCommandsAreBoundedByTheCommandTimeout(t *testing.T) {
	// The backgrounded sleep is the point. Killing docker does not kill what
	// docker started, and a surviving grandchild holds the output pipe open;
	// without a wait delay the call blocks for the child's full lifetime
	// instead of the timeout's.
	cli, _ := newCLI(t, "sleep 5 &\nsleep 5", container.WithCommandTimeout(150*time.Millisecond))

	start := time.Now()
	err := cli.StartContainer(t.Context(), "runner")

	if err == nil {
		t.Fatal("StartContainer: expected a timeout, got nil")
	}
	// Near the timeout, not the timeout plus the wait delay: the backgrounded
	// process has to be killed, not merely waited out.
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("StartContainer took %v, want it cut off near the timeout", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error should unwrap to context.DeadlineExceeded, got: %v", err)
	}
}

func TestWaitContainerIsNotBoundedByTheCommandTimeout(t *testing.T) {
	// A judge run legitimately outlasts every other docker call, so the wait
	// answers to the caller's context alone.
	cli, _ := newCLI(t, "sleep 0.4; echo 0", container.WithCommandTimeout(100*time.Millisecond))

	code, err := cli.WaitContainer(t.Context(), "runner")
	if err != nil {
		t.Fatalf("WaitContainer: %v", err)
	}
	if code != 0 {
		t.Errorf("WaitContainer returned %d, want 0", code)
	}
}

func TestCommandsHonourACancelledContext(t *testing.T) {
	cli, _ := newCLI(t, "sleep 5")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := cli.StartContainer(ctx, "runner"); !errors.Is(err, context.Canceled) {
		t.Fatalf("StartContainer: got %v, want context.Canceled", err)
	}
}

func TestCreateContainerFailsWhenDockerPrintsNoID(t *testing.T) {
	cli, _ := newCLI(t, "exit 0")

	_, err := cli.CreateContainer(t.Context(), container.Spec{Image: "alpine:3"})
	if err == nil {
		t.Fatal("CreateContainer: expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "no container id") {
		t.Errorf("error should say what was missing, got: %v", err)
	}
}

func TestLogsFailsWhenTheContainerIsGone(t *testing.T) {
	cli, _ := newCLI(t, "echo 'Error response from daemon: No such container: runner' >&2; exit 1")

	// Reading logs is not teardown, so an absent container is a real failure
	// here rather than something to absorb.
	_, err := cli.Logs(t.Context(), "runner")
	if err == nil {
		t.Fatal("Logs: expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "No such container") {
		t.Errorf("error should carry docker's explanation, got: %v", err)
	}
}

func TestNewCLIFallsBackOnNonsenseOptions(t *testing.T) {
	// A caller cannot switch the cap off by passing a silly value; the
	// default applies instead.
	cli, _ := newCLI(t,
		"head -c 600000 /dev/zero | tr '\\0' x",
		container.WithLogLimit(-1),
		container.WithCommandTimeout(-1),
	)

	logs, err := cli.Logs(t.Context(), "runner")
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if !logs.Truncated() {
		t.Error("Truncated() = false, want the default cap to apply")
	}
	if got := len(logs.Stdout); got > container.DefaultLogLimit+64 {
		t.Errorf("kept %d bytes, want no more than the default cap", got)
	}
}

func TestCreateContainerRendersMounts(t *testing.T) {
	cli, fake := newCLI(t, "echo id")

	_, err := cli.CreateContainer(t.Context(), container.Spec{
		Image: "alpine:3",
		Mounts: []container.Mount{
			{Source: "/srv/workspace", Target: "/app", ReadOnly: true},
			{Source: "/srv/scratch", Target: "/scratch"},
		},
	})
	if err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}

	wantArgs(t, fake.onlyCall(t), []string{
		"create",
		"--mount", "type=bind,source=/srv/workspace,target=/app,readonly",
		"--mount", "type=bind,source=/srv/scratch,target=/scratch",
		"alpine:3",
	})
}

func TestCreateContainerRendersTheSandbox(t *testing.T) {
	cli, fake := newCLI(t, "echo id")

	_, err := cli.CreateContainer(t.Context(), container.Spec{
		Image: "alpine:3",
		Sandbox: container.Sandbox{
			CPUs:             1.5,
			MemoryMB:         512,
			PIDs:             128,
			ReadOnlyRoot:     true,
			Tmpfs:            map[string]string{"/tmp": "rw,noexec,nosuid,size=64m", "/run": "rw,size=8m"},
			DropCapabilities: []string{"ALL"},
			NoNewPrivileges:  true,
			User:             "65534:65534",
		},
	})
	if err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}

	wantArgs(t, fake.onlyCall(t), []string{
		"create",
		"--cpus", "1.5",
		// Swap pinned to memory: over the limit the container dies rather
		// than swapping the host to a standstill.
		"--memory", "512m",
		"--memory-swap", "512m",
		"--pids-limit", "128",
		"--read-only",
		"--tmpfs", "/run:rw,size=8m",
		"--tmpfs", "/tmp:rw,noexec,nosuid,size=64m",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--user", "65534:65534",
		"alpine:3",
	})
}

func TestCreateContainerOmitsSandboxFlagsItWasNotGiven(t *testing.T) {
	// An unconfined container is a deliberate act, not a rendering accident:
	// the zero Sandbox produces no flags at all.
	cli, fake := newCLI(t, "echo id")

	if _, err := cli.CreateContainer(t.Context(), container.Spec{Image: "alpine:3"}); err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}

	wantArgs(t, fake.onlyCall(t), []string{"create", "alpine:3"})
}

func TestCreateContainerRendersFractionalCPUsExactly(t *testing.T) {
	cases := map[float64]string{0.5: "0.5", 1: "1", 2.25: "2.25", 0.125: "0.125"}

	for cpus, want := range cases {
		t.Run(want, func(t *testing.T) {
			cli, fake := newCLI(t, "echo id")

			_, err := cli.CreateContainer(t.Context(), container.Spec{
				Image:   "alpine:3",
				Sandbox: container.Sandbox{CPUs: cpus},
			})
			if err != nil {
				t.Fatalf("CreateContainer: %v", err)
			}
			wantArgs(t, fake.onlyCall(t), []string{"create", "--cpus", want, "alpine:3"})
		})
	}
}

func TestBuildImage(t *testing.T) {
	cases := []struct {
		name string
		spec container.ImageSpec
		want []string
	}{
		{
			"a context and a tag",
			container.ImageSpec{Tag: "devduel/todo-api:1", ContextDir: "/srv/challenge/image"},
			[]string{"build", "--tag", "devduel/todo-api:1", "/srv/challenge/image"},
		},
		{
			"everything",
			container.ImageSpec{
				Tag:        "devduel/todo-api:1",
				ContextDir: "/srv/challenge/image",
				Dockerfile: "/srv/challenge/image/Dockerfile.test",
				NoCache:    true,
				Labels:     map[string]string{"devduel.challenge": "todo-api"},
			},
			[]string{
				"build", "--tag", "devduel/todo-api:1",
				"--file", "/srv/challenge/image/Dockerfile.test",
				"--no-cache",
				"--label", "devduel.challenge=todo-api",
				"/srv/challenge/image",
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cli, fake := newCLI(t, "")
			if err := cli.BuildImage(t.Context(), c.spec); err != nil {
				t.Fatalf("BuildImage: %v", err)
			}
			wantArgs(t, fake.onlyCall(t), c.want)
		})
	}
}

func TestBuildImageCarriesTheBuildOutputOnFailure(t *testing.T) {
	cli, _ := newCLI(t, "echo 'Step 3/7 : RUN npm ci'; echo 'npm ERR! 404 Not Found' >&2; exit 1")

	err := cli.BuildImage(t.Context(), container.ImageSpec{Tag: "x:1", ContextDir: "."})
	if err == nil {
		t.Fatal("BuildImage: expected an error, got nil")
	}
	// A build failure is unreadable without the build log.
	for _, want := range []string{"Step 3/7", "npm ERR! 404"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should carry the build output %q, got:\n%v", want, err)
		}
	}
}

func TestBuildImageIsNotBoundedByTheCommandTimeout(t *testing.T) {
	// Builds legitimately outlast every other docker call.
	cli, _ := newCLI(t, "sleep 0.4", container.WithCommandTimeout(100*time.Millisecond))

	if err := cli.BuildImage(t.Context(), container.ImageSpec{Tag: "x:1", ContextDir: "."}); err != nil {
		t.Fatalf("BuildImage: %v", err)
	}
}
