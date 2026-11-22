package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/spf13/cobra"

	meetstream "github.com/meetstream-ai/cli"
	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"github.com/meetstream-ai/cli/internal/agentkit/checks"
	kitconfig "github.com/meetstream-ai/cli/internal/agentkit/config"
	agentlogin "github.com/meetstream-ai/cli/internal/agentkit/login"
	"github.com/meetstream-ai/cli/internal/agentkit/meetingctx"
	"github.com/meetstream-ai/cli/internal/agentkit/pairing"
	"github.com/meetstream-ai/cli/internal/agentkit/relay"
	"github.com/meetstream-ai/cli/internal/agentkit/warmcheck"
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

// doctorCheck is one doctor line in --json: ok and fix keep the earlier shape,
// status adds "warn" (worth showing, not a problem).
type doctorCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// doctorWarm runs the warm check without a turn (no model tokens); tests swap it.
var doctorWarm = func(ctx context.Context, c kitconfig.Config) warmcheck.Result {
	return warmcheck.Check(ctx, c, warmcheck.Options{})
}

// doctorAgentChecks are the agent kit's checks (checks.Doctor) for the agent of
// the saved setup, --agent, or the first one installed. With no agent CLI at
// all it is one warning: your own agent is optional.
func doctorAgentChecks(cmd *cobra.Command, agentName, binary string, noWarm bool) []checks.Check {
	saved, _ := kitconfig.LoadSetup("")
	if agentName == "" && saved != nil && saved.Command == "" {
		agentName = saved.Agent
	}
	if agentName == "" {
		for _, c := range detectAgents() {
			if c.installed {
				agentName = c.name
				break
			}
		}
	}
	if agentName == "" {
		return []checks.Check{{Name: "agent", Status: checks.Warn, Detail: "no agent CLI found (optional)", Hint: "To answer in meetings with your own agent, install one and run: meetstream setup"}}
	}
	in := checks.DoctorInput{Override: binary, Home: pairing.Home()}
	if spec, ok := agents.All[agentName]; ok {
		in.Spec = &spec
	} else {
		in.AgentErr = &checks.Check{Name: "agent", Status: checks.Fail, Detail: "unknown agent " + agentName, Hint: "choose: " + strings.Join(agentOrder, ", ")}
	}
	in.Workdir, _ = os.Getwd()
	if saved != nil && saved.Workdir != "" && (in.Spec == nil || saved.Agent == in.Spec.Name) {
		in.Workdir = saved.Workdir
	}
	in.Login = func(b string) (string, string) {
		st := (&agentlogin.Checker{Agent: *in.Spec, Binary: b}).Check(true)
		return st.State, st.Detail
	}
	if !noWarm {
		in.Warm = func(b string) warmcheck.Result {
			return doctorWarm(cmd.Context(), kitconfig.Config{Agent: *in.Spec, Binary: b, Workdir: in.Workdir, Owner: setupOwner()})
		}
	}
	creds, _ := relay.NewStore().Load()
	if creds != nil || (saved != nil && saved.Tunnel == "relay") {
		in.Relay = func() (bool, string, string) {
			if creds == nil {
				return false, "", ""
			}
			return true, relay.DeviceName(), creds.RelayURL
		}
	}
	ignore := saved != nil && saved.IgnoreRules == "yes"
	useMeeting := saved == nil || saved.MeetingMD != "no"
	in.Context = func(spec agents.Spec, workdir string) ([]meetingctx.Item, string, string) {
		return meetingctx.Effective(spec, workdir, "", ignore, useMeeting), meetingctx.Mode(workdir, ignore), meetingctx.Workspace()
	}
	env := agentEnv()
	return checks.Doctor(env, in)
}

// doctorKeyChecks are the instant checks: the API key and the server.
func doctorKeyChecks() (all []checks.Check, keyMissing bool) {
	key, src := config.ResolveKey()
	switch src {
	case "env":
		all = append(all, checks.Check{Name: "API key", Status: checks.OK, Detail: "MEETSTREAM_API_KEY " + config.Mask(key)})
	case "file":
		all = append(all, checks.Check{Name: "API key", Status: checks.OK, Detail: ui.ShortPath(config.Path()) + " " + config.Mask(key)})
	default:
		keyMissing = true
		all = append(all, checks.Check{Name: "API key", Status: checks.Fail, Detail: "not set", Hint: "meetstream login"})
	}
	all = append(all, checks.Check{Name: "API URL", Status: checks.OK, Detail: config.APIURL()})
	return all, keyMissing
}

// renderDoctorLines is one line per check, with the fix under each problem.
func renderDoctorLines(all []checks.Check) string {
	var lines []string
	for _, c := range all {
		switch c.Status {
		case checks.OK:
			lines = append(lines, ui.OKLine(fmt.Sprintf("%-10s %s", c.Name, ui.DimStyle.Render(c.Detail))))
		case checks.Warn:
			lines = append(lines, ui.WarnLine(fmt.Sprintf("%-10s %s", c.Name, c.Detail)))
		default:
			lines = append(lines, ui.FailLine(fmt.Sprintf("%-10s %s", c.Name, c.Detail)))
		}
		if c.Status != checks.OK && c.Hint != "" {
			lines = append(lines, ui.CommandLine(c.Hint))
		}
	}
	return strings.Join(lines, "\n")
}

// renderDoctor is the checks and the verdict, as `meetstream doctor` prints them.
func renderDoctor(all []checks.Check, verdict string, failed int) string {
	end := ui.OKLine(verdict)
	if failed > 0 {
		end = ui.FailLine(fmt.Sprintf("%d problem(s) to fix.", failed))
	}
	return renderDoctorLines(all) + "\n\n" + end
}

func newDoctorCmd(info BuildInfo) *cobra.Command {
	var agentName, binary string
	var noWarm bool
	cmd := &cobra.Command{
		Use: "doctor", Short: "Check your setup: API key, config file, your agent CLI", GroupID: "start", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			all, keyMissing := doctorKeyChecks()
			all = append(all, doctorAgentChecks(cmd, agentName, binary, noWarm)...)
			verdict, failed := checks.Verdict(all)
			out := make([]doctorCheck, len(all))
			for i, c := range all {
				out[i] = doctorCheck{Name: c.Name, OK: c.Passed(), Status: string(c.Status), Detail: c.Detail, Fix: c.Hint}
			}
			err := emit(map[string]any{"ok": failed == 0, "version": info.Version, "verdict": verdict, "checks": out}, func() {
				ui.Header(info.Version, "Checking your setup")
				fmt.Fprintln(ui.Out)
				fmt.Fprintln(ui.Out, renderDoctor(all, verdict, failed))
			})
			switch {
			case err != nil:
				return err
			case keyMissing:
				return &ExitError{Code: 2, Message: "setup is not complete", Hint: "run: meetstream login"}
			case failed > 0:
				return &ExitError{Code: 1, Message: fmt.Sprintf("%d problem(s) to fix", failed), Hint: "follow the fix under each ✗ line"}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&agentName, "agent", "", "agent CLI to check (default: your setup, or the first installed)")
	f.StringVar(&binary, "binary", "", "path to the agent CLI")
	f.BoolVar(&noWarm, "no-warm", false, "skip starting the agent's warm (ACP) session")
	return cmd
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
