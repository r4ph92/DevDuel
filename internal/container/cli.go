package container

import (
	"context"
	"errors"
	"maps"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DefaultLogLimit is how much of each output stream is kept per container.
const DefaultLogLimit = 256 << 10

// DefaultCommandTimeout bounds every docker invocation except the wait, which
// is as long as the job it is waiting for.
const DefaultCommandTimeout = 60 * time.Second

// waitDelay bounds how long Run may spend after the deadline has already
// fired.
//
// Cancelling the context kills docker, but it does not kill anything docker
// started, and a surviving grandchild holds the inherited output pipe open.
// Run waits for that pipe to close, so without this the call blocks long past
// the timeout it is supposed to be enforcing — five seconds, in the test that
// caught it. This is what makes the timeout mean what it says.
const waitDelay = time.Second

// CLI is a [Runtime] backed by the docker command-line binary.
//
// It holds no state between calls, so one CLI is safe to share across
// concurrent judge jobs.
type CLI struct {
	binary   string
	logLimit int
	timeout  time.Duration
}

// Option configures a CLI.
type Option func(*CLI)

// WithBinary runs a different binary in place of "docker".
func WithBinary(path string) Option { return func(c *CLI) { c.binary = path } }

// WithLogLimit changes how much of each stream Logs keeps. A value of zero or
// less restores DefaultLogLimit; there is no way to ask for unbounded reads.
func WithLogLimit(bytes int) Option { return func(c *CLI) { c.logLimit = bytes } }

// WithCommandTimeout changes the ceiling on a single docker invocation.
func WithCommandTimeout(d time.Duration) Option { return func(c *CLI) { c.timeout = d } }

// NewCLI returns a Runtime that shells out to docker.
func NewCLI(opts ...Option) *CLI {
	c := &CLI{binary: "docker", logLimit: DefaultLogLimit, timeout: DefaultCommandTimeout}
	for _, opt := range opts {
		opt(c)
	}

	if c.binary == "" {
		c.binary = "docker"
	}
	if c.logLimit <= 0 {
		c.logLimit = DefaultLogLimit
	}
	if c.timeout <= 0 {
		c.timeout = DefaultCommandTimeout
	}
	return c
}

var _ Runtime = (*CLI)(nil)

// CreateNetwork runs `docker network create`.
func (c *CLI) CreateNetwork(ctx context.Context, spec NetworkSpec) error {
	args := []string{"network", "create"}
	if spec.Internal {
		args = append(args, "--internal")
	}
	args = appendLabels(args, spec.Labels)
	args = append(args, spec.Name)

	_, err := c.run(ctx, args)
	return err
}

// RemoveNetwork runs `docker network rm`, treating an absent network as done.
func (c *CLI) RemoveNetwork(ctx context.Context, name string) error {
	_, err := c.run(ctx, []string{"network", "rm", name})
	return ignoreAlreadyGone(err)
}

// CreateContainer runs `docker create` and returns the id it prints.
func (c *CLI) CreateContainer(ctx context.Context, spec Spec) (string, error) {
	args := []string{"create"}
	if spec.Name != "" {
		args = append(args, "--name", spec.Name)
	}
	if spec.Network != "" {
		args = append(args, "--network", spec.Network)
	}
	for _, alias := range spec.Aliases {
		args = append(args, "--network-alias", alias)
	}
	if spec.Workdir != "" {
		args = append(args, "--workdir", spec.Workdir)
	}
	for _, k := range slices.Sorted(maps.Keys(spec.Env)) {
		args = append(args, "--env", k+"="+spec.Env[k])
	}
	args = appendMounts(args, spec.Mounts)
	args = appendLabels(args, spec.Labels)
	args = appendSandbox(args, spec.Sandbox)
	args = append(args, spec.Image)
	args = append(args, spec.Command...)

	res, err := c.run(ctx, args)
	if err != nil {
		return "", err
	}

	id := strings.TrimSpace(res.stdout.String())
	if id == "" {
		return "", res.failure("printed no container id")
	}
	return id, nil
}

// StartContainer runs `docker start`, which returns as soon as the container
// is running.
func (c *CLI) StartContainer(ctx context.Context, name string) error {
	_, err := c.run(ctx, []string{"start", name})
	return err
}

// WaitContainer runs `docker wait` and parses the exit code it prints.
func (c *CLI) WaitContainer(ctx context.Context, name string) (int, error) {
	// No command timeout here: this call lasts as long as the job does, and
	// the caller's context is what knows how long that may be.
	res, err := c.exec(ctx, []string{"wait", name})
	if err != nil {
		return 0, err
	}

	code, convErr := strconv.Atoi(strings.TrimSpace(res.stdout.String()))
	if convErr != nil {
		return 0, res.failure("did not print an exit code")
	}
	return code, nil
}

// Logs runs `docker logs`, keeping both ends of each stream within the cap.
func (c *CLI) Logs(ctx context.Context, name string) (Logs, error) {
	res, err := c.run(ctx, []string{"logs", name})
	if err != nil {
		return Logs{}, err
	}
	return Logs{
		Stdout: res.stdout.String(),
		Stderr: res.stderr.String(),
		Elided: res.stdout.Elided() + res.stderr.Elided(),
	}, nil
}

// RemoveContainer runs `docker rm --force`, treating an absent container as
// done.
func (c *CLI) RemoveContainer(ctx context.Context, name string) error {
	_, err := c.run(ctx, []string{"rm", "--force", "--volumes", name})
	return ignoreAlreadyGone(err)
}

// result is one docker invocation and everything it produced.
type result struct {
	args   []string // the full command, binary first
	code   int
	stdout *cappedBuffer
	stderr *cappedBuffer
}

// combined is stdout and stderr together, which is what a person needs to see
// when a command fails: docker splits the explanation across both.
func (r *result) combined() string {
	out := strings.TrimSpace(r.stdout.String())
	errOut := strings.TrimSpace(r.stderr.String())

	switch {
	case out == "":
		return errOut
	case errOut == "":
		return out
	default:
		return out + "\n" + errOut
	}
}

// failure reports a command that exited cleanly but did not say what it was
// supposed to say.
func (r *result) failure(msg string) error {
	return &Error{Args: r.args, ExitCode: r.code, Output: r.combined(), Err: errors.New(msg)}
}

// run executes docker under the command timeout, on top of the caller's
// context.
func (c *CLI) run(ctx context.Context, args []string) (*result, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	return c.exec(ctx, args)
}

// exec executes docker under the caller's context alone.
func (c *CLI) exec(ctx context.Context, args []string) (*result, error) {
	res := &result{
		args:   append([]string{c.binary}, args...),
		stdout: newCappedBuffer(c.logLimit),
		stderr: newCappedBuffer(c.logLimit),
	}

	cmd := exec.CommandContext(ctx, c.binary, args...)
	cmd.Stdout = res.stdout
	cmd.Stderr = res.stderr
	cmd.WaitDelay = waitDelay
	confine(cmd)

	err := cmd.Run()
	if err == nil {
		return res, nil
	}

	res.code = -1
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// The exit code is the whole story for an ordinary failure, and the
		// output below says the rest.
		res.code = exitErr.ExitCode()
		err = nil
	}

	// A process killed by our own deadline reports only "signal: killed",
	// which says nothing about why. The context error does, and it is what
	// lets a caller tell a timeout from a cancellation.
	if ctxErr := ctx.Err(); ctxErr != nil {
		err = ctxErr
	}
	return res, &Error{Args: res.args, ExitCode: res.code, Output: res.combined(), Err: err}
}

