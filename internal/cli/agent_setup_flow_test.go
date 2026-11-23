package cli

import (
	"context"
	"encoding/json"
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
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/spf13/cobra"

	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"github.com/meetstream-ai/cli/internal/agentkit/bench"
	kitconfig "github.com/meetstream-ai/cli/internal/agentkit/config"
	"github.com/meetstream-ai/cli/internal/agentkit/estimates"
	"github.com/meetstream-ai/cli/internal/agentkit/models"
	"github.com/meetstream-ai/cli/internal/agentkit/usage"
	"github.com/meetstream-ai/cli/internal/agentkit/warmcheck"
	"github.com/meetstream-ai/cli/internal/ui"
)

// press sends keys to a Bubble Tea model until it quits.
func press(m tea.Model, keys ...string) (tea.Model, error) {
	for _, k := range keys {
		var cmd tea.Cmd
		m, cmd = m.Update(key(k))
		if isQuit(cmd) {
			return m, nil
		}
	}
	return m, fmt.Errorf("%T did not finish after %v", m, keys)
}

func questionOf(m tea.Model) string {
	switch mm := m.(type) {
	case stepMenuModel:
		return mm.question
	case checklistModel:
		return mm.question
	case textPromptModel:
		return mm.question
	case pickModel:
		return agentQuestion
	}
	return ""
}

// backOnce is the kit's _PressBackOnce: it drives the real menus with key
// messages, pressing Esc the first time each question with a Back is asked and
// Enter (the default) after that. It draws every row's detail line on the way.
type backOnce struct {
	t       *testing.T
	backed  map[string]bool
	asked   []string
	calls   int
	details int
}

func (d *backOnce) run(m tea.Model) (tea.Model, error) {
	d.calls++
	if d.calls > 200 {
		d.t.Fatal("setup keeps asking")
	}
	if sm, ok := m.(stepMenuModel); ok {
		var walk tea.Model = sm
		for i := 0; i < sm.rows(); i++ { // every row, the Back row included, has its detail line
			want := sm.detail((sm.cursor + i) % sm.rows())
			if want == "" && len(sm.details) > 0 {
				d.t.Errorf("%q row %d has no detail line", sm.question, i)
			}
			if !strings.Contains(strings.Join(strings.Fields(stripANSI(walk.View())), " "), strings.Join(strings.Fields(want), " ")) {
				d.t.Errorf("%q: detail %q not drawn", sm.question, want)
			}
			d.details++
			walk, _ = walk.Update(key("down"))
		}
	}
	q := questionOf(m)
	d.asked = append(d.asked, q)
	if sm, ok := m.(stepMenuModel); ok && sm.noBack { // step 1: Esc there cancels setup
		return press(m, "enter")
	}
	if q != "" && !d.backed[q] {
		d.backed[q] = true
		return press(m, "esc")
	}
	return press(m, "enter")
}

func useDriver(t *testing.T, run func(tea.Model) (tea.Model, error)) {
	t.Helper()
	old := runTea
	runTea = run
	t.Cleanup(func() { runTea = old })
}

