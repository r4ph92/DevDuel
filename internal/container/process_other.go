//go:build !unix

package container

import "os/exec"

// confine does nothing on platforms without process groups.
//
// The judge is not one of those platforms: it needs a Docker daemon and runs
// on Linux, with macOS for development. This file exists so the package still
// compiles elsewhere, not so it can be relied on there. Without a process
// group there is no way to reach what docker started, and only the WaitDelay
// in exec keeps a call from blocking on an orphan holding the output pipe.
func confine(cmd *exec.Cmd) {}
