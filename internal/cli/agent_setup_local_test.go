package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"github.com/meetstream-ai/cli/internal/agentkit/bridge"
	kitconfig "github.com/meetstream-ai/cli/internal/agentkit/config"
	agentlogin "github.com/meetstream-ai/cli/internal/agentkit/login"
	"github.com/meetstream-ai/cli/internal/agentkit/meetingctx"
	"github.com/meetstream-ai/cli/internal/agentkit/relay"
	"github.com/meetstream-ai/cli/internal/agentkit/warmcheck"
	"github.com/meetstream-ai/cli/internal/testutil/fakecli"
)

// localWizard is a wizard on the local-agent path with spec chosen.
func localWizard(t *testing.T, name string) *setupWizard {
	t.Helper()
	t.Setenv("MEETSTREAM_AGENT_KIT_HOME", t.TempDir())
	t.Setenv("MEETSTREAM_AGENT_KIT_WORKSPACE", filepath.Join(t.TempDir(), "MeetStream Agent"))
	w := testWizard()
	w.mode, w.interactive, w.maxAccess, w.useMeetingMD = modeLocal, true, "readonly", true
	w.spec = agents.All[name]
	w.chosen = &agentChoice{name: name, label: w.spec.Label, binary: "/fake/" + name, installed: true}
	return w
}

// menuView is a step menu's drawn rows, without colour.
func menuView(m tea.Model) string { return stripANSI(m.View()) }

// script answers each menu with keys, checking its question (and, when set, a
// part of its view) first.
type scripted struct {
	question, sees string
	keys           []string
}

func script(t *testing.T, steps ...scripted) func(tea.Model) (tea.Model, error) {
	i := 0
	t.Cleanup(func() {
		if i != len(steps) {
			t.Errorf("only %d of %d menus asked", i, len(steps))
		}
	})
	return func(m tea.Model) (tea.Model, error) {
		if i >= len(steps) {
			t.Fatalf("unexpected menu %q:\n%s", questionOf(m), menuView(m))
		}
		s := steps[i]
		i++
		if q := questionOf(m); q != s.question {
			t.Fatalf("menu %d: asked %q, want %q", i, q, s.question)
		}
		if s.sees != "" && !strings.Contains(menuView(m), s.sees) {
			t.Fatalf("%q does not show %q:\n%s", s.question, s.sees, menuView(m))
		}
		return press(m, s.keys...)
	}
}

// ── 1. the sign-in gate ───────────────────────────────────────────────────────

func TestSignedOutGateMenu(t *testing.T) {
	out := captureUI(t)
	w := localWizard(t, "claude")
	states := []string{"expired", "expired", "ok"}
	swap(t, &setupLoginCheck, func(agents.Spec, string) agentlogin.Status {
		s := states[0]
		states = states[1:]
		return agentlogin.Status{State: s}
	})
	var ran [][]string
	swap(t, &setupSignIn, func(argv []string) int { ran = append(ran, argv); return 0 })
	// Sign in now runs `claude auth login`; Check again checks once more.
	useDriver(t, script(t,
		scripted{"Sign in to Claude Code?", "Sign in now (`claude auth login`)\n    Check again\n    ← Back\n    Quit", []string{"enter"}},
		scripted{"Sign in to Claude Code?", "", []string{"down", "enter"}},
	))
	if res, err := w.gateSignIn(); res != menuChosen || err != nil {
		t.Fatalf("%v %v", res, err)
	}
	// The CLI setup found runs (it may be off PATH), not its bare name.
	if len(ran) != 1 || strings.Join(ran[0], " ") != "/fake/claude auth login" {
		t.Fatalf("sign-in ran %v", ran)
	}
	for _, want := range []string{"✗ Claude Code is signed out on this computer", "Running claude auth login.", "✓ Claude Code is signed in"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}

	// Back picks the agent again; Quit stops with exit 2 and keeps the answers.
	swap(t, &setupLoginCheck, func(agents.Spec, string) agentlogin.Status { return agentlogin.Status{State: "expired"} })
	useDriver(t, script(t, scripted{"Sign in to Claude Code?", "", []string{"esc"}}))
	if res, err := w.gateSignIn(); res != menuBack || err != nil {
		t.Fatalf("back: %v %v", res, err)
	}
	useDriver(t, script(t, scripted{"Sign in to Claude Code?", "", []string{"down", "down", "down", "enter"}}))
	res, err := w.gateSignIn()
	var ee *ExitError
	if res != menuQuit || !errors.As(err, &ee) || ee.Code != 2 || ee.Message != "Claude Code is signed out on this computer" {
		t.Fatalf("quit: %v %v", res, err)
	}
	if s, err := kitconfig.LoadSetup(""); err != nil || s.Agent != "claude" {
		t.Fatalf("answers not saved on Quit: %+v %v", s, err)
	}
}