func swap[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

func catalogOf(c models.Catalog) func(agents.Spec, string) models.Catalog {
	return func(agents.Spec, string) models.Catalog { return c }
}

func twoModels(efforts ...string) models.Catalog {
	return models.Catalog{Models: []models.Choice{{ID: "m1"}, {ID: "m2"}}, DefaultModel: "m1", Efforts: efforts, Source: "test"}
}

// setupFakes: no CLI listing, no shared-estimates fetch, no warm agent, a
// test turn that answers, and no sign-in command run.
func setupFakes(t *testing.T, c models.Catalog) {
	t.Helper()
	swap(t, &setupCatalog, catalogOf(c))
	swap(t, &setupShared, func() []estimates.Entry { return nil })
	swap(t, &setupWarmCheck, func(context.Context, kitconfig.Config, warmcheck.Options) warmcheck.Result {
		return warmcheck.Result{OK: true, Message: "Warm mode (ACP) ready · meeting tools work (test tool called)"}
	})
	swap(t, &setupTestTurn, func(_ context.Context, _ kitconfig.Config, onStage func(string, time.Duration)) testTurn {
		onStage("output", 300*time.Millisecond)
		onStage("first_delta", 900*time.Millisecond)
		return testTurn{Answer: "ready", Total: 1200 * time.Millisecond}
	})
	swap(t, &setupSignIn, func(argv []string) int {
		t.Errorf("sign-in ran: %v", argv)
		return 1
	})
}

func relayCreds(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("MEETSTREAM_AGENT_KIT_HOME", home)
	t.Setenv("MEETSTREAM_AGENT_KIT_CREDENTIALS", "file")
	if err := os.WriteFile(home+"/credentials.json", []byte(`{"device_id":"dev_test1","refresh_token":"rt","auth_url":"http://a.invalid","relay_url":"wss://r.invalid/v1/connect"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

// The kit's test_every_step_goes_back_without_crashing: Back is pressed once in
// every step after the first (and in the effort question), every menu row draws
// its detail line, and setup still finishes: the MIA agent is created with the
// picked model, the calendar step runs, and a server without one-click calendar
// connect only leaves a note.
func TestEveryStepGoesBackWithoutCrashing(t *testing.T) {
	var mu sync.Mutex
	var created map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/mia") {
			_, _ = w.Write([]byte(`{"agent_configs":[],"mia_managed":` + demoCatalog + `}`))
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/mia") {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			_ = json.Unmarshal(b, &created)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"agent_config_id":"cfg_test"}`))
			return
		}
		http.NotFound(w, r) // no one-click calendar connect on this server
	}))
	defer srv.Close()
	t.Setenv("MEETSTREAM_API_URL", srv.URL)
	relayCreds(t)
	t.Setenv("PATH", fakeBin(t, "agy"))
	setupFakes(t, twoModels("low", "high"))
	swap(t, &setupInteractive, func() bool { return true })
	drv := &backOnce{t: t, backed: map[string]bool{}}
	useDriver(t, drv.run)
	var code int
	out := withStderr(t, func() {
		_, code = run(t, map[string]string{"MEETSTREAM_API_KEY": "ms_test_key"}, "setup", "--no-start")
	})
	if code != 0 {
		t.Fatalf("exit %d\n%s\nasked %v", code, out, drv.asked)
	}
	if !contains(drv.asked, modeQuestion) || strings.Count(strings.Join(drv.asked, "|"), agentQuestion) < 2 {
		t.Errorf("Back from the agent picker did not return to step 1: %v", drv.asked)
	}
	for _, q := range []string{agentQuestion, "Which model?", effortQuestion, "Folder your agent works in", "Most access this computer allows",
		"Your name as it shows in meetings", "Ears and voice for your agent?", "Connect this computer to MeetStream?",
		"Create the MIA agent for Antigravity CLI?", "Connect your calendar so MIA can join your meetings?"} {
		if !drv.backed[q] {
			t.Errorf("Back never pressed at %q (asked %v)", q, drv.asked)
		}
	}
	if drv.details == 0 {
		t.Fatal("no detail lines drawn")
	}
	mu.Lock()
	model, _ := created["model"].(map[string]any)
	mu.Unlock()
	voice, _ := created["voice"].(map[string]any)
	if model["model"] != "m1" || model["provider"] != "local_agent" || voice["provider"] != "elevenlabs" || voice["key_source"] != "meetstream" {
		t.Fatalf("created %+v", created)
	}
	for _, want := range []string{"1/11", "2/11", "7/11", "11/11", "2 models from test", "Model: m1", "isn't available on this server yet", "meetstream calendar connect",
		"Created ~/MeetStream Agent", "Meeting context: meeting only", "edit with: meetstream agent context edit", "Access: Meeting only",
		"Woke Antigravity CLI", "First words", "Answered", "Antigravity CLI answered in 1.2 s: ready", "Warm mode (ACP) ready · meeting tools work (test tool called) (",
		"warm session per meeting"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "Warm mode (ACP): Warm mode") {
		t.Errorf("warm text doubled:\n%s", out)
	}
	saved, err := kitconfig.LoadSetup("")
	if err != nil || saved.Agent != "agy" || saved.Model != "m1" || saved.Effort != "" || saved.MaxAccess != "readonly" ||
		!strings.HasSuffix(saved.Workdir, "MeetStream Agent") || saved.MIAAgentID != "cfg_test" {
		t.Fatalf("saved %+v %v", saved, err)
	}
	if _, err := os.Stat(filepath.Join(saved.Workdir, "MEETING.md")); err != nil {
		t.Fatal("MEETING.md not created:", err)
	}
}

// withStderr runs f with os.Stderr (where the CLI's human output goes) in a file.
func withStderr(t *testing.T, f func()) string {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = file
	defer func() { os.Stderr = old }()
	f()
	_ = file.Close()
	b, _ := os.ReadFile(file.Name())
	return string(b)
}

