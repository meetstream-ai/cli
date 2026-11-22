package cli

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/meetstream-ai/cli/internal/config"
	"github.com/meetstream-ai/cli/internal/mascot"
	"github.com/meetstream-ai/cli/internal/ui"
)

// The home screen: `meetstream` with no arguments in a terminal.

type homeItem struct {
	args  []string
	title string
	desc  string
	soon  string
}

var homeItems = []homeItem{
	{[]string{"doctor"}, "Check my setup", "API key, config file and agent CLIs", ""},
	{[]string{"login"}, "Sign in", "Browser sign-in, stores a named key", ""},
	{[]string{"join"}, "Send a bot to a meeting", "Zoom, Google Meet, Microsoft Teams", ""},
	{[]string{"bots"}, "My bots", "List, tail, transcripts, recordings", ""},
	{[]string{"setup"}, "Bring my agent into the call", "Claude Code, Codex, Hermes, Copilot CLI…", "M4"},
	{[]string{"docs"}, "Read the docs", "README rendered in your terminal", ""},
}

type homeModel struct {
	version  string
	signedIn string
	cursor   int
	chosen   []string
	width    int
	frame    int
	waves    int
}

type tickMsg struct{}

// MIA waves when the home screen opens, then rests on her first frame.
func tick() tea.Cmd {
	return tea.Tick(time.Second/12, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m homeModel) Init() tea.Cmd {
	if mascot.Enabled() && mascot.Frames("idle") > 1 {
		return tick()
	}
	return nil
}

func (m homeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		m.frame = (m.frame + 1) % mascot.Frames("idle")
		if m.frame == 0 {
			m.waves++
		}
		if m.waves < 2 {
			return m, tick()
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			return m, tea.Quit
		case "up", "k":
			m.cursor = (m.cursor - 1 + len(homeItems)) % len(homeItems)
		case "down", "j", "tab":
			m.cursor = (m.cursor + 1) % len(homeItems)
		case "enter":
			m.chosen = homeItems[m.cursor].args
			return m, tea.Quit
		}
	}
	return m, nil
}

var (
	selStyle  = lipgloss.NewStyle().Foreground(ui.Orange).Bold(true)
	soonStyle = lipgloss.NewStyle().Foreground(ui.Dim).Italic(true)
)

func (m homeModel) View() string {
	var b strings.Builder
	head := lipgloss.JoinVertical(lipgloss.Left,
		ui.TitleStyle.Render("◆ MeetStream")+"  "+ui.DimStyle.Render("v"+m.version),
		"Meeting bots, transcripts and your own agent in the call",
		ui.DimStyle.Render(m.signedIn),
	)
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ui.Border).Padding(0, 2).Render(head)
	b.WriteString(mascot.Beside("idle", m.frame, box))
	b.WriteString("\n\n  " + ui.BoldStyle.Render("What do you want to do?") + "\n\n")
	for i, it := range homeItems {
		pointer, title := "  ", it.title
		if i == m.cursor {
			pointer, title = selStyle.Render("❯ "), selStyle.Render(it.title)
		}
		soon := ""
		if it.soon != "" {
			soon = "  " + soonStyle.Render("coming in "+it.soon)
		}
		fmt.Fprintf(&b, "  %s%s%s\n      %s\n", pointer, title, soon, ui.DimStyle.Render(it.desc))
	}
	b.WriteString("\n  " + ui.DimStyle.Render("↑/↓ move · enter choose · q quit · meetstream --help for all commands") + "\n")
	return b.String()
}

func runHome(cmd *cobra.Command, info BuildInfo) error {
	status := "Not signed in · run meetstream login"
	if key, src := config.ResolveKey(); key != "" {
		status = "Signed in · key " + config.Mask(key) + " (" + map[string]string{"env": "MEETSTREAM_API_KEY", "file": "~/.meetstream"}[src] + ")"
	}
	final, err := tea.NewProgram(homeModel{version: info.Version, signedIn: status}).Run()
	if err != nil {
		return err
	}
	m := final.(homeModel)
	if m.chosen == nil {
		return nil
	}
	root := cmd.Root()
	root.SetArgs(m.chosen)
	root.RunE = nil // don't loop back into the home screen
	return root.Execute()
}