func TestSignedOutStopsNonInteractiveSetup(t *testing.T) {
	relayCreds(t)
	t.Setenv("PATH", fakeBin(t, "claude"))
	swap(t, &setupLoginCheck, func(agents.Spec, string) agentlogin.Status { return agentlogin.Status{State: "expired"} })
	ee := setupExit(t, nil, "setup", "--agent", "claude", "--no-input", "--dry-run")
	if ee == nil || ee.Code != 2 || ee.Message != "Claude Code is signed out on this computer" || !strings.Contains(ee.Hint, "claude auth login") {
		t.Fatalf("%+v", ee)
	}
}

// setupExit runs `meetstream <args>` keeping MEETSTREAM_AGENT_KIT_HOME (the relay
// sign-in and setup.json) and returns the ExitError, nil on success.
func setupExit(t *testing.T, env map[string]string, args ...string) *ExitError {
	t.Helper()
	t.Setenv("MEETSTREAM_CONFIG_DIR", t.TempDir())
	t.Setenv("MEETSTREAM_API_KEY", "")
	isolateAgentLookup(t)
	for k, v := range env {
		t.Setenv(k, v)
	}
	g = globals{}
	root := NewRoot(BuildInfo{Version: "1.2.3"})
	root.SetArgs(args)
	root.SetOut(io.Discard)
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

// ── 2. the test turn ──────────────────────────────────────────────────────────

func TestTestTurnSuccess(t *testing.T) {
	out := captureUI(t)
	w := localWizard(t, "claude")
	w.workdir, w.owner = t.TempDir(), "Nav"
	var seen kitconfig.Config
	swap(t, &setupTestTurn, func(_ context.Context, c kitconfig.Config, onStage func(string, time.Duration)) testTurn {
		seen = c
		onStage("output", 400*time.Millisecond)
		onStage("first_delta", 1500*time.Millisecond)
		return testTurn{Answer: "ready", Total: 2100 * time.Millisecond}
	})
	useDriver(t, script(t))
	asked, res, err := w.stepTest()
	if asked || res != menuChosen || err != nil || w.testResult != "passed" {
		t.Fatalf("%v %v %v %q", asked, res, err, w.testResult)
	}
	if seen.Owner != "Nav" || seen.Workdir != w.workdir || seen.Agent.Name != "claude" {
		t.Fatalf("config %+v", seen)
	}
	for _, want := range []string{"Woke Claude Code", "0.4 s", "First words", "1.5 s", "Answered", "2.1 s", "Claude Code answered in 2.1 s: ready"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	// Unchanged answers: not run again (Back and forward).
	swap(t, &setupTestTurn, func(context.Context, kitconfig.Config, func(string, time.Duration)) testTurn {
		t.Fatal("tested again")
		return testTurn{}
	})
	if _, _, err := w.stepTest(); err != nil {
		t.Fatal(err)
	}
}

func TestTestTurnFailureMenu(t *testing.T) {
	out := captureUI(t)
	w := localWizard(t, "claude")
	w.workdir = t.TempDir()
	turns := []testTurn{
		{Error: "exited with code 1", StderrTail: "line 1\nline 2\nline 3\nline 4\nPlease log in", Total: 800 * time.Millisecond},
		{Answer: "ready", Total: time.Second},
	}
	swap(t, &setupTestTurn, func(_ context.Context, _ kitconfig.Config, onStage func(string, time.Duration)) testTurn {
		r := turns[0]
		turns = turns[1:]
		return r
	})
	var ran [][]string
	swap(t, &setupSignIn, func(argv []string) int { ran = append(ran, argv); return 0 })
	// Signed out: the default is "Sign in now"; then the test runs again and passes.
	useDriver(t, script(t, scripted{"What now?",
		"❯ Sign in now (claude auth login), then test again\n    Test again\n    Start anyway (MIA cannot use your agent until it answers)\n    ← Back\n    Quit",
		[]string{"enter"}}))
	asked, res, err := w.stepTest()
	if !asked || res != menuChosen || err != nil || w.testResult != "passed" || len(ran) != 1 || ran[0][0] != w.chosen.binary {
		t.Fatalf("%v %v %v %q %v", asked, res, err, w.testResult, ran)
	}
	for _, want := range []string{"✗ Waking up Claude Code", "(failed)", "✗ Claude Code is signed out", "│ Please log in", "Fix: Sign in with: claude auth login",
		"Running claude auth login. Finish signing in there; this test runs again after.", "Claude Code answered in 1.0 s: ready"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out.String(), "│ line 1") {
		t.Errorf("more than the last 4 lines:\n%s", out)
	}

	// Another failure: Test again, then Start anyway.
	w.tested = ""
	turns = []testTurn{{Error: "boom"}, {Error: "boom"}}
	useDriver(t, script(t,
		scripted{"What now?", "", []string{"down", "enter"}},
		scripted{"What now?", "", []string{"down", "down", "enter"}},
	))
	if _, res, err := w.stepTest(); res != menuChosen || err != nil || w.testResult != "started anyway" {
		t.Fatalf("anyway: %v %v %q", res, err, w.testResult)
	}

	// Quit saves the answers and stops; Back returns to the step before.
	turns = []testTurn{{Error: "boom"}, {Error: "boom"}}
	useDriver(t, script(t, scripted{"What now?", "", []string{"esc"}}))
	if _, res, _ := w.stepTest(); res != menuBack {
		t.Fatalf("back: %v", res)
	}
	useDriver(t, script(t, scripted{"What now?", "", []string{"up", "enter"}}))
	_, res, err = w.stepTest()
	var ee *ExitError
	if res != menuQuit || !errors.As(err, &ee) || ee.Code != 1 || !strings.Contains(ee.Hint, "Saved your answers") {
		t.Fatalf("quit: %v %v", res, err)
	}
	if s, err := kitconfig.LoadSetup(""); err != nil || s.Workdir != w.workdir {
		t.Fatalf("answers not saved: %+v %v", s, err)
	}
}

// ACP agents also get the warm check, with its own What now? menu.
func TestWarmCheckFailureMenu(t *testing.T) {
	out := captureUI(t)
	w := localWizard(t, "agy")
	w.workdir = t.TempDir()
	swap(t, &setupTestTurn, func(context.Context, kitconfig.Config, func(string, time.Duration)) testTurn {
		return testTurn{Answer: "ready"}
	})
	results := []warmcheck.Result{{Message: "the warm turn did not finish", Fix: "Run `agy acp` yourself to see its error."},
		{OK: true, Message: "Warm mode (ACP) ready · meeting tools work (test tool called)", Total: 3 * time.Second}}
	swap(t, &setupWarmCheck, func(context.Context, kitconfig.Config, warmcheck.Options) warmcheck.Result {
		r := results[0]
		results = results[1:]
		return r
	})
	useDriver(t, script(t, scripted{"What now?", "❯ Test again\n    Start anyway (meetings may fail or have no meeting tools)\n    ← Back\n    Quit", []string{"enter"}}))
	if _, res, err := w.stepTest(); res != menuChosen || err != nil || w.testResult != "passed" {
		t.Fatalf("%v %v %q", res, err, w.testResult)
	}
	for _, want := range []string{"✗ Warm mode (ACP): the warm turn did not finish", "Fix: Run `agy acp`", "✓ Warm mode (ACP) ready · meeting tools work (test tool called) (3.0 s)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}

// Without a terminal the test is skipped unless --test; --json reports it.
func TestTestTurnNonInteractive(t *testing.T) {
	relayCreds(t)
	t.Setenv("PATH", fakeBin(t, "claude"))
	swap(t, &setupLoginCheck, func(agents.Spec, string) agentlogin.Status { return agentlogin.Status{State: "ok"} })
	calls := 0
	swap(t, &setupTestTurn, func(context.Context, kitconfig.Config, func(string, time.Duration)) testTurn {
		calls++
		return testTurn{Answer: "ready"}
	})
	out, code := run(t, nil, "setup", "--agent", "claude", "--no-input", "--dry-run", "--json")
	if code != 0 || calls != 0 || !strings.Contains(out, `"test": "skipped"`) {
		t.Fatalf("code=%d calls=%d %s", code, calls, out)
	}
	out, code = run(t, nil, "setup", "--agent", "claude", "--no-input", "--dry-run", "--json", "--test")
	if code != 0 || calls != 1 || !strings.Contains(out, `"test": "passed"`) {
		t.Fatalf("code=%d calls=%d %s", code, calls, out)
	}
	swap(t, &setupTestTurn, func(context.Context, kitconfig.Config, func(string, time.Duration)) testTurn {
		return testTurn{Error: "x", StderrTail: "Please log in"}
	})
	ee := setupExit(t, nil, "setup", "--agent", "claude", "--no-input", "--dry-run", "--test")
	if ee == nil || ee.Code != 2 || ee.Message != "test turn: Claude Code is signed out" {
		t.Fatalf("%+v", ee)
	}
}

// ── 3. folder, access, name ───────────────────────────────────────────────────

func TestFolderAccessNameDefaults(t *testing.T) {
	out := captureUI(t)
	w := localWizard(t, "claude")
	t.Setenv("USER", "nav")
	// No setup.json: the meeting workspace (created), Meeting only, $USER.
	useDriver(t, script(t,
		scripted{"Folder your agent works in", "MeetStream Agent", []string{"enter"}},
		scripted{"Most access this computer allows", "❯ Meeting only  a meeting companion: reads the folder, changes nothing on this computer", []string{"enter"}},
		scripted{"Your name as it shows in meetings", "nav", []string{"enter"}},
	))
	for _, step := range []func() (bool, menuResult, error){w.stepFolder, w.stepAccess, w.stepName} {
		if _, res, err := step(); res != menuChosen || err != nil {
			t.Fatalf("%v %v", res, err)
		}
	}
	ws, _ := filepath.Abs(meetingctx.Workspace())
	if w.workdir != ws || w.maxAccess != "readonly" || w.owner != "nav" {
		t.Fatalf("%q %q %q", w.workdir, w.maxAccess, w.owner)
	}
	if _, err := os.Stat(filepath.Join(ws, "MEETING.md")); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"✓ Created", "✓ Meeting context: meeting only · ", "k tokens/turn", "MEETING.md", "(edit with: meetstream agent context edit)",
		"✓ Access: Meeting only", "✓ Your name: nav"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}

	// With setup.json: its folder, access and name are the defaults.
	project := t.TempDir()
	if err := (kitconfig.Setup{Agent: "claude", Workdir: project, MaxAccess: "confirm", Owner: "Priya", Tunnel: "relay"}).Save(""); err != nil {
		t.Fatal(err)
	}
	w2 := testWizard()
	w2.mode, w2.interactive, w2.maxAccess, w2.spec, w2.chosen = modeLocal, true, "readonly", w.spec, w.chosen
	w2.loadLast()
	useDriver(t, script(t,
		scripted{"Folder your agent works in", "", []string{"enter"}},
		scripted{"Most access this computer allows", "❯ Ask first", []string{"enter"}},
		scripted{"Your name as it shows in meetings", "Priya", []string{"enter"}},
	))
	for _, step := range []func() (bool, menuResult, error){w2.stepFolder, w2.stepAccess, w2.stepName} {
		if _, res, err := step(); res != menuChosen || err != nil {
			t.Fatalf("%v %v", res, err)
		}
	}
	if w2.workdir != project || w2.maxAccess != "confirm" || w2.owner != "Priya" {
		t.Fatalf("%q %q %q", w2.workdir, w2.maxAccess, w2.owner)
	}

	// A folder that does not exist is asked again; Full access is picked.
	w2.workdir = ""
	useDriver(t, script(t,
		scripted{"Folder your agent works in", "", append(strings.Split(filepath.Join(project, "nope"), ""), "enter")},
		scripted{"Folder your agent works in", "", append(strings.Split(project, ""), "enter")},
		scripted{"Most access this computer allows", "", []string{"down", "enter"}},
	))
	if _, res, err := w2.stepFolder(); res != menuChosen || err != nil || w2.workdir != project {
		t.Fatalf("%v %v %q", res, err, w2.workdir)
	}
	if !strings.Contains(out.String(), "nope does not exist") {
		t.Errorf("no folder error in\n%s", out)
	}
	if _, _, _ = w2.stepAccess(); w2.maxAccess != "full" {
		t.Fatal(w2.maxAccess)
	}
}

// A folder with project rules: Meeting only skips them where the CLI can
// (Claude Code), else uses the meeting workspace (Antigravity).
func TestProjectRulesStep(t *testing.T) {
	out := captureUI(t)
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "CLAUDE.md"), bytes.Repeat([]byte("x"), 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	w := localWizard(t, "claude")
	w.workdir = project
	useDriver(t, script(t,
		scripted{"Folder your agent works in", "", []string{"enter"}},
		scripted{"Meeting context", "Meeting only                  skips the folder's CLAUDE.md", []string{"enter"}},
	))
	if _, res, err := w.stepFolder(); res != menuChosen || err != nil || w.workdir != project || !w.ignoreRules {
		t.Fatalf("%v %v %q %v", res, err, w.workdir, w.ignoreRules)
	}
	for _, want := range []string{"This folder has project rules your agent reads on every meeting turn (≈ 1024 tokens):", "CLAUDE.md  4.0 KB ≈ 1024 tokens",
		"Meeting context: project, rules ignored"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	// Include this project's rules.
	useDriver(t, script(t, scripted{"Folder your agent works in", "", []string{"enter"}}, scripted{"Meeting context", "", []string{"down", "enter"}}))
	if _, _, _ = w.stepFolder(); w.ignoreRules || w.workdir != project {
		t.Fatal("include")
	}
	// Antigravity cannot skip them: Meeting only is the meeting workspace.
	a := localWizard(t, "agy")
	a.workdir = project
	useDriver(t, script(t, scripted{"Folder your agent works in", "", []string{"enter"}}, scripted{"Meeting context", "use ", []string{"enter"}}))
	ws, _ := filepath.Abs(meetingctx.Workspace())
	if _, _, _ = a.stepFolder(); a.workdir != ws || a.ignoreRules {
		t.Fatalf("%q", a.workdir)
	}
	// Custom: keep the rules, leave MEETING.md out; Back from the rules menu asks the folder again.
	useDriver(t, script(t,
		scripted{"Folder your agent works in", "", []string{"enter"}},
		scripted{"Meeting context", "", []string{"esc"}},
		scripted{"Folder your agent works in", "", []string{"enter"}},
		scripted{"Meeting context", "", []string{"down", "down", "enter"}},
		scripted{"What goes into meetings?", "", []string{"down", " ", "enter"}},
	))
	w.workdir = project
	if _, _, _ = w.stepFolder(); w.useMeetingMD || w.ignoreRules {
		t.Fatalf("custom: md=%v ignore=%v", w.useMeetingMD, w.ignoreRules)
	}
}

// ── 4. running setup again ────────────────────────────────────────────────────

// miaServer counts the MIA agent requests: POST creates cfg_1, GET and PUT update.
type miaServer struct {
	mu      sync.Mutex
	calls   []string
	put     map[string]any
	post    func(w http.ResponseWriter)
	catalog string // GET /mia's mia_managed; "" is demoCatalog
	get     string // GET /mia?agent_config_id's answer; "" is a pipeline agent with a voice
}

func (s *miaServer) start(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !strings.HasSuffix(r.URL.Path, "/mia") {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodGet && r.URL.Query().Get("agent_config_id") == "" {
			_, _ = w.Write([]byte(`{"agent_configs":[],"mia_managed":` + orDefault(s.catalog, demoCatalog) + `}`))
			return
		}
		s.calls = append(s.calls, r.Method+" "+r.URL.Query().Get("agent_config_id"))
		switch r.Method {
		case http.MethodPost:
			if s.post != nil {
				s.post(w)
				return
			}
			_, _ = w.Write([]byte(`{"agent_config_id":"cfg_1"}`))
		case http.MethodGet:
			_, _ = w.Write([]byte(orDefault(s.get, `{"agent_config":{"agent_name":"Claude Code in the meeting","voice":{"provider":"elevenlabs"}}}`)))
		case http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &s.put)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestRerunUpdatesSavedAgent(t *testing.T) {
	home := relayCreds(t)
	t.Setenv("PATH", fakeBin(t, "claude"))
	swap(t, &setupLoginCheck, func(agents.Spec, string) agentlogin.Status { return agentlogin.Status{State: "ok"} })
	s := &miaServer{}
	env := map[string]string{"MEETSTREAM_API_URL": s.start(t), "MEETSTREAM_API_KEY": "ms_test_key"}
	for i := 0; i < 3; i++ {
		if _, code := run(t, env, "setup", "--agent", "claude", "--no-input", "--no-start", "--json"); code != 0 {
			t.Fatalf("run %d: exit %d", i, code)
		}
	}
	want := []string{"POST ", "GET cfg_1", "PUT ", "GET cfg_1", "PUT "}
	if strings.Join(s.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls %v, want %v", s.calls, want)
	}
	if s.put["agent_config_id"] != "cfg_1" || s.put["model"].(map[string]any)["provider"] != "local_agent" || s.put["voice"] == nil {
		t.Fatalf("put %+v", s.put)
	}
	saved, err := kitconfig.LoadSetup(home)
	if err != nil || saved.MIAAgentID != "cfg_1" {
		t.Fatalf("%+v %v", saved, err)
	}
	// --new-agent creates another, and the next run updates that one.
	s.calls = nil
	if _, code := run(t, env, "setup", "--agent", "claude", "--no-input", "--no-start", "--new-agent", "--json"); code != 0 {
		t.Fatal(code)
	}
	if strings.Join(s.calls, ",") != "POST " {
		t.Fatalf("calls %v", s.calls)
	}
}

// Interactive re-run: the Last setup box, Start with it? Yes goes straight to
// the test, then updates the saved MIA agent.
func TestRerunStartWithLast(t *testing.T) {
	home := relayCreds(t)
	t.Setenv("PATH", fakeBin(t, "claude"))
	setupFakes(t, twoModels())
	swap(t, &setupLoginCheck, func(agents.Spec, string) agentlogin.Status { return agentlogin.Status{State: "ok"} })
	swap(t, &setupInteractive, func() bool { return true })
	project := t.TempDir()
	if err := (kitconfig.Setup{Agent: "claude", Workdir: project, MaxAccess: "full", Owner: "Priya", Model: "m2", Tunnel: "relay", MIAAgentID: "cfg_1", AutoStart: "yes"}).Save(home); err != nil {
		t.Fatal(err)
	}
	s := &miaServer{}
	env := map[string]string{"MEETSTREAM_API_URL": s.start(t), "MEETSTREAM_API_KEY": "ms_test_key"}
	useDriver(t, script(t,
		scripted{"Start with it?", "❯ Yes", []string{"enter"}},
		scripted{"Connect this computer to MeetStream?", "", []string{"enter"}},
		scripted{"Update your MIA agent for Claude Code?", "❯ Update MIA agent cfg_1\n    Create a new MIA agent", []string{"enter"}},
	))
	var code int
	out := withStderr(t, func() { _, code = run(t, env, "setup", "--no-start", "--no-calendar") })
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, want := range []string{"Last setup", "Priya", "Access      Full", "MIA agent   cfg_1", "7/11  Test", "Claude Code answered in", "Updating MIA agent cfg_1",
		"FULL access: the agent may run shell commands"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Join(s.calls, ",") != "GET cfg_1,PUT " {
		t.Fatalf("calls %v", s.calls)
	}
	saved, _ := kitconfig.LoadSetup(home)
	if saved.MIAAgentID != "cfg_1" || saved.AutoStart != "yes" || saved.Model != "m2" || saved.Owner != "Priya" {
		t.Fatalf("%+v", saved)
	}
}

// ── 5. a server without the relay ─────────────────────────────────────────────

func TestRelayUnsupportedServer(t *testing.T) {
	relayCreds(t)
	t.Setenv("PATH", fakeBin(t, "claude"))
	swap(t, &setupLoginCheck, func(agents.Spec, string) agentlogin.Status { return agentlogin.Status{State: "ok"} })
	s := &miaServer{post: func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"model.local_agent.url must be an https URL (your bridge tunnel) without credentials."}`))
	}}
	env := map[string]string{"MEETSTREAM_API_URL": s.start(t), "MEETSTREAM_API_KEY": "ms_test_key"}
	ee := setupExit(t, env, "setup", "--agent", "claude", "--no-input", "--no-start")
	if ee == nil || ee.Code != 3 || ee.Message != "This MeetStream server doesn't accept agents on your computer yet (relay not enabled here)." ||
		ee.Hint != "Your setup is saved; run meetstream setup again once it's enabled." {
		t.Fatalf("%+v", ee)
	}
	if strings.Contains(ee.Message+ee.Hint, "https URL") {
		t.Fatal("raw API text shown")
	}
	if saved, err := kitconfig.LoadSetup(""); err != nil || saved.Agent != "claude" {
		t.Fatalf("setup not saved: %+v %v", saved, err)
	}

	// The relay sign-in not offered: the same message.
	t.Setenv("MEETSTREAM_AGENT_KIT_HOME", t.TempDir()) // no relay sign-in yet
	w := localWizard(t, "claude")
	swap(t, &setupRelayLogin, func(context.Context, relay.LoginOptions) (*relay.Tokens, error) {
		return nil, fmt.Errorf("discovery: %w", relay.ErrNotOffered)
	})
	captureUI(t)
	useDriver(t, script(t, scripted{"Connect this computer to MeetStream?", "", []string{"enter"}}))
	_, _, err := w.stepConnect()
	if !errors.As(err, &ee) || ee.Code != 3 || !strings.Contains(ee.Message, "relay not enabled here") {
		t.Fatalf("%v", err)
	}
}

// ── 6. serve's log lines ──────────────────────────────────────────────────────

// A bridge with a stand-in agent (echo, one run per turn): a session, the
// activation code claimed and released, and a turn print the kit's lines.
func TestServeLogLinesFromBridge(t *testing.T) {
	echo := fakecli.Echo(t, t.TempDir(), "echo")
	spec, err := agents.Custom("'" + echo + "' ready")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var lines, events []string
	code := ""
	c := kitconfig.Config{Agent: spec, Binary: echo, Workdir: t.TempDir(), RuntimeDir: t.TempDir(),
		Log:    func(l string) { mu.Lock(); lines = append(lines, l); mu.Unlock() },
		Event:  func(kind string, f map[string]any) { mu.Lock(); events = append(events, kind); mu.Unlock() },
		Notify: func(title, text string) { code = regexp.MustCompile(`^\d{6}`).FindString(text) },
	}
	srv := bridge.New(c)
	srv.SetInternalToken("tok")
	post := func(path, body string) string {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer tok")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		return rec.Body.String()
	}
	defer srv.Close()
	post("/v1/session/start", `{"session_id":"s1","claim_required":true,"tools":[{"name":"lookup","description":"x"}]}`)
	post("/v1/claim", `{"session_id":"s1","code":"`+code+`","participant_id":"p1","name":"Nav"}`)
	post("/v1/turn", `{"session_id":"s1","messages":[{"speaker":"Sam","text":"Agent, are we on track?"}]}`)
	post("/v1/release", `{"session_id":"s1","participant_ids":["p1"]}`)
	mu.Lock()
	got := strings.Join(lines, "\n")
	mu.Unlock()
	for _, want := range []string{
		"[session] s1: started (" + echo + ", one run per turn, 1 meeting tools, access readonly",
		"[activate] s1: activated for Nav",
		"[turn] Sam: Agent, are we on track?",
		"[done] first output ",
		", total ",
		"[activate] s1: Nav left: locked",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	// Events stay metadata: no names, no meeting text.
	if strings.Contains(strings.Join(events, ","), "Nav") || !contains(events, "session_started") || !contains(events, "activation") || !contains(events, "turn_done") {
		t.Errorf("events %v", events)
	}

	// The terminal's lines for the connection, and the kit's error, tool and timeout lines.
	for kind, want := range map[string]string{"relay_connected": "✓ Relay connected", "relay_disconnected": "! Relay reconnecting: closed (1006)",
		"relay_stopped": "✗ Relay stopped: closed (1006)", "ready": "● Running."} {
		if line, ok := serveEventLine(kind, map[string]any{"detail": "closed (1006)"}, false); !ok || !strings.Contains(stripANSI(line), want) {
			t.Errorf("%s: %q", kind, line)
		}
	}
	for _, kind := range []string{"session_started", "turn_done", "turn_error", "tool_call", "activation", "protocol"} {
		if line, ok := serveEventLine(kind, map[string]any{}, false); ok {
			t.Errorf("%s printed %q", kind, line)
		}
	}
}

// Lines wait for the header box.
func TestServeOutputWaitsForHeader(t *testing.T) {
	buf := captureUI(t)
	o := newServeOutput()
	done := make(chan struct{})
	go func() { o.say("[session] s1: started"); close(done) }()
	select {
	case <-done:
		t.Fatal("printed before the header")
	case <-time.After(50 * time.Millisecond):
	}
	fmt.Fprintln(buf, "HEADER")
	o.headerShown()
	<-done
	if !strings.HasPrefix(buf.String(), "HEADER\n[session]") {
		t.Fatal(buf.String())
	}
}
