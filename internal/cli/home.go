package cli

// The home app: `meetstream` with no arguments in a terminal. A full-screen
// app you stay in (q quits): actions run inside it with a spinner and show in
// a pane, Esc goes back to the menu, flows with prompts suspend the app and
// come back to it, and your agent for meetings can run in the background.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	meetstream "github.com/meetstream-ai/cli"
	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"github.com/meetstream-ai/cli/internal/agentkit/checks"
	kitconfig "github.com/meetstream-ai/cli/internal/agentkit/config"
	"github.com/meetstream-ai/cli/internal/agentkit/proctree"
	"github.com/meetstream-ai/cli/internal/agentkit/redact"
	"github.com/meetstream-ai/cli/internal/api"
	"github.com/meetstream-ai/cli/internal/config"
	"github.com/meetstream-ai/cli/internal/mascot"
	"github.com/meetstream-ai/cli/internal/ui"
)

type homeKind int

const (
	kindView   homeKind = iota // runs inside the app, result in the pane
	kindExec                   // a flow with prompts: suspends the app, then comes back
	kindBots                   // the bots table you can pick from
	kindSend                   // send a bot: a small form in the pane
	kindAgent                  // start or stop your agent for meetings
	kindDocs                   // the README in the pane
	kindDoctor                 // checks, the instant ones first
)

type homeItem struct {
	section string
	title   string
	desc    string
	kind    homeKind
	args    []string // kindExec: the subcommand
	loading string
	view    func(ctx context.Context, h *homeEnv) (string, error)
}

// homeEnv is what actions need: the command tree (for its context) and version.
type homeEnv struct {
	root    *cobra.Command
	version string
}

func (h *homeEnv) ctx() context.Context {
	if h.root != nil && h.root.Context() != nil {
		return h.root.Context()
	}
	return context.Background()
}

func (h *homeEnv) client() (*api.Client, error) {
	key, _ := config.ResolveKey()
	if key == "" {
		return nil, usageErr("not signed in", "choose Sign in")
	}
	return api.New(config.APIURL(), key, h.version), nil
}

// homeMenu is the menu for the current sign-in state.
func homeMenu() []homeItem {
	key, _ := config.ResolveKey()
	signIn := homeItem{section: "Get started", title: "Sign in", desc: "Browser sign-in; stores a named API key", kind: kindExec, args: []string{"login"}}
	items := []homeItem{}
	if key == "" {
		items = append(items, signIn)
	}
	items = append(items,
		homeItem{section: "Get started", title: "Bring an AI agent into my meetings", desc: "MeetStream-hosted, your own keys, or your own agent like Claude Code", kind: kindExec, args: []string{"setup"}},
		homeItem{section: "Get started", title: "Connect my calendar", desc: "MIA joins the meetings you host or accepted", kind: kindExec, args: []string{"calendar", "connect"}},
		homeItem{section: "Meetings", title: "Send a bot to a meeting", desc: "Google Meet, Zoom or Microsoft Teams link", kind: kindSend},
		homeItem{section: "Meetings", title: "My recent bots", desc: "Status, time and length; Enter opens the transcript", kind: kindBots},
		homeItem{section: "Meetings", title: "Transcripts", desc: "Pick a bot, then Enter to read what was said", kind: kindBots},
		homeItem{section: "Meetings", title: "My AI agents", desc: "How each runs, its voice, when it changed", kind: kindView, loading: "Loading your agents…", view: homeMiaList},
		homeItem{section: "Meetings", title: "Run my agent for meetings", desc: "Keeps your agent connected while this app is open", kind: kindAgent},
		homeItem{section: "Account & help", title: "Check my setup", desc: "API key, server, your agent CLI, sign-in, relay", kind: kindDoctor, loading: "Checking your agent…"},
		homeItem{section: "Account & help", title: "My calendar", desc: "Connected accounts and auto-join", kind: kindView, loading: "Loading your calendars…", view: homeCalendar},
		homeItem{section: "Account & help", title: "Balance", desc: "Prepaid balance and where to top up", kind: kindView, loading: "Loading your balance…", view: homeBalance},
		homeItem{section: "Account & help", title: "Docs", desc: "The README, in this window", kind: kindDocs},
	)
	if key != "" {
		signIn.section, signIn.title, signIn.desc = "Account & help", "Switch account", "Sign in again with another key"
		items = append(items, signIn)
	}
	return items
}

// ── background agent ─────────────────────────────────────────────────────────

// homeAgentCommand runs your agent for meetings: `meetstream agent serve`
// with the saved setup (the same bridge and relay; no new port). Its lines are
// kept in the app, so they name who spoke, never what was said. Tests swap it.
var homeAgentCommand = func() *exec.Cmd {
	self, err := os.Executable()
	if err != nil {
		self = "meetstream"
	}
	return exec.Command(self, "agent", "serve", "--hide-turn-text")
}