// alreadyGone is how docker says the thing you asked it to remove is not
// there. Teardown runs on every path, including paths where an earlier
// teardown already ran, so the runtime absorbs this rather than making every
// caller special-case it.
var alreadyGone = []string{"no such container", "no such object", "not found"}

func ignoreAlreadyGone(err error) error {
	var cmdErr *Error
	if !errors.As(err, &cmdErr) {
		return err
	}

	out := strings.ToLower(cmdErr.Output)
	for _, phrase := range alreadyGone {
		if strings.Contains(out, phrase) {
			return nil
		}
	}
	return err
}

func appendMounts(args []string, mounts []Mount) []string {
	for _, m := range mounts {
		spec := "type=bind,source=" + m.Source + ",target=" + m.Target
		if m.ReadOnly {
			spec += ",readonly"
		}
		args = append(args, "--mount", spec)
	}
	return args
}

// appendSandbox renders the confinement flags. A zero field renders nothing,
// so an unconfined container is something a caller asked for rather than
// something that happened.
func appendSandbox(args []string, s Sandbox) []string {
	if s.CPUs > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(s.CPUs, 'f', -1, 64))
	}
	if s.MemoryMB > 0 {
		size := strconv.Itoa(s.MemoryMB) + "m"
		// Equal memory and memory-swap means no swap. Without this a
		// container over its limit swaps instead of dying, and takes the
		// host's disk throughput with it.
		args = append(args, "--memory", size, "--memory-swap", size)
	}
	if s.PIDs > 0 {
		args = append(args, "--pids-limit", strconv.Itoa(s.PIDs))
	}
	if s.ReadOnlyRoot {
		args = append(args, "--read-only")
	}
	for _, path := range slices.Sorted(maps.Keys(s.Tmpfs)) {
		mount := path
		if opts := s.Tmpfs[path]; opts != "" {
			mount += ":" + opts
		}
		args = append(args, "--tmpfs", mount)
	}
	for _, capability := range s.DropCapabilities {
		args = append(args, "--cap-drop", capability)
	}
	if s.NoNewPrivileges {
		args = append(args, "--security-opt", "no-new-privileges")
	}
	if s.User != "" {
		args = append(args, "--user", s.User)
	}
	return args
}

func appendLabels(args []string, labels map[string]string) []string {
	for _, k := range slices.Sorted(maps.Keys(labels)) {
		args = append(args, "--label", k+"="+labels[k])
	}
	return args
}
