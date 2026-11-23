//go:build !windows

package drivers

import (
	"os/exec"
	"syscall"

	"github.com/meetstream-ai/cli/internal/agentkit/proctree"
)

// killGroupAfterExit: a process group id is not reused while the group has
// members, so killing it after the leader exited reaches only its helpers.
const killGroupAfterExit = true

func prepare(cmd *exec.Cmd) { proctree.Prepare(cmd) }
func signal(cmd *exec.Cmd, kill bool) {
	if kill {
		_ = proctree.Kill(cmd)
		return
	}
	_ = proctree.Signal(cmd, syscall.SIGTERM)
}
