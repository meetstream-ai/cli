//go:build !windows

package drivers

import (
	"os/exec"
	"syscall"
)

func prepare(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func signal(cmd *exec.Cmd, kill bool) {
	if cmd.Process == nil {
		return
	}
	sig := syscall.SIGTERM
	if kill {
		sig = syscall.SIGKILL
	}
	_ = syscall.Kill(-cmd.Process.Pid, sig)
}
