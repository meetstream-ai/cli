package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/meetstream-ai/cli/internal/agentkit/access"
	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"github.com/meetstream-ai/cli/internal/agentkit/checks"
	kitconfig "github.com/meetstream-ai/cli/internal/agentkit/config"
	"github.com/meetstream-ai/cli/internal/agentkit/drivers"
	agentlogin "github.com/meetstream-ai/cli/internal/agentkit/login"
	"github.com/meetstream-ai/cli/internal/agentkit/meetingctx"
	"github.com/meetstream-ai/cli/internal/agentkit/muse"
	"github.com/meetstream-ai/cli/internal/agentkit/parsers"
	"github.com/meetstream-ai/cli/internal/agentkit/prompts"
	"github.com/meetstream-ai/cli/internal/agentkit/warmcheck"
	"github.com/meetstream-ai/cli/internal/api"
	"github.com/meetstream-ai/cli/internal/ui"
)

// The kit's local-agent questions after the model (meeting context, access,
// your name), its test turn, the sign-in gate, and re-running from the last
// setup (setup.json).

// ── sign-in gate ──────────────────────────────────────────────────────────────

// setupLoginCheck runs the agent CLI's sign-in check; tests swap it.
var setupLoginCheck = func(spec agents.Spec, binary string) agentlogin.Status {
	return (&agentlogin.Checker{Agent: spec, Binary: binary}).Check(true)
}

// setupSignIn runs the agent's own sign-in command attached to this terminal
// (no shell); tests swap it.
var setupSignIn = func(argv []string) int {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		return 127
	}
	return 0
}

// signInArgv is the sign-in command run with the CLI setup found (binary),
// which may be off PATH (e.g. ~/.local/bin), in place of its bare name.
func signInArgv(argv []string, binary string) []string {
	if binary == "" || len(argv) == 0 {
		return argv
	}
	return append([]string{binary}, argv[1:]...)
}

func signInOption(argv []string) string {
	return "Sign in now (`" + strings.Join(argv, " ") + "`)"
}

// signedOut is setup stopping on a signed-out agent: exit 2 with the sign-in command.
func signedOut(spec agents.Spec) error {
	return &ExitError{Code: 2, Message: spec.Label + " is signed out on this computer", Hint: "sign in with: " + spec.Relogin + ", then run meetstream setup again"}
}

// gateSignIn checks the chosen agent's sign-in. Signed out, it stops with a
// menu: sign in (the agent's own command, then check again), check again,
// Back (pick the agent again) or Quit. Without a terminal it stops with exit 2.
func (w *setupWizard) gateSignIn() (menuResult, error) {
	spec := w.spec
	st := setupLoginCheck(spec, w.chosen.binary)
	for st.State == "expired" {
		w.loginState = st.State
		if !g.JSON {
			ui.Fail(spec.Label + " is signed out on this computer")
		}
		if !w.interactive {
			return menuQuit, signedOut(spec)
		}
		argv := spec.LoginArgv()
		var options, details []string
		if argv != nil {
			options = append(options, signInOption(argv))
			details = append(details, "Runs "+strings.Join(argv, " ")+" here; follow its prompts, then setup checks again.")
		} else {
			ui.Note("Sign in with: " + spec.Relogin)
		}
		options = append(options, "Check again")
		details = append(details, "Signed in from another window? Check again.")
		res, i, err := ask(newStepMenu("Sign in to "+spec.Label+"?", options...).withDetails(details).withQuit())
		switch {
		case err != nil:
			return menuQuit, err
		case res == menuBack:
			return menuBack, nil
		case res == menuQuit:
			_ = w.saveAnswers() // warned there; the sign-in is what to fix
			return menuQuit, signedOut(spec)
		}
		if argv != nil && i == 0 {
			ui.Note("Running " + strings.Join(argv, " ") + ". Finish signing in there; setup checks again after.")
			setupSignIn(signInArgv(argv, w.chosen.binary))
		}
		st = setupLoginCheck(spec, w.chosen.binary)
	}
	w.loginState = st.State
	if g.JSON {
		return menuChosen, nil
	}
	switch st.State {
	case "ok":
		ui.OK(spec.Label + " is signed in")
	case "unknown":
		ui.Note(spec.Label + " has no sign-in check; the test turn at the end will tell.")
	default:
		ui.Warn(spec.Label + " sign-in check: " + st.State + " (" + st.Detail + ")")
	}
	return menuChosen, nil
}

