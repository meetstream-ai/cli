package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"github.com/meetstream-ai/cli/internal/ui"
)

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func captureUI(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := ui.Out
	ui.Out = &buf
	t.Cleanup(func() { ui.Out = old })
	return &buf
}

func noInstalls(t *testing.T) {
	t.Helper()
	old := setupInstaller
	setupInstaller = installer{in: strings.NewReader(""), run: func(stages [][]string) int {
		t.Fatalf("installer ran: %v", stages)
		return 1
	}}
	t.Cleanup(func() { setupInstaller = old })
}

// executeSetup runs `meetstream <args>` and returns the ExitError (nil on success).
func executeSetup(t *testing.T, args ...string) *ExitError {
	t.Helper()
	t.Setenv("MEETSTREAM_CONFIG_DIR", t.TempDir())
	t.Setenv("MEETSTREAM_AGENT_KIT_HOME", t.TempDir())
	t.Setenv("MEETSTREAM_AGENT_KIT_CREDENTIALS", "file")
	g = globals{}
	root := NewRoot(BuildInfo{Version: "1.2.3"})
	root.SetArgs(args)
	oldOut := os.Stdout
	devnull, _ := os.Open(os.DevNull)
	os.Stdout = devnull
	err := root.Execute()
	os.Stdout = oldOut
	devnull.Close()
	if err == nil {
		return nil
	}
	var ee *ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("not an ExitError: %v", err)
	}
	return ee
}

// --no-input and --json never install: exit 2 with the install command as the hint.
func TestSetupMissingAgentNonInteractiveHintsAndExits2(t *testing.T) {
	noInstalls(t)
	t.Setenv("PATH", t.TempDir())
	for _, flag := range []string{"--no-input", "--json"} {
		ee := executeSetup(t, "setup", "--agent", "muse", flag)
		if ee == nil || ee.Code != 2 || !strings.Contains(ee.Hint, "curl -fsSL https://dev.meta.ai/install.sh | bash") {
			t.Fatalf("%s: %+v", flag, ee)
		}
	}
	// No agent named and none installed: unchanged usage error.
	if ee := executeSetup(t, "setup", "--no-input"); ee == nil || ee.Code != 2 || !strings.Contains(ee.Message, "no supported agent") {
		t.Fatalf("%+v", ee)
	}
}

// fakeBin writes executable stubs into a fresh folder.
func fakeBin(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func withInstallDirs(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	old := systemInstallDirs
	systemInstallDirs = nil
	t.Cleanup(func() { systemInstallDirs = old })
}

// The kit's check for a launcher the installer put off PATH (Muse's goes to
// ~/.local/bin): found, PATH extended for this run, and the export line shown.
func TestInstallFindsMuseLauncherOffPath(t *testing.T) {
	out := captureUI(t)
	home := t.TempDir()
	withInstallDirs(t, home)
	t.Setenv("PATH", fakeBin(t, "curl", "bash"))
	var ran [][]string
	in := installer{in: strings.NewReader("y\nn\n"), run: func(stages [][]string) int {
		ran = append(ran, stages...)
		dir := filepath.Join(home, ".local", "bin")
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, "muse"), []byte("#!/bin/sh\n"), 0o755)
		return 0
	}}
	got := in.offer(agents.All["muse"])
	want := filepath.Join(home, ".local", "bin", "muse")
	if got != want {
		t.Fatalf("got %q want %q\n%s", got, want, out)
	}
	if len(ran) != 2 || strings.Join(ran[0], " ") != "curl -fsSL https://dev.meta.ai/install.sh" || strings.Join(ran[1], " ") != "bash" {
		t.Fatalf("ran %v", ran)
	}
	s := out.String()
	for _, w := range []string{"curl -fsSL https://dev.meta.ai/install.sh | bash", `export PATH="` + filepath.Dir(want) + `:$PATH"`, "not on your PATH", "Sign in and set it up now (muse)?"} {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q in\n%s", w, s)
		}
	}
	if !strings.HasPrefix(os.Getenv("PATH"), filepath.Dir(want)+string(os.PathListSeparator)) {
		t.Errorf("PATH not extended: %s", os.Getenv("PATH"))
	}
}

func TestInstallLauncherMissingExplains(t *testing.T) {
	out := captureUI(t)
	withInstallDirs(t, t.TempDir())
	t.Setenv("PATH", fakeBin(t, "curl", "bash"))
	in := installer{in: strings.NewReader("\n"), run: func([][]string) int { return 0 }}
	if got := in.offer(agents.All["muse"]); got != "" {
		t.Fatalf("got %q", got)
	}
	if s := out.String(); !strings.Contains(s, "Installed, but `muse` is not on your PATH yet.") || !strings.Contains(s, "Open a new terminal window") {
		t.Fatalf("%s", s)
	}
}

