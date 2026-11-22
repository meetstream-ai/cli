package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/charmbracelet/glamour"
	"github.com/spf13/cobra"

	meetstream "github.com/meetstream-ai/cli"
	"github.com/meetstream-ai/cli/internal/api"
	"github.com/meetstream-ai/cli/internal/config"
	"github.com/meetstream-ai/cli/internal/ui"
)

// ── Working in M1 ─────────────────────────────────────────────────────────────

func newVersionCmd(info BuildInfo) *cobra.Command {
	return &cobra.Command{
		Use: "version", Short: "Print the version", GroupID: "tool", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			v := map[string]string{"version": info.Version, "commit": info.Commit, "date": info.Date, "os": runtime.GOOS, "arch": runtime.GOARCH}
			return emit(v, func() {
				fmt.Fprintf(ui.Out, "meetstream %s (%s, %s/%s)\n", info.Version, info.Commit, runtime.GOOS, runtime.GOARCH)
			})
		},
	}
}

type check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

func newDoctorCmd(info BuildInfo) *cobra.Command {
	return &cobra.Command{
		Use: "doctor", Short: "Check your setup: API key, config file, tools", GroupID: "start", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var checks []check
			key, src := config.ResolveKey()
			switch src {
			case "env":
				checks = append(checks, check{"API key", true, "MEETSTREAM_API_KEY " + config.Mask(key), ""})
			case "file":
				checks = append(checks, check{"API key", true, ui.ShortPath(config.Path()) + " " + config.Mask(key), ""})
			default:
				checks = append(checks, check{"API key", false, "not set", "meetstream login"})
			}
			checks = append(checks, check{"API URL", true, config.APIURL(), ""})
			for _, tool := range []string{"claude", "codex", "gemini"} {
				if p, err := exec.LookPath(tool); err == nil {
					checks = append(checks, check{"Agent CLI " + tool, true, ui.ShortPath(p), ""})
				}
			}
			ok := true
			for _, c := range checks {
				ok = ok && c.OK
			}
			err := emit(map[string]any{"ok": ok, "version": info.Version, "checks": checks}, func() {
				ui.Header(info.Version, "Checking your setup")
				fmt.Fprintln(ui.Out)
				for _, c := range checks {
					if c.OK {
						ui.OK(fmt.Sprintf("%-16s %s", c.Name, ui.DimStyle.Render(c.Detail)))
					} else {
						ui.Fail(fmt.Sprintf("%-16s %s", c.Name, c.Detail))
						ui.Command(c.Fix)
					}
				}
				fmt.Fprintln(ui.Out)
			})
			if err == nil && !ok {
				return &ExitError{Code: 2, Message: "setup is not complete", Hint: "run: meetstream login"}
			}
			return err
		},
	}
}

func renderMarkdown(md string) string {
	if g.JSON || !ui.IsTerminal(os.Stdout) || g.NoColor || os.Getenv("NO_COLOR") != "" {
		return md
	}
	r, err := glamour.NewTermRenderer(glamour.WithAutoStyle(), glamour.WithWordWrap(96))
	if err != nil {
		return md
	}
	out, err := r.Render(md)
	if err != nil {
		return md
	}
	return out
}

func newDocsCmd() *cobra.Command {
	return &cobra.Command{
		Use: "docs", Short: "Read the README in your terminal", GroupID: "tool", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return emit(map[string]string{"format": "markdown", "content": meetstream.Readme, "url": "https://docs.meetstream.ai"}, func() {
				fmt.Fprint(os.Stdout, renderMarkdown(meetstream.Readme))
			})
		},
	}
}

func newSkillCmd() *cobra.Command {
	return &cobra.Command{
		Use: "skill", Short: "Print SKILL.md, the guide for coding agents", GroupID: "tool", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return emit(map[string]string{"format": "markdown", "content": meetstream.Skill}, func() {
				fmt.Fprint(os.Stdout, meetstream.Skill)
			})
		},
	}
}