// Stopping the background agent: ask, then force after agentStopGrace; quitting
// the app waits up to agentQuitWait for it to be gone.
var (
	agentStopGrace = 3 * time.Second
	agentQuitWait  = 5 * time.Second
)

// agentLogLines is how many activity lines the app keeps.
const agentLogLines = 200

type bgAgent struct {
	cmd      *exec.Cmd
	changed  chan struct{} // a new line (holds at most one wake-up)
	exited   chan struct{} // closed once the process is gone
	done     chan error    // its exit, once
	err      error
	stopOnce sync.Once
	mu       sync.Mutex
	log      []string
	started  time.Time
}

type agentLineMsg struct{}
type agentExitMsg struct{ err error }

// startAgent starts the background agent. Its output is read all the time into
// a bounded buffer, so a slow or suspended app (a flow running in the
// terminal) never blocks the bridge's writes.
func startAgent() (*bgAgent, error) {
	c := homeAgentCommand()
	pr, pw := io.Pipe()
	c.Stdout, c.Stderr, c.Stdin = pw, pw, nil
	proctree.PrepareInterrupt(c)
	if err := c.Start(); err != nil {
		return nil, err
	}
	a := &bgAgent{cmd: c, changed: make(chan struct{}, 1), exited: make(chan struct{}), done: make(chan error, 1), started: time.Now()}
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 4096), 1024*1024)
		for sc.Scan() {
			a.add(sc.Text())
		}
		_, _ = io.Copy(io.Discard, pr) // a line over the limit: keep draining
	}()
	go func() {
		err := c.Wait()
		pw.Close()
		a.err = err
		close(a.exited)
		a.done <- err
	}()
	return a, nil
}

// next waits for the agent's next log line, or its exit.
func (a *bgAgent) next() tea.Cmd {
	return func() tea.Msg {
		select {
		case <-a.changed:
			return agentLineMsg{}
		case <-a.exited:
			return agentExitMsg{a.err}
		}
	}
}

// stop asks the agent to stop (Ctrl-C to its bridge) and kills its tree if it
// is still running after agentStopGrace. Calling it again does nothing more.
func (a *bgAgent) stop() {
	if a == nil || a.cmd.Process == nil {
		return
	}
	a.stopOnce.Do(func() {
		if err := proctree.Interrupt(a.cmd); err != nil {
			_ = proctree.Kill(a.cmd)
		}
		grace := agentStopGrace
		go func() {
			select {
			case <-a.exited:
			case <-time.After(grace):
				_ = proctree.Kill(a.cmd)
			}
		}()
	})
}

// shutdown stops the agent and waits until it is gone, at most wait (then it
// is killed); false if it still did not exit.
func (a *bgAgent) shutdown(wait time.Duration) bool {
	if a == nil || a.cmd.Process == nil {
		return true
	}
	a.stop()
	select {
	case <-a.exited:
		return true
	case <-time.After(wait):
	}
	_ = proctree.Kill(a.cmd)
	select {
	case <-a.exited:
		return true
	case <-time.After(time.Second):
		return false
	}
}

// add keeps line (secrets hidden) in the ring of the last agentLogLines and
// wakes the app; it never waits.
func (a *bgAgent) add(line string) {
	line = redact.Secrets(line)
	a.mu.Lock()
	a.log = append(a.log, line)
	if len(a.log) > agentLogLines {
		a.log = a.log[len(a.log)-agentLogLines:]
	}
	a.mu.Unlock()
	select {
	case a.changed <- struct{}{}:
	default:
	}
}

// ── exec flows ───────────────────────────────────────────────────────────────

// pauseCmd runs a subcommand in the terminal, then waits for Enter so its last
// lines stay readable before the app comes back.
type pauseCmd struct {
	c           *exec.Cmd
	in          io.Reader
	out, errOut io.Writer
}

func (p *pauseCmd) SetStdin(r io.Reader)  { p.in = r }
func (p *pauseCmd) SetStdout(w io.Writer) { p.out = w }
func (p *pauseCmd) SetStderr(w io.Writer) { p.errOut = w }
func (p *pauseCmd) Run() error {
	p.c.Stdin, p.c.Stdout, p.c.Stderr = p.in, p.out, p.errOut
	err := p.c.Run()
	fmt.Fprint(p.out, "\n  "+ui.DimStyle.Render("Press Enter to return to MeetStream…"))
	_, _ = bufio.NewReader(p.in).ReadString('\n')
	return err
}

type execDoneMsg struct {
	title string
	err   error
}

// homeExec suspends the app to run a flow; tests swap it.
var homeExec = func(title string, args []string) tea.Cmd {
	self, err := os.Executable()
	if err != nil {
		self = "meetstream"
	}
	return tea.Exec(&pauseCmd{c: exec.Command(self, args...)}, func(err error) tea.Msg { return execDoneMsg{title, err} })
}

