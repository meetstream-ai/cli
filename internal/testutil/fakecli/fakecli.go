// Package fakecli makes stand-in agent CLIs for tests that run the same on
// macOS, Linux and Windows. Each fake is a copy of one tiny Go program (built
// once per test process, cached across runs) named like a real program on this
// OS (claude, or claude.exe on Windows). Beside it, <name>.fakecli says what it
// does: nothing (exit 0), echo its arguments, or run another program, such as
// Python with a stub script, with its own arguments appended. No shell
// scripts, shebangs or execute bits are needed, so Windows runs them too.
package fakecli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// The environment and temp folder when the test binary started: tests change
// HOME, PATH and the like, and the build must keep using the real Go cache.
var (
	startEnv = os.Environ()
	startTmp = os.TempDir()
)

// Name is a program file name on this OS: name, or name.exe on Windows.
func Name(name string) string {
	if runtime.GOOS == "windows" && filepath.Ext(name) == "" {
		return name + ".exe"
	}
	return name
}

// spec is the <name>.fakecli file the program reads.
type spec struct {
	Argv []string `json:"argv,omitempty"` // run this with the fake's own arguments appended
	Echo bool     `json:"echo,omitempty"` // print the arguments, like echo
	Exit int      `json:"exit,omitempty"` // exit code when Argv is empty
}

// Program writes a program called name into dir that exits 0 and prints
// nothing (a stand-in that only has to be found, or to succeed). It returns
// the program's path.
func Program(t testing.TB, dir, name string) string {
	t.Helper()
	return write(t, dir, name, spec{})
}

// Echo writes a program called name into dir that prints its arguments joined
// by spaces and a newline, like /bin/echo.
func Echo(t testing.TB, dir, name string) string {
	t.Helper()
	return write(t, dir, name, spec{Echo: true})
}

// Run writes a program called name into dir that runs argv with its own
// arguments appended, passing stdin, stdout, stderr and the exit code through.
func Run(t testing.TB, dir, name string, argv ...string) string {
	t.Helper()
	if len(argv) == 0 {
		t.Fatal("fakecli.Run: no program to run")
	}
	return write(t, dir, name, spec{Argv: argv})
}

// Python writes a program called name into dir that runs script with Python 3
// (python3, python or py -3, whichever works here), never through a shebang.
// The test is skipped when this computer has no Python 3.
func Python(t testing.TB, dir, name, script string) string {
	t.Helper()
	py := Python3(t)
	abs, err := filepath.Abs(script)
	if err != nil {
		t.Fatal(err)
	}
	// -u: unbuffered, as a streaming agent is; -X utf8: the same text on
	// every OS (Windows pipes default to the ANSI code page).
	return Run(t, dir, name, append(py, "-u", "-X", "utf8", abs)...)
}

var (
	pyOnce sync.Once
	pyArgv []string
)

// Python3 is the command that runs Python 3 here (skips the test without one).
// A name on PATH is not enough: Windows' Store alias python3.exe exists but
// runs nothing, so each candidate must run a line of Python.
func Python3(t testing.TB) []string {
	t.Helper()
	pyOnce.Do(func() {
		candidates := [][]string{{"python3"}, {"python"}}
		if runtime.GOOS == "windows" {
			candidates = append(candidates, []string{"py", "-3"})
		}
		for _, c := range candidates {
			p, err := exec.LookPath(c[0])
			if err != nil {
				continue
			}
			argv := append([]string{p}, c[1:]...)
			check := exec.Command(argv[0], append(argv[1:], "-c", "import sys; sys.exit(0 if sys.version_info >= (3, 8) else 1)")...)
			if check.Run() == nil {
				pyArgv = argv
				return
			}
		}
	})
	if pyArgv == nil {
		t.Skip("Python 3 is needed for the stub agent")
	}
	return append([]string(nil), pyArgv...)
}

