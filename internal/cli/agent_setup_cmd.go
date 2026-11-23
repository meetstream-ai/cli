package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/meetstream-ai/cli/internal/agentkit/access"
	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"github.com/meetstream-ai/cli/internal/agentkit/checks"
	kitconfig "github.com/meetstream-ai/cli/internal/agentkit/config"
	"github.com/meetstream-ai/cli/internal/agentkit/meetingctx"
	"github.com/meetstream-ai/cli/internal/agentkit/muse"
	"github.com/meetstream-ai/cli/internal/agentkit/relay"
	"github.com/meetstream-ai/cli/internal/agentkit/warmcheck"
	"github.com/meetstream-ai/cli/internal/api"
	"github.com/meetstream-ai/cli/internal/config"
	"github.com/meetstream-ai/cli/internal/login"
	"github.com/meetstream-ai/cli/internal/ui"
)

// `meetstream agent setup`: pick your agent CLI, connect this computer to the
// relay, and create (or update) the MIA agent that uses it. No website visit,
// no tunnel, no pairing token.

var agentOrder = []string{"claude", "codex", "hermes", "copilot", "muse", "openclaw", "agy"}

type agentChoice struct {
	name, label, binary string
	installed           bool
	offPath             bool // found in an installer's folder that is not on PATH
}

// agentEnv is how agent CLIs are found: PATH first, then the folders installers
// use when PATH does not have them yet (~/.local/bin, Homebrew, /usr/local/bin).
func agentEnv() checks.Env {
	e := checks.DefaultEnv()
	e.InstallDirs = installDirs()
	return e
}

func detectAgents() []agentChoice {
	e := agentEnv()
	var out []agentChoice
	for _, n := range agentOrder {
		a, ok := agents.All[n]
		if !ok {
			continue
		}
		p, off := checks.ResolveBinary(e, a, "")
		out = append(out, agentChoice{name: n, label: a.Label, binary: p, installed: p != "", offPath: off})
	}
	// installed first, keeping the kit's order otherwise
	sort.SliceStable(out, func(i, j int) bool { return out[i].installed && !out[j].installed })
	return out
}

// pathHint shows the export line for a CLI found off PATH.
func pathHint(c agentChoice) {
	if !c.offPath || g.JSON {
		return
	}
	dir := filepath.Dir(c.binary)
	lead, command := checks.PathHint(dir, runtime.GOOS == "windows")
	ui.Warn("`" + filepath.Base(c.binary) + "` is in " + ui.ShortPath(dir) + ", which is not on your PATH. " + lead)
	ui.Command(command)
}

// ── picker ────────────────────────────────────────────────────────────────────

const agentQuestion = "Which agent should answer in your meetings?"

type pickModel struct {
	items  []agentChoice
	cursor int
	chosen *agentChoice
	back   bool // Esc: back to step 1 (q or ctrl+c cancels)
}

func (m pickModel) Init() tea.Cmd { return nil }

func (m pickModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "esc":
			m.back = true
			return m, tea.Quit
		case "up", "k":
			m.cursor = (m.cursor - 1 + len(m.items)) % len(m.items)
		case "down", "j", "tab":
			m.cursor = (m.cursor + 1) % len(m.items)
		case "enter":
			c := m.items[m.cursor]
			m.chosen = &c
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m pickModel) View() string {
	var b strings.Builder
	b.WriteString("\n  " + ui.BoldStyle.Render(agentQuestion) + "\n\n")
	sel := lipgloss.NewStyle().Foreground(ui.Orange).Bold(true)
	for i, it := range m.items {
		pointer, title := "  ", it.label
		if i == m.cursor {
			pointer, title = sel.Render("❯ "), sel.Render(it.label)
		}
		state := ui.DimStyle.Render("not installed")
		switch {
		case it.installed && muse.LauncherWithoutApp(it.name, it.binary):
			state = lipgloss.NewStyle().Foreground(ui.Yellow).Render(muse.MenuHint)
		case it.offPath:
			state = lipgloss.NewStyle().Foreground(ui.Yellow).Render("✓ " + ui.ShortPath(it.binary) + " (not on PATH)")
		case it.installed:
			state = lipgloss.NewStyle().Foreground(ui.Green).Render("✓ " + ui.ShortPath(it.binary))
		}
		fmt.Fprintf(&b, "  %s%-22s %s\n", pointer, title, state)
	}
	b.WriteString("\n  " + ui.DimStyle.Render("↑/↓ move · enter choose · esc back · q quit") + "\n")
	return b.String()
}

// ── command ───────────────────────────────────────────────────────────────────

// setupWizard holds setup's flags and the answers so far. Each step returns
// whether it asked anything (Back skips steps that did not) and how it ended.
type setupWizard struct {
	cmd         *cobra.Command
	info        BuildInfo
	interactive bool

	agentName, maxAccess, workdir, configID, name, model, effort string
	dryRun, noStart, noCalendar, modelFlags                      bool

	mode, voiceKeys, earsFlag, brainFlag, voiceFlag string
	modeFlag, modeAgain, voiceKeysFlag              bool
	cat                                             miaCatalog
	picks                                           map[string]sectionPick // ears, brain and voice
	at, total                                       int                    // this step's number, and how many

	chosen    *agentChoice
	spec      agents.Spec
	creds     *relay.Credentials
	pickAgain bool   // back at step 1 (or an install declined): show the picker even with --agent
	calendar  string // "google" or "microsoft" to connect at the end; "" skips

	// Your agent on this computer: the kit's questions after the model.
	owner                              string
	ignoreRules, useMeetingMD          bool
	workdirFlag, accessFlag, ownerFlag bool
	testFlag, newAgent                 bool
	last                               *kitconfig.Setup // setup.json from the last run, nil the first time
	lastAgent, savedConfigID, miaID    string           // the last run's agent and MIA agent; the one this run made
	tested, testResult, loginState     string           // the test that passed; passed/skipped/failed/started anyway; ok/expired/unknown
	saveWarned                         bool             // a failed save of the answers was shown once
}

