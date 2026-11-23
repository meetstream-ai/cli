//go:build !windows

package proctree

import (
	"bufio"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// startTree starts sh with a background sleep (a helper the agent CLI started)
// and returns the helper's pid.
func startTree(t *testing.T) (*exec.Cmd, int) {
	t.Helper()
	cmd := exec.Command("sh", "-c", "sleep 30 & echo $!; wait")
	Prepare(cmd)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	return cmd, pid
}

// gone waits until pid no longer runs (an orphan is reaped by init).
func gone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("helper %d still runs", pid)
}

func TestKillReachesTheWholeTree(t *testing.T) {
	cmd, helper := startTree(t)
	if err := Kill(cmd); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	gone(t, helper)
}

func TestReapKillsHelpersLeftAfterExit(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 30 >/dev/null 2>&1 & echo $!")
	Prepare(cmd)
	out, err := cmd.Output() // sh exits at once; its sleep stays in the group
	if err != nil {
		t.Fatal(err)
	}
	helper, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	if syscall.Kill(helper, 0) != nil {
		t.Fatal("helper did not outlive sh")
	}
	Reap(cmd)
	gone(t, helper)
}
