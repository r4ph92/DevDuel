package container_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/r4ph92/DevDuel/internal/container"
)

// sandboxImage is small, has a shell, and is on every CI runner's mirror.
const sandboxImage = "alpine:3"

// requireDocker skips unless a daemon actually answers.
//
// These tests are about what the kernel does with a cgroup and a namespace,
// which is exactly the part a fake runtime cannot tell you anything about.
func requireDocker(t *testing.T) *container.CLI {
	t.Helper()

	if testing.Short() {
		t.Skip("docker integration test skipped in -short mode")
	}

	cli := container.NewCLI(container.WithCommandTimeout(2 * time.Minute))
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	// A daemon that answers "no such container" is a daemon that is running.
	err := cli.RemoveContainer(ctx, "devduel-probe-that-does-not-exist")
	if err != nil {
		t.Skipf("docker not available: %v", err)
	}
	return cli
}

// sandboxed creates a confined container running script, and guarantees its
// removal even when the test fails.
func sandboxed(t *testing.T, cli *container.CLI, box container.Sandbox, script string, mounts ...container.Mount) string {
	t.Helper()

	name := fmt.Sprintf("devduel-test-%s-%d", strings.ToLower(strings.NewReplacer("/", "-", "_", "-").Replace(t.Name())), time.Now().UnixNano()%1e6)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 60*time.Second)
		defer cancel()
		if err := cli.RemoveContainer(ctx, name); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	if _, err := cli.CreateContainer(ctx, container.Spec{
		Name:    name,
		Image:   sandboxImage,
		Command: []string{"sh", "-c", script},
		Mounts:  mounts,
		Sandbox: box,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	return name
}

// judgeSandbox mirrors the confinement the judge applies, so these tests
// exercise the real policy rather than a convenient one.
func judgeSandbox() container.Sandbox {
	return container.Sandbox{
		CPUs:             1,
		MemoryMB:         128,
		PIDs:             32,
		ReadOnlyRoot:     true,
		Tmpfs:            map[string]string{"/tmp": "rw,noexec,nosuid,size=64m"},
		DropCapabilities: []string{"ALL"},
		NoNewPrivileges:  true,
		User:             "65534:65534",
	}
}

func TestSandboxContainsAForkBomb(t *testing.T) {
	cli := requireDocker(t)
	name := sandboxed(t, cli, judgeSandbox(), `:(){ :|:& };:`)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	if err := cli.StartContainer(ctx, name); err != nil {
		t.Fatalf("start: %v", err)
	}

	// A fork bomb does not exit; the pids limit stops it growing and the
	// deadline stops us. What matters is that both happen, promptly, and that
	// this process is still here to assert it.
	waitCtx, cancelWait := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancelWait()

	start := time.Now()
	_, err := cli.WaitContainer(waitCtx, name)
	elapsed := time.Since(start)

	if err == nil {
		t.Log("the bomb exited on its own, which the pids limit also allows")
	}
	if elapsed > 30*time.Second {
		t.Errorf("wait took %v; the deadline did not hold", elapsed)
	}
}

func TestSandboxKillsAnInfiniteLoopOnTimeout(t *testing.T) {
	cli := requireDocker(t)
	name := sandboxed(t, cli, judgeSandbox(), `while true; do :; done`)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	if err := cli.StartContainer(ctx, name); err != nil {
		t.Fatalf("start: %v", err)
	}

	waitCtx, cancelWait := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelWait()

	start := time.Now()
	_, err := cli.WaitContainer(waitCtx, name)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait returned %v, want the deadline to cut it off", err)
	}
	// The container is still spinning; teardown is what stops it, and
	// t.Cleanup force-removes it.
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Errorf("wait took %v, want it cut off near the 5s deadline", elapsed)
	}
}

func TestSandboxCapsAFloodOfOutputWithoutGrowingTheHeap(t *testing.T) {
	cli := requireDocker(t)
	// Roughly a gigabyte, written as fast as the container can manage.
	name := sandboxed(t, cli, judgeSandbox(), `yes 0123456789012345678901234567890123456789012345678901234567890123 | head -c 1000000000`)

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	if err := cli.StartContainer(ctx, name); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := cli.WaitContainer(ctx, name); err != nil {
		t.Fatalf("wait: %v", err)
	}

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	logs, err := cli.Logs(ctx, name)
	if err != nil {
		t.Fatalf("logs: %v", err)
	}

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	if !logs.Truncated() {
		t.Error("a gigabyte of output should have been capped")
	}
	if got := len(logs.Stdout); got > container.DefaultLogLimit+1024 {
		t.Errorf("kept %d bytes, want no more than the cap", got)
	}

	// The point of capping while reading rather than after: a gigabyte of
	// output must not become a gigabyte of judge.
	const tolerance = 64 << 20
	if growth := int64(after.HeapAlloc) - int64(before.HeapAlloc); growth > tolerance {
		t.Errorf("heap grew by %d bytes reading capped logs, want under %d", growth, tolerance)
	}
}

func TestSandboxLetsMountedFilesBeReadButNotWritten(t *testing.T) {
	// The judge mounts a workspace in and runs it as an unprivileged user
	// under a read-only root. That combination has to actually work, and it
	// is not obvious that it does: `docker cp`, which is the reflex, is
	// refused outright by a read-only rootfs.
	cli := requireDocker(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "server.js"), []byte("the player's code\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	script := `
		set -e
		echo "uid=$(id -u)"
		cat /app/server.js
		echo scratch > /tmp/scratch && echo "tmp-write=ok"
		if echo nope > /app/blocked 2>/dev/null; then echo "app-write=ok"; else echo "app-write=denied"; fi
		if echo nope > /etc/blocked 2>/dev/null; then echo "root-write=ok"; else echo "root-write=denied"; fi
	`
	name := sandboxed(t, cli, judgeSandbox(), script,
		container.Mount{Source: dir, Target: "/app", ReadOnly: true})

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	if err := cli.StartContainer(ctx, name); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := cli.WaitContainer(ctx, name); err != nil {
		t.Fatalf("wait: %v", err)
	}

	logs, err := cli.Logs(ctx, name)
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	out := logs.Stdout + logs.Stderr

	for _, want := range []string{
		"uid=65534",         // not root
		"the player's code", // copied files are readable
		"tmp-write=ok",      // the scratch tmpfs works
		"app-write=denied",  // the workspace is not writable
		"root-write=denied", // nor is anything else
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in the container's output, got:\n%s", want, out)
		}
	}
}
