package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/meetstream-ai/cli/internal/agentkit/access"
	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"github.com/meetstream-ai/cli/internal/agentkit/alerts"
	"github.com/meetstream-ai/cli/internal/agentkit/bridge"
	kitconfig "github.com/meetstream-ai/cli/internal/agentkit/config"
	"github.com/meetstream-ai/cli/internal/agentkit/localapps"
	"github.com/meetstream-ai/cli/internal/agentkit/meetingctx"
	"github.com/meetstream-ai/cli/internal/agentkit/pairing"
	"github.com/meetstream-ai/cli/internal/agentkit/relay"
	"github.com/meetstream-ai/cli/internal/ui"
	"github.com/spf13/cobra"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

func newAgentCmd(info BuildInfo) *cobra.Command {
	cmd := &cobra.Command{Use: "agent", Short: "Run your own agent CLI in MIA meetings", GroupID: "agent"}
	setup := newAgentSetupCmd(info)
	cmd.AddCommand(newAgentServeCmd(info), newAgentContextCmd(), setup, newAgentStdioCmd(),
		newAgentLoginCmd(info), newAgentLogoutCmd(), newAgentStatusCmd())
	return cmd
}
func newAgentStdioCmd() *cobra.Command {
	var address string
	cmd := &cobra.Command{Use: "mcp-stdio", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return bridge.Stdio(cmd.Context(), address, os.Getenv(kitconfig.MCPSecretEnv), cmd.InOrStdin(), cmd.OutOrStdout())
	}}
	cmd.Flags().StringVar(&address, "url", "", "local MCP endpoint")
	_ = cmd.MarkFlagRequired("url")
	return cmd
}

type agentServeOptions struct {
	agent, command, binary, workdir, owner, model, effort, maxAccess, tunnel, tunnelURL string
	port                                                                                int
	timeout                                                                             float64
	noPrime, noMeetingMD, ignoreRules, debug, relay, noRelay                            bool
}

