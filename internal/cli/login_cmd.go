package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"golang.org/x/term"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/meetstream-ai/cli/internal/api"
	"github.com/meetstream-ai/cli/internal/config"
	"github.com/meetstream-ai/cli/internal/login"
	"github.com/meetstream-ai/cli/internal/ui"
)

func newLoginCmd(info BuildInfo) *cobra.Command {
	var withKey, noBrowser, noCalendar bool
	cmd := &cobra.Command{
		Use:     "login",
		Short:   "Sign in with your browser and store a named API key",
		GroupID: "start",
		Args:    cobra.NoArgs,
		Long: `Sign in with your browser. MeetStream creates an API key named for this CLI and
stores it in ` + config.Path() + ` (0600). The key is never shown.

For CI, pipe a key in instead:  printf %s "$KEY" | meetstream login --with-key`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			var res *login.Result
			if withKey {
				key, err := readKey(os.Stdin)
				if err != nil {
					return err
				}
				res = &login.Result{APIKey: key, APIKeyName: "set with --with-key"}
			} else {
				opts := login.Options{
					ShowURL: func(u string) {
						if !g.JSON {
							ui.Header(info.Version, "Signing in with your browser")
							fmt.Fprintln(ui.Out)
							ui.Note("If the browser doesn't open, open this link:")
							ui.Command(u)
							fmt.Fprintln(ui.Out)
						} else {
							fmt.Fprintln(os.Stderr, "Open this link to sign in: "+u)
						}
					},
				}
				if noBrowser {
					opts.OpenBrowser = func(string) error { return nil }
				}
				var err error
				res, err = login.Run(ctx, opts)
				if errors.Is(err, login.ErrNoKey) {
					// Browser sign-in for the CLI needs the CLI client on the
					// sign-in server. Until then, a pasted key is the way in.
					if g.JSON || g.NoInput || !ui.Interactive() {
						return &ExitError{Code: 2, Message: "browser sign-in isn't available on " + login.AuthURL() + " yet",
							Hint: "create a key at https://app.meetstream.ai/api-keys, then: printf %s \"$KEY\" | meetstream auth set-key  (or set MEETSTREAM_API_KEY)"}
					}
					ui.Header(info.Version, "Sign in with an API key")
					fmt.Fprintln(ui.Out)
					ui.Warn("Browser sign-in for the CLI isn't switched on at " + login.AuthURL() + " yet.")
					ui.Note("Use an API key instead. Create one here, then paste it below (it stays hidden):")
					ui.Command("https://app.meetstream.ai/api-keys")
					fmt.Fprintln(ui.Out)
					key, perr := promptKey()
					if perr != nil {
						return perr
					}
					res, err = &login.Result{APIKey: key, APIKeyName: "pasted at meetstream login"}, nil
				}
				if err != nil {
					return err
				}
			}

			if err := saveKey(cmd, info, res); err != nil {
				return err
			}
			if noCalendar || !calendarCanPrompt() {
				return nil
			}
			return offerCalendarConnect(cmd)
		},
	}
	cmd.Flags().BoolVar(&withKey, "with-key", false, "read an API key from stdin instead of the browser (for CI)")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "print the sign-in link instead of opening a browser")
	cmd.Flags().BoolVar(&noCalendar, "no-calendar", false, "don't offer to connect a calendar after signing in")
	return cmd
}

func botsLabel(n int, more bool) string {
	if more {
		return fmt.Sprintf("%d+", n)
	}
	return fmt.Sprint(n)
}

// readKey reads one key from stdin; it refuses an interactive terminal so the
// key never sits in shell history or on screen.
func readKey(r io.Reader) (string, error) {
	if f, ok := r.(*os.File); ok && ui.IsTerminal(f) {
		return "", usageErr("--with-key reads the key from stdin", `pipe it in: printf %s "$MEETSTREAM_KEY" | meetstream login --with-key`)
	}
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	key := strings.TrimSpace(line)
	if key == "" {
		return "", usageErr("no key on stdin", `pipe it in: printf %s "$MEETSTREAM_KEY" | meetstream login --with-key`)
	}
	return key, nil
}

// validate checks a key against the live API for auth status.
func validate(ctx context.Context, version, key string) (string, error) {
	n, more, err := api.New(config.APIURL(), key, version).BotCount(ctx)
	if err != nil {
		return "", err
	}
	return botsLabel(n, more), nil
}

