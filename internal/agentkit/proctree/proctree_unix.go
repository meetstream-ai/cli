//go:build !windows

package proctree

import (
	"os"
	"os/exec"
	"syscall"
)

// Prepare puts the child in its own process group (call before Start).
func Prepare(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// PrepareInterrupt lets Interrupt reach the child. Unix signals need nothing.
func PrepareInterrupt(cmd *exec.Cmd) {}

// Signal sends sig to the child's process group (the child itself when it is
// not a group leader).
func Signal(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	pid := cmd.Process.Pid
	if pgid, err := syscall.Getpgid(pid); err == nil && pgid == pid {
		return syscall.Kill(-pid, sig)
	}
	return cmd.Process.Signal(sig)
}

// Kill kills the child's process group. A group outlives its leader, so this
// also reaches helpers left behind after the child exited.
func Kill(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if err != nil {
		err = cmd.Process.Kill()
	}
	return err
}

// Reap kills what is left in the child's process group after it exited (call
// after Wait). A group id is not reused while the group has members, so this
// reaches only the child's own helpers.
func Reap(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// Interrupt asks the child to stop.
func Interrupt(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	return cmd.Process.Signal(os.Interrupt)
}
