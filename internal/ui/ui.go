// Package ui holds the CLI's look: the kit's orange ◆ header, status marks and
// boxes, drawn with Lip Gloss. Everything degrades to plain text when stdout is
// not a terminal or NO_COLOR is set, so scripts and logs stay readable.
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/meetstream-ai/cli/internal/mascot"
	"github.com/muesli/termenv"
	"golang.org/x/term"
)

// Palette, matching the agent kit (orange 208, cyan #22d3ee, greys).
var (
	Orange = lipgloss.Color("208")
	Cyan   = lipgloss.Color("#22d3ee")
	Green  = lipgloss.Color("#22c55e")
	Red    = lipgloss.Color("#ef4444")
	Yellow = lipgloss.Color("#eab308")
	Dim    = lipgloss.Color("244")
	Border = lipgloss.Color("240")
)

var (
	TitleStyle = lipgloss.NewStyle().Foreground(Orange).Bold(true)
	DimStyle   = lipgloss.NewStyle().Foreground(Dim)
	CmdStyle   = lipgloss.NewStyle().Foreground(Cyan)
	BoldStyle  = lipgloss.NewStyle().Bold(true)
	okStyle    = lipgloss.NewStyle().Foreground(Green).Bold(true)
	failStyle  = lipgloss.NewStyle().Foreground(Red).Bold(true)
	warnStyle  = lipgloss.NewStyle().Foreground(Yellow).Bold(true)
)

// Out is where human output goes. With --json it is stderr, so stdout stays parseable.
var Out io.Writer = os.Stdout

// Setup decides colour once, from the flags and the environment.
func Setup(noColor bool, jsonMode bool) {
	headerShown = false
	if jsonMode {
		Out = os.Stderr
	}
	switch {
	case noColor || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb":
		lipgloss.SetColorProfile(termenv.Ascii)
	case os.Getenv("FORCE_COLOR") != "":
		lipgloss.SetColorProfile(termenv.TrueColor)
	case !IsTerminal(os.Stdout):
		lipgloss.SetColorProfile(termenv.Ascii)
	}
}

// ShortPath shows ~/x instead of /Users/you/x, to keep lines short and private.
func ShortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "/" && strings.HasPrefix(p, home+string(os.PathSeparator)) {
		return "~" + p[len(home):]
	}
	return p
}

// IsTerminal reports whether f is a terminal.
func IsTerminal(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

// Interactive is true when a person is at the keyboard: stdin and stdout are terminals.
func Interactive() bool { return IsTerminal(os.Stdin) && IsTerminal(os.Stdout) }

func line(s string) { fmt.Fprintln(Out, s) }

func OK(s string)      { line(OKLine(s)) }
func Fail(s string)    { line(FailLine(s)) }
func Warn(s string)    { line(WarnLine(s)) }
func Note(s string)    { line(NoteLine(s)) }
func Command(s string) { line(CommandLine(s)) }

// The same lines as strings, for views drawn inside the home app.
func OKLine(s string) string      { return "  " + okStyle.Render("✓") + " " + s }
func FailLine(s string) string    { return "  " + failStyle.Render("✗") + " " + s }
func WarnLine(s string) string    { return "  " + warnStyle.Render("!") + " " + s }
func NoteLine(s string) string    { return "    " + DimStyle.Render(s) }
func CommandLine(s string) string { return "      " + CmdStyle.Render(s) }

// headerShown makes Header draw the mascot banner once per command run; a
// sub-flow (login, then the calendar offer) gets a small titled rule instead.
var headerShown bool

// Header is the kit's rounded banner: "◆ MeetStream  v1.2.3" over a tagline.
// The second and later calls in one run draw only a small section rule.
func Header(version, tagline string) {
	if headerShown {
		SubHeader(tagline)
		return
	}
	headerShown = true
	top := TitleStyle.Render("◆ MeetStream") + "  " + DimStyle.Render("v"+version)
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(Border).
		Padding(0, 2).
		Render(top + "\n" + tagline)
	line("")
	line(mascot.Beside("idle", 0, box))
}

// Box draws a titled panel of "label  value" rows.
func Box(title string, rows [][2]string) {
	w := 0
	for _, r := range rows {
		if len(r[0]) > w {
			w = len(r[0])
		}
	}
	var b strings.Builder
	b.WriteString(BoldStyle.Render(title))
	for _, r := range rows {
		b.WriteString("\n" + DimStyle.Render(fmt.Sprintf("%-*s", w, r[0])) + "  " + r[1])
	}
	line(lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(Border).Padding(0, 1).Render(b.String()))
}

// SubHeader is a small titled rule for a flow inside a command: "── Title ────".
func SubHeader(title string) {
	label := " " + title + " "
	line("")
	line(DimStyle.Render("──") + lipgloss.NewStyle().Foreground(Cyan).Bold(true).Render(label) + DimStyle.Render(strings.Repeat("─", max(4, 60-len(label)))))
}

// Step is a numbered section rule: "── 1/3  Title ─────".
func Step(n, total int, title string) {
	label := fmt.Sprintf(" %d/%d  %s ", n, total, title)
	rule := strings.Repeat("─", max(4, 60-len(label)))
	line("")
	line(DimStyle.Render("──") + lipgloss.NewStyle().Foreground(Cyan).Bold(true).Render(label) + DimStyle.Render(rule))
}
