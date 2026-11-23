package muse

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

const version = "1.4.25-R9361.1"

func launcher(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "bin", "muse")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/usr/bin/env bash\nexec \"$binary\" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func download(t *testing.T, launcher string) {
	t.Helper()
	dir := filepath.Dir(launcher)
	if err := os.WriteFile(filepath.Join(dir, ".muse-version"), []byte(version+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "muse-bin-"+version), []byte("\x7fELF"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestTheAppBesideTheLauncherDecides(t *testing.T) {
	l := launcher(t, t.TempDir())
	if !LauncherWithoutApp("muse", l) {
		t.Fatal("launcher alone counts as installed")
	}
	_ = os.WriteFile(filepath.Join(filepath.Dir(l), ".muse-version"), []byte(version), 0o644)
	if !LauncherWithoutApp("muse", l) {
		t.Fatal("version written, app missing: still launcher only")
	}
	download(t, l)
	if LauncherWithoutApp("muse", l) {
		t.Fatal("app present but reported missing")
	}
	_ = os.WriteFile(filepath.Join(filepath.Dir(l), ".muse-version"), []byte("garbage"), 0o644)
	if !LauncherWithoutApp("muse", l) {
		t.Fatal("a bad version must not count as installed")
	}
	if LauncherWithoutApp("claude", l) {
		t.Fatal("only Muse has a launcher")
	}
}

func TestANonExecutableAppIsNotInstalled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("execute bits are Unix-only: on Windows the app file itself is the app")
	}
	l := launcher(t, t.TempDir())
	download(t, l)
	_ = os.Chmod(filepath.Join(filepath.Dir(l), "muse-bin-"+version), 0o644)
	if !LauncherWithoutApp("muse", l) {
		t.Fatal("non-executable app counts as installed")
	}
}

// Windows: no execute bits, and the app may carry .exe.
func TestWindowsAppWithExeIsInstalled(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows file naming")
	}
	l := launcher(t, t.TempDir())
	dir := filepath.Dir(l)
	_ = os.WriteFile(filepath.Join(dir, ".muse-version"), []byte(version), 0o644)
	if err := os.WriteFile(filepath.Join(dir, "muse-bin-"+version+".exe"), []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}
	if LauncherWithoutApp("muse", l) {
		t.Fatal("muse-bin-<version>.exe not counted as the app")
	}
}

func TestASymlinkedLauncherIsFollowed(t *testing.T) {
	dir := t.TempDir()
	l := launcher(t, dir)
	download(t, l)
	link := filepath.Join(dir, "elsewhere", "muse")
	_ = os.MkdirAll(filepath.Dir(link), 0o755)
	if err := os.Symlink(l, link); err != nil {
		t.Skip("symlinks not available:", err)
	}
	if LauncherWithoutApp("muse", link) {
		t.Fatal("symlink not followed to the launcher's folder")
	}
}

type said struct{ lines []string }

func (s *said) say(kind, text string) { s.lines = append(s.lines, kind+": "+text) }
func (s *said) text() string          { return strings.Join(s.lines, "\n") }

func answers(replies ...string) func(string) (bool, bool) {
	return func(string) (bool, bool) {
		if len(replies) == 0 {
			return true, true // input ended: the default (yes) must not be acted on
		}
		r := replies[0]
		replies = replies[1:]
		return r == "y", false
	}
}

func TestSetupOffersTheDownloadAndARetry(t *testing.T) {
	l := launcher(t, t.TempDir())
	var runs []Command
	out := &said{}
	ok := Downloader{Label: "Muse Code", Binary: l, Offer: true, Confirm: answers("y", "y"), Say: out.say, Run: func(c Command) int {
		runs = append(runs, c)
		if len(runs) == 1 {
			return 56 // curl: connection reset, nothing downloaded
		}
		download(t, l)
		return 0
	}}.Download()
	if !ok {
		t.Fatalf("download not reported ready:\n%s", out.text())
	}
	for _, want := range []string{"only the launcher is installed", "The download did not finish (exit code 56)", "Muse Code app downloaded"} {
		if !strings.Contains(out.text(), want) {
			t.Errorf("missing %q in:\n%s", want, out.text())
		}
	}
	want := Command{Argv: []string{l}, Env: []string{"MUSE_LAUNCHER_INSTALL=1"}}
	if !reflect.DeepEqual(runs, []Command{want, want}) {
		t.Fatalf("runs = %v", runs)
	}
}

func TestDecliningTheDownloadRunsNothing(t *testing.T) {
	l := launcher(t, t.TempDir())
	ok := Downloader{Label: "Muse Code", Binary: l, Offer: true, Confirm: answers("n"), Run: func(Command) int {
		t.Fatal("must not download after a no")
		return 0
	}}.Download()
	if ok {
		t.Fatal("declined download reported ready")
	}
}

func TestNeverLoopsOnADefaultAnswerAfterInputEnds(t *testing.T) {
	l := launcher(t, t.TempDir())
	runs := 0
	ok := Downloader{Label: "Muse Code", Binary: l, Offer: true, Confirm: answers("y"), Run: func(Command) int {
		runs++
		return 56
	}}.Download()
	if ok || runs != 1 {
		t.Fatalf("ok=%v runs=%d: after input ends it must stop, not retry", ok, runs)
	}
}

func TestWithoutATerminalItShowsTheCommandAndStops(t *testing.T) {
	l := launcher(t, t.TempDir())
	out := &said{}
	ok := Downloader{Label: "Muse Code", Binary: l, Offer: false, Say: out.say, Run: func(Command) int {
		t.Fatal("never downloads without a terminal")
		return 0
	}}.Download()
	if ok {
		t.Fatal("reported ready without a download")
	}
	if !strings.Contains(out.text(), "command: MUSE_LAUNCHER_INSTALL=1 "+l) {
		t.Fatalf("command not shown:\n%s", out.text())
	}
	if AbortMessage("Muse Code") != "Muse Code has not finished downloading" {
		t.Fatal(AbortMessage("Muse Code"))
	}
}

func TestCmdAddsTheInstallEnvWithoutAShell(t *testing.T) {
	cmd := InstallCommand("/x/muse").Cmd()
	if cmd.Path != "/x/muse" || len(cmd.Args) != 1 || cmd.Env[len(cmd.Env)-1] != "MUSE_LAUNCHER_INSTALL=1" {
		t.Fatalf("cmd = %v %v", cmd.Args, cmd.Env[len(cmd.Env)-1])
	}
}