// keysFor drives each menu with its own keys, in order.
func keysFor(t *testing.T, script ...[]string) func(tea.Model) (tea.Model, error) {
	i := 0
	return func(m tea.Model) (tea.Model, error) {
		if i >= len(script) {
			t.Fatalf("unexpected menu %q", questionOf(m))
		}
		keys := script[i]
		i++
		return press(m, keys...)
	}
}

func testWizard() *setupWizard {
	c := &cobra.Command{}
	c.SetContext(context.Background())
	return &setupWizard{cmd: c}
}

// test_back_from_the_effort_question_returns_to_the_model_menu.
func TestBackFromEffortReturnsToModelMenu(t *testing.T) {
	captureUI(t)
	t.Setenv("MEETSTREAM_AGENT_KIT_HOME", t.TempDir())
	setupFakes(t, twoModels("low", "high"))
	useDriver(t, keysFor(t,
		[]string{"down", "enter"}, // m2
		[]string{"esc"},           // effort: back
		[]string{"up", "enter"},   // the menu again, on m2: up to m1
		[]string{"down", "enter"}, // low
	))
	pick, res, err := testWizard().pickModel(agents.All["agy"], "agy", "", "")
	if err != nil || res != menuChosen || pick != (modelPick{"m1", "low", true}) {
		t.Fatalf("%+v %v %v", pick, res, err)
	}
}

// "Type another model id": Esc returns to the menu; a typed id is kept.
func TestTypedModelIDAndEscInTextPrompt(t *testing.T) {
	captureUI(t)
	t.Setenv("MEETSTREAM_AGENT_KIT_HOME", t.TempDir())
	setupFakes(t, twoModels())
	useDriver(t, keysFor(t,
		[]string{"down", "down", "enter"}, // Type another model id
		[]string{"x", "esc"},              // typed, then Esc: back to the menu
		[]string{"down", "down", "enter"},
		[]string{"my-model", "enter"},
	))
	pick, res, err := testWizard().pickModel(agents.All["claude"], "claude", "", "")
	if err != nil || res != menuChosen || pick.model != "my-model" || pick.effort != "" {
		t.Fatalf("%+v %v %v", pick, res, err)
	}
}

// Copilot's "auto" alone is a line, not a menu; a CLI that lists just its
// default still gets the menu.
func TestAutoOnlyShortcutIsOnlyForAuto(t *testing.T) {
	out := captureUI(t)
	t.Setenv("MEETSTREAM_AGENT_KIT_HOME", t.TempDir())
	setupFakes(t, models.Catalog{Models: []models.Choice{{ID: "auto"}}, Source: "copilot acp"})
	useDriver(t, func(m tea.Model) (tea.Model, error) { t.Fatalf("menu shown: %q", questionOf(m)); return m, nil })
	pick, res, err := testWizard().pickModel(agents.All["copilot"], "copilot", "", "")
	if err != nil || res != menuChosen || pick.model != "auto" || pick.asked {
		t.Fatalf("%+v %v %v", pick, res, err)
	}
	if !strings.Contains(out.String(), "GitHub Copilot CLI picks the model itself (Auto)") {
		t.Fatal(out.String())
	}

	setupFakes(t, models.Catalog{Models: []models.Choice{{ID: "openai:gpt-x"}}, DefaultModel: "openai:gpt-x"})
	shown := 0
	useDriver(t, func(m tea.Model) (tea.Model, error) { shown++; return press(m, "enter") })
	if pick, _, _ := testWizard().pickModel(agents.All["hermes"], "hermes", "", ""); !pick.asked || shown == 0 || pick.model != "openai:gpt-x" {
		t.Fatalf("single default model should still get the menu: %+v", pick)
	}
	// The source line is never blank.
	if !strings.Contains(out.String(), "1 model from "+models.SettingsSource) {
		t.Fatal(out.String())
	}
}

