package bench

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	green  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	red    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	yellow = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	cyan   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	grey   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	bold   = lipgloss.NewStyle().Bold(true)
	plain  = lipgloss.NewStyle()
)

// Frames is the spinner shown beside running models.
const Frames = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"

// Clip fits text into room terminal cells, ending in "…" when cut. Widths
// ignore ANSI codes and count wide characters as two cells.
func Clip(text string, room int) string {
	if lipgloss.Width(text) <= room {
		return text
	}
	if room <= 0 {
		return ""
	}
	var b strings.Builder
	used := 0
	for _, r := range text {
		w := lipgloss.Width(string(r))
		if used+w > room-1 {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String() + "…"
}

func firstLine(s string) string { return strings.SplitN(s, "\n", 2)[0] }

func modelName(m string) string {
	if m == "" {
		return "default"
	}
	return m
}

// Money formats a price like the kit: "$0.17", "<$0.01" below a cent.
func Money(v float64) string {
	if v >= 0.01 || v == 0 {
		return fmt.Sprintf("$%.2f", v)
	}
	return "<$0.01"
}

// HumanTokens formats a token count: 950, 1.2k, 45k, 1.3M.
func HumanTokens(n float64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", n/1_000_000)
	case n >= 10_000:
		return fmt.Sprintf("%.0fk", n/1_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", n/1_000)
	}
	return fmt.Sprint(int(n))
}

// HourText is a finished model's tokens per hour and that hour's price ("free", "≈ $0.17/h" or "—").
func HourText(m *Measured) (tokens, cost string) {
	if m == nil || m.Error != "" {
		return "", ""
	}
	if m.Usage == nil {
		return "usage n/a", ""
	}
	tokens = HumanTokens(float64(m.Usage.TokensPerHour)) + "/h"
	switch {
	case m.Usage.Free:
		cost = "free"
	case m.Usage.CostPerHour != nil:
		cost = "≈ " + Money(*m.Usage.CostPerHour) + "/h"
	default:
		cost = "—"
	}
	return tokens, cost
}

// Status is one row's mark, text and style: queued, a stage, retrying (1/1), ✓ or ✗ with the reason.
func Status(j Job, frame string) (mark, text string, style lipgloss.Style) {
	retried := ""
	if j.Retried {
		retried = " (retried once)"
	}
	switch j.State {
	case Done:
		return "✓", "answered" + retried, green
	case Failed:
		reason := firstLine(j.Reason)
		if reason == "" {
			reason = "failed"
		}
		return "✗", Clip(reason+retried, 36), red
	case Cancelled:
		if j.Reason != "" {
			return "·", j.Reason, grey
		}
		return "·", string(Cancelled), grey
	case Retrying:
		text := "retrying (1/1) · " + j.Stage
		if why := firstLine(j.Reason); why != "" {
			text += " · " + why
		}
		return frame, Clip(text, 36), yellow
	case Running:
		return frame, j.Stage, cyan
	}
	return "·", "queued", grey
}

type cell struct {
	text  string
	right bool
}

// Table renders the live table: one row per model, then a summary and the keys.
// stopping is true after Esc, while running models are being stopped.
func Table(jobs []Job, workers int, frame string, now time.Time, stopping bool) string {
	rows := make([][]cell, 0, len(jobs))
	for _, j := range jobs {
		mark, text, style := Status(j, frame)
		tokens, cost := HourText(j.Measured)
		seconds := ""
		if !j.Started.IsZero() {
			seconds = fmt.Sprintf("%.1f s", j.Elapsed(now).Seconds())
		}
		costStyle := grey
		if strings.HasPrefix(cost, "≈") {
			costStyle = yellow
		}
		rows = append(rows, []cell{
			{style.Render(mark), false},
			{plain.Render(Clip(modelName(j.Model), 34)), false},
			{style.Render(text), false},
			{grey.Render(seconds), true},
			{plain.Render(tokens), true},
			{costStyle.Render(cost), true},
		})
	}
	widths := make([]int, 6)
	for _, r := range rows {
		for i, c := range r {
			widths[i] = max(widths[i], lipgloss.Width(c.text))
		}
	}
	var b strings.Builder
	for _, r := range rows {
		parts := make([]string, len(r))
		for i, c := range r {
			pad := strings.Repeat(" ", widths[i]-lipgloss.Width(c.text))
			if c.right {
				parts[i] = pad + c.text
			} else {
				parts[i] = c.text + pad
			}
		}
		b.WriteString("  " + strings.TrimRight(strings.Join(parts, "  "), " ") + "\n")
	}
	finished, running := 0, 0
	counts := map[State]int{}
	for _, j := range jobs {
		counts[j.State]++
		if j.Finished() {
			finished++
		}
		if j.State == Running || j.State == Retrying {
			running++
		}
	}
	var summary string
	if finished < len(jobs) {
		summary = fmt.Sprintf("%d/%d done · %d running", finished, len(jobs), running)
		if left, ok := TimeLeft(jobs, workers, now); ok {
			summary += fmt.Sprintf(" · ~%.0f s left", left.Seconds())
		}
	} else {
		var parts []string
		for _, o := range []struct {
			state State
			word  string
		}{{Done, "answered"}, {Failed, "failed"}, {Cancelled, "stopped"}} {
			if counts[o.state] > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", counts[o.state], o.word))
			}
		}
		summary = strings.Join(parts, " · ")
	}
	b.WriteString("  " + bold.Render(summary) + "\n")
	if finished < len(jobs) {
		keys := "Esc stop (keeps the results so far)"
		if stopping {
			keys = "Stopping; the results so far are kept"
		}
		b.WriteString("  " + grey.Render(keys) + "\n")
	}
	return b.String()
}

// PlainLine is one line per finished (or retried) model, for logs and pipes
// where the live table cannot run; false for changes not worth a line.
// label is the agent's label ("Copilot CLI").
func PlainLine(label string, j Job) (string, bool) {
	name := modelName(j.Model)
	switch {
	case j.State == Retrying && j.Stage == "waiting":
		return fmt.Sprintf("  ↻ %s: %s; retrying once", name, Clip(firstLine(j.Reason), 100)), true
	case j.State == Done && j.Measured != nil:
		m := j.Measured
		if m.Usage == nil {
			return fmt.Sprintf("  ✓ %s answered in %.1f s · %s doesn't report token usage", name, m.Seconds, label), true
		}
		tokens, cost := HourText(m)
		return fmt.Sprintf("  ✓ %s  %s in · %s out  (%.1f s) · %s %s", name, HumanTokens(float64(m.Usage.Input)),
			HumanTokens(float64(m.Usage.Output)), m.Seconds, tokens, cost), true
	case j.State == Failed:
		return fmt.Sprintf("  ✗ %s: %s", name, Clip(j.Reason, 160)), true
	}
	return "", false
}

// ── Bubble Tea live table ──────────────────────────────────────────────────────

type tickMsg struct{}
type doneMsg struct{ jobs []Job }

// Model is the live table as a Bubble Tea model. It runs the queue, redraws
// ten times a second, and quits when every model is finished. Esc (or q,
// ctrl+c) stops: running models are cancelled, finished results are kept,
// and the model quits once the queue has wound down.
type Model struct {
	queue       *Queue
	ctx         context.Context
	frame       int
	stopping    bool
	interrupted bool // ctrl+c, not Esc: the command exits 130
	final       []Job
	now         func() time.Time
}

// NewModel shows q while it runs; ctx cancels it like Esc does.
func NewModel(ctx context.Context, q *Queue) Model {
	return Model{queue: q, ctx: ctx, now: time.Now}
}

func tick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

// Init starts the queue and the redraw tick.
func (m Model) Init() tea.Cmd {
	q, ctx := m.queue, m.ctx
	return tea.Batch(func() tea.Msg { return doneMsg{q.Run(ctx)} }, tick())
}

// Update handles keys, ticks and the queue finishing.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q", "ctrl+c":
			m.stopping = true
			m.interrupted = m.interrupted || msg.String() == "ctrl+c"
			m.queue.Stop()
		}
	case tickMsg:
		if m.final == nil {
			m.frame++
			return m, tick()
		}
	case doneMsg:
		m.final = msg.jobs
		return m, tea.Quit
	}
	return m, nil
}

// View is the table: the spinner while running, none once finished.
func (m Model) View() string {
	if m.final != nil {
		return Table(m.final, m.queue.Workers(), "", m.now(), false)
	}
	frames := []rune(Frames)
	return Table(m.queue.Snapshot(), m.queue.Workers(), string(frames[m.frame%len(frames)]), m.now(), m.stopping)
}

// Jobs are the final jobs once the queue has finished (else a snapshot).
func (m Model) Jobs() []Job {
	if m.final != nil {
		return m.final
	}
	return m.queue.Snapshot()
}

// Stopped reports whether the person pressed Esc.
func (m Model) Stopped() bool { return m.stopping }

// Interrupted reports whether the stop was ctrl+c.
func (m Model) Interrupted() bool { return m.interrupted }
