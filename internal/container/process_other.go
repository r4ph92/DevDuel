//go:build !unix

package container

import "os/exec"

// confine is a no-op where process groups do not exist. The WaitDelay in exec
// still bounds the call; only the orphaned grandchildren are not cleaned up.
func confine(cmd *exec.Cmd) {}