// ── messages ─────────────────────────────────────────────────────────────────

type tickMsg struct{}
type spinMsg struct{}
type viewMsg struct {
	req  int
	text string
	err  error
}
type botsMsg struct {
	req  int
	view botsListView
	next string
	err  error
}

func tick() tea.Cmd { return tea.Tick(time.Second/12, func(time.Time) tea.Msg { return tickMsg{} }) }
func spinTick() tea.Cmd {
	return tea.Tick(time.Second/10, func(time.Time) tea.Msg { return spinMsg{} })
}

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// ── model ────────────────────────────────────────────────────────────────────

type homeFocus int

const (
	focusMenu homeFocus = iota
	focusPane
	focusForm
)

type botsState struct {
	view    botsListView
	cursors []string // cursor of each page shown so far ("" for page 1)
	next    string
	reading bool // showing a transcript; Esc goes back to the table
}

type sendForm struct {
	link, name string
	field      int
}

type homeModel struct {
	env      *homeEnv
	version  string
	items    []homeItem
	cursor   int
	focus    homeFocus
	width    int
	height   int
	frame    int
	waves    int
	title    string // pane title
	pane     string // pane content
	scroll   int
	loading  string
	spin     int
	req      int
	bots     *botsState
	form     *sendForm
	agent    *bgAgent
	agentErr string
	quitting bool
}

func newHomeModel(env *homeEnv) homeModel {
	m := homeModel{env: env, version: env.version, items: homeMenu(), width: 100, height: 32}
	m.title, m.pane = "Welcome", homeWelcome()
	return m
}

func homeWelcome() string {
	return strings.Join([]string{
		"Pick something on the left and press Enter.",
		"",
		ui.DimStyle.Render(miaIntro),
		"",
		ui.DimStyle.Render("Everything here is also a command: meetstream --help"),
	}, "\n")
}

func (m homeModel) Init() tea.Cmd {
	var cmds []tea.Cmd
	if mascot.Enabled() && mascot.Frames("idle") > 1 {
		cmds = append(cmds, tick())
	}
	if s, err := kitconfig.LoadSetup(""); err == nil && s.AutoStart == "yes" && s.Tunnel == "relay" {
		cmds = append(cmds, func() tea.Msg { return startAgentMsg{} })
	}
	return tea.Batch(cmds...)
}

type startAgentMsg struct{}

func (m homeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		m.frame = (m.frame + 1) % max(1, mascot.Frames("idle"))
		if m.frame == 0 {
			m.waves++
		}
		if m.waves < 2 {
			return m, tick()
		}
		return m, nil
	case spinMsg:
		if m.loading == "" {
			return m, nil
		}
		m.spin++
		return m, spinTick()
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case viewMsg:
		if msg.req != m.req {
			return m, nil
		}
		m.loading, m.scroll = "", 0
		if msg.err != nil {
			m.pane = errorView(msg.err)
		} else {
			m.pane = msg.text
		}
		return m, nil
	case botsMsg:
		if msg.req != m.req || m.bots == nil {
			return m, nil
		}
		m.loading, m.scroll = "", 0
		if msg.err != nil {
			m.pane = errorView(msg.err)
			return m, nil
		}
		m.bots.view, m.bots.next = msg.view, msg.next
		m.bots.view.Pick = 0
		m.pane = m.botsPane()
		return m, nil
	case execDoneMsg:
		m.items = homeMenu() // sign-in may have changed
		m.cursor = min(m.cursor, len(m.items)-1)
		m.focus, m.loading, m.scroll = focusPane, "", 0
		m.title = msg.title
		if msg.err != nil {
			m.pane = ui.WarnLine("That did not finish: "+firstLine(msg.err.Error())) + "\n\n" + ui.DimStyle.Render("Esc for the menu.")
		} else {
			m.pane = ui.OKLine("Done.") + "\n\n" + ui.DimStyle.Render("Esc for the menu.")
		}
		return m, nil
	case startAgentMsg:
		return m.toggleAgent(true)
	case agentLineMsg:
		if m.agent == nil {
			return m, nil
		}
		if m.isAgentPane() {
			m.pane = m.agentPane()
		}
		return m, m.agent.next()
	case agentExitMsg:
		if m.agent != nil {
			if msg.err != nil && !m.quitting {
				m.agentErr = firstLine(msg.err.Error())
			}
			m.agent = nil
		}
		if m.isAgentPane() {
			m.pane = m.agentPane()
		}
		return m, nil
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func firstLine(s string) string { l, _, _ := strings.Cut(s, "\n"); return l }

func errorView(err error) string {
	var ee *ExitError
	if errors.As(err, &ee) {
		out := ui.FailLine(ee.Message)
		if ee.Hint != "" {
			out += "\n" + ui.NoteLine(ee.Hint)
		}
		return out
	}
	return ui.FailLine(apiError(err).Error())
}

func (m homeModel) isAgentPane() bool {
	return m.items[m.cursor].kind == kindAgent && m.focus == focusPane
}

func (m homeModel) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if s == "ctrl+c" {
		return m.quit()
	}
	switch m.focus {
	case focusForm:
		return m.formKey(k)
	case focusPane:
		return m.paneKey(s)
	}
	switch s {
	case "q", "esc":
		return m.quit()
	case "up", "k", "shift+tab":
		m.cursor = (m.cursor - 1 + len(m.items)) % len(m.items)
	case "down", "j", "tab":
		m.cursor = (m.cursor + 1) % len(m.items)
	case "enter", "right", "l":
		return m.run()
	}
	return m, nil
}

