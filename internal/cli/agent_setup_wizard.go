package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"github.com/meetstream-ai/cli/internal/ui"
)

// ── step menu: a step's choice plus "← Back" ──────────────────────────────────

const backLabel = "← Back"

// backDetail is the Back row's own detail line. Every row has one, the Back row
// included (the kit's menu crashed once looking up a detail for it).
const backDetail = "Return to the previous question."

type menuResult int

const (
	menuOpen menuResult = iota
	menuChosen
	menuBack
	menuQuit
)

// runTea runs a Bubble Tea model at the terminal. Tests swap it to drive the
// models with key messages, so every step can be walked without a terminal.
var runTea = func(m tea.Model) (tea.Model, error) { return tea.NewProgram(m).Run() }

// stepMenuModel asks one question in a setup step. Esc (or the "← Back" row)
// goes back to the previous question; ctrl+c or q cancels setup.
type stepMenuModel struct {
	question string
	options  []string // without the Back row; it is always last
	hints    []string // dim text beside a row (optional)
	details  []string // the line under the menu for the highlighted row (optional)
	disabled []bool   // rows that cannot be chosen (dimmed; their hint says why)
	noBack   bool     // the first question: no Back row, Esc cancels setup
	cursor   int
	result   menuResult
	chosen   int
}

func newStepMenu(question string, options ...string) stepMenuModel {
	return stepMenuModel{question: question, options: options}
}

// withHints sets the dim text beside each row.
func (m stepMenuModel) withHints(hints []string) stepMenuModel { m.hints = hints; return m }

// withDetails sets the line shown under the menu for each row; the Back row
// always has backDetail.
func (m stepMenuModel) withDetails(details []string) stepMenuModel { m.details = details; return m }

// at starts the cursor on row i.
func (m stepMenuModel) at(i int) stepMenuModel {
	if i >= 0 && i < m.rows() {
		m.cursor = i
	}
	return m
}

func (m stepMenuModel) rows() int {
	if m.noBack {
		return len(m.options)
	}
	return len(m.options) + 1
}

func (m stepMenuModel) isDisabled(i int) bool { return i < len(m.disabled) && m.disabled[i] }

// detail is the highlighted row's detail line, "" when it has none.
func (m stepMenuModel) detail(i int) string {
	switch {
	case i == len(m.options) && !m.noBack:
		return backDetail
	case i < len(m.details):
		return m.details[i]
	}
	return ""
}

func (m stepMenuModel) Init() tea.Cmd { return nil }

func (m stepMenuModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch k.String() {
	case "ctrl+c", "q":
		m.result = menuQuit
		return m, tea.Quit
	case "esc", "left", "h":
		m.result = menuBack
		return m, tea.Quit
	case "up", "k":
		m.cursor = (m.cursor - 1 + m.rows()) % m.rows()
	case "down", "j", "tab":
		m.cursor = (m.cursor + 1) % m.rows()
	case "enter":
		if m.isDisabled(m.cursor) {
			return m, nil // not offered: its hint says why
		}
		if m.cursor == len(m.options) {
			m.result = menuBack
		} else {
			m.result, m.chosen = menuChosen, m.cursor
		}
		return m, tea.Quit
	}
	return m, nil
}

func (m stepMenuModel) View() string {
	var b strings.Builder
	b.WriteString("\n  " + ui.BoldStyle.Render(m.question) + "\n\n")
	sel := lipgloss.NewStyle().Foreground(ui.Orange).Bold(true)
	width := 0
	for _, o := range m.options {
		width = max(width, lipgloss.Width(o))
	}
	labels := append([]string{}, m.options...)
	if !m.noBack {
		labels = append(labels, backLabel)
	}
	for i, label := range labels {
		pointer, title := "  ", label
		if i == len(m.options) || m.isDisabled(i) {
			title = ui.DimStyle.Render(label)
		}
		if i == m.cursor {
			pointer, title = sel.Render("❯ "), sel.Render(label)
			if m.isDisabled(i) {
				title = ui.DimStyle.Render(label)
			}
		}
		line := "  " + pointer + title
		if i < len(m.hints) && m.hints[i] != "" {
			// lipgloss.Width ignores colour codes, so styled rows line up.
			line += strings.Repeat(" ", max(0, width-lipgloss.Width(label))) + "  " + ui.DimStyle.Render(m.hints[i])
		}
		b.WriteString(line + "\n")
	}
	if d := m.detail(m.cursor); d != "" {
		// Wrapped, so a long detail line never widens the menu.
		for _, l := range strings.Split(lipgloss.NewStyle().Width(96).Render(d), "\n") {
			b.WriteString("\n  " + ui.DimStyle.Render(strings.TrimRight(l, " ")))
		}
		b.WriteString("\n")
	}
	keys := "↑/↓ move · enter choose · esc back · q quit"
	if m.noBack {
		keys = "↑/↓ move · enter choose · esc/q quit"
	}
	b.WriteString("\n  " + ui.DimStyle.Render(keys) + "\n")
	return b.String()
}

