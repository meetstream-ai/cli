//go:build !windows

package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// gone waits until pid no longer runs.
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
	t.Fatalf("process %d still runs", pid)
}

// A bench run that times out kills everything the CLI started, not only the
// CLI: here a helper the CLI left in the background.
func TestBenchExecTimeoutKillsTheTree(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	var helper int
	t0 := time.Now()
	_, err := benchExec(ctx, []string{"sh", "-c", "sleep 30 & echo $!; wait"}, t.TempDir(), "", os.Environ(), func(line string) {
		helper, _ = strconv.Atoi(strings.TrimSpace(line))
	})
	if err == nil || helper == 0 {
		t.Fatalf("err=%v helper=%d", err, helper)
	}
	if time.Since(t0) > 5*time.Second {
		t.Fatalf("took %v", time.Since(t0))
	}
	gone(t, helper)

	// A CLI that exits but leaves a helper behind: the helper is reaped too.
	helper = 0
	if _, err := benchExec(context.Background(), []string{"sh", "-c", "sleep 30 >/dev/null 2>&1 & echo $!"}, t.TempDir(), "", os.Environ(), func(line string) {
		helper, _ = strconv.Atoi(strings.TrimSpace(line))
	}); err != nil || helper == 0 {
		t.Fatalf("err=%v helper=%d", err, helper)
	}
	gone(t, helper)
}

// Ctrl-C (SIGINT) during a plain-lines bench: exit 130, and the model that
// finished is in the one JSON document.
func TestAgentBenchCtrlCExits130(t *testing.T) {
	t.Setenv("MEETSTREAM_AGENT_KIT_HOME", t.TempDir())
	t.Setenv("PATH", fakeBin(t, "claude"))
	finish := fakeClaudeExec(nil)
	var n atomic.Int32
	swap(t, &benchExec, func(ctx context.Context, argv []string, dir, input string, env []string, onLine func(string)) (string, error) {
		if n.Add(1) == 1 {
			return finish(ctx, argv, dir, input, env, onLine)
		}
		_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
		<-ctx.Done()
		return "", ctx.Err()
	})
	out, code := run(t, nil, "agent", "bench", "--agent", "claude", "--models", "opus,sonnet", "--parallel", "1", "--json", "--no-input")
	var doc struct {
		Results []struct{ Model, State string } `json:"results"`
		Error   struct {
			ExitCode int `json:"exit_code"`
		} `json:"error"`
	}
	dec := json.NewDecoder(strings.NewReader(out))
	if code != 130 || dec.Decode(&doc) != nil || dec.More() {
		t.Fatalf("code=%d %s", code, out)
	}
	if doc.Error.ExitCode != 130 || len(doc.Results) != 2 || doc.Results[0].State != "done" || doc.Results[1].State == "done" {
		t.Fatalf("%s", out)
	}
}

// While the app is suspended (nobody reads its messages) the background
// bridge keeps writing: its lines go into a bounded ring.
func TestHomeAgentOutputNeverBlocks(t *testing.T) {
	homeFor(t, nil)
	marker := filepath.Join(t.TempDir(), "wrote-all")
	old := homeAgentCommand
	homeAgentCommand = func() *exec.Cmd {
		return exec.Command("sh", "-c", "i=0; while [ $i -lt 3000 ]; do echo line $i; i=$((i+1)); done; touch '"+marker+"'; exec sleep 30")
	}
	t.Cleanup(func() { homeAgentCommand = old })
	a, err := startAgent()
	if err != nil {
		t.Fatal(err)
	}
	defer a.shutdown(time.Second)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the bridge was blocked writing its lines")
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	a.mu.Lock()
	n, last := len(a.log), a.log[len(a.log)-1]
	a.mu.Unlock()
	if n != agentLogLines || last != "line 2999" {
		t.Fatalf("%d lines, last %q", n, last)
	}
}

// Quitting waits for the background bridge: asked to stop, then killed with
// its tree when it does not.
func TestHomeAgentShutdownWaits(t *testing.T) {
	homeFor(t, nil)
	swap(t, &agentStopGrace, time.Second)
	stopped := filepath.Join(t.TempDir(), "stopped")
	old := homeAgentCommand
	t.Cleanup(func() { homeAgentCommand = old })

	// Stops on Ctrl-C after cleaning up.
	homeAgentCommand = func() *exec.Cmd {
		return exec.Command("sh", "-c", "trap 'sleep 0.3; touch \""+stopped+"\"; exit 0' INT; while :; do sleep 0.05; done")
	}
	a, err := startAgent()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // the trap is set
	if !a.shutdown(5 * time.Second) {
		t.Fatal("did not stop")
	}
	if _, err := os.Stat(stopped); err != nil {
		t.Fatal("returned before the bridge finished stopping")
	}

	// Ignores Ctrl-C: killed after the grace period.
	homeAgentCommand = func() *exec.Cmd { return exec.Command("sh", "-c", "trap '' INT; while :; do sleep 0.05; done") }
	if a, err = startAgent(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	t0 := time.Now()
	if !a.shutdown(5*time.Second) || time.Since(t0) > 4*time.Second {
		t.Fatalf("not killed in time: %v", time.Since(t0))
	}
}
