// Package container runs the judge's containers.
//
// Everything here shells out to the `docker` binary rather than using the
// official SDK. That is a deliberate first step: while the sandbox is being
// built, every command the judge issues can be copied out of an error message
// and re-run by hand. [Runtime] is the seam that keeps that choice reversible
// — swapping in the SDK later means writing a second implementation, not
// touching the judge.
//
// Nothing in this package decides policy. It does not know what a match is,
// and it applies no resource limits or hardening of its own; the judge passes
// down what it wants.
package container

import "context"

// Runtime is the set of container operations the judge needs.
//
// Every method takes a context and honours its cancellation. Wherever a
// method names a container, either its name or its id will do. Remove methods
// are idempotent: removing something that is already gone is success, so
// teardown can run on every path without the caller tracking what it created.
type Runtime interface {
	// CreateNetwork creates a network. The name must not already be in use.
	CreateNetwork(ctx context.Context, spec NetworkSpec) error
	// RemoveNetwork removes a network, or does nothing if it is already gone.
	RemoveNetwork(ctx context.Context, name string) error

	// CreateContainer creates a container without starting it and returns its
	// id. Creating it separately is what lets the judge copy files in before
	// any player code runs.
	CreateContainer(ctx context.Context, spec Spec) (string, error)
	// StartContainer starts a created container and returns immediately.
	StartContainer(ctx context.Context, name string) error
	// WaitContainer blocks until the container exits and returns its exit
	// code. It is the one call whose duration is the job's rather than the
	// runtime's, so it is bounded only by the caller's context.
	WaitContainer(ctx context.Context, name string) (int, error)
	// Logs returns the container's output, capped so a chatty or hostile
	// container cannot exhaust the judge's memory.
	Logs(ctx context.Context, name string) (Logs, error)
	// RemoveContainer force-removes a container, or does nothing if it is
	// already gone.
	RemoveContainer(ctx context.Context, name string) error
}

// NetworkSpec describes a network to create.
type NetworkSpec struct {
	Name string
	// Internal cuts the network off from the outside world. Judge networks
	// are always internal: challenge images ship with their dependencies
	// baked in precisely so that nothing needs egress at judge time.
	Internal bool
	// Labels are attached to the network so an interrupted judge run can be
	// swept up later by label rather than by remembered name.
	Labels map[string]string
}

// Spec describes a container to create.
type Spec struct {
	Name    string
	Image   string
	Command []string
	Workdir string
	// Env is rendered in sorted key order, so the command a failure prints is
	// the same command every time.
	Env map[string]string
	// Network is the network to attach to, and Aliases are the names the
	// container answers to on it. The tester reaches the runner by alias, so
	// neither container needs to know the other's address.
	Network string
	Aliases []string
	Labels  map[string]string
	// Mounts are host paths made visible inside the container. This is how
	// files get in: a read-only root filesystem refuses `docker cp`, so
	// anything confined has to be given its files rather than handed them.
	Mounts []Mount
	// Sandbox is the confinement to run under. The zero value is no
	// confinement at all, so anything running code it did not write must set
	// it deliberately.
	Sandbox Sandbox
}

// Sandbox is the confinement applied to a container, on the assumption that
// what runs inside it is hostile.
//
// Each field is one docker flag. Nothing here is a default: this package
// applies exactly what it is given, and the policy of what to give it belongs
// to whoever knows what is being run.
type Sandbox struct {
	// CPUs is the share of a core the container may use, as --cpus.
	CPUs float64
	// MemoryMB caps memory. Swap is pinned to the same value, so a container
	// over its limit is killed rather than quietly swapping the host to a
	// standstill.
	MemoryMB int
	// PIDs caps the process count, which is what stops a fork bomb.
	PIDs int
	// ReadOnlyRoot mounts the root filesystem read-only. Anything that needs
	// to write needs a Tmpfs entry for it.
	ReadOnlyRoot bool
	// Tmpfs maps a path to its mount options, e.g. "/tmp" to
	// "rw,noexec,nosuid,size=64m". These are the only writable paths under a
	// read-only root, and they never touch the host's disk.
	Tmpfs map[string]string
	// DropCapabilities are Linux capabilities to remove. "ALL" is the useful
	// value; anything a challenge genuinely needs is a challenge that should
	// be rewritten.
	DropCapabilities []string
	// NoNewPrivileges stops a process gaining privileges through setuid, so
	// a setuid binary in the image cannot become an escalation path.
	NoNewPrivileges bool
	// User is the uid[:gid] to run as. Running as root inside the container
	// is one kernel bug away from running as root on the host.
	User string
}

// Mount is a host path made visible inside a container.
type Mount struct {
	// Source is an absolute path on the host.
	Source string
	// Target is the absolute path it appears at inside the container.
	Target string
	// ReadOnly stops the container writing through the mount, which also
	// stops it editing the files it was given.
	ReadOnly bool
}

// Logs is a container's captured output.
type Logs struct {
	Stdout string
	Stderr string
	// Elided counts the bytes dropped from the middle of the output to stay
	// under the cap. The beginning and the end are kept: startup failures
	// show up in the first bytes, and the tester's result marker in the last.
	Elided int
}

// Truncated reports whether anything was dropped to stay under the cap.
func (l Logs) Truncated() bool { return l.Elided > 0 }