func (m homeModel) quit() (tea.Model, tea.Cmd) {
	m.quitting = true
	m.agent.stop()
	return m, tea.Quit
}

func (m homeModel) paneKey(s string) (tea.Model, tea.Cmd) {
	page := max(3, m.paneHeight()-2)
	switch s {
	case "esc", "left", "h", "backspace":
		if m.bots != nil && m.bots.reading {
			m.bots.reading, m.scroll = false, 0
			m.pane = m.botsPane()
			return m, nil
		}
		m.focus, m.loading = focusMenu, ""
		return m, nil
	case "q":
		return m.quit()
	case "pgdown", " ":
		m.scroll += page
	case "pgup":
		m.scroll -= page
	case "home", "g":
		m.scroll = 0
	}
	m.clampScroll()
	if m.bots != nil && !m.bots.reading && m.loading == "" && len(m.bots.view.Bots) > 0 {
		v := &m.bots.view
		switch s {
		case "up", "k":
			v.Pick = max(0, v.Pick-1)
		case "down", "j":
			v.Pick = min(len(v.Bots)-1, v.Pick+1)
		case "enter":
			return m.openTranscript()
		case "r":
			return m.loadBots(len(m.bots.cursors) - 1)
		case "n":
			if m.bots.next != "" {
				m.bots.cursors = append(m.bots.cursors, m.bots.next)
				return m.loadBots(len(m.bots.cursors) - 1)
			}
		case "p":
			if len(m.bots.cursors) > 1 {
				m.bots.cursors = m.bots.cursors[:len(m.bots.cursors)-1]
				return m.loadBots(len(m.bots.cursors) - 1)
			}
		}
		m.pane = m.botsPane()
		return m, nil
	}
	if m.items[m.cursor].kind == kindAgent {
		switch s {
		case "enter", "s":
			return m.toggleAgent(m.agent == nil)
		case "a":
			m.toggleAutoStart()
			m.pane = m.agentPane()
			return m, nil
		}
	}
	switch s {
	case "up", "k":
		m.scroll--
	case "down", "j":
		m.scroll++
	}
	m.clampScroll()
	return m, nil
}

func (m *homeModel) clampScroll() {
	lines := strings.Count(m.pane, "\n") + 1
	if m.loading != "" {
		lines++
	}
	m.scroll = max(0, min(m.scroll, lines-max(1, m.paneHeight()-2)))
}

// run starts the highlighted item.
func (m homeModel) run() (tea.Model, tea.Cmd) {
	it := m.items[m.cursor]
	m.title, m.scroll, m.bots, m.form = it.title, 0, nil, nil
	m.focus = focusPane
	m.req++
	switch it.kind {
	case kindExec:
		m.pane = ui.DimStyle.Render("Opening " + strings.ToLower(it.title) + "…")
		return m, homeExec(it.title, it.args)
	case kindSend:
		if key, _ := config.ResolveKey(); key == "" {
			m.pane = errorView(usageErr("not signed in", "choose Sign in first"))
			return m, nil
		}
		m.focus, m.form = focusForm, &sendForm{name: "MeetStream Bot"}
		m.pane = m.formPane()
		return m, nil
	case kindBots:
		m.bots = &botsState{cursors: []string{""}}
		return m.loadBots(0)
	case kindAgent:
		m.pane = m.agentPane()
		return m, nil
	case kindDocs:
		m.pane = homeDocs(m.paneWidth())
		return m, nil
	case kindDoctor:
		all, _ := doctorKeyChecks()
		m.pane = renderDoctorLines(all)
		m.loading = it.loading
		req, env := m.req, m.env
		return m, tea.Batch(spinTick(), func() tea.Msg {
			more := homeDoctorAgent(env.root)
			all := append(all, more...)
			verdict, failed := checks.Verdict(all)
			return viewMsg{req: req, text: renderDoctor(all, verdict, failed)}
		})
	}
	m.pane, m.loading = "", it.loading
	req, env, view, w := m.req, m.env, it.view, m.paneWidth()
	return m, tea.Batch(spinTick(), func() tea.Msg {
		text, err := view(context.WithValue(env.ctx(), paneWidthKey{}, w), env)
		return viewMsg{req: req, text: text, err: err}
	})
}

