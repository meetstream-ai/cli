//go:build windows

package proctree

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

const (
	createNewProcessGroup = 0x00000200
	ctrlBreakEvent        = 1
)

var generateConsoleCtrlEvent = syscall.NewLazyDLL("kernel32.dll").NewProc("GenerateConsoleCtrlEvent")

// Prepare puts the child in its own process group (call before Start).
func Prepare(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNewProcessGroup
}

// PrepareInterrupt lets Interrupt reach the child: CTRL_BREAK goes to a
// process group, so the child needs its own.
func PrepareInterrupt(cmd *exec.Cmd) { Prepare(cmd) }

// Kill kills the child and every process it started (taskkill /T), else the
// child alone.
func Kill(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	tk := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	tk.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := tk.Run(); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}

// Reap does nothing on Windows: a process id is reused as soon as the process
// exits, so a tree kill by id afterwards could reach an unrelated process.
func Reap(cmd *exec.Cmd) {}

// Interrupt sends CTRL_BREAK to the child's process group (Prepare or
// PrepareInterrupt first); Go programs receive it as os.Interrupt. os.Interrupt
// itself cannot be sent on Windows.
func Interrupt(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	if r, _, err := generateConsoleCtrlEvent.Call(ctrlBreakEvent, uintptr(cmd.Process.Pid)); r == 0 {
		return err
	}
	return nil
}