// ask runs a step menu at the terminal.
func ask(m stepMenuModel) (menuResult, int, error) {
	final, err := runTea(m)
	if err != nil {
		return menuQuit, 0, err
	}
	sm := final.(stepMenuModel)
	return sm.result, sm.chosen, nil
}

// askStep runs a plain step menu at the terminal.
func askStep(question string, options ...string) (menuResult, int, error) {
	return ask(newStepMenu(question, options...))
}

// ── tick list: several choices plus "← Back" ──────────────────────────────────

// checklistModel picks several rows: space ticks, enter confirms, Esc (or the
// Back row) goes back, q or ctrl+c cancels setup.
type checklistModel struct {
	question string
	options  []string
	hints    []string
	ticked   []bool
	cursor   int
	result   menuResult
}

func newChecklist(question string, options, hints []string, ticked []int) checklistModel {
	m := checklistModel{question: question, options: options, hints: hints, ticked: make([]bool, len(options))}
	for _, i := range ticked {
		if i >= 0 && i < len(options) {
			m.ticked[i] = true
		}
	}
	return m
}

func (m checklistModel) rows() int { return len(m.options) + 1 }

// picked are the ticked rows, in order.
func (m checklistModel) picked() []int {
	var out []int
	for i, t := range m.ticked {
		if t {
			out = append(out, i)
		}
	}
	return out
}

func (m checklistModel) Init() tea.Cmd { return nil }

func (m checklistModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch k.String() {
	case "ctrl+c", "q":
		m.result = menuQuit
		return m, tea.Quit
	case "esc", "left", "h":
		m.result = menuBack
		return m, tea.Quit
	case "up", "k":
		m.cursor = (m.cursor - 1 + m.rows()) % m.rows()
	case "down", "j", "tab":
		m.cursor = (m.cursor + 1) % m.rows()
	case " ", "x":
		if m.cursor < len(m.options) {
			m.ticked[m.cursor] = !m.ticked[m.cursor]
		}
	case "enter":
		if m.cursor == len(m.options) {
			m.result = menuBack
		} else {
			m.result = menuChosen
		}
		return m, tea.Quit
	}
	return m, nil
}

func (m checklistModel) View() string {
	var b strings.Builder
	b.WriteString("\n  " + ui.BoldStyle.Render(m.question) + "\n\n")
	sel := lipgloss.NewStyle().Foreground(ui.Orange).Bold(true)
	for i, label := range append(append([]string{}, m.options...), backLabel) {
		pointer, box := "  ", ""
		if i < len(m.options) {
			box = "[ ] "
			if m.ticked[i] {
				box = "[" + lipgloss.NewStyle().Foreground(ui.Green).Render("x") + "] "
			}
		}
		title := label
		if i == len(m.options) {
			title = ui.DimStyle.Render(label)
		}
		if i == m.cursor {
			pointer, title = sel.Render("❯ "), sel.Render(label)
		}
		line := "  " + pointer + box + title
		if i < len(m.hints) && m.hints[i] != "" {
			line += "  " + ui.DimStyle.Render(m.hints[i])
		}
		b.WriteString(line + "\n")
	}
	if m.cursor == len(m.options) {
		b.WriteString("\n  " + ui.DimStyle.Render(backDetail) + "\n")
	}
	b.WriteString("\n  " + ui.DimStyle.Render("↑/↓ move · space tick · enter measure · esc back · q quit") + "\n")
	return b.String()
}

