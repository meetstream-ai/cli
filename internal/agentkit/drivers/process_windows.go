//go:build windows

package drivers

import (
	"os/exec"

	"github.com/meetstream-ai/cli/internal/agentkit/proctree"
)

// killGroupAfterExit is off: Windows reuses a process id as soon as it exits,
// so taskkill by id afterwards could reach an unrelated process.
const killGroupAfterExit = false

func prepare(cmd *exec.Cmd) { proctree.Prepare(cmd) }

// signal kills the whole tree either way: Windows has no SIGTERM to ask with.
func signal(cmd *exec.Cmd, kill bool) { _ = proctree.Kill(cmd) }
