package ui

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

// Blue is the "scheduled" colour; the rest of the palette is in ui.go.
var Blue = lipgloss.Color("#60a5fa")

// Width is the terminal width for human views: $COLUMNS, then the size of
// stdout or stderr, else 100. It never goes below 40.
func Width() int {
	if n, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && n > 0 {
		return max(n, 40)
	}
	for _, f := range []*os.File{os.Stdout, os.Stderr} {
		if w, _, err := term.GetSize(int(f.Fd())); err == nil && w > 0 {
			return max(w, 40)
		}
	}
	return 100
}

// Column describes one table column. Shrink marks the columns that may be cut
// (with …) when the table is wider than the terminal; the widest go first.
type Column struct {
	Title  string
	Shrink bool
	Dim    bool
	Min    int // floor when shrinking (default 8)
	Drop   int // when the table still does not fit, columns go highest Drop first (0 = never)
}

// Table renders rows under a dim header with a rounded border and no column
// rules, fitted to width (0 = Width()). Cells may already carry colour.
func Table(cols []Column, rows [][]string, width int) string {
	if width <= 0 {
		width = Width()
	}
	for {
		need := 2 + 2*len(cols)
		drop := -1
		for i, c := range cols {
			floor := c.Min
			if floor == 0 {
				floor = 8
			}
			w := ansi.StringWidth(c.Title)
			for _, r := range rows {
				if i < len(r) {
					w = max(w, ansi.StringWidth(r[i]))
				}
			}
			if c.Shrink {
				w = min(w, floor)
			}
			need += w
			if c.Drop > 0 && (drop < 0 || c.Drop > cols[drop].Drop) {
				drop = i
			}
		}
		if need <= width || drop < 0 {
			break
		}
		cols = append(append([]Column{}, cols[:drop]...), cols[drop+1:]...)
		trimmed := make([][]string, len(rows))
		for j, r := range rows {
			if drop < len(r) {
				r = append(append([]string{}, r[:drop]...), r[drop+1:]...)
			}
			trimmed[j] = r
		}
		rows = trimmed
	}
	n := len(cols)
	widths := make([]int, n)
	for i, c := range cols {
		widths[i] = ansi.StringWidth(c.Title)
	}
	for _, r := range rows {
		for i := 0; i < n && i < len(r); i++ {
			widths[i] = max(widths[i], ansi.StringWidth(r[i]))
		}
	}
	// Each column has one space of padding on each side, plus the two outer borders.
	budget := width - 2 - 2*n
	for sum(widths) > budget {
		wi := -1
		for i, c := range cols {
			floor := c.Min
			if floor == 0 {
				floor = 8
			}
			if c.Shrink && widths[i] > floor && (wi < 0 || widths[i] > widths[wi]) {
				wi = i
			}
		}
		if wi < 0 {
			break
		}
		widths[wi]--
	}
	headers := make([]string, n)
	for i, c := range cols {
		headers[i] = ansi.Truncate(c.Title, widths[i], "…")
	}
	t := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(Border)).
		BorderColumn(false).BorderRow(false).BorderHeader(true).
		Headers(headers...)
	for _, r := range rows {
		cells := make([]string, n)
		for i := 0; i < n && i < len(r); i++ {
			cells[i] = ansi.Truncate(strings.ReplaceAll(r[i], "\n", " "), widths[i], "…")
		}
		t.Row(cells...)
	}
	t.StyleFunc(func(row, col int) lipgloss.Style {
		s := lipgloss.NewStyle().Padding(0, 1).Width(widths[col] + 2)
		if row == table.HeaderRow {
			return s.Foreground(Dim)
		}
		if cols[col].Dim {
			return s.Foreground(Dim)
		}
		return s
	})
	return t.Render()
}

func sum(xs []int) int {
	t := 0
	for _, x := range xs {
		t += x
	}
	return t
}

// Truncate cuts s to w cells with an ellipsis.
func Truncate(s string, w int) string { return ansi.Truncate(s, w, "…") }

// Section is a small bold heading for a group of rows.
func Section(title string) string { return BoldStyle.Render(title) }

// KV renders aligned "label  value" rows, labels dim, indented by indent.
func KV(rows [][2]string, indent int) string {
	w := 0
	for _, r := range rows {
		w = max(w, ansi.StringWidth(r[0]))
	}
	pad := strings.Repeat(" ", indent)
	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteString("\n")
		}
		label := r[0] + strings.Repeat(" ", w-ansi.StringWidth(r[0]))
		b.WriteString(pad + DimStyle.Render(label) + "  " + r[1])
	}
	return b.String()
}