// ── text prompt with Esc for Back ──────────────────────────────────────────────

// textPromptModel reads one line. Enter keeps the typed text (or the default),
// Esc goes back, ctrl+c cancels setup.
type textPromptModel struct {
	question, value, fallback string
	result                    menuResult
}

func (m textPromptModel) Init() tea.Cmd { return nil }

func (m textPromptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch k.Type {
	case tea.KeyCtrlC:
		m.result = menuQuit
		return m, tea.Quit
	case tea.KeyEsc:
		m.result = menuBack
		return m, tea.Quit
	case tea.KeyEnter:
		m.result = menuChosen
		if strings.TrimSpace(m.value) == "" {
			m.value = m.fallback
		}
		m.value = strings.TrimSpace(m.value)
		return m, tea.Quit
	case tea.KeyBackspace:
		if r := []rune(m.value); len(r) > 0 {
			m.value = string(r[:len(r)-1])
		}
	case tea.KeySpace:
		m.value += " "
	case tea.KeyRunes:
		m.value += string(k.Runes)
	}
	return m, nil
}

func (m textPromptModel) View() string {
	shown := m.value
	if shown == "" && m.fallback != "" {
		shown = ui.DimStyle.Render(m.fallback)
	}
	return "\n  " + ui.BoldStyle.Render(m.question) + "\n\n  " + lipgloss.NewStyle().Foreground(ui.Orange).Render("❯ ") + shown + "█\n\n  " +
		ui.DimStyle.Render("enter keep · esc back · ctrl+c quit") + "\n"
}

// askText runs a text prompt at the terminal.
func askText(question, fallback string) (menuResult, string, error) {
	final, err := runTea(textPromptModel{question: question, fallback: fallback})
	if err != nil {
		return menuQuit, "", err
	}
	m := final.(textPromptModel)
	return m.result, m.value, nil
}

// ── install offer (the kit's _offer_install) ──────────────────────────────────

var prerequisiteHelp = map[string]string{
	"npm":  "npm comes with Node.js: https://nodejs.org/en/download" + map[bool]string{true: " (or: brew install node)"}[runtime.GOOS == "darwin"],
	"curl": "Install curl with your system's package manager (for example: sudo apt install curl).",
	"bash": "Install bash with your system's package manager.",
}

// systemInstallDirs are where installers commonly put a CLI besides ~/.local/bin,
// for when the new command is not on PATH yet (the kit's _INSTALL_DIRS).
var systemInstallDirs = []string{"/opt/homebrew/bin", "/usr/local/bin"}

func installDirs() []string {
	var dirs []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs, filepath.Join(home, ".local", "bin"))
	}
	return append(dirs, systemInstallDirs...)
}

