package fakecli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProgramIsFoundOnPathAndExitsZero(t *testing.T) {
	dir := t.TempDir()
	p := Program(t, dir, "claude")
	if filepath.Base(p) != Name("claude") || (runtime.GOOS == "windows") != strings.HasSuffix(p, ".exe") {
		t.Fatalf("path %q", p)
	}
	t.Setenv("PATH", dir)
	found, err := exec.LookPath("claude")
	if err != nil || found != p {
		t.Fatalf("LookPath = %q %v", found, err)
	}
	out, err := exec.Command(found, "auth", "status").CombinedOutput()
	if err != nil || len(out) != 0 {
		t.Fatalf("out %q err %v", out, err)
	}
}

func TestEchoPrintsItsArguments(t *testing.T) {
	out, err := exec.Command(Echo(t, t.TempDir(), "echo"), "ready", "now").Output()
	if err != nil || string(out) != "ready now\n" {
		t.Fatalf("out %q err %v", out, err)
	}
}

func TestRunPassesArgumentsStdinAndExitCode(t *testing.T) {
	dir := t.TempDir()
	// The fake runs another fake: an echo, then one that exits 3.
	echo := Echo(t, dir, "inner")
	outer := Run(t, dir, "outer", echo, "first")
	out, err := exec.Command(outer, "second").Output()
	if err != nil || string(out) != "first second\n" {
		t.Fatalf("out %q err %v", out, err)
	}
	failing := write(t, dir, "failing", spec{Exit: 3})
	err = exec.Command(Run(t, dir, "wrapper", failing)).Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Fatalf("exit %v", err)
	}
}

// The program is source text built at test time, so compiling the tests for
// Windows does not check it: build it for Windows here.
func TestProgramBuildsForWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("every test here builds it on Windows")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fakecli\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", filepath.Join(dir, "fakecli.exe"), ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0", "GOWORK=off", "GOFLAGS=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestPythonRunsTheScriptWithoutAShebang(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "stub.py")
	if err := os.WriteFile(script, []byte("import sys\nprint(sys.stdin.read().strip() + ' ' + ' '.join(sys.argv[1:]))\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(Python(t, dir, "agent", script), "a", "b c")
	cmd.Stdin = strings.NewReader("hello\n")
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) != "hello a b c" {
		t.Fatalf("out %q err %v", out, err)
	}
}
