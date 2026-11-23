package cli

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/spf13/cobra"

	"github.com/meetstream-ai/cli/internal/agentkit/checks"
	kitconfig "github.com/meetstream-ai/cli/internal/agentkit/config"
	"github.com/meetstream-ai/cli/internal/agentkit/warmcheck"
)

// collect runs cmd and returns the messages it produces, skipping animation
// ticks; quit reports a tea.Quit.
func collect(t *testing.T, cmd tea.Cmd) (msgs []tea.Msg, quit bool) {
	t.Helper()
	if cmd == nil {
		return nil, false
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-ch:
	case <-time.After(5 * time.Second):
		return nil, false // a long-running wait (e.g. the agent's next line)
	}
	switch x := msg.(type) {
	case tea.BatchMsg:
		for _, c := range x {
			m, q := collect(t, c)
			msgs, quit = append(msgs, m...), quit || q
		}
	case tea.QuitMsg:
		quit = true
	case tickMsg, spinMsg, nil:
	default:
		msgs = append(msgs, msg)
	}
	return msgs, quit
}

// press sends keys and settles every command they start.
func homePress(t *testing.T, m homeModel, keys ...string) homeModel {
	t.Helper()
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		m = settle(t, m, msg)
	}
	return m
}

func settle(t *testing.T, m homeModel, msg tea.Msg) homeModel {
	t.Helper()
	queue := []tea.Msg{msg}
	for len(queue) > 0 {
		next, cmd := m.Update(queue[0])
		queue = queue[1:]
		m = next.(homeModel)
		msgs, quit := collect(t, cmd)
		if quit {
			t.Fatalf("the app quit after %T", msg)
		}
		queue = append(queue, msgs...)
	}
	return m
}

func homeFor(t *testing.T, env map[string]string) homeModel {
	t.Helper()
	t.Setenv("MEETSTREAM_CONFIG_DIR", t.TempDir())
	t.Setenv("MEETSTREAM_AGENT_KIT_HOME", t.TempDir())
	t.Setenv("MEETSTREAM_API_KEY", "")
	isolateAgentLookup(t)
	for k, v := range env {
		t.Setenv(k, v)
	}
	g = globals{}
	m := newHomeModel(&homeEnv{version: "1.2.3"})
	m.width, m.height = 120, 40
	return m
}

func moveTo(t *testing.T, m homeModel, title string) homeModel {
	t.Helper()
	for i, it := range m.items {
		if it.title == title {
			m.cursor = i
			return m
		}
	}
	t.Fatalf("no menu item %q", title)
	return m
}