// setupInteractive is whether setup may ask; tests swap it to drive the menus.
var setupInteractive = func() bool { return !g.JSON && !g.NoInput && ui.Interactive() }

var errSetupCancelled = &ExitError{Code: 130, Message: "setup cancelled"}

// setupWarmCheck runs the warm (ACP) check; tests swap it so no agent starts.
var setupWarmCheck = func(ctx context.Context, c kitconfig.Config, o warmcheck.Options) warmcheck.Result {
	return warmcheck.Check(ctx, c, o)
}

// stepAgent is step 1: pick the agent CLI, install it or its app, check sign-in.
func (w *setupWizard) stepAgent() (bool, menuResult, error) {
	for {
		w.step("Your agent")
		choices := detectAgents()
		prev := orDefault(w.agentName, w.lastAgent)
		if w.chosen != nil {
			prev = w.chosen.name
		}
		var chosen *agentChoice
		switch {
		case w.agentName != "" && !w.pickAgain:
			for i := range choices {
				if choices[i].name == w.agentName {
					chosen = &choices[i]
				}
			}
			if chosen == nil {
				return false, menuQuit, usageErr("unknown agent "+w.agentName, "choose: "+strings.Join(agentOrder, ", "))
			}
		case w.interactive:
			pm := pickModel{items: choices}
			for i := range choices {
				if choices[i].name == prev {
					pm.cursor = i
				}
			}
			final, err := runTea(pm)
			if err != nil {
				return false, menuQuit, err
			}
			if final.(pickModel).back {
				return true, menuBack, nil
			}
			if chosen = final.(pickModel).chosen; chosen == nil {
				return false, menuQuit, errSetupCancelled
			}
		default:
			for i := range choices { // the last setup's agent, else the first installed
				if choices[i].installed && choices[i].name == w.lastAgent {
					chosen = &choices[i]
				}
			}
			for i := range choices {
				if chosen == nil && choices[i].installed {
					chosen = &choices[i]
					break
				}
			}
			if chosen == nil {
				return false, menuQuit, usageErr("no supported agent CLI is installed", "install one, e.g. npm install -g @anthropic-ai/claude-code, or pass --agent")
			}
		}
		spec := agents.All[chosen.name]
		if !chosen.installed {
			if !w.interactive {
				return false, menuQuit, &ExitError{Code: 2, Message: spec.Label + " is not installed", Hint: spec.InstallHint}
			}
			// Like the kit: offer the vendor's installer; without it, pick again.
			p := setupInstaller.offer(spec)
			if p == "" {
				w.pickAgain = true
				continue
			}
			chosen.binary, chosen.installed = p, true
		}
		pathHint(*chosen)
		// Muse's launcher without the app it downloads is not an installed agent.
		if muse.LauncherWithoutApp(spec.Name, chosen.binary) {
			d := muse.Downloader{Label: spec.Label, Binary: chosen.binary, Offer: w.interactive, Say: museSay}
			if w.interactive {
				d.Confirm = func(q string) (bool, bool) { return confirmEnded(setupInstaller.in, q) }
				d.Run = func(c muse.Command) int {
					cmd := c.Cmd()
					cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, ui.Out, ui.Out
					if err := cmd.Run(); err != nil {
						var ee *exec.ExitError
						if errors.As(err, &ee) {
							return ee.ExitCode()
						}
						return 127
					}
					return 0
				}
			}
			if !d.Download() {
				if !w.interactive {
					return false, menuQuit, &ExitError{Code: 2, Message: muse.AbortMessage(spec.Label), Hint: "run: " + muse.InstallCommand(chosen.binary).String()}
				}
				w.pickAgain = true
				continue
			}
		}
		before := w.lastAgent
		if w.chosen != nil {
			before = w.chosen.name
		}
		if before != "" && before != chosen.name && !w.modelFlags {
			w.model, w.effort = "", "" // another agent: its own models
		}
		w.chosen, w.spec = chosen, spec
		if !g.JSON {
			ui.OK(spec.Label + "  " + ui.DimStyle.Render(ui.ShortPath(chosen.binary)))
		}
		if !access.Valid(w.maxAccess) {
			return false, menuQuit, usageErr("invalid --access", "choose readonly, confirm, or full")
		}
		// Like the kit: a signed-out agent stops here, with a sign-in offer.
		res, err := w.gateSignIn()
		if err != nil {
			return true, menuQuit, err
		}
		if res == menuBack {
			w.pickAgain = true
			continue
		}
		return true, menuChosen, nil
	}
}

func museSay(kind, text string) {
	if g.JSON {
		return
	}
	switch kind {
	case "ok":
		ui.OK(text)
	case "fail":
		ui.Fail(text)
	case "command":
		ui.Command(text)
	default:
		ui.Note(text)
	}
}

func setupOwner() string {
	if u := loginName(); u != "" {
		return u
	}
	return "the user"
}

// loginName is this computer's user name: $USER, or on Windows (which sets
// no USER) %USERNAME%.
func loginName() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return os.Getenv("USERNAME")
}

