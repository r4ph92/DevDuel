//go:build unix

package container

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// confine puts the command in its own process group and kills that whole
// group when the context ends.
//
// Killing docker alone is not enough. Anything docker started is a separate
// process that survives its parent, holds the inherited output pipe open, and
// keeps running long after the deadline that was supposed to stop it. The
// group is the unit that has to die.
func confine(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		// The negative pid is the process group. Setpgid above made the
		// child its own leader, so this cannot reach anything else.
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