func write(t testing.TB, dir, name string, s spec) string {
	t.Helper()
	src, err := program()
	if err != nil {
		t.Fatalf("fakecli: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, Name(name))
	if err := copyFile(src, path); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(s)
	if err := os.WriteFile(specPath(path), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// specPath is <program without .exe>.fakecli; the program finds it the same way.
func specPath(program string) string {
	if strings.EqualFold(filepath.Ext(program), ".exe") {
		program = program[:len(program)-4]
	}
	return program + ".fakecli"
}

func copyFile(src, dst string) error {
	_ = os.Remove(dst)
	if os.Link(src, dst) == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

var (
	buildOnce sync.Once
	built     string
	buildErr  error
)

// program builds the fake program once per process, into a folder keyed by
// its source and Go version so later runs (and parallel packages) reuse it.
func program() (string, error) {
	buildOnce.Do(func() { built, buildErr = build() })
	return built, buildErr
}

func build() (string, error) {
	sum := sha256.Sum256([]byte(source + runtime.Version() + runtime.GOOS + runtime.GOARCH))
	dir := filepath.Join(startTmp, "meetstream-fakecli-"+hex.EncodeToString(sum[:8]))
	bin := filepath.Join(dir, Name("fakecli"))
	if fi, err := os.Stat(bin); err == nil && fi.Mode().IsRegular() {
		return bin, nil
	}
	work, err := os.MkdirTemp(startTmp, "meetstream-fakecli-build-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	srcDir, outDir := filepath.Join(work, "src"), filepath.Join(work, "out")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(srcDir, "go.mod"), []byte("module fakecli\n\ngo 1.21\n"), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), []byte(source), 0o644); err != nil {
		return "", err
	}
	goTool := filepath.Join(runtime.GOROOT(), "bin", Name("go"))
	if _, err := os.Stat(goTool); err != nil {
		goTool = "go"
	}
	cmd := exec.Command(goTool, "build", "-o", filepath.Join(outDir, Name("fakecli")), ".")
	cmd.Dir = srcDir
	cmd.Env = append(append([]string{}, startEnv...), "GOWORK=off", "GOFLAGS=", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build: %v\n%s", err, out)
	}
	// Another process may have built it meanwhile: theirs is as good.
	if err := os.Rename(outDir, dir); err != nil {
		if _, statErr := os.Stat(bin); statErr == nil {
			return bin, nil
		}
		return "", errors.Join(err, fmt.Errorf("fakecli: cannot place the program in %s", dir))
	}
	return bin, nil
}

// source is the fake program. On Unix it replaces itself with the program it
// runs (exec), so signals and process groups reach that program as before;
// Windows has no exec, so it runs it as a child and passes the exit code on
// (a tree kill, taskkill /T, reaches both).
const source = `package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

type spec struct {
	Argv []string ` + "`json:\"argv\"`" + `
	Echo bool     ` + "`json:\"echo\"`" + `
	Exit int      ` + "`json:\"exit\"`" + `
}

func main() {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakecli:", err)
		os.Exit(127)
	}
	if strings.EqualFold(filepath.Ext(self), ".exe") {
		self = self[:len(self)-4]
	}
	var s spec
	if b, err := os.ReadFile(self + ".fakecli"); err == nil {
		if err := json.Unmarshal(b, &s); err != nil {
			fmt.Fprintln(os.Stderr, "fakecli:", err)
			os.Exit(127)
		}
	}
	if s.Echo {
		fmt.Println(strings.Join(os.Args[1:], " "))
		return
	}
	if len(s.Argv) == 0 {
		os.Exit(s.Exit)
	}
	argv := append(append([]string{}, s.Argv...), os.Args[1:]...)
	_ = syscall.Exec(argv[0], argv, os.Environ()) // returns only where exec is not supported (Windows)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	signal.Ignore(os.Interrupt) // CTRL_BREAK reaches the child too (same process group)
	err = cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		os.Exit(exit.ExitCode())
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakecli:", err)
		os.Exit(127)
	}
}
`