// stepModel is step 2: the model and effort. --model/--effort (or a run that
// cannot ask) skip the menu; "" is the CLI's own default.
func (w *setupWizard) stepModel() (bool, menuResult, error) {
	w.step("Model and effort")
	if !w.interactive || w.modelFlags {
		if !g.JSON {
			ui.OK("Model: " + orDefault(w.model, "the CLI's default") + "  " + ui.DimStyle.Render("effort: "+orDefault(w.effort, "the CLI's default")))
		}
		return false, menuChosen, nil
	}
	pick, res, err := w.pickModel(w.spec, w.chosen.binary, w.model, w.effort)
	if err != nil || res != menuChosen {
		return pick.asked, res, err
	}
	w.model, w.effort = pick.model, pick.effort
	if !g.JSON {
		ui.OK("Model: " + orDefault(w.model, "the CLI's default") + "  " + ui.DimStyle.Render("effort: "+orDefault(w.effort, "the CLI's default")))
	}
	return pick.asked, menuChosen, nil
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// setupRelayLogin is the relay's browser sign-in; tests swap it.
var setupRelayLogin = relay.Login

// stepConnect is step 3: this computer's relay sign-in.
func (w *setupWizard) stepConnect() (bool, menuResult, error) {
	w.step("Connect this computer")
	store := relay.NewStore()
	var err error
	if w.creds, err = store.Load(); err != nil {
		return false, menuQuit, err
	}
	if w.interactive {
		option := "Connect this computer (opens your browser)"
		if w.creds != nil {
			option = "Use this computer: " + relay.DeviceName()
		}
		res, _, err := askStep("Connect this computer to MeetStream?", option)
		if err != nil || res != menuChosen {
			return true, res, err
		}
	}
	if w.creds == nil {
		if !w.interactive {
			return false, menuQuit, &ExitError{Code: 2, Message: "this computer isn't connected to MeetStream", Hint: "run: meetstream agent login"}
		}
		t, err := setupRelayLogin(w.cmd.Context(), relay.LoginOptions{OpenBrowser: login.Open, ShowURL: func(u string) {
			ui.Note("Opening your browser to connect this computer. If it doesn't open:")
			ui.Command(u)
		}})
		if errors.Is(err, relay.ErrNotOffered) {
			return true, menuQuit, errRelayNotEnabled()
		}
		if err != nil {
			return true, menuQuit, &ExitError{Code: 1, Message: err.Error(), Hint: "check your connection, then run meetstream setup again"}
		}
		w.creds = &relay.Credentials{DeviceID: t.DeviceID, RefreshToken: t.RefreshToken, AuthURL: relay.AuthURL(), RelayURL: relay.RelayURL()}
		if err := store.Save(w.creds); err != nil {
			return true, menuQuit, err
		}
	}
	if !g.JSON {
		ui.OK(relay.DeviceName() + "  " + ui.DimStyle.Render(w.creds.DeviceID+" · "+w.creds.RelayURL))
	}
	return w.interactive, menuChosen, nil
}

// step prints this step's header, e.g. "3/7  Ears and voice".
func (w *setupWizard) step(title string) {
	if !g.JSON {
		ui.Step(w.at, w.total, title)
	}
}

type setupStep struct {
	id  string
	run func() (bool, menuResult, error)
}

// steps are the chosen mode's steps; step 1 is always the mode.
func (w *setupWizard) steps() []setupStep {
	s := []setupStep{{"mode", w.stepMode}}
	switch w.mode {
	case modeHosted:
		s = append(s, setupStep{"mia", w.stepMIA})
	case modeBYOK:
		s = append(s, setupStep{"stack", w.stepStack}, setupStep{"mia", w.stepMIA})
	default:
		s = append(s, setupStep{"agent", w.stepAgent}, setupStep{"model", w.stepModel}, setupStep{"folder", w.stepFolder},
			setupStep{"access", w.stepAccess}, setupStep{"name", w.stepName}, setupStep{"test", w.stepTest},
			setupStep{"voice", w.stepVoiceKeys}, setupStep{"connect", w.stepConnect}, setupStep{"mia", w.stepMIA})
	}
	return append(s, setupStep{"calendar", w.stepCalendar})
}

// sections are the pipeline sections setup sends: the local agent is its own brain.
func (w *setupWizard) sections() []string {
	if w.mode == modeLocal {
		return []string{secEars, secVoice}
	}
	return pipelineSections
}

// checkFlags rejects flag combinations before anything is asked.
func (w *setupWizard) checkFlags(cmd *cobra.Command) error {
	if !contains([]string{modeHosted, modeBYOK, modeLocal}, w.mode) {
		return usageErr("invalid --mode "+w.mode, "choose hosted, byok, or local-agent")
	}
	if !contains([]string{keysMeetStream, keysOwn}, w.voiceKeys) {
		return usageErr("invalid --voice-keys "+w.voiceKeys, "choose meetstream or own")
	}
	if w.mode != modeLocal {
		for _, f := range []string{"voice-keys", "agent", "model", "effort", "agent-config", "owner", "test", "new-agent"} {
			if cmd.Flags().Changed(f) {
				return usageErr("--"+f+" works with --mode local-agent", "")
			}
		}
	}
	if w.newAgent && w.configID != "" {
		return usageErr("--new-agent and --agent-config cannot be used together", "")
	}
	if w.mode == modeLocal && w.brainFlag != "" {
		return usageErr("--brain does not apply to --mode local-agent", "your agent CLI is the brain; pick its model with --model")
	}
	return nil
}

// resolvePicks sets what each section runs on: an earlier choice in this
// setup when its key source still matches, else the flag, else the catalog's
// default. A mode that needs MeetStream keys the server does not offer stops
// with exit 3.
func (w *setupWizard) resolvePicks() error {
	managed := map[string]bool{}
	switch w.mode {
	case modeHosted:
		managed = map[string]bool{secEars: true, secBrain: true, secVoice: true}
		if !w.cat.hostedOffered(pipelineSections...) {
			return &ExitError{Code: 3, Message: "MeetStream hosted is not offered on this server", Hint: "use --mode byok (your keys), or --mode local-agent --voice-keys own"}
		}
	case modeLocal:
		// Updating an agent keeps its ears and voice: what this server offers
		// for new ones does not matter.
		if !w.cat.hostedOffered(secEars, secVoice) && w.voiceKeys == keysMeetStream && w.targetID() == "" {
			if w.interactive && !w.voiceKeysFlag {
				w.voiceKeys = keysOwn
			} else {
				return &ExitError{Code: 3, Message: "MeetStream ears and voice are not offered on this server", Hint: "use --voice-keys own (your provider keys)"}
			}
		}
		m := w.voiceKeys == keysMeetStream
		managed = map[string]bool{secEars: m, secVoice: m}
	}
	flags := map[string][2]string{secEars: {"ears", w.earsFlag}, secBrain: {"brain", w.brainFlag}, secVoice: {"voice", w.voiceFlag}}
	picks := map[string]sectionPick{}
	for _, s := range w.sections() {
		if p, ok := w.picks[s]; ok && p.Managed == managed[s] {
			picks[s] = p
			continue
		}
		if f := flags[s]; f[1] != "" {
			p, err := parsePickFlag(f[0], s, f[1], managed[s], w.cat)
			if err != nil {
				return err
			}
			picks[s] = p
			continue
		}
		picks[s] = w.cat.defaultPick(s, managed[s])
	}
	w.picks = picks
	return nil
}

// stepMode is step 1: how MIA runs. --mode (or --agent, today's local agent
// setup) skips the menu unless Back returns here.
func (w *setupWizard) stepMode() (bool, menuResult, error) {
	w.step("How MIA runs")
	asked := false
	if w.interactive && (!w.modeFlag || w.modeAgain) {
		w.modeAgain = false
		res, i, err := ask(modeMenu(w.cat, w.mode))
		if err != nil || res != menuChosen {
			return true, res, err
		}
		if id := modeRows(w.cat)[i].id; id != w.mode {
			w.mode, w.picks = id, nil
		}
		asked = true
	}
	if err := w.resolvePicks(); err != nil {
		return asked, menuQuit, err
	}
	if !g.JSON {
		for _, r := range modeRows(w.cat) {
			if r.id == w.mode {
				ui.OK(r.label + "  " + ui.DimStyle.Render(w.quote().total()))
			}
		}
	}
	return asked, menuChosen, nil
}

// stepVoiceKeys is the local agent's ears and voice: MeetStream keys or yours.
func (w *setupWizard) stepVoiceKeys() (bool, menuResult, error) {
	w.step("Ears and voice")
	if id := w.targetID(); id != "" {
		if !g.JSON {
			ui.OK("Updating MIA agent " + id + ": it keeps its ears and voice.")
		}
		return false, menuChosen, nil
	}
	asked := false
	if w.interactive && !w.voiceKeysFlag {
		both := []string{secEars, secVoice}
		ms := quoteFor(w.cat, w.cat.managedPicks(), both)
		own := quoteFor(w.cat, w.cat.ownPicks(), both)
		hints := []string{ms.total() + " total", own.total()}
		if ms.Total == nil {
			hints[0] = ms.total()
		}
		offered := w.cat.hostedOffered(both...)
		if !offered {
			hints[0] = notOffered
		}
		details := []string{
			"MeetStream runs speech to text (" + w.cat.defaultPick(secEars, true).String() + ") and the voice (" + w.cat.defaultPick(secVoice, true).String() + ") on its keys.\n" + ms.breakdown(),
			"Your own provider keys for speech to text and the voice. Add them in the dashboard: " + keysURL() + " (setup never asks for keys).\n" + own.breakdown(),
		}
		cursor := 0
		if w.voiceKeys == keysOwn {
			cursor = 1
		}
		m := newStepMenu("Ears and voice for your agent?", "MeetStream", "Your keys").withHints(hints).withDetails(details).at(cursor)
		m.disabled = []bool{!offered, false}
		res, i, err := ask(m)
		if err != nil || res != menuChosen {
			return true, res, err
		}
		w.voiceKeys = []string{keysMeetStream, keysOwn}[i]
		if err := w.resolvePicks(); err != nil {
			return true, menuQuit, err
		}
		asked = true
	}
	if !g.JSON {
		ui.OK("Ears: " + w.pickText(secEars) + "  Voice: " + w.pickText(secVoice) + "  " + ui.DimStyle.Render(w.quote().total()))
	}
	return asked, menuChosen, nil
}

// stackQuestions are the BYOK step's questions, one per section.
var stackQuestions = []struct{ section, question string }{
	{secEars, "Ears (speech to text)?"}, {secBrain, "Brain (LLM)?"}, {secVoice, "Voice (text to speech)?"},
}

// stepStack is BYOK's ears, brain and voice on your own keys. Back from the
// brain or the voice returns to the question before it.
func (w *setupWizard) stepStack() (bool, menuResult, error) {
	w.step("Ears, brain and voice")
	if w.interactive {
		for i := 0; i < len(stackQuestions); {
			sec := stackQuestions[i].section
			choices := w.cat.choicesFor(sec)
			cur := w.picks[sec]
			cursor := -1
			for j, c := range choices {
				if c.Provider == cur.Provider && c.Model == cur.Model {
					cursor = j
				}
			}
			if cursor < 0 { // a --flag value the list does not have
				choices, cursor = append([]sectionPick{cur}, choices...), 0
			}
			var labels, hints, details []string
			for _, c := range choices {
				labels = append(labels, c.Provider+" · "+c.Model)
				hints = append(hints, "your "+c.Provider+" key")
				details = append(details, "Runs on your "+c.Provider+" key; "+c.Provider+" bills you for it. Add the key in the dashboard: "+keysURL())
			}
			res, j, err := ask(newStepMenu(stackQuestions[i].question, labels...).withHints(hints).withDetails(details).at(cursor))
			if err != nil || res == menuQuit {
				return true, menuQuit, err
			}
			if res == menuBack {
				if i == 0 {
					return true, menuBack, nil
				}
				i--
				continue
			}
			w.picks[sec] = choices[j]
			i++
		}
	}
	if !g.JSON {
		ui.OK("Ears: " + w.pickText(secEars) + "  Brain: " + w.pickText(secBrain) + "  Voice: " + w.pickText(secVoice))
		ui.Note("Add your provider keys in the dashboard: " + keysURL() + " (setup never asks for keys)")
	}
	return w.interactive, menuChosen, nil
}

func (w *setupWizard) pickText(section string) string {
	p := w.picks[section]
	if p.Managed {
		return p.String() + " (MeetStream key)"
	}
	return p.String() + " (your key)"
}

// quote is the price of what setup will create.
func (w *setupWizard) quote() quote {
	if w.mode == modeLocal && w.targetID() != "" {
		return quote{Note: "Price unchanged: the MIA agent keeps its ears and voice."}
	}
	return quoteFor(w.cat, w.picks, w.sections())
}

// summary is the MIA step's detail line: what runs where, and the price.
func (w *setupWizard) summary() string {
	var parts []string
	if w.mode == modeLocal {
		parts = append(parts, "Brain: "+w.spec.Label+" on this computer ("+orDefault(w.model, "the CLI's default")+")")
		if w.targetID() == "" {
			parts = append(parts, "Ears: "+w.pickText(secEars), "Voice: "+w.pickText(secVoice))
		}
	} else {
		parts = append(parts, "Ears: "+w.pickText(secEars), "Brain: "+w.pickText(secBrain), "Voice: "+w.pickText(secVoice))
	}
	return strings.Join(parts, " · ") + "\n" + w.quote().breakdown()
}

func (w *setupWizard) defaultName() string {
	switch {
	case w.name != "":
		return w.name
	case w.mode == modeLocal:
		return w.spec.Label + " in the meeting"
	}
	return "MIA"
}

// stepMIA confirms the MIA agent to create or update, with its price. Hosted
// and BYOK ask for its name first; Back from the confirm asks the name again.
func (w *setupWizard) stepMIA() (bool, menuResult, error) {
	w.step("Your MIA agent")
	if !w.interactive {
		return false, menuChosen, nil
	}
	option := func() string {
		switch {
		case w.dryRun:
			return "Show the request (--dry-run, nothing sent)"
		case w.targetID() != "":
			return "Update MIA agent " + w.targetID()
		}
		return "Create MIA agent \"" + w.defaultName() + "\""
	}
	if w.mode == modeLocal && w.configID == "" && w.savedConfigID != "" && !w.dryRun {
		// The last setup made one: update it, or make another.
		create := "Create a new MIA agent \"" + w.defaultName() + "\""
		was := w.newAgent
		w.newAgent = false
		update := w.summary()
		w.newAgent = true
		fresh := w.summary()
		w.newAgent = was
		cursor := 0
		if was {
			cursor = 1
		}
		res, i, err := ask(newStepMenu("Update your MIA agent for "+w.spec.Label+"?", "Update MIA agent "+w.savedConfigID, create).
			withDetails([]string{update, fresh}).at(cursor))
		if err == nil && res == menuChosen {
			w.newAgent = i == 1
		}
		return true, res, err
	}
	if w.mode == modeLocal {
		res, _, err := ask(newStepMenu("Create the MIA agent for "+w.spec.Label+"?", option()).withDetails([]string{w.summary()}))
		return true, res, err
	}
	for {
		res, name, err := askText("Name your MIA agent", w.defaultName())
		if err != nil || res != menuChosen {
			return true, res, err
		}
		w.name = name
		res, _, err = ask(newStepMenu("Create this MIA agent?", option()).withDetails([]string{w.summary()}))
		if err != nil || res == menuQuit {
			return true, menuQuit, err
		}
		if res == menuChosen {
			return true, menuChosen, nil
		}
	}
}

// calendarChoices are the calendar step's rows: provider id, label and detail line.
// Microsoft is left out until its sign-in app is live (calendar connect
// --provider microsoft still reaches it).
var calendarChoices = []struct{ id, label, detail string }{
	{"google", "Google", "Sign in with Google in your browser; MIA joins meetings you host or accepted."},
	{"", "Skip", "Connect later with: meetstream calendar connect"},
}

// stepCalendar is the last step (optional): ask which calendar to connect once
// the MIA agent exists. Skipped with --no-calendar, --dry-run, --json or --no-input.
func (w *setupWizard) stepCalendar() (bool, menuResult, error) {
	w.calendar = ""
	if !w.interactive || w.noCalendar || w.dryRun {
		return false, menuChosen, nil
	}
	w.step("Calendar (optional)")
	labels, details := []string{}, []string{}
	for _, c := range calendarChoices {
		labels, details = append(labels, c.label), append(details, c.detail)
	}
	res, i, err := ask(newStepMenu("Connect your calendar so MIA can join your meetings?", labels...).withDetails(details))
	if err != nil || res != menuChosen {
		return true, res, err
	}
	w.calendar = calendarChoices[i].id
	return true, menuChosen, nil
}

// run walks the chosen mode's steps. Back (Esc or "← Back") returns to the last
// step that asked something; Esc at step 1, or q anywhere, cancels with exit 130.
// Back to a step a flag answered (the mode, the agent) asks it again. start
// skips to that step (the last setup's answers, straight to its test). Each
// finished step saves the answers so far.
func (w *setupWizard) run(start string) error {
	asked := map[string]bool{}
	i := 0
	for j, s := range w.steps() {
		if s.id == start {
			i = j
		}
	}
	for {
		steps := w.steps()
		if i >= len(steps) {
			return nil
		}
		w.at, w.total = i+1, len(steps)
		a, res, err := steps[i].run()
		if err != nil {
			return err
		}
		asked[steps[i].id] = a
		if res == menuChosen {
			_ = w.saveAnswers() // warned there; setup goes on, and finish saves again
		}
		switch res {
		case menuQuit:
			return errSetupCancelled
		case menuBack:
			if i == 0 {
				return errSetupCancelled
			}
			steps = w.steps()
			i--
			for i > 0 && !asked[steps[i].id] { // skip steps that asked nothing (nothing to change)
				i--
			}
			switch steps[i].id {
			case "mode":
				w.modeAgain = true
			case "agent":
				w.pickAgain = true
			}
			continue
		}
		i++
	}
}

func newAgentSetupCmd(info BuildInfo) *cobra.Command {
	w := &setupWizard{info: info}
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Set up MIA for your meetings: MeetStream hosted, your own keys, or your agent CLI on this computer",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			w.cmd = cmd
			w.interactive = setupInteractive()
			w.modelFlags = cmd.Flags().Changed("model") || cmd.Flags().Changed("effort")
			w.modeFlag = cmd.Flags().Changed("mode") || cmd.Flags().Changed("agent")
			w.voiceKeysFlag = cmd.Flags().Changed("voice-keys")
			w.workdirFlag = cmd.Flags().Changed("workdir")
			w.accessFlag = cmd.Flags().Changed("access")
			w.ownerFlag = cmd.Flags().Changed("owner")
			w.useMeetingMD = true
			if w.effort != "" && !effortPattern.MatchString(w.effort) {
				return usageErr("invalid --effort", "for example: low, medium, high")
			}
			if err := w.checkFlags(cmd); err != nil {
				return err
			}
			if !g.JSON {
				ui.Header(info.Version, "Bring an AI agent into your meetings")
				ui.Note(miaIntro)
			}
			w.cat = fetchCatalog(w)
			if w.mode == modeLocal {
				w.loadLast()
			}
			start := ""
			if w.interactive && w.last != nil && !w.modeFlag {
				again, err := w.startWithLast()
				if err != nil {
					return err
				}
				if again {
					start = "test"
				}
			}
			if err := w.run(start); err != nil {
				return err
			}
			return w.finish()
		},
	}
	f := cmd.Flags()
	f.StringVar(&w.mode, "mode", modeLocal, "how MIA runs: hosted (MeetStream keys), byok (your provider keys), local-agent (your agent CLI on this computer)")
	f.StringVar(&w.voiceKeys, "voice-keys", keysMeetStream, "local-agent: ears and voice on MeetStream keys (meetstream) or your own provider keys (own)")
	f.StringVar(&w.earsFlag, "ears", "", "speech to text as provider:model (default: the hosted catalog's default)")
	f.StringVar(&w.brainFlag, "brain", "", "hosted/byok: the LLM as provider:model (default: the hosted catalog's default)")
	f.StringVar(&w.voiceFlag, "voice", "", "text to speech as provider:model (default: the hosted catalog's default)")
	f.StringVar(&w.agentName, "agent", "", "agent CLI: "+strings.Join(agentOrder, ", ")+" (default: pick, or the first installed)")
	f.StringVar(&w.model, "model", "", "model the agent CLI uses (default: pick, or the CLI's own default)")
	f.StringVar(&w.effort, "effort", "", "reasoning effort where the CLI supports it, e.g. low, medium, high (default: the CLI's own)")
	f.StringVar(&w.maxAccess, "access", "readonly", "the most access this computer allows: readonly (meeting only), confirm (ask first), full")
	f.StringVar(&w.workdir, "workdir", "", "folder the agent works in (default: the last setup's, else ~/MeetStream Agent)")
	f.StringVar(&w.owner, "owner", "", "your name as it shows in meetings (default: the last setup's, else $USER)")
	f.BoolVar(&w.testFlag, "test", false, "run the test turn even without a terminal (--no-input, --json)")
	f.BoolVar(&w.newAgent, "new-agent", false, "create a new MIA agent instead of updating the one the last setup made")
	f.StringVar(&w.configID, "agent-config", "", "update this MIA agent instead of creating one (keeps its voice and prompts)")
	f.StringVar(&w.name, "name", "", "name of the new MIA agent")
	f.BoolVar(&w.dryRun, "dry-run", false, "print the MIA agent request and its price without sending it")
	f.BoolVar(&w.noStart, "no-start", false, "don't offer to start serving")
	f.BoolVar(&w.noCalendar, "no-calendar", false, "don't offer to connect a calendar")
	return cmd
}