func (o agentServeOptions) build() (kitconfig.Config, error) {
	a, ok := agents.All[o.agent]
	var err error
	if o.command != "" {
		a, err = agents.Custom(o.command)
		if err != nil {
			return kitconfig.Config{}, usageErr(err.Error(), "check --command quoting")
		}
	} else if !ok {
		return kitconfig.Config{}, usageErr("unknown agent", "choose claude, codex, hermes, copilot, muse, openclaw, agy, or use --command")
	}
	if !access.Valid(o.maxAccess) {
		return kitconfig.Config{}, usageErr("invalid --max-access", "choose readonly, confirm, or full")
	}
	if o.timeout <= 0 || math.IsInf(o.timeout, 0) || math.IsNaN(o.timeout) || o.timeout > float64(math.MaxInt64)/float64(time.Second) {
		return kitconfig.Config{}, usageErr("--timeout must be a positive finite duration", "")
	}
	if o.port < 0 || o.port > 65535 {
		return kitconfig.Config{}, usageErr("invalid port", "")
	}
	if o.effort != "" && !regexp.MustCompile(`^[a-z]{2,12}$`).MatchString(o.effort) {
		return kitconfig.Config{}, usageErr("invalid --effort", "for example: low, medium, high")
	}
	workdir, err := filepath.Abs(meetingctx.Expand(o.workdir))
	if err != nil {
		return kitconfig.Config{}, err
	}
	stat, err := os.Stat(workdir)
	if err != nil || !stat.IsDir() {
		return kitconfig.Config{}, usageErr("workdir must be an existing directory", "pass --workdir")
	}
	binary := o.binary
	if binary == "" {
		binary = a.Binary
	}
	binary, err = exec.LookPath(meetingctx.Expand(binary))
	if err != nil {
		return kitconfig.Config{}, usageErr("agent CLI is not installed", a.InstallHint)
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return kitconfig.Config{}, err
	}
	notes := ""
	if !o.noMeetingMD {
		notes = meetingctx.Notes()
	}
	return kitconfig.Config{Agent: a, Binary: binary, Workdir: workdir, MaxAccess: o.maxAccess, Owner: o.owner, Model: o.model, Effort: o.effort, Timeout: time.Duration(o.timeout * float64(time.Second)), Prime: !o.noPrime, MeetingNotes: notes, IgnoreRules: o.ignoreRules, Debug: o.debug || os.Getenv("MS_AGENT_KIT_DEBUG") == "1"}, nil
}
func newAgentServeCmd(info BuildInfo) *cobra.Command {
	cwd, _ := os.Getwd()
	o := agentServeOptions{}
	cmd := &cobra.Command{Use: "serve", Short: "Run the local bridge on 127.0.0.1", Args: cobra.NoArgs}
	f := cmd.Flags()
	f.StringVar(&o.agent, "agent", "claude", "agent CLI: claude, codex, hermes, copilot, muse, openclaw, agy")
	f.StringVar(&o.command, "command", "", "custom command; {prompt} is replaced, otherwise stdin")
	f.StringVar(&o.binary, "binary", "", "path to the agent CLI")
	f.StringVar(&o.workdir, "workdir", cwd, "folder the agent works in")
	owner := os.Getenv("USER")
	if owner == "" {
		owner = "the user"
	}
	f.StringVar(&o.owner, "owner", owner, "your name as MIA hears it")
	f.Float64Var(&o.timeout, "timeout", 60, "stop a turn after this many seconds with no output")
	f.StringVar(&o.model, "model", "", "agent model (MIA can override)")
	f.StringVar(&o.effort, "effort", "", "reasoning effort where supported")
	f.IntVar(&o.port, "port", kitconfig.DefaultPort, "loopback port")
	f.StringVar(&o.maxAccess, "max-access", "readonly", "highest access: readonly, confirm, full")
	f.StringVar(&o.maxAccess, "mode", "readonly", "alias of --max-access")
	_ = f.MarkHidden("mode")
	f.BoolVar(&o.noPrime, "no-prime", false, "skip the warm-up turn")
	f.BoolVar(&o.noMeetingMD, "no-meeting-md", false, "leave MEETING.md out of meetings")
	f.BoolVar(&o.ignoreRules, "ignore-rules", false, "skip project rules where the agent supports it")
	f.BoolVar(&o.debug, "debug", false, "log protocol message types, never their text")
	f.StringVar(&o.tunnel, "tunnel", "none", "none, cloudflare, ngrok, or url")
	f.StringVar(&o.tunnelURL, "tunnel-url", "", "existing https address or ngrok domain")
	f.BoolVar(&o.relay, "relay", false, "require the MeetStream relay (default when this computer is connected)")
	f.BoolVar(&o.noRelay, "no-relay", false, "self-hosted: no relay; MIA reaches the bridge through your tunnel with a pairing token")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		var creds *relay.Credentials
		if !o.noRelay {
			var err error
			if creds, err = relay.NewStore().Load(); err != nil {
				return err
			}
			if creds == nil && o.relay {
				return usageErr("this computer isn't connected to MeetStream", "run: meetstream agent login")
			}
		}
		relayMode := creds != nil
		switch o.tunnel {
		case "none", "cloudflare", "ngrok", "url":
		default:
			return usageErr("invalid --tunnel", "choose none, cloudflare, ngrok, or url")
		}
		if o.tunnel == "url" {
			u, err := url.Parse(o.tunnelURL)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
				return usageErr("--tunnel url requires an https address", "pass --tunnel-url")
			}
		}
		if saved, err := kitconfig.LoadSetup(""); err == nil {
			fl := cmd.Flags()
			if !fl.Changed("agent") && !fl.Changed("command") {
				o.agent, o.command = saved.Agent, saved.Command
			}
			if !fl.Changed("workdir") && saved.Workdir != "" {
				o.workdir = saved.Workdir
			}
			if !fl.Changed("max-access") && saved.MaxAccess != "" {
				o.maxAccess = saved.MaxAccess
			}
			if !fl.Changed("model") && saved.Model != "" {
				o.model = saved.Model
			}
		}
		c, err := o.build()
		if err != nil {
			return err
		}
		if !relayMode {
			pairs, err := (&pairing.Store{}).List()
			if err != nil {
				return err
			}
			if len(pairs) == 0 {
				return usageErr("no pairing and no relay sign-in yet", "run: meetstream agent login  (or meetstream agent setup)")
			}
		}
		var outputMu sync.Mutex
		c.Event = func(kind string, fields map[string]any) {
			outputMu.Lock()
			defer outputMu.Unlock()
			if g.JSON {
				fields["event"] = kind
				_ = json.NewEncoder(cmd.OutOrStdout()).Encode(fields)
			} else if kind != "protocol" || c.Debug {
				ui.Note(strings.ReplaceAll(kind, "_", " "))
			}
		}
		c.Notify = func(title, text string) {
			outputMu.Lock()
			fmt.Fprintln(ui.Out, ui.BoldStyle.Render(title)+": "+text)
			outputMu.Unlock()
			alerts.Desktop(title, text)
		}
		c.Confirm = agentConfirmer(cmd)
		server := bridge.New(c)
		ln, err := server.Listen(o.port)
		if err != nil {
			return fmt.Errorf("cannot listen on loopback port %d", o.port)
		}
		defer ln.Close()
		defer server.Close()
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		defer stop()
		httpServer := server.HTTPServer()
		defer httpServer.Close()
		served := make(chan error, 1)
		go func() { served <- httpServer.Serve(ln) }()
		var tunnel *localapps.CommandTunnel
		address := o.tunnelURL
		if o.tunnel == "cloudflare" || o.tunnel == "ngrok" {
			tunnel = &localapps.CommandTunnel{Kind: o.tunnel, Port: server.Port, URL: o.tunnelURL}
			defer tunnel.Stop()
			address, err = tunnel.Start(ctx)
			if err != nil {
				return err
			}
		}
		status := server.Login.Check(true)
		relayErr := make(chan error, 1)
		relayState := "off (tunnel + pairing token)"
		if relayMode {
			internal := pairing.Secret(32)
			server.SetInternalToken(internal)
			relayState = "connecting"
			cl := &relay.Client{
				Store: relay.NewStore(), Creds: creds, Bridge: server, InternalToken: internal,
				Hello: relay.HelloFields(c.Agent.Name, c.Agent.Label, status.State, c.MaxAccess),
				Logf: func(f string, a ...any) {
					if c.Debug {
						c.Event("relay", map[string]any{"detail": fmt.Sprintf(f, a...)})
					}
				},
				OnState: func(on bool, detail string) {
					if on {
						c.Event("relay_connected", map[string]any{})
					} else {
						c.Event("relay_disconnected", map[string]any{"detail": detail})
					}
				},
			}
			go func() { relayErr <- cl.Run(ctx) }()
		}
		if !g.JSON {
			ui.Header(info.Version, "Your agent in the meeting")
			ui.Box("Local bridge", [][2]string{{"agent", c.Agent.Label}, {"access", c.MaxAccess}, {"folder", ui.ShortPath(c.Workdir)}, {"listen", fmt.Sprintf("127.0.0.1:%d", server.Port)}, {"login", status.State}, {"relay", relayState}})
			ui.Note("Leave this window open; Ctrl-C stops the bridge.")
		}
		fields := map[string]any{"port": server.Port, "agent": c.Agent.Name, "access": c.MaxAccess, "login": status.State}
		if address != "" {
			fields["address"] = address
		}
		c.Event("ready", fields)
		select {
		case <-ctx.Done():
			_ = httpServer.Close()
			c.Event("stopped", map[string]any{})
			return nil
		case err := <-relayErr:
			if err != nil {
				return &ExitError{Code: 2, Message: err.Error(), Hint: "run: meetstream agent login"}
			}
			return nil
		case err := <-served:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		}
	}
	return cmd
}

