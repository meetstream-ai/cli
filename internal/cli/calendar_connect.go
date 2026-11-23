package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/meetstream-ai/cli/internal/api"
	"github.com/meetstream-ai/cli/internal/login"
	"github.com/meetstream-ai/cli/internal/ui"
)

// calendarConnectOptions drives the one-click (hosted sign-in) calendar flow.
type calendarConnectOptions struct {
	Provider         string         // "google" or "microsoft"; empty asks with a menu (interactive only)
	JoinRule         string         // "accepted" (default) or "all"
	Open             bool           // open the browser even with --no-input
	DefaultBotConfig map[string]any // optional; the server keeps or sets its defaults when nil
	Question         string         // menu question when Provider is empty
	CancelLabel      string         // last menu row: "Cancel" (default) or "Skip"
}

// calendarConnectResult is the --json shape of `calendar connect`.
type calendarConnectResult struct {
	Provider  string `json:"provider"`
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	Email     string `json:"email"`
}

// errCalendarSkipped: the person chose Cancel/Skip in the provider menu.
var errCalendarSkipped = errors.New("calendar connect skipped")

// Seams for tests.
var (
	calendarOpenBrowser  = login.Open
	calendarPollInterval = 2 * time.Second
	calendarPollTimeout  = 10 * time.Minute
	calendarPick         = pickOption
)

// calendarProviders are the menu choices. Microsoft stays reachable with
// --provider microsoft but is left out of menus until its sign-in app is live,
// so nobody picks an option that always answers "not available".
var calendarProviders = []struct{ id, label string }{{"google", "Google"}}

var joinRuleText = map[string]string{
	"accepted": "MIA joins meetings you host or accepted",
	"all":      "MIA joins every meeting with a link",
}

// calendarCanPrompt is true when a person can answer a menu.
func calendarCanPrompt() bool { return !g.JSON && !g.NoInput && ui.Interactive() }

// calendarUnavailable is the answer for a server without hosted calendar sign-in.
func calendarUnavailable(msg string) error {
	return &ExitError{Code: 3, Message: msg,
		Hint: "connect with your own OAuth app instead: meetstream calendar connect --own-app --client-id <id> --client-secret <secret> --refresh-token <token>"}
}

// runCalendarConnect runs the one-click calendar sign-in: start a hosted OAuth
// session, open the browser (link printed as a fallback), and poll until the
// account is connected, failed, or the session expires. Human output goes to
// ui.Out; the caller emits the result. It returns errCalendarSkipped when the
// person picks Cancel/Skip, and an *ExitError with Code 3 when the server does
// not offer hosted sign-in yet.
func runCalendarConnect(cmd *cobra.Command, opts calendarConnectOptions) (*calendarConnectResult, error) {
	if opts.JoinRule == "" {
		opts.JoinRule = "accepted"
	}
	if _, ok := joinRuleText[opts.JoinRule]; !ok {
		return nil, usageErr("--join-rule must be accepted or all", "")
	}
	opts.Provider = strings.ToLower(strings.TrimSpace(opts.Provider))
	if opts.Provider != "" && opts.Provider != "google" && opts.Provider != "microsoft" {
		return nil, usageErr("--provider must be google or microsoft", "")
	}
	if opts.Provider == "" && !calendarCanPrompt() {
		return nil, usageErr("--provider is required here", "pass --provider google")
	}
	c, err := apiClient(cmd)
	if err != nil {
		return nil, err
	}

	if !g.JSON {
		ui.Header(cmd.Root().Version, "Connect your calendar")
		fmt.Fprintln(ui.Out)
		ui.Note("Join rule: " + joinRuleText[opts.JoinRule] + ".")
	}
	if opts.Provider == "" {
		question, cancel := opts.Question, opts.CancelLabel
		if question == "" {
			question = "Which calendar do you want to connect?"
		}
		if cancel == "" {
			cancel = "Cancel"
		}
		labels := []string{}
		for _, p := range calendarProviders {
			labels = append(labels, p.label)
		}
		i, err := calendarPick(question, append(labels, cancel)...)
		if err != nil {
			return nil, err
		}
		if i < 0 || i >= len(calendarProviders) {
			return nil, errCalendarSkipped
		}
		opts.Provider = calendarProviders[i].id
	}
	label := map[string]string{"google": "Google", "microsoft": "Microsoft"}[opts.Provider]

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()
	start, err := c.StartCalendarOAuth(ctx, opts.Provider, opts.JoinRule, opts.DefaultBotConfig)
	if err != nil {
		var ae *api.Error
		if errors.As(err, &ae) && ae.Status == 404 {
			return nil, calendarUnavailable("one-click calendar connect isn't available on this server yet")
		}
		if errors.As(err, &ae) && ae.Status == 503 {
			return nil, calendarUnavailable("calendar sign-in isn't available right now: " + ae.Detail())
		}
		return nil, apiError(err)
	}

	if g.JSON {
		fmt.Fprintln(os.Stderr, "Open this link to connect your calendar: "+start.AuthURL)
	} else {
		fmt.Fprintln(ui.Out)
		ui.Note("Sign in with " + label + " in your browser. If it doesn't open, open this link:")
		ui.Command(start.AuthURL)
		fmt.Fprintln(ui.Out)
	}
	if !g.NoInput || opts.Open {
		_ = calendarOpenBrowser(start.AuthURL) // the link above is the fallback
	}

	st, err := pollCalendarOAuth(ctx, c, start)
	if err != nil {
		return nil, err
	}
	res := &calendarConnectResult{Provider: opts.Provider, SessionID: start.SessionID, Status: st.Status, Email: st.AccountEmail}
	if st.Status != "connected" {
		return res, &ExitError{Code: 1, Message: "calendar sign-in did not finish (" + st.Status + ")",
			Hint: "try again: meetstream calendar connect"}
	}
	if !g.JSON {
		ui.OK("Connected " + orDash(st.AccountEmail))
		ui.Note("Auto-join is on: " + joinRuleText[opts.JoinRule] + ". Turn it off with:")
		ui.Command("meetstream calendar auto-join off")
	}
	return res, nil
}