// Card is a rounded panel around body.
func Card(body string) string {
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(Border).Padding(0, 1).Render(body)
}

// Pill is a coloured status label: "● Done".
func Pill(label string, color lipgloss.TerminalColor) string {
	return lipgloss.NewStyle().Foreground(color).Render("● " + label)
}

// DimPill is a status that needs no attention (cancelled, denied).
func DimPill(label string) string { return DimStyle.Render("○ " + label) }

// Count formats 7334 as "7,334".
func Count(n int) string {
	s := strconv.Itoa(n)
	if n < 0 {
		return "-" + Count(-n)
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// ParseTime reads the API's timestamps: RFC 3339 / ISO 8601 strings (with or
// without a zone; no zone means UTC) and Unix seconds or milliseconds.
func ParseTime(v any) (time.Time, bool) {
	switch x := v.(type) {
	case float64:
		return unix(x)
	case int:
		return unix(float64(x))
	case int64:
		return unix(float64(x))
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return time.Time{}, false
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return unix(f)
		}
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999", "2006-01-02T15:04", "2006-01-02"} {
			if t, err := time.Parse(layout, s); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

func unix(f float64) (time.Time, bool) {
	if f <= 0 || math.IsNaN(f) {
		return time.Time{}, false
	}
	if f > 1e12 { // milliseconds
		f /= 1000
	}
	sec, frac := math.Modf(f)
	return time.Unix(int64(sec), int64(frac*1e9)), true
}

// Now is the clock for relative times; tests pin it.
var Now = time.Now

// When is a short relative time: "just now", "5 min ago", "2 h ago",
// "yesterday 14:05", "3 days ago", "in 20 min", "today 15:00",
// "tomorrow 10:00", "Mon 10:00", "Mar 4", "Mar 4, 2025". Empty when v is not a time.
func When(v any) string {
	t, ok := ParseTime(v)
	if !ok {
		return ""
	}
	return WhenTime(t)
}

func WhenTime(t time.Time) string {
	now := Now()
	t = t.In(now.Location())
	d := now.Sub(t)
	day := func(x time.Time) time.Time {
		y, m, dd := x.Date()
		return time.Date(y, m, dd, 0, 0, 0, 0, x.Location())
	}
	days := int(math.Round(day(t).Sub(day(now)).Hours() / 24))
	clock := t.Format("15:04")
	switch {
	case d >= 0 && d < time.Minute:
		return "just now"
	case d >= 0 && d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d >= 0 && d < 12*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	case d >= 0 && days == 0:
		return "today " + clock
	case d >= 0 && days == -1:
		return "yesterday " + clock
	case d >= 0 && days > -7:
		return fmt.Sprintf("%d days ago", -days)
	case d < 0 && -d < time.Hour:
		return fmt.Sprintf("in %d min", int(math.Ceil(-d.Minutes())))
	case d < 0 && days == 0:
		return "today " + clock
	case d < 0 && days == 1:
		return "tomorrow " + clock
	case d < 0 && days < 7:
		return t.Format("Mon") + " " + clock
	case t.Year() == now.Year():
		return t.Format("Jan 2")
	}
	return t.Format("Jan 2, 2006")
}

// Duration formats seconds as "35 s", "42 min" or "1 h 05 min"; "" for none.
func Duration(v any) string {
	var s float64
	switch x := v.(type) {
	case float64:
		s = x
	case int:
		s = float64(x)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			return ""
		}
		s = f
	default:
		return ""
	}
	if s <= 0 || math.IsNaN(s) {
		return ""
	}
	n := int(math.Round(s))
	switch {
	case n < 60:
		return fmt.Sprintf("%d s", n)
	case n < 3600:
		return fmt.Sprintf("%d min", n/60)
	}
	return fmt.Sprintf("%d h %02d min", n/3600, n%3600/60)
}

// Clock formats seconds from the start of a meeting as "04:12" or "1:04:12".
func Clock(sec float64) string {
	n := int(sec)
	if n < 0 {
		n = 0
	}
	if n >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", n/3600, n%3600/60, n%60)
	}
	return fmt.Sprintf("%02d:%02d", n/60, n%60)
}

// Wrap word-wraps s to w cells and indents every line after the first by hang spaces.
func Wrap(s string, w, hang int) string {
	if w < 10 {
		w = 10
	}
	out := lipgloss.NewStyle().Width(w).Render(s)
	lines := strings.Split(out, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
		if i > 0 {
			lines[i] = strings.Repeat(" ", hang) + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}