// test_measure_live_is_not_offered_when_the_cli_cannot_report_usage, and the
// cost columns of the rows.
func TestModelMenuRows(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	menu, _, _ := modelMenu(agents.All["copilot"], twoModels(), "", nil)
	v := menu.View()
	if strings.Contains(v, measureLabel) || !strings.Contains(v, "usage n/a") || !strings.Contains(v, "plan") {
		t.Fatalf("copilot menu:\n%s", v)
	}
	cached := map[string]estimates.Estimate{"claude-sonnet-4-5": {Usage: estimatesUsage(12000, 300), Source: estimates.MeasuredHere}}
	listed := models.Catalog{Models: []models.Choice{{ID: "claude-sonnet-4-5"}, {ID: "haiku"}}, DefaultModel: "claude-sonnet-4-5"}
	menu, shown, measurable := modelMenu(agents.All["claude"], listed, "", cached)
	v = menu.View()
	for _, want := range []string{measureLabel, "/h", "≈$", "not measured", "—", typeModelLabel, cliDefaultLabel, "Which model?"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q in\n%s", want, v)
		}
	}
	if len(shown) != 2 || len(measurable) != 3 || len(menu.options) != 5 || menu.detail(len(menu.options)) != backDetail {
		t.Fatalf("rows %d %d %d", len(shown), len(measurable), len(menu.options))
	}
}

func estimatesUsage(in, out int64) usage.Usage { return usage.Usage{Input: in, Output: out} }