// ── meeting context ───────────────────────────────────────────────────────────

// ignoreRulesNote says how each agent skips the folder's rules (the kit's ignore_rules_note).
var ignoreRulesNote = map[string]string{
	"claude": "skips the folder's CLAUDE.md and project settings (--setting-sources user)",
	"codex":  "skips the folder's AGENTS.md (-c project_doc_max_bytes=0)",
	"agy":    "has no switch to skip the folder's rules; use the meeting workspace",
	"hermes": "skips AGENTS.md, SOUL.md and memory in single runs (--ignore-rules); in meetings (hermes acp) only auto-loaded skills are skipped, so use the meeting workspace",
}

// folderDefault is the folder question's default: this setup's answer, the
// last setup's, else the meeting workspace (~/MeetStream Agent).
func (w *setupWizard) folderDefault() string {
	if w.workdir != "" {
		return w.workdir
	}
	return meetingctx.Workspace()
}

// useFolder checks a folder for the agent: the meeting workspace is created
// when missing; any other folder must exist, be a folder and be readable.
func useFolder(dir string) (string, *checks.Check) {
	dir = meetingctx.Expand(strings.TrimSpace(dir))
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	ws, _ := filepath.Abs(meetingctx.Workspace())
	if dir == ws {
		if _, err := os.Stat(ws); os.IsNotExist(err) {
			if err := os.MkdirAll(ws, 0o700); err == nil && !g.JSON {
				ui.OK("Created " + ui.ShortPath(ws))
			}
		}
	}
	if c := checks.Workdir(agentEnv(), dir); !c.Passed() {
		return dir, &c
	}
	return dir, nil
}

// stepFolder is the kit's "Meeting context": the folder the agent works in,
// the project rules there (Meeting only / Include / Custom), then MEETING.md.
func (w *setupWizard) stepFolder() (bool, menuResult, error) {
	w.step("Meeting context")
	asked := false
	if w.interactive && !w.workdirFlag {
		asked = true
		if !g.JSON {
			ui.Note(ui.ShortPath(meetingctx.Workspace()) + " is a quiet meeting workspace: no project rules on every turn.")
			ui.Note("Type a project folder instead to let your agent see that code.")
		}
		for {
			res, typed, err := askText("Folder your agent works in", ui.ShortPath(w.folderDefault()))
			if err != nil || res != menuChosen {
				return true, res, err
			}
			dir, bad := useFolder(typed)
			if bad != nil {
				ui.Fail(bad.Detail)
				continue
			}
			res, err = w.reviewRules(dir)
			if err != nil || res == menuQuit {
				return true, menuQuit, err
			}
			if res == menuBack {
				continue // the folder again
			}
			break
		}
	} else {
		dir, bad := useFolder(w.folderDefault())
		if bad != nil {
			return false, menuQuit, usageErr(bad.Detail, bad.Hint)
		}
		w.workdir = dir
	}
	w.showContext()
	return asked, menuChosen, nil
}

