package checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meetstream-ai/cli/internal/agentkit/agents"
)

// Windows has no execute bits: a file is a program by its PATHEXT extension,
// and an install folder's claude is claude.exe, claude.cmd …; ~\ is home.
func TestWindowsExecutablesByPathExt(t *testing.T) {
	dir := t.TempDir()
	e := env(t, dir)
	e.GOOS = "windows"
	e.Getenv = func(k string) string {
		if k == "PATHEXT" {
			return ".COM;.EXE;.BAT;.CMD"
		}
		return ""
	}
	claude := agents.All["claude"]
	plain := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if p, _ := ResolveBinary(e, claude, plain); p != "" {
		t.Fatal("a .txt is not a program")
	}
	cmd := filepath.Join(dir, "claude.cmd")
	if err := os.WriteFile(cmd, []byte("@echo off"), 0o644); err != nil { // no execute bit, like on Windows
		t.Fatal(err)
	}
	if p, _ := ResolveBinary(e, claude, cmd); p != cmd {
		t.Fatalf("override %q", p)
	}
	if p, off := ResolveBinary(e, claude, ""); p != cmd || !off {
		t.Fatalf("install folder: %q %v", p, off)
	}
	// No export or shell profile on Windows: a PowerShell line for the user's Path.
	if c := Binary(e, claude, ""); !strings.Contains(c.Hint, `SetEnvironmentVariable("Path", "`+dir+`;"`) || strings.Contains(c.Hint, "export") {
		t.Fatalf("hint %q", c.Hint)
	}
	e.GOOS = "linux" // the same file without an execute bit is not a program there
	if p, _ := ResolveBinary(e, claude, cmd); p != "" {
		t.Fatal("unix needs the execute bit")
	}
	if got := (Env{Home: dir}).expand(`~\bin\claude.exe`); got != filepath.Join(dir, `\bin\claude.exe`) {
		t.Fatalf("expand %q", got)
	}
}