// promptKey reads a key from the terminal without echoing it.
func promptKey() (string, error) {
	fmt.Fprint(os.Stderr, "  API key: ")
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(b))
	if key == "" {
		return "", usageErr("no key entered", "create one at https://app.meetstream.ai/api-keys")
	}
	return key, nil
}

// newSetKeyCmd is `auth set-key`, the JS CLI's command, kept for scripts and
// as the sign-in path until browser login is live everywhere.
func newSetKeyCmd(info BuildInfo) *cobra.Command {
	return &cobra.Command{
		Use:   "set-key [api-key]",
		Short: "Store an API key (from stdin, a hidden prompt, or the argument)",
		Long: `Store an API key in ` + config.Path() + ` (0600), after checking it with the API.

  printf %s "$KEY" | meetstream auth set-key     # from stdin (best for scripts)
  meetstream auth set-key                        # hidden prompt
  meetstream auth set-key ms_...                 # works, but lands in shell history`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var key string
			var err error
			switch {
			case len(args) == 1:
				key = strings.TrimSpace(args[0])
				if !g.JSON {
					ui.Warn("A key typed as an argument is saved in your shell history. Prefer stdin or the prompt.")
				}
			case !ui.IsTerminal(os.Stdin):
				key, err = readKey(os.Stdin)
			case g.NoInput:
				return usageErr("no key given", `pipe it in: printf %s "$KEY" | meetstream auth set-key`)
			default:
				ui.Note("Create a key at https://app.meetstream.ai/api-keys")
				key, err = promptKey()
			}
			if err != nil {
				return err
			}
			return saveKey(cmd, info, &login.Result{APIKey: key, APIKeyName: "set with auth set-key"})
		},
	}
}

// saveKey checks a key with the API, stores it, and reports (never showing the key).
func saveKey(cmd *cobra.Command, info BuildInfo, res *login.Result) error {
	ctx := cmd.Context()
	bots, more, verr := api.New(config.APIURL(), res.APIKey, info.Version).BotCount(ctx)
	var apiErr *api.Error
	if errors.As(verr, &apiErr) && (apiErr.Status == 401 || apiErr.Status == 403) {
		return &ExitError{Code: 2, Message: "MeetStream rejected that API key", Hint: "check the key, or run meetstream login without --with-key"}
	}

	c, err := config.Load()
	if err != nil {
		return fmt.Errorf("could not read %s: %w", config.Path(), err)
	}
	c.APIKey, c.APIKeyName = res.APIKey, res.APIKeyName
	if err := c.Save(); err != nil {
		return fmt.Errorf("could not save %s: %w", config.Path(), err)
	}
	envWins := os.Getenv("MEETSTREAM_API_KEY") != ""
	out := map[string]any{"signed_in": true, "key": config.Mask(res.APIKey), "key_name": res.APIKeyName,
		"config_path": config.Path(), "validated": verr == nil, "env_overrides": envWins}
	if verr == nil {
		out["bots_on_account"] = botsLabel(bots, more)
	}
	return emit(out, func() {
		ui.OK("Signed in. Key " + config.Mask(res.APIKey) + " saved to " + ui.ShortPath(config.Path()))
		if res.APIKeyName != "" {
			ui.Note("Key name: " + res.APIKeyName + " (manage it at https://app.meetstream.ai/api-keys)")
		}
		if verr != nil {
			ui.Warn("Saved, but could not check the key with the API: " + verr.Error())
		}
		if envWins {
			ui.Warn("MEETSTREAM_API_KEY is set in this shell and takes precedence over the saved key.")
		}
		fmt.Fprintln(ui.Out)
		ui.Note("Next:")
		ui.Command("meetstream doctor")
	})
}

// offerCalendarConnect asks, after an interactive login, whether to connect a
// calendar. Skip, or a server without one-click connect, leaves a note for later;
// other failures are warnings, since the login itself succeeded.
func offerCalendarConnect(cmd *cobra.Command) error {
	_, err := runCalendarConnect(cmd, calendarConnectOptions{
		Question:    "Connect your calendar so MIA can join your meetings?",
		CancelLabel: "Skip",
	})
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
	fmt.Fprintln(ui.Out)
	ui.Note("Connect your calendar later with:")
	ui.Command("meetstream calendar connect")
	return nil
}
