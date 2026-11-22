//go:build windows

package drivers

import "os/exec"

func prepare(cmd *exec.Cmd) {}
func signal(cmd *exec.Cmd, kill bool) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