// reviewRules asks what goes into meetings when dir has project rules, and
// sets the folder, ignoreRules and useMeetingMD.
func (w *setupWizard) reviewRules(dir string) (menuResult, error) {
	rules := meetingctx.ProjectRules(dir)
	if len(rules) == 0 {
		w.workdir, w.ignoreRules, w.useMeetingMD = dir, false, true
		return menuChosen, nil
	}
	spec := w.spec
	var total int64
	for _, r := range rules {
		total += r.Size
	}
	ui.Warn(fmt.Sprintf("This folder has project rules your agent reads on every meeting turn (≈ %d tokens):", total/4))
	for _, r := range rules {
		ui.Note(fmt.Sprintf("%s  %.1f KB ≈ %d tokens", r.Name, float64(r.Size)/1024, r.Size/4))
	}
	canSkip := len(spec.IgnoreRulesArgs) > 0 || len(spec.IgnoreRulesEnv) > 0
	note := ignoreRulesNote[spec.Name]
	ws := meetingctx.Workspace()
	meetingHint := note
	if !canSkip {
		ui.Note(spec.Label + " " + orDefault(note, "has no switch to skip them") + ".")
		meetingHint = "use " + ui.ShortPath(ws) + " instead"
	}
	hints := []string{meetingHint, fmt.Sprintf("≈ %d tokens on every turn", total/4), "choose what goes in"}
	cursor := 0
	if w.workdir == dir && !w.ignoreRules && w.last != nil {
		cursor = 1 // last time: this project's rules
	}
	for {
		res, i, err := ask(newStepMenu("Meeting context", "Meeting only", "Include this project's rules", "Custom").withHints(hints).withDetails(hints).at(cursor))
		if err != nil || res != menuChosen {
			return res, err
		}
		switch i {
		case 0:
			if !canSkip {
				_ = os.MkdirAll(ws, 0o700)
				w.workdir, w.ignoreRules, w.useMeetingMD = ws, false, true
			} else {
				w.workdir, w.ignoreRules, w.useMeetingMD = dir, true, true
			}
			return menuChosen, nil
		case 1:
			w.workdir, w.ignoreRules, w.useMeetingMD = dir, false, true
			return menuChosen, nil
		}
		var parts []string
		if canSkip {
			parts = append(parts, "This project's rules")
		}
		parts = append(parts, "My MEETING.md instructions")
		ticked := make([]int, len(parts))
		for k := range parts {
			ticked[k] = k
		}
		final, err := runTea(newChecklist("What goes into meetings?", parts, nil, ticked))
		if err != nil {
			return menuQuit, err
		}
		cl := final.(checklistModel)
		if cl.result == menuQuit {
			return menuQuit, nil
		}
		if cl.result == menuBack {
			continue
		}
		keep := map[int]bool{}
		for _, k := range cl.picked() {
			keep[k] = true
		}
		w.workdir = dir
		w.ignoreRules = canSkip && !keep[0]
		w.useMeetingMD = keep[len(parts)-1]
		return menuChosen, nil
	}
}

// showContext makes MEETING.md (from the kit's template) and shows what the
// agent reads on every meeting turn.
func (w *setupWizard) showContext() {
	path, err := meetingctx.Ensure()
	if g.JSON {
		return
	}
	if err != nil {
		ui.Warn("Could not create " + ui.ShortPath(path) + ": " + err.Error())
	}
	items := meetingctx.Effective(w.spec, w.workdir, "", w.ignoreRules, w.useMeetingMD)
	ui.OK("Meeting context: " + meetingctx.Summary(w.spec, w.workdir, w.ignoreRules, w.useMeetingMD))
	for _, row := range meetingctx.Table(items) {
		ui.Note(row)
	}
	ui.Note("Your meeting instructions: " + ui.ShortPath(meetingctx.File()) + " (edit with: meetstream agent context edit)")
}

// ── access and name ───────────────────────────────────────────────────────────

// accessChoices are the kit's ACCESS_CHOICES: level, label, what it allows.
var accessChoices = []struct{ id, label, detail string }{
	{"readonly", "Meeting only", "a meeting companion: reads the folder, changes nothing on this computer"},
	{"confirm", "Ask first", "may do anything on this computer; each command or change waits for your yes"},
	{"full", "Full", "may do anything on this computer without asking; only when you alone can talk to it"},
}

func accessLabel(level string) string {
	for _, c := range accessChoices {
		if c.id == level {
			return c.label
		}
	}
	return level
}