func TestInstallNeedsExplicitYes(t *testing.T) {
	withInstallDirs(t, t.TempDir())
	t.Setenv("PATH", fakeBin(t, "npm"))
	for _, answer := range []string{"n\n", "no\n", ""} { // "" = end of input
		captureUI(t)
		in := installer{in: strings.NewReader(answer), run: func(s [][]string) int { t.Fatalf("ran %v on %q", s, answer); return 0 }}
		if got := in.offer(agents.All["claude"]); got != "" {
			t.Fatalf("%q: got %q", answer, got)
		}
	}
}

func TestInstallMissingPrerequisiteAndFailedInstaller(t *testing.T) {
	withInstallDirs(t, t.TempDir())
	out := captureUI(t)
	t.Setenv("PATH", t.TempDir()) // no npm
	in := installer{in: strings.NewReader("y\n"), run: func(s [][]string) int { t.Fatalf("ran %v", s); return 0 }}
	if in.offer(agents.All["codex"]) != "" || !strings.Contains(out.String(), "needs npm") || !strings.Contains(out.String(), "nodejs.org") {
		t.Fatalf("%s", out)
	}
	out.Reset()
	t.Setenv("PATH", fakeBin(t, "npm"))
	in = installer{in: strings.NewReader("y\n"), run: func([][]string) int { return 1 }}
	if in.offer(agents.All["codex"]) != "" || !strings.Contains(out.String(), "exit code 1") {
		t.Fatalf("%s", out)
	}
}

func TestRunPipelineWithoutShell(t *testing.T) {
	var out bytes.Buffer
	if code := runPipeline([][]string{{"echo", "a|b; $HOME"}, {"cat"}}, strings.NewReader(""), &out); code != 0 || out.String() != "a|b; $HOME\n" {
		t.Fatalf("code=%d out=%q", code, out.String())
	}
	out.Reset()
	if code := runPipeline([][]string{{"false"}, {"cat"}}, strings.NewReader(""), &out); code != 1 {
		t.Fatalf("first stage failure: code=%d", code)
	}
	if code := runPipeline([][]string{{"no-such-program-xyz"}}, strings.NewReader(""), &out); code != 127 {
		t.Fatalf("missing program: code=%d", code)
	}
}

func key(s string) tea.KeyMsg {
	switch s {
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// Step 1: Esc cancels (setup returns exit 130); Enter picks.
func TestPickModelEscCancels(t *testing.T) {
	items := []agentChoice{{name: "claude", label: "Claude Code"}, {name: "muse", label: "Muse Code"}}
	m, cmd := pickModel{items: items}.Update(key("esc"))
	if !isQuit(cmd) || m.(pickModel).chosen != nil {
		t.Fatal("esc at step 1 should quit with nothing chosen")
	}
	m, _ = pickModel{items: items}.Update(key("down"))
	m, cmd = m.Update(key("enter"))
	if !isQuit(cmd) || m.(pickModel).chosen == nil || m.(pickModel).chosen.name != "muse" {
		t.Fatalf("enter: %+v", m)
	}
}

// Steps 2 and 3: Esc or the "← Back" row go back; q / ctrl+c cancel; Enter picks.
func TestStepMenuBack(t *testing.T) {
	menu := newStepMenu("Connect this computer to MeetStream?", "Connect this computer (opens your browser)")
	if !strings.Contains(menu.View(), backLabel) {
		t.Fatal("no Back row")
	}
	cases := []struct {
		keys   []string
		result menuResult
	}{
		{[]string{"esc"}, menuBack},
		{[]string{"down", "enter"}, menuBack}, // the Back row
		{[]string{"up", "enter"}, menuBack},   // wraps to the Back row
		{[]string{"enter"}, menuChosen},
		{[]string{"down", "down", "enter"}, menuChosen},
		{[]string{"q"}, menuQuit},
		{[]string{"ctrl+c"}, menuQuit},
	}
	for _, c := range cases {
		var m tea.Model = menu
		var cmd tea.Cmd
		for _, k := range c.keys {
			m, cmd = m.Update(key(k))
		}
		sm := m.(stepMenuModel)
		if sm.result != c.result || !isQuit(cmd) {
			t.Errorf("%v: result %d quit=%v", c.keys, sm.result, isQuit(cmd))
		}
		if c.result == menuChosen && sm.chosen != 0 {
			t.Errorf("%v: chosen %d", c.keys, sm.chosen)
		}
	}
	// Moving alone does not finish the menu.
	if m, cmd := menu.Update(key("down")); cmd != nil || m.(stepMenuModel).result != menuOpen {
		t.Fatal("down should only move")
	}
}
