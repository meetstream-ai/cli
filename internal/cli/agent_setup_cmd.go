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
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/meetstream-ai/cli/internal/agentkit/access"
	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"github.com/meetstream-ai/cli/internal/agentkit/checks"
	kitconfig "github.com/meetstream-ai/cli/internal/agentkit/config"
	agentlogin "github.com/meetstream-ai/cli/internal/agentkit/login"
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
	ui.Warn("`" + filepath.Base(c.binary) + "` is in " + ui.ShortPath(dir) + ", which is not on your PATH. Add it to your shell profile:")
	ui.Command(`export PATH="` + dir + `:$PATH"`)
}

// ── picker ────────────────────────────────────────────────────────────────────

type pickModel struct {
	items  []agentChoice
	cursor int
	chosen *agentChoice
}

func (m pickModel) Init() tea.Cmd { return nil }

func (m pickModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "ctrl+c", "q", "esc":
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
	b.WriteString("\n  " + ui.BoldStyle.Render("Which agent should answer in your meetings?") + "\n\n")
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
	b.WriteString("\n  " + ui.DimStyle.Render("↑/↓ move · enter choose · esc/q quit") + "\n")
	return b.String()
}

// ── command ───────────────────────────────────────────────────────────────────

const setupSteps = 5

// setupWizard holds setup's flags and the answers so far. Each step returns
// whether it asked anything (Back skips steps that did not) and how it ended.
type setupWizard struct {
	cmd         *cobra.Command
	info        BuildInfo
	interactive bool

	agentName, maxAccess, workdir, configID, name, model, effort string
	dryRun, noStart, noCalendar, modelFlags                      bool

	chosen    *agentChoice
	spec      agents.Spec
	creds     *relay.Credentials
	pickAgain bool   // back at step 1 (or an install declined): show the picker even with --agent
	warmed    string // the agent whose warm path was already checked
	calendar  string // "google" or "microsoft" to connect at the end; "" skips
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
		if !g.JSON {
			ui.Step(1, setupSteps, "Your agent")
		}
		choices := detectAgents()
		prev := w.agentName
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
			if chosen = final.(pickModel).chosen; chosen == nil {
				return false, menuQuit, errSetupCancelled
			}
		default:
			for i := range choices {
				if choices[i].installed {
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
		if w.chosen != nil && w.chosen.name != chosen.name && !w.modelFlags {
			w.model, w.effort = "", "" // another agent: its own models
		}
		w.chosen, w.spec = chosen, spec
		st := (&agentlogin.Checker{Agent: spec, Binary: chosen.binary}).Check(true)
		if !g.JSON {
			ui.OK(spec.Label + "  " + ui.DimStyle.Render(ui.ShortPath(chosen.binary)))
			if !st.Usable() {
				ui.Warn(spec.Label + " needs a sign-in: run " + ui.CmdStyle.Render(spec.Relogin))
			}
		}
		if !access.Valid(w.maxAccess) {
			return false, menuQuit, usageErr("invalid --access", "choose readonly, confirm, or full")
		}
		if w.workdir == "" {
			w.workdir, _ = os.Getwd()
		}
		if abs, err := filepath.Abs(w.workdir); err == nil {
			w.workdir = abs
		}
		if w.interactive && st.Usable() && w.warmed != spec.Name && warmcheck.AppliesTo(spec) {
			w.warmed = spec.Name
			w.warmCheck()
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

// warmCheck tests the warm (ACP) path meetings use, one stage at a time with a
// spinner. A failure warns with its fix; setup goes on.
func (w *setupWizard) warmCheck() {
	type stage struct{ event, doing, done string }
	stages := []stage{{"ready", "Starting " + w.spec.Label + " (ACP)", "Session ready"}}
	if w.spec.ACPMCP {
		stages = append(stages, stage{"tool_call", "Calling a meeting tool", "Tool called"})
	}
	stages = append(stages, stage{"done", "Answering", "Answered"})
	at := 0
	stop := spin(stages[0].doing)
	c := kitconfig.Config{Agent: w.spec, Binary: w.chosen.binary, Workdir: w.workdir, Owner: setupOwner()}
	r := setupWarmCheck(w.cmd.Context(), c, warmcheck.Options{Turn: true, OnStage: func(event string, elapsed time.Duration) {
		for i := at; i < len(stages); i++ {
			if stages[i].event != event {
				continue
			}
			stop()
			if event != "done" {
				ui.OK(fmt.Sprintf("%s  %s", stages[i].done, ui.DimStyle.Render(fmt.Sprintf("%.1f s", elapsed.Seconds()))))
			}
			at = i + 1
			if at < len(stages) {
				stop = spin(stages[at].doing)
			}
			return
		}
	}})
	stop()
	if r.OK {
		ui.OK(fmt.Sprintf("Warm mode (ACP): %s (%.1f s)", r.Message, r.Total.Seconds()))
		return
	}
	ui.Warn("Warm mode (ACP): " + r.Message)
	if r.Fix != "" {
		ui.Note("Fix: " + r.Fix)
	}
	ui.Note("Setup goes on; meetings may fail or have no meeting tools until this is fixed.")
}

func setupOwner() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "the user"
}

// stepModel is step 2: the model and effort. --model/--effort (or a run that
// cannot ask) skip the menu; "" is the CLI's own default.
func (w *setupWizard) stepModel() (bool, menuResult, error) {
	if !g.JSON {
		ui.Step(2, setupSteps, "Model and effort")
	}
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

// stepConnect is step 3: this computer's relay sign-in.
func (w *setupWizard) stepConnect() (bool, menuResult, error) {
	if !g.JSON {
		ui.Step(3, setupSteps, "Connect this computer")
	}
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
		t, err := relay.Login(w.cmd.Context(), relay.LoginOptions{OpenBrowser: login.Open, ShowURL: func(u string) {
			ui.Note("Opening your browser to connect this computer. If it doesn't open:")
			ui.Command(u)
		}})
		if err != nil {
			return true, menuQuit, &ExitError{Code: 1, Message: err.Error(), Hint: "the relay is on for test stacks first; set MEETSTREAM_AUTH_URL and MEETSTREAM_RELAY_URL"}
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

// stepMIA is step 4: confirm the MIA agent to create or update.
func (w *setupWizard) stepMIA() (bool, menuResult, error) {
	if !g.JSON {
		ui.Step(4, setupSteps, "Your MIA agent")
	}
	if !w.interactive {
		return false, menuChosen, nil
	}
	title := w.name
	if title == "" {
		title = w.spec.Label + " in the meeting"
	}
	option := "Create MIA agent \"" + title + "\""
	switch {
	case w.dryRun:
		option = "Show the request (--dry-run, nothing sent)"
	case w.configID != "":
		option = "Update MIA agent " + w.configID
	}
	res, _, err := askStep("Create the MIA agent for "+w.spec.Label+"?", option)
	return true, res, err
}

// calendarChoices are step 5's rows: provider id, label and detail line.
var calendarChoices = []struct{ id, label, detail string }{
	{"google", "Google", "Sign in with Google in your browser; MIA joins meetings you host or accepted."},
	{"microsoft", "Microsoft", "Sign in with Microsoft in your browser; MIA joins meetings you host or accepted."},
	{"", "Skip", "Connect later with: meetstream calendar connect"},
}

// stepCalendar is step 5 (optional): ask which calendar to connect once the
// MIA agent exists. Skipped with --no-calendar, --dry-run, --json or --no-input.
func (w *setupWizard) stepCalendar() (bool, menuResult, error) {
	w.calendar = ""
	if !w.interactive || w.noCalendar || w.dryRun {
		return false, menuChosen, nil
	}
	if !g.JSON {
		ui.Step(5, setupSteps, "Calendar (optional)")
	}
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

// run walks the steps. Back (Esc or "← Back") returns to the last step that
// asked something; Esc or q at step 1, or q anywhere, cancels with exit 130.
func (w *setupWizard) run() error {
	steps := []func() (bool, menuResult, error){w.stepAgent, w.stepModel, w.stepConnect, w.stepMIA, w.stepCalendar}
	asked := make([]bool, len(steps))
	for i := 0; i < len(steps); {
		a, res, err := steps[i]()
		if err != nil {
			return err
		}
		asked[i] = a
		switch res {
		case menuQuit:
			return errSetupCancelled
		case menuBack:
			i--
			for i > 0 && !asked[i] { // skip steps that asked nothing (nothing to change)
				i--
			}
			if i == 0 {
				w.pickAgain = true
			}
			continue
		}
		i++
	}
	return nil
}

func newAgentSetupCmd(info BuildInfo) *cobra.Command {
	w := &setupWizard{info: info}
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Connect your agent CLI to MIA: pick it and its model, connect this computer, create the MIA agent",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			w.cmd = cmd
			w.interactive = setupInteractive()
			w.modelFlags = cmd.Flags().Changed("model") || cmd.Flags().Changed("effort")
			if w.effort != "" && !effortPattern.MatchString(w.effort) {
				return usageErr("invalid --effort", "for example: low, medium, high")
			}
			if !g.JSON {
				ui.Header(info.Version, "Bring your agent into the meeting")
			}
			if err := w.run(); err != nil {
				return err
			}
			return w.finish()
		},
	}
	f := cmd.Flags()
	f.StringVar(&w.agentName, "agent", "", "agent CLI: "+strings.Join(agentOrder, ", ")+" (default: pick, or the first installed)")
	f.StringVar(&w.model, "model", "", "model the agent CLI uses (default: pick, or the CLI's own default)")
	f.StringVar(&w.effort, "effort", "", "reasoning effort where the CLI supports it, e.g. low, medium, high (default: the CLI's own)")
	f.StringVar(&w.maxAccess, "access", "readonly", "what MIA's turns may do: readonly, confirm, full")
	f.StringVar(&w.workdir, "workdir", "", "folder the agent works in (default: here)")
	f.StringVar(&w.configID, "agent-config", "", "update this MIA agent instead of creating one (keeps its voice and prompts)")
	f.StringVar(&w.name, "name", "", "name of the new MIA agent")
	f.BoolVar(&w.dryRun, "dry-run", false, "print the MIA agent request without sending it")
	f.BoolVar(&w.noStart, "no-start", false, "don't offer to start serving")
	f.BoolVar(&w.noCalendar, "no-calendar", false, "don't offer to connect a calendar")
	return cmd
}

var effortPattern = regexp.MustCompile(`^[a-z]{2,12}$`)

// finish creates (or updates) the MIA agent, saves the setup, then connects the
// calendar picked in step 5 and offers to start serving.
func (w *setupWizard) finish() error {
	cmd, spec, creds := w.cmd, w.spec, w.creds
	name := w.name
	if name == "" {
		name = spec.Label + " in the meeting"
	}
	block := map[string]any{"transport": "relay", "device_id": creds.DeviceID, "label": spec.Label, "access": w.maxAccess, "require_code": true}
	modelBlock := map[string]any{"provider": "local_agent", "local_agent": block}
	if w.model != "" {
		modelBlock["model"] = w.model // MIA passes model.model to the agent CLI; "" keeps the CLI's default
	}
	body := map[string]any{
		"agent_name":  name,
		"mode":        "pipeline",
		"model":       modelBlock,
		"transcriber": map[string]any{"provider": "deepgram", "model": "nova-3", "language": "en", "key_source": "meetstream"},
		"voice":       map[string]any{"provider": "openai", "voice_id": "coral", "key_source": "meetstream"},
		"agent":       map[string]any{"response_modality": "audio"},
	}
	local := map[string]any{"agent": spec.Name, "model": w.model, "effort": w.effort, "workdir": w.workdir}
	if w.dryRun {
		return emit(map[string]any{"dry_run": true, "method": map[bool]string{true: "PUT", false: "POST"}[w.configID != ""], "agent_config_id": w.configID, "body": body, "setup": local}, func() {
			b, _ := json.MarshalIndent(body, "  ", "  ")
			ui.Note("Would send (nothing sent, --dry-run):")
			fmt.Fprintln(ui.Out, "  "+string(b))
			ui.Note("Would save for this computer: model " + orDefault(w.model, "the CLI's default") + ", effort " + orDefault(w.effort, "the CLI's default"))
		})
	}
	key, _ := config.ResolveKey()
	if key == "" {
		return &ExitError{Code: 2, Message: "an API key is needed to create the MIA agent", Hint: "run: meetstream auth set-key  (or meetstream login)"}
	}
	c := api.New(config.APIURL(), key, w.info.Version)
	var resp any
	var err error
	if w.configID != "" {
		// Update only the model block of an existing agent; keep its voice, prompts and tools.
		var existing map[string]any
		if err := c.Do(cmd.Context(), http.MethodGet, "/mia?agent_config_id="+w.configID, nil, &existing); err != nil {
			return apiError(err)
		}
		cfg := existing
		if inner, ok := existing["agent_config"].(map[string]any); ok {
			cfg = inner
		}
		cfg["agent_config_id"] = w.configID
		cfg["model"] = body["model"]
		b, _ := json.Marshal(cfg)
		err = c.Do(cmd.Context(), http.MethodPut, "/mia", bytes.NewReader(b), &resp)
	} else {
		b, _ := json.Marshal(body)
		err = c.Do(cmd.Context(), http.MethodPost, "/mia", bytes.NewReader(b), &resp)
	}
	if err != nil {
		var ae *api.Error
		if errors.As(err, &ae) && strings.Contains(ae.Detail(), "relay") {
			return &ExitError{Code: 1, Message: ae.Detail(), Hint: "relay agents are on for test stacks first; point MEETSTREAM_API_URL at one"}
		}
		return apiError(err)
	}
	id := w.configID
	if m, ok := resp.(map[string]any); ok && id == "" {
		id = first(m, "agent_config_id", "id")
	}

	saved := kitconfig.Setup{Agent: spec.Name, Workdir: w.workdir, MaxAccess: w.maxAccess, Tunnel: "relay", Model: w.model, Effort: w.effort}
	if err := saved.Save(""); err != nil {
		return err
	}
	out := map[string]any{"agent": spec.Name, "device_id": creds.DeviceID, "agent_config_id": id, "access": w.maxAccess, "workdir": w.workdir, "model": w.model, "effort": w.effort}
	if err := emit(out, func() {
		ui.OK("MIA agent " + ui.BoldStyle.Render(name) + "  " + ui.DimStyle.Render(id))
		fmt.Fprintln(ui.Out)
		ui.Box("Ready", [][2]string{{"agent", spec.Label}, {"model", orDefault(w.model, "the CLI's default")}, {"effort", orDefault(w.effort, "the CLI's default")}, {"access", w.maxAccess}, {"folder", ui.ShortPath(w.workdir)}, {"mia agent", id}})
		ui.Note("Start your agent, then send MIA to a meeting:")
		ui.Command("meetstream agent serve")
		ui.Command("meetstream join <meeting-url> --agent-config " + id)
	}); err != nil {
		return err
	}
	if w.calendar != "" {
		if err := w.connectCalendar(); err != nil {
			return err
		}
	}
	if w.interactive && !w.noStart {
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

// connectCalendar runs the calendar sign-in picked in step 5. A server without
// one-click connect, or a failure, leaves a note: the MIA agent already exists.
func (w *setupWizard) connectCalendar() error {
	_, err := runCalendarConnect(w.cmd, calendarConnectOptions{Provider: w.calendar})
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