// One reader owns stdin. Expired answers are tagged and discarded so a late yes
// cannot approve a different action. --no-input and JSON mode always deny.
func agentConfirmer(cmd *cobra.Command) func(string) bool {
	if g.NoInput || g.JSON || !ui.Interactive() {
		return func(string) bool { return false }
	}
	var mu sync.Mutex
	type answer struct {
		id   uint64
		text string
	}
	lines := make(chan answer)
	requests := make(chan uint64)
	go func() {
		scanner := bufio.NewScanner(cmd.InOrStdin())
		for id := range requests {
			if !scanner.Scan() {
				return
			}
			lines <- answer{id, scanner.Text()}
		}
	}()
	var next uint64
	var pending bool
	return func(description string) bool {
		mu.Lock()
		defer mu.Unlock()
		next++
		id := next
		if pending {
			select {
			case <-lines:
				pending = false
			default:
				return false
			}
		}
		fmt.Fprintf(ui.Out, "Allow %s? [y/N] (20s) ", description)
		select {
		case requests <- id:
			pending = true
		case <-cmd.Context().Done():
			return false
		}
		timer := time.NewTimer(20 * time.Second)
		defer timer.Stop()
		select {
		case a := <-lines:
			pending = false
			return a.id == id && strings.EqualFold(strings.TrimSpace(a.text), "y")
		case <-timer.C:
			return false
		case <-cmd.Context().Done():
			return false
		}
	}
}
func newAgentContextCmd() *cobra.Command {
	var name, workdir string
	var ignore bool
	cmd := &cobra.Command{Use: "context [show|edit]", Short: "Show what the agent reads or edit MEETING.md", Args: cobra.MatchAll(cobra.MaximumNArgs(1), cobra.OnlyValidArgs), ValidArgs: []string{"show", "edit"}, RunE: func(cmd *cobra.Command, args []string) error {
		action := "show"
		if len(args) > 0 {
			action = args[0]
		}
		saved, _ := kitconfig.LoadSetup("")
		agentName, folder := name, workdir
		useMeeting := true
		if saved != nil {
			if agentName == "" {
				agentName = saved.Agent
			}
			if folder == "" {
				folder = saved.Workdir
			}
			ignore = ignore || saved.IgnoreRules == "yes"
			useMeeting = saved.MeetingMD != "no"
		}
		if agentName == "" {
			agentName = "claude"
		}
		a, ok := agents.All[agentName]
		if !ok && saved != nil && saved.Command != "" {
			var err error
			a, err = agents.Custom(saved.Command)
			if err != nil {
				return err
			}
		} else if !ok {
			return usageErr("unknown agent", "pass --agent")
		}
		if folder == "" {
			folder = meetingctx.Workspace()
		}
		folder = meetingctx.Expand(folder)
		if action == "edit" {
			if g.NoInput || g.JSON {
				return usageErr("context edit needs an interactive editor", "edit MEETING.md directly or run without --no-input/--json")
			}
			editor := os.Getenv("VISUAL")
			if editor == "" {
				editor = os.Getenv("EDITOR")
			}
			if editor == "" {
				editor = "vi"
			}
			parts, err := agents.SplitCommand(editor)
			if err != nil || len(parts) == 0 {
				return usageErr("invalid editor command", "set EDITOR")
			}
			path, err := meetingctx.Ensure()
			if err != nil {
				return err
			}
			proc := exec.CommandContext(cmd.Context(), parts[0], append(parts[1:], path)...)
			proc.Stdin = cmd.InOrStdin()
			proc.Stdout = cmd.OutOrStdout()
			proc.Stderr = cmd.ErrOrStderr()
			return proc.Run()
		}
		items := meetingctx.Effective(a, folder, "", ignore, useMeeting)
		return emit(map[string]any{"agent": a.Name, "workdir": folder, "mode": meetingctx.Mode(folder, ignore), "items": items, "tokens_per_turn": meetingctx.Tokens(items), "meeting_file": meetingctx.File()}, func() {
			ui.Box("Meeting context", [][2]string{{"agent", a.Label}, {"folder", ui.ShortPath(folder)}, {"mode", meetingctx.Mode(folder, ignore)}})
			for _, i := range items {
				ui.Note(fmt.Sprintf("%s  %d bytes  ≈%d tokens  loaded=%t", i.Name, i.Size, i.Size/4, i.Loaded))
			}
			ui.Command("meetstream agent context edit")
		})
	}}
	cmd.Flags().StringVar(&name, "agent", "", "agent (default: saved setup)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "folder (default: saved setup)")
	cmd.Flags().BoolVar(&ignore, "ignore-rules", false, "show project rules as ignored where supported")
	return cmd
}