// homeDoctorAgent is the agent half of Check my setup; tests swap it.
var homeDoctorAgent = func(root *cobra.Command) []checks.Check { return doctorAgentChecks(root, "", "", false) }

type paneWidthKey struct{}

func paneWidth(ctx context.Context) int {
	if w, ok := ctx.Value(paneWidthKey{}).(int); ok {
		return w
	}
	return ui.Width()
}

func homeMiaList(ctx context.Context, h *homeEnv) (string, error) {
	c, err := h.client()
	if err != nil {
		return "", err
	}
	v, err := c.MiaList(ctx)
	if err != nil {
		return "", err
	}
	return renderMiaList(v, paneWidth(ctx)), nil
}

func homeCalendar(ctx context.Context, h *homeEnv) (string, error) {
	c, err := h.client()
	if err != nil {
		return "", err
	}
	var v, s any
	if err := c.Do(ctx, "GET", "/calendar/calendars", nil, &v); err != nil {
		var ae *api.Error
		if errors.As(err, &ae) && ae.Status == 400 {
			return "No calendar connected. Choose " + ui.BoldStyle.Render("Connect my calendar") + ".", nil
		}
		return "", err
	}
	_ = c.Do(ctx, "GET", "/calendar/auto-schedule/settings", nil, &s)
	return renderCalendarList(v, object(s), paneWidth(ctx)), nil
}

func homeBalance(ctx context.Context, h *homeEnv) (string, error) {
	c, err := h.client()
	if err != nil {
		return "", err
	}
	v, err := c.Balance(ctx)
	var ae *api.Error
	if errors.As(err, &ae) && ae.Status == 404 {
		out := "Balance is not available for this account here yet."
		if u := dashboardURL("/billing"); u != "" {
			out += "\nSee it at " + ui.CmdStyle.Render(u)
		}
		return out, nil
	}
	if err != nil {
		return "", err
	}
	return renderBalance(v), nil
}

func homeDocs(width int) string {
	r, err := glamour.NewTermRenderer(glamour.WithAutoStyle(), glamour.WithWordWrap(max(40, width-4)))
	if err != nil {
		return meetstream.Readme
	}
	out, err := r.Render(meetstream.Readme)
	if err != nil {
		return meetstream.Readme
	}
	return strings.Trim(out, "\n")
}

// ── bots in the app ──────────────────────────────────────────────────────────

func (m homeModel) loadBots(page int) (tea.Model, tea.Cmd) {
	m.req++
	m.loading, m.bots.reading = "Loading your bots…", false
	req, env, cursor := m.req, m.env, m.bots.cursors[page]
	return m, tea.Batch(spinTick(), func() tea.Msg {
		c, err := env.client()
		if err != nil {
			return botsMsg{req: req, err: err}
		}
		v, err := c.ListBotsPage(env.ctx(), defaultBotsLimit, cursor)
		if err != nil {
			return botsMsg{req: req, err: err}
		}
		bp := readBotsPage(v)
		view := botsListView{Bots: bp.bots, Total: bp.total, HasMore: bp.hasMore, Page: page + 1, Limit: defaultBotsLimit, Start: page*defaultBotsLimit + 1}
		return botsMsg{req: req, view: view, next: bp.next}
	})
}

func (m homeModel) botsPane() string {
	v := m.bots.view
	if len(v.Bots) == 0 {
		return renderBotsList(v, m.paneWidth())
	}
	table := renderBotsList(v, m.paneWidth())
	// In the app, paging is n/p, not a command.
	lines := strings.Split(table, "\n")
	lines = lines[:len(lines)-2]
	foot := fmt.Sprintf("Bots %s–%s", ui.Count(v.Start), ui.Count(v.Start+len(v.Bots)-1))
	if v.Total > 0 {
		foot += " of " + ui.Count(v.Total)
	}
	keys := "↑/↓ pick · enter transcript · r refresh"
	if v.HasMore {
		keys += " · n next page"
	}
	if v.Page > 1 {
		keys += " · p previous"
	}
	return strings.Join(lines, "\n") + "\n" + ui.DimStyle.Render(foot+" · "+keys)
}