func newAuthCmd(info BuildInfo) *cobra.Command {
	auth := &cobra.Command{Use: "auth", Short: "Show or clear the stored API key", GroupID: "start"}
	var offline bool
	status := &cobra.Command{
		Use: "status", Short: "Show which API key is in use (masked) and check it with the API", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			key, src := config.ResolveKey()
			c, _ := config.Load()
			out := map[string]any{"signed_in": key != "", "source": src, "key": "", "key_name": c.APIKeyName, "config_path": config.Path()}
			var verr error
			if key != "" {
				out["key"] = config.Mask(key)
				if !offline {
					bots, err := validate(cmd.Context(), info.Version, key)
					verr = err
					out["valid"] = err == nil
					if err == nil {
						out["bots_on_account"] = bots
					}
				}
			}
			if err := emit(out, func() {
				if key == "" {
					ui.Fail("No API key. Run " + ui.CmdStyle.Render("meetstream login") + " or set MEETSTREAM_API_KEY.")
					return
				}
				rows := [][2]string{{"key", config.Mask(key)}, {"from", map[string]string{"env": "MEETSTREAM_API_KEY", "file": ui.ShortPath(config.Path())}[src]}, {"name", orDash(c.APIKeyName)}}
				if !offline && verr == nil {
					rows = append(rows, [2]string{"bots", fmt.Sprint(out["bots_on_account"])})
				}
				ui.Box("Signed in", rows)
				if offline {
					return
				}
				if verr == nil {
					ui.OK("The API accepts this key")
				} else {
					ui.Fail("API check failed: " + verr.Error())
				}
			}); err != nil {
				return err
			}
			if key == "" {
				return &ExitError{Code: 2, Message: "not signed in", Hint: "run: meetstream login"}
			}
			if verr != nil {
				var apiErr *api.Error
				if errors.As(verr, &apiErr) && (apiErr.Status == 401 || apiErr.Status == 403) {
					return &ExitError{Code: 2, Message: "the API key was rejected", Hint: "run: meetstream login"}
				}
				return verr
			}
			return nil
		},
	}
	status.Flags().BoolVar(&offline, "offline", false, "don't call the API; only show the stored key")
	auth.AddCommand(status)
	auth.AddCommand(newSetKeyCmd(info))
	auth.AddCommand(&cobra.Command{
		Use: "logout", Short: "Remove the key from " + config.Path(), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := config.Load()
			if err != nil {
				return err
			}
			had := c.APIKey != ""
			c.APIKey, c.APIKeyName = "", ""
			if had {
				if err := c.Save(); err != nil {
					return err
				}
			}
			return emit(map[string]any{"removed": had}, func() {
				if had {
					ui.OK("Key removed from " + config.Path())
				} else {
					ui.Note("No stored key.")
				}
			})
		},
	})
	return auth
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// ── Planned (M2–M4): listed so the shape is visible, not wired yet ────────────

func planned(use, short, group, milestone string) *cobra.Command {
	return &cobra.Command{
		Use: use, Short: short, GroupID: group,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, a := range args { // flags are not parsed here, so honour the globals by hand
				switch a {
				case "--json":
					g.JSON = true
				case "-h", "--help":
					return cmd.Help()
				}
			}
			return &ExitError{Code: 2, Message: cmd.Name() + " is not in this build yet (" + milestone + ")",
				Hint: hintFor(cmd.Name())}
		},
	}
}

// hintFor names the old tool that does the job until the Go command ships.
func hintFor(name string) string {
	switch name {
	case "setup", "bridge":
		return "coming next: meetstream agent setup"
	}
	return "until then: npx @meetstream/cli " + name
}

func newBridgeCmd(info BuildInfo) *cobra.Command {
	cmd := newAgentServeCmd(info)
	cmd.Use = "bridge"
	cmd.GroupID = "agent"
	cmd.Hidden = true
	return cmd
}
