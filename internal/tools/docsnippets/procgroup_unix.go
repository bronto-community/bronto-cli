//go:build !windows

package docsnippets

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup starts cmd as the leader of a new process group, so
// killGroup reaches everything the snippet's shell spawned.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup kills cmd's whole process group: the shell and any background
// descendants still holding its stdout or stderr.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