func (m homeModel) openTranscript() (tea.Model, tea.Cmd) {
	bot := object(m.bots.view.Bots[m.bots.view.Pick])
	id := pickStr(bot, "bot_id", "BotID", "id")
	name := orDefault(pickStr(bot, "bot_username", "BotUsername", "bot_name"), "Bot "+shortID(id))
	m.req++
	m.bots.reading, m.scroll = true, 0
	m.loading = "Loading the transcript…"
	m.pane = ""
	req, env, w := m.req, m.env, m.paneWidth()
	return m, tea.Batch(spinTick(), func() tea.Msg {
		c, err := env.client()
		if err != nil {
			return viewMsg{req: req, err: err}
		}
		head := ui.BoldStyle.Render(name) + "  " + statusPill(pickStr(bot, "status", "Status")) + "  " + ui.DimStyle.Render(id) + "\n\n"
		res, err := c.GetTranscript(env.ctx(), id, api.TranscriptOpt{})
		if failed := api.FailedTranscript(err); failed != nil {
			return viewMsg{req: req, text: head + ui.FailLine("Transcript failed: "+failed.Reason)}
		}
		if err != nil {
			return viewMsg{req: req, err: err}
		}
		if res.Transcript == nil {
			return viewMsg{req: req, text: head + ui.DimStyle.Render("No transcript yet. It appears here after the meeting is processed.")}
		}
		return viewMsg{req: req, text: head + renderTranscript(res.Transcript, w)}
	})
}

// ── send a bot ───────────────────────────────────────────────────────────────

func (m homeModel) formPane() string {
	f := m.form
	field := func(i int, label, val, hint string) string {
		cursor := " "
		style := lipgloss.NewStyle()
		if f.field == i {
			cursor = selStyle.Render("❯")
			style = selStyle
			val += "█"
		}
		return fmt.Sprintf("%s %s\n  %s\n  %s", cursor, ui.BoldStyle.Render(label), style.Render(val), ui.DimStyle.Render(hint))
	}
	return strings.Join([]string{
		field(0, "Meeting link", f.link, "Google Meet, Zoom or Microsoft Teams"),
		"",
		field(1, "Bot name", f.name, "Shown to people in the meeting"),
		"",
		ui.DimStyle.Render("enter next / send · tab switch · esc cancel"),
	}, "\n")
}

func (m homeModel) formKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := m.form
	val := &f.link
	if f.field == 1 {
		val = &f.name
	}
	switch k.Type {
	case tea.KeyEsc:
		m.focus, m.form = focusMenu, nil
		m.title, m.pane = "Welcome", homeWelcome()
		return m, nil
	case tea.KeyTab, tea.KeyShiftTab, tea.KeyUp, tea.KeyDown:
		f.field = 1 - f.field
	case tea.KeyBackspace:
		if r := []rune(*val); len(r) > 0 {
			*val = string(r[:len(r)-1])
		}
	case tea.KeyRunes, tea.KeySpace:
		*val += string(k.Runes)
	case tea.KeyEnter:
		if f.field == 0 {
			if !validMeetingLink(f.link) {
				m.pane = m.formPane() + "\n\n" + ui.WarnLine("Paste a meeting link that starts with https://")
				return m, nil
			}
			f.field = 1
			break
		}
		return m.sendBot()
	}
	m.pane = m.formPane()
	return m, nil
}