var effortPattern = regexp.MustCompile(`^[a-z]{2,12}$`)

// sectionBody is one pipeline section of the request. A section on your own
// keys carries no key_source (the API reads that as your key), so a section you
// have no key for can fall back to a MeetStream key; the response says so.
func (w *setupWizard) sectionBody(s string) map[string]any {
	p := w.picks[s]
	b := map[string]any{}
	if p.Provider != "" {
		b["provider"] = p.Provider
	}
	if p.Model != "" {
		b["model"] = p.Model
	}
	if p.Managed {
		b["key_source"] = keysMeetStream
	}
	switch s {
	case secEars:
		b["language"] = "en"
	case secVoice:
		if id := defaultVoiceID[p.Provider]; id != "" {
			b["voice_id"] = id
		}
	case secBrain:
		b["system_prompt"] = defaultSystemPrompt
	}
	return b
}

// fallbackQuote prices the agent after the API moved sections you have no key
// for onto MeetStream keys, at the rates the response sent.
func (w *setupWizard) fallbackQuote(sections []string, rates any) quote {
	c := w.cat
	if r := parseCatalog(map[string]any{"rates": rates}); r.Base != nil {
		c.Base, c.Agent, c.Managed = r.Base, r.Agent, r.Managed
	}
	picks := map[string]sectionPick{}
	for k, v := range w.picks {
		picks[k] = v
	}
	for _, s := range sections {
		p := picks[s]
		p.Managed = true
		picks[s] = p
	}
	return quoteFor(c, picks, w.sections())
}