// Measure live: the ticked models run through the queue (a fake CLI here),
// each finished measurement is saved for the picker, the chart is printed and
// the model menu comes back.
func TestMeasureLiveRunsTickedModels(t *testing.T) {
	out := captureUI(t)
	t.Setenv("MEETSTREAM_AGENT_KIT_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	setupFakes(t, twoModels())
	swap(t, &benchExec, fakeClaudeExec(nil))
	swap(t, &runBenchLive, func(ctx context.Context, q *bench.Queue) ([]bench.Job, bool) { return q.Run(ctx), false })
	useDriver(t, keysFor(t,
		[]string{"down", "down", "down", "down", "enter"}, // Measure live
		[]string{"enter"}, // the ticked models
		[]string{"esc"},   // back from the model menu
	))
	_, res, err := testWizard().pickModel(agents.All["claude"], "claude", "", "")
	if err != nil || res != menuBack {
		t.Fatalf("%v %v", res, err)
	}
	saved := estimates.Load("claude")
	if _, ok := saved["m1"]; !ok {
		t.Fatalf("not saved: %v\n%s", saved, out)
	}
	if !strings.Contains(out.String(), "m2") || !strings.Contains(out.String(), "/h") {
		t.Fatal(out.String())
	}
}

// fakeClaudeExec answers like `claude -p --output-format stream-json`, with usage.
func fakeClaudeExec(seen *[]string) func(context.Context, []string, string, string, []string, func(string)) (string, error) {
	var mu sync.Mutex
	return func(_ context.Context, argv []string, dir, input string, _ []string, onLine func(string)) (string, error) {
		mu.Lock()
		if seen != nil {
			*seen = append(*seen, strings.Join(argv, " "))
		}
		mu.Unlock()
		if dir == "" || !strings.Contains(input, "on track to ship") {
			return "", fmt.Errorf("no temp folder or prompt")
		}
		onLine(`{"type":"result","result":"Yes, on track.","usage":{"input_tokens":12000,"output_tokens":40},"total_cost_usd":0.01}`)
		return "", nil
	}
}

// --no-input with --model/--effort: no menu, and the dry-run carries both.
func TestSetupNonInteractiveModelAndEffort(t *testing.T) {
	relayCreds(t)
	t.Setenv("PATH", fakeBin(t, "claude"))
	swap(t, &setupCatalog, func(agents.Spec, string) models.Catalog {
		t.Fatal("listed models without a terminal")
		return models.Catalog{}
	})
	out, code := run(t, nil, "setup", "--agent", "claude", "--model", "opus", "--effort", "high", "--dry-run", "--json", "--no-input")
	var m struct {
		Body struct {
			Model map[string]any `json:"model"`
		} `json:"body"`
		Setup map[string]any `json:"setup"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &m) != nil {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if m.Body.Model["model"] != "opus" || m.Setup["model"] != "opus" || m.Setup["effort"] != "high" || m.Body.Model["provider"] != "local_agent" {
		t.Fatalf("%s", out)
	}
	// Without flags: the CLI's default, nothing in the body.
	out, code = run(t, nil, "setup", "--agent", "claude", "--dry-run", "--json", "--no-input")
	if code != 0 || strings.Contains(out, `"model": "opus"`) || !strings.Contains(out, `"effort": ""`) {
		t.Fatalf("code=%d %s", code, out)
	}
	if _, code := run(t, nil, "setup", "--agent", "claude", "--effort", "HIGH!", "--dry-run", "--json", "--no-input"); code != 2 {
		t.Fatalf("bad effort: exit %d", code)
	}
}

// Gap 15: a CLI installed where PATH does not look yet is found, shown with its
// export line, and its path is what serve uses.
func TestDetectFindsAgentOffPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	swap(t, &systemInstallDirs, nil)
	t.Setenv("PATH", t.TempDir())
	local := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var claude agentChoice
	for _, c := range detectAgents() {
		if c.name == "claude" {
			claude = c
		}
	}
	if !claude.installed || !claude.offPath || claude.binary != filepath.Join(local, "claude") {
		t.Fatalf("%+v", claude)
	}
	if v := (pickModel{items: []agentChoice{claude}}).View(); !strings.Contains(v, "not on PATH") {
		t.Fatal(v)
	}
	out := captureUI(t)
	g = globals{}
	pathHint(claude)
	if !strings.Contains(out.String(), `export PATH="`+local+`:$PATH"`) {
		t.Fatal(out.String())
	}
	c, err := agentServeOptions{agent: "claude", workdir: t.TempDir(), maxAccess: "readonly", timeout: 60}.build()
	if err != nil || c.Binary != filepath.Join(local, "claude") {
		t.Fatalf("%v %v", c.Binary, err)
	}
}

// Muse's launcher without its app: the picker says so, and setup without a
// terminal stops with the download command.
func TestMuseLauncherWithoutAppStopsNonInteractiveSetup(t *testing.T) {
	relayCreds(t)
	bin := fakeBin(t, "muse") // the launcher alone: no .muse-version, no app beside it
	t.Setenv("PATH", bin)
	_, code := run(t, nil, "setup", "--agent", "muse", "--dry-run", "--json", "--no-input")
	if code != 2 {
		t.Fatalf("exit %d", code)
	}
	ee := executeSetup(t, "setup", "--agent", "muse", "--no-input")
	if ee == nil || ee.Message != "Muse Code has not finished downloading" || !strings.Contains(ee.Hint, "MUSE_LAUNCHER_INSTALL=1") {
		t.Fatalf("%+v", ee)
	}
	v := (pickModel{items: []agentChoice{{name: "muse", label: "Muse Code", binary: filepath.Join(bin, "muse"), installed: true}}}).View()
	if !strings.Contains(v, "launcher only; app not downloaded yet") {
		t.Fatal(v)
	}
}

// The calendar step: Skip connects nothing; --no-calendar never asks.
func TestCalendarStep(t *testing.T) {
	captureUI(t)
	w := testWizard()
	w.interactive = true
	// Rows: Google, Skip (Microsoft stays out of the menu until its sign-in app is live).
	for _, c := range calendarChoices {
		if c.id == "microsoft" {
			t.Fatal("Microsoft is offered in the calendar step")
		}
	}
	useDriver(t, keysFor(t, []string{"down", "enter"}))
	if asked, res, err := w.stepCalendar(); !asked || res != menuChosen || err != nil || w.calendar != "" {
		t.Fatalf("skip: %v %v %v %q", asked, res, err, w.calendar)
	}
	useDriver(t, keysFor(t, []string{"enter"}))
	if _, _, _ = w.stepCalendar(); w.calendar != "google" {
		t.Fatalf("%q", w.calendar)
	}
	w.noCalendar = true
	useDriver(t, keysFor(t))
	if asked, res, _ := w.stepCalendar(); asked || res != menuChosen {
		t.Fatal("--no-calendar asked")
	}
}

// Step menus and the tick list: Esc and the Back row go back, q quits.
func TestChecklistAndTextPromptKeys(t *testing.T) {
	cl := newChecklist("Models", []string{"a", "b", "c"}, nil, []int{0, 2})
	m, _ := press(cl, "down", " ", "enter")
	if got := m.(checklistModel); got.result != menuChosen || fmt.Sprint(got.picked()) != "[0 1 2]" {
		t.Fatalf("%+v", got)
	}
	for keys, want := range map[string]menuResult{"esc": menuBack, "q": menuQuit, "up enter": menuBack} {
		m, _ := press(cl, strings.Fields(keys)...)
		if m.(checklistModel).result != want {
			t.Errorf("%s: %v", keys, m.(checklistModel).result)
		}
	}
	tp, _ := press(textPromptModel{question: "id", fallback: "keep"}, "enter")
	if tp.(textPromptModel).value != "keep" {
		t.Fatal("default not kept")
	}
	tp, _ = press(textPromptModel{question: "id"}, "a", "b", "esc")
	if tp.(textPromptModel).result != menuBack {
		t.Fatal("esc in a text prompt should go back")
	}
}

// dumpView writes a colour rendering for screenshots when env names a file.
func dumpView(t *testing.T, env, view string) {
	t.Helper()
	if p := os.Getenv(env); p != "" {
		if err := os.WriteFile(p, []byte(view), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestSetupModelView renders setup's model step from a stub catalog and stub
// estimates; MEETSTREAM_DUMP_SETUP_MODEL=<file> writes it in colour for
// screenshots (scripts/screenshot.sh).
func TestSetupModelView(t *testing.T) {
	if os.Getenv("MEETSTREAM_DUMP_SETUP_MODEL") != "" {
		lipgloss.SetColorProfile(termenv.TrueColor)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MEETSTREAM_AGENT_KIT_HOME", t.TempDir())
	_ = os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"oauthAccount":{}}`), 0o600) // signed in on a plan
	listed := models.Catalog{Models: []models.Choice{{ID: "sonnet"}, {ID: "opus"}, {ID: "haiku"}, {ID: "fable"}},
		DefaultModel: "sonnet", Efforts: []string{"low", "medium", "high"}, Source: "claude --help"}
	ms := 4200.0
	cached := estimates.LoadWithShared("claude", estimates.Bundled())
	cached["opus"] = estimates.Estimate{Usage: usage.Usage{Input: 14800, Cached: 12000, Output: 60}, FirstOutputMS: &ms, Source: estimates.MeasuredHere}
	menu, _, _ := modelMenu(agents.All["claude"], listed, "", cached)
	out := captureUI(t)
	g = globals{}
	ui.Step(3, 7, "Model and effort")
	ui.OK(listed.Summary("Claude Code"))
	view := out.String() + menu.View()
	for _, want := range []string{"4 models from claude --help", "≈$", "plan", "/h", measureLabel, backLabel} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q in\n%s", want, view)
		}
	}
	dumpView(t, "MEETSTREAM_DUMP_SETUP_MODEL", view)
}