func validMeetingLink(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

func (m homeModel) sendBot() (tea.Model, tea.Cmd) {
	f := *m.form
	m.focus, m.form = focusPane, nil
	m.req++
	m.loading, m.pane = "Sending the bot…", ""
	req, env := m.req, m.env
	return m, tea.Batch(spinTick(), func() tea.Msg {
		c, err := env.client()
		if err != nil {
			return viewMsg{req: req, err: err}
		}
		name := orDefault(strings.TrimSpace(f.name), "MeetStream Bot")
		link := strings.TrimSpace(f.link)
		payload, err := api.BuildCreateBotPayload(api.CreateOpts{MeetingLink: link, Name: name})
		if err != nil {
			return viewMsg{req: req, err: err}
		}
		status, v, err := c.CreateBot(env.ctx(), payload, "")
		if err != nil {
			return viewMsg{req: req, err: err}
		}
		return viewMsg{req: req, text: renderJoinResult(v, name, link, status == 507)}
	})
}

// ── your agent ───────────────────────────────────────────────────────────────

func (m homeModel) toggleAgent(on bool) (tea.Model, tea.Cmd) {
	m.agentErr = ""
	if !on {
		m.agent.stop()
		if m.isAgentPane() {
			m.pane = m.agentPane() + "\n" + ui.DimStyle.Render("Stopping…")
		}
		return m, nil
	}
	if m.agent != nil {
		return m, nil
	}
	s, err := kitconfig.LoadSetup("")
	switch {
	case err != nil:
		m.agentErr = "Set up your agent first: Bring an AI agent into my meetings"
	case s.MaxAccess == "confirm":
		// Its approvals are asked on the terminal, which the app holds.
		m.agentErr = "Ask-first access needs the foreground: run meetstream agent serve"
	}
	if m.agentErr != "" {
		if m.isAgentPane() {
			m.pane = m.agentPane()
		}
		return m, nil
	}
	a, err := startAgent()
	if err != nil {
		m.agentErr = err.Error()
	} else {
		m.agent = a
	}
	if m.isAgentPane() {
		m.pane = m.agentPane()
	}
	if m.agent == nil {
		return m, nil
	}
	return m, m.agent.next()
}

func (m *homeModel) toggleAutoStart() {
	s, err := kitconfig.LoadSetup("")
	if err != nil {
		m.agentErr = "Set up your agent first: Bring an AI agent into my meetings"
		return
	}
	if s.AutoStart == "yes" {
		s.AutoStart = ""
	} else {
		s.AutoStart = "yes"
	}
	if err := s.Save(""); err != nil {
		m.agentErr = "Could not save: " + err.Error()
	}
}

// agentLabel is the saved agent's name, e.g. "Claude Code".
func agentLabel() (string, *kitconfig.Setup) {
	s, err := kitconfig.LoadSetup("")
	if err != nil {
		return "", nil
	}
	if spec, ok := agents.All[s.Agent]; ok && spec.Label != "" {
		return spec.Label, s
	}
	if s.Command != "" {
		return "your command", s
	}
	return s.Agent, s
}

func (m homeModel) agentTile() string {
	label, saved := agentLabel()
	switch {
	case saved == nil:
		return ui.DimStyle.Render("Your agent: not set up")
	case m.agent != nil:
		return "Your agent: " + label + " · " + ui.Pill("running", ui.Green)
	}
	return "Your agent: " + label + " · " + ui.DimStyle.Render("stopped")
}

func (m homeModel) agentPane() string {
	label, saved := agentLabel()
	var b strings.Builder
	if saved == nil {
		b.WriteString("Your own agent (Claude Code, Codex and others) can answer in your meetings.\n")
		b.WriteString(ui.DimStyle.Render("Set it up first: Bring an AI agent into my meetings.") + "\n")
		return b.String()
	}
	state := ui.DimStyle.Render("○ stopped")
	if m.agent != nil {
		state = ui.Pill("running", ui.Green) + ui.DimStyle.Render(" since "+m.agent.started.Format("15:04"))
	}
	auto := "off"
	if saved.AutoStart == "yes" {
		auto = "on"
	}
	rows := [][2]string{{"Agent", label}, {"State", state}, {"Folder", ui.ShortPath(saved.Workdir)}, {"Start with app", auto}}
	b.WriteString(ui.KV(rows, 0) + "\n")
	if m.agentErr != "" {
		b.WriteString("\n" + ui.FailLine(m.agentErr) + "\n")
	}
	b.WriteString("\n" + ui.Section("Activity") + "\n")
	var lines []string
	if m.agent != nil {
		m.agent.mu.Lock()
		lines = append(lines, m.agent.log[max(0, len(m.agent.log)-12):]...)
		m.agent.mu.Unlock()
	}
	if len(lines) == 0 {
		b.WriteString(ui.DimStyle.Render("Nothing yet.") + "\n")
	}
	for _, l := range lines {
		b.WriteString(ui.Truncate(l, max(20, m.paneWidth()-2)) + "\n")
	}
	action := "enter start"
	if m.agent != nil {
		action = "enter stop"
	}
	b.WriteString("\n" + ui.DimStyle.Render(action+" · a start with the app on/off · esc back"))
	return b.String()
}

// ── layout ───────────────────────────────────────────────────────────────────

var (
	selStyle  = lipgloss.NewStyle().Foreground(ui.Orange).Bold(true)
	headStyle = lipgloss.NewStyle().Foreground(ui.Dim).Bold(true)
)

func (m homeModel) wide() bool { return m.width >= 96 }

const menuWidth = 38

func (m homeModel) paneWidth() int {
	if m.wide() {
		return m.width - menuWidth - 6
	}
	return m.width - 4
}

func (m homeModel) header() string {
	key, _ := config.ResolveKey()
	status := ui.Pill("Not signed in", ui.Yellow) + ui.DimStyle.Render(" · choose Sign in")
	if key != "" {
		c, _ := config.Load()
		who := config.Mask(key)
		if c != nil && c.APIKeyName != "" && os.Getenv("MEETSTREAM_API_KEY") == "" {
			who = c.APIKeyName + " " + ui.DimStyle.Render(who)
		}
		status = ui.Pill("Signed in", ui.Green) + " " + who
	}
	status += ui.DimStyle.Render(" · ") + serverLabel()
	lines := []string{ui.TitleStyle.Render("◆ MeetStream") + "  " + ui.DimStyle.Render("v"+m.version)}
	if m.width >= 80 {
		lines = append(lines, "Meeting bots, transcripts and your own agent in the call")
	}
	lines = append(lines, status, m.agentTile())
	for i := range lines {
		lines[i] = ui.Truncate(lines[i], max(20, m.width-8))
	}
	head := lipgloss.JoinVertical(lipgloss.Left, lines...)
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ui.Border).Padding(0, 2).Render(head)
	if m.width < 80 || m.height < 28 {
		return box
	}
	return mascot.Beside("idle", m.frame, box)
}