// finish creates (or updates) the MIA agent, saves the local agent's setup,
// then connects the calendar picked at the end and offers to start serving.
func (w *setupWizard) finish() error {
	cmd := w.cmd
	// Again here: the last step may have switched from updating to a new agent.
	if err := w.resolvePicks(); err != nil {
		return err
	}
	name := w.defaultName()
	body := map[string]any{"agent_name": name, "mode": "pipeline", "agent": map[string]any{"response_modality": "audio"}}
	for _, s := range w.sections() {
		body[s] = w.sectionBody(s)
	}
	var local map[string]any
	if w.mode == modeLocal {
		block := map[string]any{"transport": "relay", "device_id": w.creds.DeviceID, "label": w.spec.Label, "access": w.maxAccess, "require_code": true}
		modelBlock := map[string]any{"provider": "local_agent", "local_agent": block}
		if w.model != "" {
			modelBlock["model"] = w.model // MIA passes model.model to the agent CLI; "" keeps the CLI's default
		}
		body["model"] = modelBlock
		local = map[string]any{"agent": w.spec.Name, "model": w.model, "effort": w.effort, "workdir": w.workdir,
			"access": w.maxAccess, "owner": w.owner, "ignore_rules": w.ignoreRules, "meeting_md": w.useMeetingMD, "test": w.testResult}
	}
	q := w.quote()
	target := w.targetID()
	if w.dryRun {
		out := map[string]any{"dry_run": true, "mode": w.mode, "method": map[bool]string{true: "PUT", false: "POST"}[target != ""], "agent_config_id": target, "body": body}
		if local != nil {
			out["setup"] = local
		}
		q.jsonFields(out)
		return emit(out, func() {
			b, _ := json.MarshalIndent(body, "  ", "  ")
			ui.Note("Would send (nothing sent, --dry-run):")
			fmt.Fprintln(ui.Out, "  "+string(b))
			ui.Note("Price per hour: " + q.breakdown())
			if local != nil {
				ui.Note("Would save for this computer: model " + orDefault(w.model, "the CLI's default") + ", effort " + orDefault(w.effort, "the CLI's default"))
			}
		})
	}
	key, _ := config.ResolveKey()
	if key == "" {
		return &ExitError{Code: 2, Message: "an API key is needed to create the MIA agent", Hint: "run: meetstream auth set-key  (or meetstream login)"}
	}
	c := api.New(config.APIURL(), key, w.info.Version)
	var resp any
	var err error
	if target != "" {
		// Update only the model block of an existing agent; keep its voice, prompts and tools.
		var existing map[string]any
		err = c.Do(cmd.Context(), http.MethodGet, "/mia?agent_config_id="+target, nil, &existing)
		var ae *api.Error
		if err != nil && target != w.configID && errors.As(err, &ae) && ae.Status == http.StatusNotFound {
			// The last setup's agent was deleted: make a new one.
			if !g.JSON {
				ui.Note("Your last MIA agent (" + target + ") is gone; creating a new one.")
			}
			target, w.newAgent, err = "", true, nil
			if err := w.resolvePicks(); err != nil { // a new one needs ears and voice this server offers
				return err
			}
			for _, s := range w.sections() {
				body[s] = w.sectionBody(s)
			}
			q = w.quote()
		} else if err != nil {
			return apiError(err)
		}
		if target != "" {
			cfg := existing
			if inner, ok := existing["agent_config"].(map[string]any); ok {
				cfg = inner
			}
			if cfg == nil {
				cfg = map[string]any{}
			}
			cfg["agent_config_id"] = target
			old := cfg["model"]
			if old == nil {
				old = cfg["Model"] // the stored shape's key
			}
			cfg["model"] = mergeModelBlock(old, body["model"])
			b, _ := json.Marshal(cfg)
			err = c.Do(cmd.Context(), http.MethodPut, "/mia", bytes.NewReader(b), &resp)
		}
	}
	if target == "" {
		b, _ := json.Marshal(body)
		err = c.Do(cmd.Context(), http.MethodPost, "/mia", bytes.NewReader(b), &resp)
	}
	if err != nil {
		var ae *api.Error
		if errors.As(err, &ae) && w.mode == modeLocal && relayRefused(ae) {
			return errRelayNotEnabled() // never the raw API text: the server only knows tunnel agents
		}
		if errors.As(err, &ae) && ae.Status == http.StatusBadRequest && strings.Contains(ae.Detail(), "API key") {
			return &ExitError{Code: 1, Message: ae.Detail(), Hint: "add your provider keys in the dashboard: " + keysURL()}
		}
		return apiError(err)
	}
	m, _ := resp.(map[string]any)
	id := target
	if m != nil && id == "" {
		id = first(m, "agent_config_id", "id")
	}
	if w.mode == modeLocal && w.configID == "" {
		w.miaID = id // the next setup updates this one
	}
	var fallback []string
	if list, ok := m["managed_key_sections"].([]any); ok {
		for _, s := range list {
			if name, ok := s.(string); ok && contains(w.sections(), name) {
				fallback = append(fallback, name)
			}
		}
	}
	if len(fallback) > 0 {
		q = w.fallbackQuote(fallback, m["mia_managed_rates"])
	}

	out := map[string]any{"mode": w.mode, "agent_config_id": id, "name": name}
	if w.mode == modeLocal {
		if err := w.saved().Save(""); err != nil {
			return err
		}
		for k, v := range map[string]any{"agent": w.spec.Name, "device_id": w.creds.DeviceID, "access": w.maxAccess, "workdir": w.workdir, "model": w.model, "effort": w.effort,
			"owner": w.owner, "ignore_rules": w.ignoreRules, "meeting_md": w.useMeetingMD, "test": w.testResult, "login": w.loginState, "updated": target != ""} {
			out[k] = v
		}
	}
	q.jsonFields(out)
	if len(fallback) > 0 {
		out["managed_key_sections"] = fallback
	}
	ownKeys := false
	for _, s := range w.sections() {
		ownKeys = ownKeys || !w.picks[s].Managed
	}
	if err := emit(out, func() {
		if len(fallback) > 0 {
			words := make([]string, len(fallback))
			for i, s := range fallback {
				words[i] = sectionWord[s]
			}
			ui.Warn("No saved key for the " + strings.Join(words, " and ") + ": " + map[bool]string{true: "it runs", false: "they run"}[len(words) == 1] + " on MeetStream keys instead, billed per hour.")
			ui.Note("Add your keys in the dashboard (" + keysURL() + "), then edit the agent to use them.")
		} else if ownKeys {
			ui.Note("Add your provider keys in the dashboard: " + keysURL() + " (setup never asks for keys)")
		}
		ui.OK("MIA agent " + ui.BoldStyle.Render(name) + "  " + ui.DimStyle.Render(id))
		fmt.Fprintln(ui.Out)
		rows := [][2]string{{"price", q.breakdown()}}
		if w.mode == modeLocal {
			how := "one run per turn"
			if w.spec.Warm != "" {
				how = "warm session per meeting"
			}
			rows = append([][2]string{{"agent", w.spec.Label + " (" + how + ")"}, {"model", orDefault(w.model, "the CLI's default")}, {"effort", orDefault(w.effort, "the CLI's default")},
				{"access", w.maxAccess}, {"folder", ui.ShortPath(w.workdir)}, {"context", meetingctx.Summary(w.spec, w.workdir, w.ignoreRules, w.useMeetingMD)},
				{"your name", orDefault(w.owner, "-")}, {"test", orDefault(w.testResult, "skipped")}}, rows...)
		} else {
			rows = append([][2]string{{"ears", w.pickText(secEars)}, {"brain", w.pickText(secBrain)}, {"voice", w.pickText(secVoice)}}, rows...)
		}
		ui.Box("Ready", append(rows, [2]string{"mia agent", id}))
		if w.mode == modeLocal {
			if w.loginState == "expired" {
				ui.Fail(w.spec.Label + " is signed out. Sign in again with:")
				ui.Command(w.spec.Relogin)
			}
			if w.maxAccess == "full" {
				ui.Warn("FULL access: the agent may run shell commands and change files without asking, but only in")
				ui.Note("meetings where only you (by verified id or activation code) can talk to it; otherwise it asks.")
			}
			ui.Note("Start your agent, then send MIA to a meeting:")
			ui.Command("meetstream agent serve")
		} else {
			ui.Note("Send MIA to a meeting:")
		}
		ui.Command("meetstream join <meeting-url> --agent-config " + id)
	}); err != nil {
		return err
	}
	if w.calendar != "" {
		if err := w.connectCalendar(calendarBotConfig(cmd.Context(), c, id), name); err != nil {
			return err
		}
	}
	if w.mode == modeLocal && w.interactive && !w.noStart {
		fmt.Fprint(ui.Out, "\n  Start serving now? [Y/n] ")
		var ans string
		_, _ = fmt.Fscanln(os.Stdin, &ans)
		if a := strings.ToLower(strings.TrimSpace(ans)); a == "" || a == "y" || a == "yes" {
			root := cmd.Root()
			root.SetArgs([]string{"agent", "serve"})
			return root.Execute()
		}
	}
	return nil
}