// TestBenchTableView renders the live bench table mid-run;
// MEETSTREAM_DUMP_BENCH=<file> writes it in colour for screenshots.
func TestBenchTableView(t *testing.T) {
	if os.Getenv("MEETSTREAM_DUMP_BENCH") != "" {
		lipgloss.SetColorProfile(termenv.TrueColor)
	}
	now := time.Date(2026, 11, 22, 12, 0, 30, 0, time.UTC)
	cost := func(v float64) *float64 { return &v }
	first := 3900.0
	jobs := []bench.Job{
		{Model: "gemini-3.8-flash-medium", State: bench.Done, Started: now.Add(-28 * time.Second), Ended: now.Add(-12 * time.Second),
			Measured: &bench.Measured{Model: "gemini-3.8-flash-medium", Seconds: 16.2, FirstOutputMS: &first,
				Usage: &bench.Usage{Input: 9100, Output: 48, TokensPerHour: 231000, CostPerHour: cost(0.17)}}},
		{Model: "gemini-3.8-flash-high", State: bench.Done, Started: now.Add(-28 * time.Second), Ended: now.Add(-6 * time.Second),
			Measured: &bench.Measured{Model: "gemini-3.8-flash-high", Seconds: 22.4, Usage: &bench.Usage{Input: 9100, Output: 310, TokensPerHour: 318000, CostPerHour: cost(0.40)}}},
		{Model: "claude-sonnet-4-6", State: bench.Running, Stage: "first words", Started: now.Add(-18 * time.Second)},
		{Model: "gemini-4-argon", State: bench.Retrying, Stage: "thinking", Retried: true, Reason: "rate limit (429)", Started: now.Add(-9 * time.Second), Attempts: []time.Duration{14 * time.Second}},
		{Model: "gemini-3.8-flash-low", State: bench.Queued},
	}
	view := bench.Table(jobs, 4, "●", now, false) // the font freeze uses has no braille spinner
	for _, want := range []string{"gemini-3.8-flash-medium", "first words", "queued"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q in\n%s", want, view)
		}
	}
	dumpView(t, "MEETSTREAM_DUMP_BENCH", "\n  Comparing 5 model(s) of Antigravity CLI\n"+view)
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansi.ReplaceAllString(s, "") }