func (m homeModel) menu(width int) string {
	var b strings.Builder
	section := ""
	for i, it := range m.items {
		if it.section != section {
			if section != "" {
				b.WriteString("\n")
			}
			section = it.section
			b.WriteString(" " + headStyle.Render(strings.ToUpper(section)) + "\n")
		}
		title := ui.Truncate(it.title, width-3)
		if it.kind == kindAgent && m.agent != nil {
			title = ui.Truncate(it.title, width-8) + " " + ui.Pill("", ui.Green)
		}
		if i == m.cursor {
			pointer := "❯ "
			if m.focus != focusMenu {
				pointer = "› "
			}
			b.WriteString(" " + selStyle.Render(pointer+title) + "\n")
			if m.focus == focusMenu {
				b.WriteString("   " + ui.DimStyle.Render(ui.Wrap(it.desc, width-4, 3)) + "\n")
			}
			continue
		}
		b.WriteString("   " + title + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m homeModel) paneHeight() int {
	h := m.height - lipgloss.Height(m.header()) - 4
	return max(6, h)
}

// paneView is the pane's title, the visible slice of its content and a
// scroll hint.
func (m *homeModel) paneView(width, height int) string {
	title := ui.BoldStyle.Render(m.title)
	body := m.pane
	if m.loading != "" {
		spinner := lipgloss.NewStyle().Foreground(ui.Cyan).Render(spinFrames[m.spin%len(spinFrames)])
		if body != "" {
			body += "\n"
		}
		body += spinner + " " + m.loading
	}
	lines := strings.Split(body, "\n")
	room := max(1, height-2)
	maxScroll := max(0, len(lines)-room)
	m.scroll = max(0, min(m.scroll, maxScroll))
	lines = lines[m.scroll:min(len(lines), m.scroll+room)]
	for i, l := range lines {
		if lipgloss.Width(l) > width {
			l = ui.Wrap(l, width, 0) // prose wraps; tables are already fitted
		}
		lines[i] = l
	}
	lines = strings.Split(strings.Join(lines, "\n"), "\n")
	lines = lines[:min(len(lines), room)]
	more := ""
	if m.scroll < maxScroll {
		more = "\n" + ui.DimStyle.Render(fmt.Sprintf("↓ %d more lines · space for the next page", maxScroll-m.scroll))
	}
	return title + "\n" + strings.Join(lines, "\n") + more
}

func (m homeModel) footer() string {
	switch {
	case m.focus == focusForm:
		return "enter next · tab switch field · esc cancel · ctrl+c quit"
	case m.focus == focusPane && m.bots != nil && m.bots.reading:
		return "↑/↓ scroll · esc back to bots · q quit"
	case m.focus == focusPane:
		return "↑/↓ scroll · esc menu · q quit"
	}
	if m.width < 80 {
		return "↑/↓ move · enter choose · q quit"
	}
	return "↑/↓ move · enter choose · q quit · meetstream --help for every command"
}

func (m homeModel) View() string {
	header := m.header()
	ph := m.paneHeight()
	border := ui.Border
	if m.focus != focusMenu {
		border = ui.Orange
	}
	paneStyle := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).Padding(0, 1)
	var body string
	if m.wide() {
		pw := m.paneWidth()
		menu := lipgloss.NewStyle().Width(menuWidth).Height(ph).Render(m.menu(menuWidth))
		pane := paneStyle.Width(pw + 2).Height(ph).Render(m.paneView(pw, ph))
		body = lipgloss.JoinHorizontal(lipgloss.Top, menu, " ", pane)
	} else if m.focus == focusMenu {
		body = m.menu(m.width - 2)
	} else {
		pw := m.paneWidth()
		body = paneStyle.Width(pw + 2).Render(m.paneView(pw, ph))
	}
	return header + "\n" + body + "\n " + ui.DimStyle.Render(m.footer())
}

func runHome(cmd *cobra.Command, info BuildInfo) error {
	m := newHomeModel(&homeEnv{root: cmd.Root(), version: info.Version})
	final, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if fm, ok := final.(homeModel); ok && fm.agent != nil {
		// Wait for the bridge (and the agent CLIs it started) to be gone.
		if !fm.agent.shutdown(agentQuitWait) {
			ui.Warn("Your agent for meetings did not stop; check for a leftover meetstream agent serve.")
		}
	}
	return err
}