// installedOffPath finds a CLI an installer put in a folder that is not on PATH.
func installedOffPath(binary string) string {
	for _, dir := range installDirs() {
		p := filepath.Join(dir, binary)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// installer runs the offer; its fields are swapped in tests so nothing is installed.
type installer struct {
	in  io.Reader                   // the person's answers
	run func(stages [][]string) int // runs a command pipeline, streaming its output
}

var setupInstaller = installer{in: os.Stdin, run: func(stages [][]string) int { return runPipeline(stages, os.Stdin, ui.Out) }}

// offer shows the vendor's install command and offers to run it. It returns the
// installed binary's path, or "" when the agent is still not installed.
func (in installer) offer(spec agents.Spec) string {
	ui.Fail(spec.Label + " is not on this computer.")
	if len(spec.Install) == 0 {
		ui.Note(spec.InstallHint)
		ui.Note("Install it, or pick another agent.")
		return ""
	}
	ui.Note("Official install command for " + spec.Label + ":")
	ui.Command(spec.InstallCommand())
	for _, need := range spec.InstallNeeds {
		if _, err := exec.LookPath(need); err != nil {
			ui.Fail("The install command needs " + need + ", which is not on this computer.")
			help := prerequisiteHelp[need]
			if help == "" {
				help = "Install " + need + " first."
			}
			ui.Note(help)
			return ""
		}
	}
	if !confirm(in.in, "Install "+spec.Label+" now?") {
		return ""
	}
	ui.Note("Running the installer here; its output follows.")
	// A fixed command from the agent registry, run stage by stage without a shell.
	if code := in.run(spec.Install); code != 0 {
		ui.Fail(fmt.Sprintf("The installer stopped with exit code %d. Fix what it says, or pick another agent.", code))
		return ""
	}
	found, err := exec.LookPath(spec.Binary)
	if err != nil {
		off := installedOffPath(spec.Binary)
		if off == "" {
			ui.Fail("Installed, but `" + spec.Binary + "` is not on your PATH yet.")
			ui.Note("Open a new terminal window and run setup again.")
			return ""
		}
		dir := filepath.Dir(off)
		_ = os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		ui.Warn("`" + spec.Binary + "` is in " + ui.ShortPath(dir) + ", which is not on your PATH. Add it to your shell profile:")
		ui.Command(`export PATH="` + dir + `:$PATH"`)
		found = off
	}
	ui.OK(spec.Label + " installed: " + ui.ShortPath(found))
	// Agents with a login check are asked in the sign-in check that follows.
	firstRun := spec.AfterInstall
	if len(firstRun) == 0 && len(spec.LoginCheck) == 0 {
		firstRun = spec.LoginArgv()
	}
	if len(firstRun) > 0 && confirm(in.in, "Sign in and set it up now ("+strings.Join(firstRun, " ")+")?") {
		ui.Note("Signing in is yours to do: follow the agent's own prompts.")
		in.run([][]string{firstRun})
	}
	return found
}

// confirm asks a [Y/n] question. Enter means yes; end of input means no, so
// nothing is installed without an answer.
func confirm(r io.Reader, question string) bool {
	yes, _ := confirmEnded(r, question)
	return yes
}

// confirmEnded is confirm that also reports whether input ended, so a caller
// that retries never loops on a default answer.
func confirmEnded(r io.Reader, question string) (yes, ended bool) {
	for {
		fmt.Fprint(ui.Out, "\n  "+question+" [Y/n] ")
		ans, err := readLine(r)
		ans = strings.ToLower(strings.TrimSpace(ans))
		switch {
		case ans == "" && err != nil:
			fmt.Fprintln(ui.Out)
			return false, true
		case ans == "" || ans == "y" || ans == "yes":
			return true, err != nil
		case ans == "n" || ans == "no":
			return false, err != nil
		}
		ui.Note("Please answer y or n.")
		if err != nil {
			return false, true
		}
	}
}

// readLine reads one line byte by byte, so nothing after it is taken from stdin.
func readLine(r io.Reader) (string, error) {
	var b strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				return b.String(), nil
			}
			b.WriteByte(buf[0])
		}
		if err != nil {
			if errors.Is(err, io.EOF) && b.Len() > 0 {
				return b.String(), nil
			}
			return b.String(), err
		}
	}
}

// runPipeline runs stages[0] | stages[1] | … without a shell, streaming the last
// stage's output and every stage's errors to out. It returns the first failing
// stage's exit code (like pipefail), 127 when a stage cannot start.
func runPipeline(stages [][]string, stdin io.Reader, out io.Writer) int {
	if len(stages) == 0 {
		return 0
	}
	cmds := make([]*exec.Cmd, len(stages))
	for i, st := range stages {
		cmds[i] = exec.Command(st[0], st[1:]...)
		cmds[i].Stderr = out
	}
	cmds[0].Stdin = stdin
	cmds[len(cmds)-1].Stdout = out
	var pipes []*os.File
	for i := 0; i < len(cmds)-1; i++ {
		r, w, err := os.Pipe()
		if err != nil {
			return 1
		}
		cmds[i].Stdout, cmds[i+1].Stdin = w, r
		pipes = append(pipes, r, w)
	}
	started := 0
	code := 0
	for _, c := range cmds {
		if err := c.Start(); err != nil {
			fmt.Fprintln(out, err)
			code = 127
			break
		}
		started++
	}
	for _, p := range pipes {
		_ = p.Close() // the children hold their own ends
	}
	for _, c := range cmds[:started] {
		if err := c.Wait(); err != nil && code == 0 {
			var ee *exec.ExitError
			if errors.As(err, &ee) && ee.ExitCode() > 0 {
				code = ee.ExitCode()
			} else {
				code = 1
			}
		}
	}
	return code
}