func TestHomeDoctorRunsInsideAndEscReturns(t *testing.T) {
	old := doctorWarm
	doctorWarm = func(context.Context, kitconfig.Config) warmcheck.Result { return warmcheck.Result{} }
	t.Cleanup(func() { doctorWarm = old })
	m := homeFor(t, nil)
	if p := os.Getenv("MEETSTREAM_DUMP_APP_DOCTOR"); p != "" {
		lipgloss.SetColorProfile(termenv.TrueColor)
		t.Setenv("MEETSTREAM_API_KEY", "ms_demo_key_a1b2")
		oldAgent := homeDoctorAgent
		homeDoctorAgent = func(*cobra.Command) []checks.Check { // demo machine: no real paths or sign-ins
			return []checks.Check{
				{Name: "config", Status: checks.OK, Detail: "~/.config/meetstream-agent-kit/setup.json"},
				{Name: "workdir", Status: checks.OK, Detail: "~/meetings"},
				{Name: "agent", Status: checks.OK, Detail: "Claude Code: ~/.local/bin/claude"},
				{Name: "login", Status: checks.OK, Detail: "Claude Code signed in"},
				{Name: "relay", Status: checks.OK, Detail: "Demo Mac connected to MeetStream"},
				{Name: "context", Status: checks.Warn, Detail: "no MEETING.md yet", Hint: "meetstream agent context edit"},
			}
		}
		defer func() { homeDoctorAgent = oldAgent }()
		d := homeFor(t, map[string]string{"MEETSTREAM_API_KEY": "ms_demo_key_a1b2"})
		_ = kitconfig.Setup{Agent: "claude", Workdir: "/tmp/meetings", Tunnel: "relay"}.Save("")
		d = moveTo(t, d, "Check my setup")
		d = homePress(t, d, "enter")
		if err := os.WriteFile(p, []byte(d.View()), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("MEETSTREAM_API_KEY", "")
	}
	m = moveTo(t, m, "Check my setup")
	m = homePress(t, m, "enter")
	if m.focus != focusPane || m.loading != "" {
		t.Fatalf("doctor did not finish in the pane: focus=%v loading=%q", m.focus, m.loading)
	}
	for _, want := range []string{"API key", "not set", "meetstream login", "problem(s) to fix"} {
		if !strings.Contains(m.View(), want) {
			t.Errorf("doctor pane missing %q:\n%s", want, m.View())
		}
	}
	m = homePress(t, m, "esc")
	if m.focus != focusMenu || m.items[m.cursor].title != "Check my setup" {
		t.Fatal("esc did not return to the menu")
	}
	// Run it again: still inside, still not quitting.
	m = homePress(t, m, "enter", "esc", "down", "enter", "esc")
	if m.focus != focusMenu {
		t.Fatal("not back at the menu")
	}
}

func TestHomeBotsTableAndTranscript(t *testing.T) {
	pinViews(t)
	env, _ := viewServer(t, map[string]string{
		"GET /bots?limit=10": botsPageJSON(demoBots(10, 0), demoCursor, 7334),
		"GET /bots?cursor=" + strings.ReplaceAll(demoCursor, "=", "%3D") + "&limit=10": botsPageJSON(demoBots(10, 10), "", 7334),
		"GET /bots/a1b2c301-1111-4222-8333-000000000001/detail":                        `{"transcript_id":"t1"}`,
		"GET /transcript/t1/get_transcript?raw=false":                                  demoTranscript,
	})
	m := homeFor(t, env)
	m = moveTo(t, m, "My recent bots")
	m = homePress(t, m, "enter")
	if !strings.Contains(m.pane, "Weekly product") || !strings.Contains(m.pane, "of 7,334") || strings.Contains(m.pane, demoCursor) {
		t.Fatalf("bots pane:\n%s", m.pane)
	}
	m = homePress(t, m, "down", "enter")
	if !strings.Contains(m.pane, "Sam Chen") || !m.bots.reading {
		t.Fatalf("transcript pane:\n%s", m.pane)
	}
	m = homePress(t, m, "esc")
	if m.bots.reading || m.focus != focusPane {
		t.Fatal("esc from a transcript goes back to the bots")
	}
	m = homePress(t, m, "n")
	if !strings.Contains(m.pane, "Bots 11–20") {
		t.Fatalf("next page:\n%s", m.pane)
	}
	m = homePress(t, m, "esc")
	if m.focus != focusMenu {
		t.Fatal("not back at the menu")
	}
}

func TestHomeSendBot(t *testing.T) {
	env, seen := viewServer(t, map[string]string{"POST /bots/create_bot": `{"bot_id":"b-new-1234","message":"Bot created"}`})
	m := homeFor(t, env)
	m = moveTo(t, m, "Send a bot to a meeting")
	m = homePress(t, m, "enter")
	if m.focus != focusForm {
		t.Fatal("no form")
	}
	m = homePress(t, m, "enter") // empty link
	if !strings.Contains(m.pane, "Paste a meeting link") {
		t.Fatalf("empty link accepted:\n%s", m.pane)
	}
	m = homePress(t, m, "https://meet.google.com/abc-defg-hij", "enter", "enter")
	if !strings.Contains(m.pane, "Bot sent") || !strings.Contains(m.pane, "meetstream bots status b-new-1234 --watch") || len(*seen) != 1 {
		t.Fatalf("send: %v\n%s", *seen, m.pane)
	}
}

func TestHomeExecFlowComesBack(t *testing.T) {
	var ran []string
	old := homeExec
	homeExec = func(title string, args []string) tea.Cmd {
		ran = args
		return func() tea.Msg { return execDoneMsg{title: title} }
	}
	t.Cleanup(func() { homeExec = old })
	m := homeFor(t, nil)
	m = moveTo(t, m, "Sign in")
	m = homePress(t, m, "enter")
	if strings.Join(ran, " ") != "login" || !strings.Contains(m.pane, "Done.") {
		t.Fatalf("ran %v, pane %q", ran, m.pane)
	}
	m = homePress(t, m, "esc")
	if m.focus != focusMenu {
		t.Fatal("not back at the menu")
	}
}

// TestHelperFakeBridge is not a test: run as a child with
// MEETSTREAM_TEST_FAKE_BRIDGE=<marker>, it is TestHomeAgentToggle's bridge. It
// leaves the marker, says it connected, and runs until stopped.
func TestHelperFakeBridge(t *testing.T) {
	marker := os.Getenv("MEETSTREAM_TEST_FAKE_BRIDGE")
	if marker == "" {
		return
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt) // Ctrl-C, or Ctrl-Break on Windows: what the home app sends
	_ = os.WriteFile(marker, nil, 0o644)
	os.Stdout.WriteString("connected to MeetStream\n")
	select {
	case <-stop:
	case <-time.After(30 * time.Second):
	}
	os.Exit(0)
}

func TestHomeAgentToggle(t *testing.T) {
	m := homeFor(t, nil)
	if err := (kitconfig.Setup{Agent: "claude", Workdir: t.TempDir(), Tunnel: "relay"}).Save(""); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "ran")
	old := homeAgentCommand
	homeAgentCommand = func() *exec.Cmd { // this test binary plays the bridge: no shell, so Windows runs it too
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperFakeBridge$")
		cmd.Env = append(os.Environ(), "MEETSTREAM_TEST_FAKE_BRIDGE="+marker)
		return cmd
	}
	t.Cleanup(func() { homeAgentCommand = old })
	m = moveTo(t, m, "Run my agent for meetings")
	m = homePress(t, m, "enter") // open the tile
	if !strings.Contains(m.pane, "Claude Code") || !strings.Contains(m.pane, "stopped") {
		t.Fatalf("agent pane:\n%s", m.pane)
	}
	m = homePress(t, m, "enter") // start
	if m.agent == nil {
		t.Fatalf("agent not started: %s", m.agentErr)
	}
	if !strings.Contains(m.pane, "connected to MeetStream") || !strings.Contains(m.header(), "running") {
		t.Fatalf("no activity:\n%s", m.pane)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("fake bridge did not run")
	}
	a := m.agent
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // stop
	m = next.(homeModel)
	select {
	case err := <-a.done:
		if err == nil {
			t.Log("bridge exited cleanly")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bridge did not stop")
	}
	next, _ = m.Update(agentExitMsg{})
	m = next.(homeModel)
	if m.agent != nil || !strings.Contains(m.pane, "stopped") {
		t.Fatalf("after stop:\n%s", m.pane)
	}
	m = homePress(t, m, "a")
	if s, _ := kitconfig.LoadSetup(""); s == nil || s.AutoStart != "yes" {
		t.Fatal("auto-start not saved")
	}
}

func TestHomeNarrowLayout(t *testing.T) {
	m := homeFor(t, nil)
	m.width, m.height = 60, 24
	v := m.View()
	for _, l := range strings.Split(v, "\n") {
		if w := len([]rune(stripANSI(l))); w > 60 {
			t.Errorf("narrow line too wide (%d): %q", w, stripANSI(l))
		}
	}
	if !strings.Contains(v, "Send a bot to a meeting") {
		t.Fatalf("narrow menu:\n%s", v)
	}
	m = moveTo(t, m, "Docs")
	m = homePress(t, m, "enter")
	v = m.View()
	if strings.Contains(v, "GET STARTED") || !strings.Contains(v, "Docs") {
		t.Fatalf("narrow: pane should replace the menu:\n%s", v)
	}
	for _, l := range strings.Split(v, "\n") {
		if w := len([]rune(stripANSI(l))); w > 60 {
			t.Errorf("narrow pane line too wide (%d): %q", w, stripANSI(l))
		}
	}
}