// pollCalendarOAuth polls the status every calendarPollInterval until it is no
// longer pending, the session expires, or calendarPollTimeout passes.
func pollCalendarOAuth(ctx context.Context, c *api.Client, start *api.CalendarOAuthStart) (*api.CalendarOAuthState, error) {
	deadline := time.Now().Add(calendarPollTimeout)
	if start.ExpiresAt > 0 {
		// A little grace past expiry: the server then reports failed itself.
		if exp := time.Unix(start.ExpiresAt, 0).Add(5 * time.Second); exp.After(time.Now()) && exp.Before(deadline) {
			deadline = exp
		}
	}
	stopSpin := spin("Waiting for you to finish signing in (ctrl+c to stop)")
	defer stopSpin()
	transient := 0
	for {
		select {
		case <-ctx.Done():
			stopSpin()
			return nil, &ExitError{Code: 130, Message: "stopped waiting for calendar sign-in",
				Hint: "if you finished in the browser, check with: meetstream calendar list"}
		case <-time.After(calendarPollInterval):
		}
		st, err := c.CalendarOAuthStatus(ctx, start.SessionID)
		if err != nil {
			var ae *api.Error
			if ctx.Err() == nil && (!errors.As(err, &ae) || ae.Status >= 500) && transient < 3 {
				transient++ // network blip or 5xx: try again
				continue
			}
			stopSpin()
			if errors.As(err, &ae) && ae.Status == 404 {
				return nil, &ExitError{Code: 1, Message: "the calendar sign-in session was not found", Hint: "try again: meetstream calendar connect"}
			}
			return nil, apiError(err)
		}
		transient = 0
		if st.Status != "pending" {
			stopSpin()
			return st, nil
		}
		if time.Now().After(deadline) {
			stopSpin()
			return nil, &ExitError{Code: 1, Message: "timed out waiting for calendar sign-in", Hint: "try again: meetstream calendar connect"}
		}
	}
}

// spin shows a one-line spinner on a terminal; it returns an idempotent stop.
func spin(msg string) func() {
	f, ok := ui.Out.(*os.File)
	if g.JSON || !ok || !ui.IsTerminal(f) {
		ui.Note(msg + "…")
		return func() {}
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		style := lipgloss.NewStyle().Foreground(ui.Orange)
		for i := 0; ; i++ {
			fmt.Fprintf(f, "\r  %s %s", style.Render(frames[i%len(frames)]), ui.DimStyle.Render(msg))
			select {
			case <-done:
				fmt.Fprint(f, "\r\033[K")
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}()
	var once bool
	return func() {
		if !once {
			once = true
			close(done)
			<-stopped
		}
	}
}

// ── arrow-key menu ────────────────────────────────────────────────────────────

type calendarMenuModel struct {
	question string
	options  []string
	cursor   int
	chosen   int
}

func (m calendarMenuModel) Init() tea.Cmd { return nil }

func (m calendarMenuModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch k.String() {
	case "ctrl+c", "q", "esc":
		m.chosen = -1
		return m, tea.Quit
	case "up", "k":
		m.cursor = (m.cursor - 1 + len(m.options)) % len(m.options)
	case "down", "j", "tab":
		m.cursor = (m.cursor + 1) % len(m.options)
	case "enter":
		m.chosen = m.cursor
		return m, tea.Quit
	}
	return m, nil
}

func (m calendarMenuModel) View() string {
	var b strings.Builder
	b.WriteString("\n  " + ui.BoldStyle.Render(m.question) + "\n\n")
	sel := lipgloss.NewStyle().Foreground(ui.Orange).Bold(true)
	for i, label := range m.options {
		pointer, title := "  ", label
		if i == m.cursor {
			pointer, title = sel.Render("❯ "), sel.Render(label)
		}
		fmt.Fprintf(&b, "  %s%s\n", pointer, title)
	}
	b.WriteString("\n  " + ui.DimStyle.Render("↑/↓ move · enter choose · esc cancel") + "\n")
	return b.String()
}

// pickOption asks one question with an arrow-key menu on stderr. It returns the
// chosen index, or -1 when the person quits.
func pickOption(question string, options ...string) (int, error) {
	final, err := tea.NewProgram(calendarMenuModel{question: question, options: options, chosen: -1}, tea.WithOutput(os.Stderr)).Run()
	if err != nil {
		return -1, err
	}
	return final.(calendarMenuModel).chosen, nil
}