// stepAccess is the most access this computer allows (default Meeting only).
func (w *setupWizard) stepAccess() (bool, menuResult, error) {
	w.step("Access")
	if !access.Valid(w.maxAccess) {
		return false, menuQuit, usageErr("invalid --access", "choose readonly, confirm, or full")
	}
	asked := false
	if w.interactive && !w.accessFlag {
		if !g.JSON {
			ui.Note("The most this computer allows. Your MIA agent's settings can only lower it.")
		}
		var labels, hints []string
		cursor := 0
		for i, c := range accessChoices {
			labels, hints = append(labels, c.label), append(hints, c.detail)
			if c.id == w.maxAccess {
				cursor = i
			}
		}
		res, i, err := ask(newStepMenu("Most access this computer allows", labels...).withHints(hints).withDetails(hints).at(cursor))
		if err != nil || res != menuChosen {
			return true, res, err
		}
		w.maxAccess = accessChoices[i].id
		asked = true
	}
	if !g.JSON {
		ui.OK("Access: " + accessLabel(w.maxAccess))
	}
	return asked, menuChosen, nil
}

// stepName is your name as MIA hears it (default $USER), the bridge's owner.
func (w *setupWizard) stepName() (bool, menuResult, error) {
	w.step("Your name")
	if w.owner == "" {
		w.owner = loginName()
	}
	asked := false
	if w.interactive && !w.ownerFlag {
		res, name, err := askText("Your name as it shows in meetings", w.owner)
		if err != nil || res != menuChosen {
			return true, res, err
		}
		w.owner, asked = name, true
	}
	if !g.JSON {
		ui.OK("Your name: " + orDefault(w.owner, "-"))
	}
	return asked, menuChosen, nil
}

// ── the test turn ─────────────────────────────────────────────────────────────

// testTurnAsk is the kit's test turn.
const testTurnAsk = "Reply with the single word: ready"

// testTurnTimeout is the kit's min(turn timeout, TEST_TURN_TIMEOUT_S).
var testTurnTimeout = 60 * time.Second

// testTurn is one cold turn's outcome.
type testTurn struct {
	Answer, Error, StderrTail string
	Total                     time.Duration
}

// setupTestTurn runs one read-only cold turn through the CLI's one-shot mode,
// calling onStage at "output" (first line) and "first_delta" (first words).
// Tests swap it so no agent starts.
var setupTestTurn = func(ctx context.Context, c kitconfig.Config, onStage func(event string, elapsed time.Duration)) testTurn {
	t0 := time.Now()
	prompt := prompts.System(c.Owner, "", "readonly", c.MeetingNotes) + "\n\n" + prompts.Turn([]map[string]any{{"speaker": c.Owner, "text": testTurnAsk}})
	argv, input := drivers.OneShotArgs(c.Agent, c.Binary, prompt, "readonly", c.Model, c.Effort, c.IgnoreRules)
	runCtx, cancel := context.WithTimeout(ctx, testTurnTimeout)
	defer cancel()
	var answer strings.Builder
	final, said := "", ""
	gotLine := false
	stderr, runErr := benchExec(runCtx, argv, c.Workdir, input, drivers.Env(c, c.Model, ""), func(line string) {
		if !gotLine {
			gotLine = true
			onStage("output", time.Since(t0))
		}
		p := parsers.Parse(c.Agent.Parser, line)
		if p.Delta != "" {
			if answer.Len() == 0 {
				onStage("first_delta", time.Since(t0))
			}
			answer.WriteString(p.Delta)
		}
		if p.HasFinal {
			final = p.Final
		}
		if p.HasError {
			said = p.Error
		}
	})
	out := testTurn{Answer: strings.TrimSpace(answer.String()), StderrTail: stderr, Total: time.Since(t0)}
	if out.Answer == "" {
		out.Answer = strings.TrimSpace(final)
	}
	var ee *exec.ExitError
	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		out.Error = "agent timed out"
	case runErr != nil && strings.HasPrefix(runErr.Error(), "agent CLI not found"):
		out.Error = runErr.Error()
	case said != "":
		out.Error = said
	case runErr != nil && out.Answer == "" && errors.As(runErr, &ee):
		out.Error = fmt.Sprintf("exited with code %d", ee.ExitCode())
	}
	return out
}