// mergeModelBlock is the saved agent's model block with setup's answers:
// provider, local_agent and the agent CLI's model (removed for the CLI's
// default, since MIA passes model.model to the CLI). Everything else in it
// (system_prompt, temperature, …) is kept.
func mergeModelBlock(existing, update any) any {
	up, ok := update.(map[string]any)
	if !ok {
		return existing
	}
	out := map[string]any{}
	if old, ok := existing.(map[string]any); ok {
		for k, v := range old {
			out[k] = v
		}
	}
	for _, k := range []string{"provider", "local_agent"} {
		if v, ok := up[k]; ok {
			out[k] = v
		}
	}
	if v, ok := up["model"]; ok {
		out["model"] = v
	} else {
		delete(out, "model")
	}
	return out
}

// calendarBotConfig is the calendar's default bot with the MIA agent setup made
// (agent_config_id, as create_bot takes it): the auto-join bot already saved,
// else a bot named MIA. The server stores the config it is given as a whole,
// so the saved one is the base.
func calendarBotConfig(ctx context.Context, c *api.Client, agentID string) map[string]any {
	if agentID == "" {
		return nil
	}
	cfg := map[string]any{"bot_name": "MIA", "audio_required": true, "video_required": false, "automatic_leave": map[string]any{"everyone_left_timeout": 60}}
	var s struct {
		Enabled bool           `json:"auto_schedule_enabled"`
		Config  map[string]any `json:"default_bot_config"`
	}
	if err := c.Do(ctx, http.MethodGet, "/calendar/auto-schedule/settings", nil, &s); err == nil && s.Enabled && len(s.Config) > 0 {
		cfg = s.Config
	}
	cfg["agent_config_id"] = agentID
	return cfg
}

