package bench

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// DefaultAssumptions describes the usage package's default one-hour projection.
const DefaultAssumptions = "20 answered turns an hour, one warm session; each turn adds ~120 tokens of meeting text plus the answer to the context"

const eighths = " ▏▎▍▌▋▊▉"

// Bar is a horizontal bar in eighths of a cell.
func Bar(value, largest float64, width int) string {
	if largest <= 0 || value <= 0 {
		return ""
	}
	n := max(1, int(math.RoundToEven(value/largest*float64(width)*8)))
	full, rest := n/8, n%8
	out := strings.Repeat("█", full)
	if rest > 0 {
		out += string([]rune(eighths)[rest])
	}
	return out
}

// ChartOptions shape ChartLines.
type ChartOptions struct {
	Selected    string // this model gets a ★
	Width       int    // bar width; 0 fits TermWidth
	TermWidth   int    // 0: 88; clamped to 20..100 like the kit
	Assumptions string // the projection, described; "" is DefaultAssumptions
}

var (
	cyanBar = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	orange  = lipgloss.NewStyle().Foreground(lipgloss.Color("208"))
	dim     = lipgloss.NewStyle().Faint(true)
)

func wrap(text string, width int, indent string) []string {
	var lines []string
	line := indent
	for _, word := range strings.Fields(text) {
		if line != indent && lipgloss.Width(line)+1+lipgloss.Width(word) > width {
			lines = append(lines, line)
			line = indent
		}
		if line != indent {
			line += " "
		}
		line += word
	}
	if line != indent {
		lines = append(lines, line)
	}
	return lines
}

func padRight(s string, w int) string { return s + strings.Repeat(" ", max(0, w-lipgloss.Width(s))) }
func padLeft(s string, w int) string  { return strings.Repeat(" ", max(0, w-lipgloss.Width(s))) + s }

// ChartLines draws the estimate as horizontal bars: model, tokens per hour,
// the measured turn's seconds and the hour at API rates where known.
func ChartLines(results []Measured, o ChartOptions) []string {
	term := o.TermWidth
	if term <= 0 {
		term = 88
	}
	term = max(20, min(term, 100))
	assumptions := o.Assumptions
	if assumptions == "" {
		assumptions = DefaultAssumptions
	}
	var rows []Measured
	noUsage := false
	for _, m := range results {
		if m.Error == "" && m.Usage != nil {
			rows = append(rows, m)
		} else if m.Error == "" {
			noUsage = true
		}
	}
	if len(rows) == 0 {
		if noUsage {
			return []string{"  This CLI doesn't report token usage, so there is no usage to chart (times are above)."}
		}
		return []string{"  No model reported token usage, so there is nothing to chart."}
	}
	nameW, largest := 0, 0.0
	costs := make([]string, len(rows))
	costW := 0
	for i, m := range rows {
		nameW = max(nameW, len([]rune(modelName(m.Model))))
		largest = max(largest, float64(m.Usage.TokensPerHour))
		switch {
		case m.Usage.Free:
			costs[i] = "free"
		case m.Usage.CostPerHour != nil:
			costs[i] = "≈ " + Money(*m.Usage.CostPerHour) + "/h"
		default:
			costs[i] = "—"
		}
		costW = max(costW, lipgloss.Width(costs[i]))
	}
	nameW = min(32, nameW)
	width := o.Width
	if width <= 0 { // fit the terminal: everything but the bar is fixed width
		width = max(8, min(28, term-(nameW+27+costW)-1))
	}
	lines := []string{bold.Render("Estimated tokens for one hour of meeting") + dim.Render("  (an estimate)"), ""}
	for i, m := range rows {
		name := []rune(modelName(m.Model))
		if len(name) > nameW {
			name = name[:nameW]
		}
		mark := " "
		if m.Model == o.Selected {
			mark = orange.Render("★")
		}
		bar := cyanBar.Render(padRight(Bar(float64(m.Usage.TokensPerHour), largest, width), width))
		lines = append(lines, fmt.Sprintf(" %s %s  %s  %s tok/h  %s  %s", mark, padRight(string(name), nameW), bar,
			padLeft(HumanTokens(float64(m.Usage.TokensPerHour)), 6), padLeft(fmt.Sprintf("%.0f s", m.Seconds), 4), dim.Render(costs[i])))
	}
	notes := []string{
		"Assumes " + assumptions + ".",
		"Measured from one real short turn per model on this computer. The seconds are that turn, " +
			"started cold; warm meeting turns answer faster. Real meetings vary.",
		"Prices are API-rate estimates: the CLI's own where it reports one, else the vendor's published " +
			"per-token list price (— when unknown). On a subscription or free plan the same tokens count " +
			"against your plan instead of a bill.",
	}
	lines = append(lines, "")
	for _, note := range notes {
		for _, l := range wrap(note, max(40, term-4), "  ") {
			lines = append(lines, dim.Render(l))
		}
	}
	return lines
}