// testKey is what the test turn depends on: a passed test is not run again
// when Back and forward leave it unchanged.
func (w *setupWizard) testKey() string {
	return strings.Join([]string{w.spec.Name, w.chosen.binary, w.model, w.effort, w.workdir, w.owner, fmt.Sprint(w.ignoreRules, w.useMeetingMD)}, "|")
}

// kitConfig is the bridge config the test turn and the warm check use.
func (w *setupWizard) kitConfig() kitconfig.Config {
	notes := ""
	if w.useMeetingMD {
		notes = meetingctx.Notes()
	}
	owner := w.owner
	if owner == "" {
		owner = setupOwner()
	}
	return kitconfig.Config{Agent: w.spec, Binary: w.chosen.binary, Workdir: w.workdir, Owner: owner, Model: w.model, Effort: w.effort,
		MaxAccess: w.maxAccess, IgnoreRules: w.ignoreRules, MeetingNotes: notes}
}

// stages shows a run's stages one line at a time with a spinner: each reached
// stage prints a tick and its time; fail prints the stage it stopped at.
type stages struct {
	labels [][2]string // doing, done
	at     int
	stop   func()
}

func newStages(labels [][2]string) *stages {
	s := &stages{labels: labels}
	s.stop = spin(labels[0][0])
	return s
}

// reach ticks stage i (and nothing before it that was not seen).
func (s *stages) reach(i int, elapsed time.Duration) {
	if i < s.at || i >= len(s.labels) {
		return
	}
	s.stop()
	if !g.JSON {
		ui.OK(fmt.Sprintf("%s  %s", s.labels[i][1], ui.DimStyle.Render(fmt.Sprintf("%.1f s", elapsed.Seconds()))))
	}
	s.at = i + 1
	s.stop = func() {}
	if s.at < len(s.labels) {
		s.stop = spin(s.labels[s.at][0])
	}
}

func (s *stages) fail(elapsed time.Duration) {
	s.stop()
	if !g.JSON && s.at < len(s.labels) {
		ui.Fail(fmt.Sprintf("%s  %s", s.labels[s.at][0], ui.DimStyle.Render(fmt.Sprintf("%.1f s (failed)", elapsed.Seconds()))))
	}
}

func (s *stages) done() { s.stop() }

// coldTurn runs the test turn with its stages: Woke → First words → Answered.
func (w *setupWizard) coldTurn(c kitconfig.Config) (testTurn, *checks.TurnDiagnosis) {
	label := w.spec.Label
	st := newStages([][2]string{{"Waking up " + label, "Woke " + label}, {"Thinking", "First words"}, {"Answering", "Answered"}})
	r := setupTestTurn(w.cmd.Context(), c, func(event string, elapsed time.Duration) {
		switch event {
		case "output":
			st.reach(0, elapsed)
		case "first_delta":
			st.reach(1, elapsed)
		}
	})
	d := checks.DiagnoseTurn(w.spec, r.Answer, r.Error, r.StderrTail, testTurnTimeout.Seconds())
	if d != nil {
		st.fail(r.Total)
	} else {
		st.reach(2, r.Total)
	}
	st.done()
	return r, d
}

// whatNow is the menu after a failed check. actions are ids with labels;
// Back and Quit are the menu's own rows.
func whatNow(actions [][2]string, cursor int) (string, error) {
	var labels, details []string
	for _, a := range actions {
		labels = append(labels, a[1])
		details = append(details, actionDetail[a[0]])
	}
	res, i, err := ask(newStepMenu("What now?", labels...).withDetails(details).withQuit().at(cursor))
	switch {
	case err != nil:
		return "quit", err
	case res == menuBack:
		return "back", nil
	case res == menuQuit:
		return "quit", nil
	}
	return actions[i][0], nil
}