// connectCalendar runs the calendar sign-in picked in step 5, with bot (the
// MIA agent's default bot config; nil keeps the server's). A server without
// one-click connect, or a failure, leaves a note: the MIA agent already exists.
func (w *setupWizard) connectCalendar(bot map[string]any, agentName string) error {
	if id, _ := bot["agent_config_id"].(string); id != "" && !g.JSON {
		ui.Note("Bots that join your calendar meetings will run MIA agent " + agentName + " (" + id + ").")
	}
	_, err := runCalendarConnect(w.cmd, calendarConnectOptions{Provider: w.calendar, DefaultBotConfig: bot})
	var ee *ExitError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ee) && ee.Code == 130:
		return err
	case errors.Is(err, errCalendarSkipped):
	case errors.As(err, &ee) && ee.Code == 3:
		ui.Note(ee.Message + ".")
	default:
		ui.Warn("Calendar not connected: " + err.Error())
	}
	ui.Note("Connect your calendar later with:")
	ui.Command("meetstream calendar connect")
	return nil
}

// newSetupShortcut is `meetstream setup`, the kit's entry point, as a short form of `agent setup`.
func newSetupShortcut(info BuildInfo) *cobra.Command {
	cmd := newAgentSetupCmd(info)
	cmd.GroupID = "agent"
	return cmd
}