var actionDetail = map[string]string{
	"login":  "Runs the agent's sign-in here; the test runs again after.",
	"retry":  "Run the test again.",
	"anyway": "Go on with setup; fix the agent before your next meeting.",
}

// stepTest is the kit's Test step: one cold turn for every agent, then the
// warm (ACP) check for ACP agents. A failure shows why and asks what now.
// Without a terminal it runs only with --test.
func (w *setupWizard) stepTest() (bool, menuResult, error) {
	w.step("Test")
	if !w.interactive && !w.testFlag {
		w.testResult = "skipped"
		if !g.JSON {
			ui.Note("Skipped without a terminal (pass --test to run it).")
		}
		return false, menuChosen, nil
	}
	key := w.testKey()
	if key == w.tested {
		if !g.JSON {
			ui.OK("Tested: " + w.spec.Label + " answered")
		}
		return false, menuChosen, nil
	}
	c := w.kitConfig()
	asked := false
	label := w.spec.Label
	for {
		r, d := w.coldTurn(c)
		if d == nil {
			w.loginState = "ok"
			if !g.JSON {
				ui.OK(fmt.Sprintf("%s answered in %.1f s: %s", label, r.Total.Seconds(), clipText(strings.TrimSpace(r.Answer), 80)))
			}
			w.testResult = "passed"
			break
		}
		if d.Kind == "signed_out" {
			w.loginState = "expired"
		}
		if !g.JSON {
			ui.Fail(d.Message)
			lines := strings.Split(strings.TrimSpace(d.Detail), "\n")
			if d.Detail == "" {
				lines = nil
			}
			for _, l := range lines[max(0, len(lines)-4):] {
				ui.Note("│ " + clipText(l, 200))
			}
			ui.Note("Fix: " + d.Fix)
		}
		if !w.interactive {
			w.testResult = "failed"
			code := 1
			if d.Kind == "signed_out" {
				code = 2
			}
			return false, menuQuit, &ExitError{Code: code, Message: "test turn: " + d.Message, Hint: d.Fix}
		}
		asked = true
		var login []string
		if d.Kind != "not_installed" {
			login = w.spec.LoginArgv()
		}
		var actions [][2]string
		if login != nil {
			actions = append(actions, [2]string{"login", "Sign in now (" + strings.Join(login, " ") + "), then test again"})
		}
		actions = append(actions, [2]string{"retry", "Test again"}, [2]string{"anyway", "Start anyway (MIA cannot use your agent until it answers)"})
		cursor := 1
		if login == nil || d.Kind == "signed_out" || d.Kind == "empty" || d.Kind == "failed" {
			cursor = 0
		}
		pick, err := whatNow(actions, cursor)
		if err != nil {
			return true, menuQuit, err
		}
		switch pick {
		case "back":
			return true, menuBack, nil
		case "quit":
			return true, menuQuit, w.quitAfterTest(d.Message)
		case "login":
			ui.Note("Running " + strings.Join(login, " ") + ". Finish signing in there; this test runs again after.")
			setupSignIn(signInArgv(login, w.chosen.binary))
			continue
		case "retry":
			continue
		}
		w.testResult = "started anyway"
		break
	}
	if warmcheck.AppliesTo(w.spec) {
		for {
			r := w.warmCheck(c)
			if r.OK {
				break
			}
			if !w.interactive {
				w.testResult = "failed"
				return false, menuQuit, &ExitError{Code: 1, Message: "warm mode (ACP): " + r.Message, Hint: r.Fix}
			}
			asked = true
			pick, err := whatNow([][2]string{{"retry", "Test again"}, {"anyway", "Start anyway (meetings may fail or have no meeting tools)"}}, 0)
			if err != nil {
				return true, menuQuit, err
			}
			switch pick {
			case "back":
				return true, menuBack, nil
			case "quit":
				return true, menuQuit, w.quitAfterTest("warm mode (ACP): " + r.Message)
			case "retry":
				continue
			}
			w.testResult = "started anyway"
			break
		}
	}
	if w.testResult == "passed" {
		w.tested = key
	}
	return asked, menuChosen, nil
}

// quitAfterTest saves the answers and stops: the agent did not pass its test.
func (w *setupWizard) quitAfterTest(why string) error {
	w.testResult = "failed"
	hint := "Saved your answers. Run `meetstream setup` again when the agent is ready."
	if err := w.saveAnswers(); err != nil {
		hint = "Your answers were not saved (" + err.Error() + "). Run `meetstream setup` again when the agent is ready."
	}
	return &ExitError{Code: 1, Message: "test turn: " + why, Hint: hint}
}

func clipText(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// warmCheck tests the warm (ACP) path meetings use, one stage at a time with a spinner.
func (w *setupWizard) warmCheck(c kitconfig.Config) warmcheck.Result {
	labels := [][2]string{{"Starting " + w.spec.Label + " (ACP)", "Session ready"}}
	events := []string{"ready"}
	if w.spec.ACPMCP {
		labels = append(labels, [2]string{"Calling a meeting tool", "Tool called"})
		events = append(events, "tool_call")
	}
	labels = append(labels, [2]string{"Answering", "Answered"})
	st := newStages(labels)
	r := setupWarmCheck(w.cmd.Context(), c, warmcheck.Options{Turn: true, OnStage: func(event string, elapsed time.Duration) {
		for i, e := range events {
			if e == event {
				st.reach(i, elapsed)
			}
		}
	}})
	if r.OK {
		st.reach(len(labels)-1, r.Total)
	} else {
		st.fail(r.Total)
	}
	st.done()
	if g.JSON {
		return r
	}
	if r.OK {
		ui.OK(fmt.Sprintf("%s (%.1f s)", r.Message, r.Total.Seconds()))
		return r
	}
	ui.Fail("Warm mode (ACP): " + r.Message)
	if r.Fix != "" {
		ui.Note("Fix: " + r.Fix)
	}
	return r
}

// ── the last setup ────────────────────────────────────────────────────────────

// loadLast reads setup.json and takes its answers as defaults where no flag set one.
func (w *setupWizard) loadLast() {
	s, err := kitconfig.LoadSetup("")
	if err != nil || s == nil || s.Command != "" {
		return
	}
	w.last = s
	f := w.cmd.Flags()
	w.lastAgent = s.Agent
	if !w.modelFlags {
		w.model, w.effort = s.Model, s.Effort
	}
	if !f.Changed("workdir") && s.Workdir != "" {
		w.workdir = s.Workdir
	}
	if !f.Changed("access") && access.Valid(s.MaxAccess) {
		w.maxAccess = s.MaxAccess
	}
	if !f.Changed("owner") && s.Owner != "" {
		w.owner = s.Owner
	}
	w.ignoreRules = s.IgnoreRules == "yes"
	w.useMeetingMD = s.MeetingMD != "no"
	w.savedConfigID = s.MIAAgentID
}

// lastRows are the "Last setup" box's rows.
func (w *setupWizard) lastRows() [][2]string {
	s := w.last
	spec := agents.All[s.Agent]
	rows := [][2]string{
		{"Agent", spec.Label},
		{"Folder", ui.ShortPath(s.Workdir)},
		{"Access", accessLabel(s.MaxAccess)},
		{"Model", orDefault(s.Model, "the CLI's default")},
		{"Effort", orDefault(s.Effort, "the CLI's default")},
	}
	if s.Workdir != "" {
		rows = append(rows, [2]string{"Context", meetingctx.Summary(spec, s.Workdir, s.IgnoreRules == "yes", s.MeetingMD != "no")})
	}
	rows = append(rows, [2]string{"Reached by", "MeetStream relay (this computer's sign-in)"}, [2]string{"Your name", orDefault(s.Owner, "-")})
	if s.MIAAgentID != "" {
		rows = append(rows, [2]string{"MIA agent", s.MIAAgentID})
	}
	return rows
}

// startWithLast shows the last setup and asks to start with it. Yes resolves
// the saved agent and returns true (setup goes straight to the test); No
// keeps the answers as defaults.
func (w *setupWizard) startWithLast() (bool, error) {
	fmt.Fprintln(ui.Out)
	ui.Box("Last setup", w.lastRows())
	m := newStepMenu("Start with it?", "Yes", "No, go through the questions").
		withDetails([]string{"Test your agent, then update your MIA agent with these answers.", "Ask every question again, starting from these answers."})
	m.noBack = true
	res, i, err := ask(m)
	if err != nil {
		return false, err
	}
	if res != menuChosen {
		return false, errSetupCancelled
	}
	if i != 0 {
		return false, nil
	}
	var chosen *agentChoice
	for _, c := range detectAgents() {
		if c.name == w.last.Agent && c.installed {
			c := c
			chosen = &c
		}
	}
	if chosen == nil || muse.LauncherWithoutApp(chosen.name, chosen.binary) {
		ui.Warn(agents.All[w.last.Agent].Label + " is not ready on this computer any more; setup asks again.")
		return false, nil
	}
	w.mode, w.chosen, w.spec = modeLocal, chosen, agents.All[chosen.name]
	if err := w.resolvePicks(); err != nil {
		return false, err
	}
	if _, bad := useFolder(w.folderDefault()); bad != nil {
		ui.Warn(bad.Detail + "; setup asks again.")
		return false, nil
	}
	w.workdir, _ = filepath.Abs(meetingctx.Expand(w.folderDefault()))
	return true, nil
}

// saveAnswers writes the local agent's answers so far to setup.json (each step
// saves, so quitting keeps them). It keeps what setup does not ask (auto-start).
// A failed save is returned, and warned about once.
func (w *setupWizard) saveAnswers() error {
	if w.mode != modeLocal || w.chosen == nil || w.dryRun {
		return nil
	}
	err := w.saved().Save("")
	if err != nil && !g.JSON && !w.saveWarned {
		w.saveWarned = true
		ui.Warn("Could not save your answers: " + err.Error())
	}
	return err
}

func (w *setupWizard) saved() kitconfig.Setup {
	s := kitconfig.Setup{}
	if w.last != nil {
		s = *w.last
	}
	s.Agent, s.Command = w.spec.Name, ""
	s.Workdir, s.MaxAccess, s.Tunnel, s.TunnelURL = w.workdir, w.maxAccess, "relay", ""
	s.Owner, s.Model, s.Effort = w.owner, w.model, w.effort
	s.IgnoreRules, s.MeetingMD = "", ""
	if w.ignoreRules {
		s.IgnoreRules = "yes"
	}
	if !w.useMeetingMD {
		s.MeetingMD = "no"
	}
	if s.MIAAgentID == "" || w.miaID != "" {
		s.MIAAgentID = w.miaID
	}
	return s
}

// targetID is the MIA agent setup updates: --agent-config, else (your agent on
// this computer) the one the last setup created, unless a new one was asked for.
func (w *setupWizard) targetID() string {
	switch {
	case w.configID != "":
		return w.configID
	case w.mode == modeLocal && !w.newAgent:
		return w.savedConfigID
	}
	return ""
}

// ── a server without the relay ────────────────────────────────────────────────

// errRelayNotEnabled: this server does not take agents on your computer yet.
func errRelayNotEnabled() error {
	return &ExitError{Code: 3, Message: "This MeetStream server doesn't accept agents on your computer yet (relay not enabled here).",
		Hint: "Your setup is saved; run meetstream setup again once it's enabled."}
}

// relayRefused is the API refusing a relay agent, e.g. 400 "model.local_agent.url
// must be an https URL (your bridge tunnel) without credentials." from a server
// that only knows tunnel agents.
func relayRefused(ae *api.Error) bool {
	d := strings.ToLower(ae.Detail())
	if strings.Contains(d, "relay") {
		return true
	}
	return (ae.Status == 400 || ae.Status == 422) && (strings.Contains(d, "local_agent") || strings.Contains(d, "transport"))
}
