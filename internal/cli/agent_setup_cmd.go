package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/meetstream-ai/cli/internal/agentkit/access"
	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	kitconfig "github.com/meetstream-ai/cli/internal/agentkit/config"
	agentlogin "github.com/meetstream-ai/cli/internal/agentkit/login"
	"github.com/meetstream-ai/cli/internal/agentkit/relay"
	"github.com/meetstream-ai/cli/internal/api"
	"github.com/meetstream-ai/cli/internal/config"
	"github.com/meetstream-ai/cli/internal/login"
	"github.com/meetstream-ai/cli/internal/ui"
)

// `meetstream agent setup`: pick your agent CLI, connect this computer to the
// relay, and create (or update) the MIA agent that uses it. No website visit,
// no tunnel, no pairing token (Nav, 22 Nov).

var agentOrder = []string{"claude", "codex", "hermes", "copilot", "muse", "openclaw", "agy"}

type agentChoice struct {
	name, label, binary string
	installed           bool
}

func detectAgents() []agentChoice {
	var out []agentChoice
	for _, n := range agentOrder {
		a, ok := agents.All[n]
		if !ok {
			continue
		}
		p, err := exec.LookPath(a.Binary)
		out = append(out, agentChoice{name: n, label: a.Label, binary: p, installed: err == nil})
	}
	// installed first, keeping the kit's order otherwise
	sort.SliceStable(out, func(i, j int) bool { return out[i].installed && !out[j].installed })
	return out
}

// ── picker ────────────────────────────────────────────────────────────────────

type pickModel struct {
	items  []agentChoice
	cursor int
	chosen *agentChoice
}

func (m pickModel) Init() tea.Cmd { return nil }

func (m pickModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "ctrl+c", "q", "esc":
			return m, tea.Quit
		case "up", "k":
			m.cursor = (m.cursor - 1 + len(m.items)) % len(m.items)
		case "down", "j", "tab":
			m.cursor = (m.cursor + 1) % len(m.items)
		case "enter":
			c := m.items[m.cursor]
			m.chosen = &c
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m pickModel) View() string {
	var b strings.Builder
	b.WriteString("\n  " + ui.BoldStyle.Render("Which agent should answer in your meetings?") + "\n\n")
	sel := lipgloss.NewStyle().Foreground(ui.Orange).Bold(true)
	for i, it := range m.items {
		pointer, title := "  ", it.label
		if i == m.cursor {
			pointer, title = sel.Render("❯ "), sel.Render(it.label)
		}
		state := ui.DimStyle.Render("not installed")
		if it.installed {
			state = lipgloss.NewStyle().Foreground(ui.Green).Render("✓ " + ui.ShortPath(it.binary))
		}
		fmt.Fprintf(&b, "  %s%-22s %s\n", pointer, title, state)
	}
	b.WriteString("\n  " + ui.DimStyle.Render("↑/↓ move · enter choose · esc/q quit") + "\n")
	return b.String()
}

// ── command ───────────────────────────────────────────────────────────────────

func newAgentSetupCmd(info BuildInfo) *cobra.Command {
	var agentName, maxAccess, workdir, configID, name string
	var dryRun, noStart bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Connect your agent CLI to MIA: pick it, connect this computer, create the MIA agent",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			interactive := !g.JSON && !g.NoInput && ui.Interactive()
			if !g.JSON {
				ui.Header(info.Version, "Bring your agent into the meeting")
			}

			// The interactive wizard can step back: Esc (or "← Back") at step 2 or 3
			// returns to the step before; Esc at step 1 cancels. Non-interactive runs
			// go straight through, as before.
			var chosen *agentChoice
			var spec agents.Spec
			var creds *relay.Credentials
			pickAgain := false // back at step 1 (or an install declined): show the picker even with --agent
			step := 1
		steps:
			for {
				switch step {
				case 1:
					// 1. Agent.
					if !g.JSON {
						ui.Step(1, 3, "Your agent")
					}
					choices := detectAgents()
					prev := agentName
					if chosen != nil {
						prev = chosen.name
					}
					chosen = nil
					switch {
					case agentName != "" && !pickAgain:
						for i := range choices {
							if choices[i].name == agentName {
								chosen = &choices[i]
							}
						}
						if chosen == nil {
							return usageErr("unknown agent "+agentName, "choose: "+strings.Join(agentOrder, ", "))
						}
					case interactive:
						pm := pickModel{items: choices}
						for i := range choices {
							if choices[i].name == prev {
								pm.cursor = i
							}
						}
						final, err := tea.NewProgram(pm).Run()
						if err != nil {
							return err
						}
						if chosen = final.(pickModel).chosen; chosen == nil {
							return &ExitError{Code: 130, Message: "setup cancelled"}
						}
					default:
						for i := range choices {
							if choices[i].installed {
								chosen = &choices[i]
								break
							}
						}
						if chosen == nil {
							return usageErr("no supported agent CLI is installed", "install one, e.g. npm install -g @anthropic-ai/claude-code, or pass --agent")
						}
					}
					spec = agents.All[chosen.name]
					if !chosen.installed {
						if !interactive {
							return &ExitError{Code: 2, Message: spec.Label + " is not installed", Hint: spec.InstallHint}
						}
						// Like the kit: offer the vendor's installer; without it, pick again.
						p := setupInstaller.offer(spec)
						if p == "" {
							pickAgain = true
							continue
						}
						chosen.binary, chosen.installed = p, true
					}
					st := (&agentlogin.Checker{Agent: spec, Binary: chosen.binary}).Check(true)
					if !g.JSON {
						ui.OK(spec.Label + "  " + ui.DimStyle.Render(ui.ShortPath(chosen.binary)))
						if !st.Usable() {
							ui.Warn(spec.Label + " needs a sign-in: run " + ui.CmdStyle.Render(spec.Relogin))
						}
					}
					if !access.Valid(maxAccess) {
						return usageErr("invalid --access", "choose readonly, confirm, or full")
					}
					if workdir == "" {
						workdir, _ = os.Getwd()
					}
					if abs, err := filepath.Abs(workdir); err == nil {
						workdir = abs
					}
					step = 2

				case 2:
					// 2. This computer.
					if !g.JSON {
						ui.Step(2, 3, "Connect this computer")
					}
					store := relay.NewStore()
					var err error
					if creds, err = store.Load(); err != nil {
						return err
					}
					if interactive {
						option := "Connect this computer (opens your browser)"
						if creds != nil {
							option = "Use this computer: " + relay.DeviceName()
						}
						res, _, err := askStep("Connect this computer to MeetStream?", option)
						if err != nil {
							return err
						}
						switch res {
						case menuBack:
							step, pickAgain = 1, true
							continue steps
						case menuQuit:
							return &ExitError{Code: 130, Message: "setup cancelled"}
						}
					}
					if creds == nil {
						if !interactive {
							return &ExitError{Code: 2, Message: "this computer isn't connected to MeetStream", Hint: "run: meetstream agent login"}
						}
						t, err := relay.Login(cmd.Context(), relay.LoginOptions{OpenBrowser: login.Open, ShowURL: func(u string) {
							ui.Note("Opening your browser to connect this computer. If it doesn't open:")
							ui.Command(u)
						}})
						if err != nil {
							return &ExitError{Code: 1, Message: err.Error(), Hint: "the relay is on for test stacks first; set MEETSTREAM_AUTH_URL and MEETSTREAM_RELAY_URL"}
						}
						creds = &relay.Credentials{DeviceID: t.DeviceID, RefreshToken: t.RefreshToken, AuthURL: relay.AuthURL(), RelayURL: relay.RelayURL()}
						if err := store.Save(creds); err != nil {
							return err
						}
					}
					if !g.JSON {
						ui.OK(relay.DeviceName() + "  " + ui.DimStyle.Render(creds.DeviceID+" · "+creds.RelayURL))
					}
					step = 3

				case 3:
					// 3. The MIA agent.
					if !g.JSON {
						ui.Step(3, 3, "Your MIA agent")
					}
					if !interactive {
						break steps
					}
					title := name
					if title == "" {
						title = spec.Label + " in the meeting"
					}
					option := "Create MIA agent \"" + title + "\""
					switch {
					case dryRun:
						option = "Show the request (--dry-run, nothing sent)"
					case configID != "":
						option = "Update MIA agent " + configID
					}
					res, _, err := askStep("Create the MIA agent for "+spec.Label+"?", option)
					if err != nil {
						return err
					}
					switch res {
					case menuBack:
						step = 2
						continue steps
					case menuQuit:
						return &ExitError{Code: 130, Message: "setup cancelled"}
					}
					break steps
				}
			}

			if name == "" {
				name = spec.Label + " in the meeting"
			}
			block := map[string]any{"transport": "relay", "device_id": creds.DeviceID, "label": spec.Label, "access": maxAccess, "require_code": true}
			body := map[string]any{
				"agent_name":  name,
				"mode":        "pipeline",
				"model":       map[string]any{"provider": "local_agent", "local_agent": block},
				"transcriber": map[string]any{"provider": "deepgram", "model": "nova-3", "language": "en", "key_source": "meetstream"},
				"voice":       map[string]any{"provider": "openai", "voice_id": "coral", "key_source": "meetstream"},
				"agent":       map[string]any{"response_modality": "audio"},
			}
			if dryRun {
				return emit(map[string]any{"dry_run": true, "method": map[bool]string{true: "PUT", false: "POST"}[configID != ""], "agent_config_id": configID, "body": body}, func() {
					b, _ := json.MarshalIndent(body, "  ", "  ")
					ui.Note("Would send (nothing sent, --dry-run):")
					fmt.Fprintln(ui.Out, "  "+string(b))
				})
			}
			key, _ := config.ResolveKey()
			if key == "" {
				return &ExitError{Code: 2, Message: "an API key is needed to create the MIA agent", Hint: "run: meetstream auth set-key  (or meetstream login)"}
			}
			c := api.New(config.APIURL(), key, info.Version)
			var resp any
			var err error
			if configID != "" {
				// Update only the model block of an existing agent; keep its voice, prompts and tools.
				var existing map[string]any
				if err := c.Do(cmd.Context(), http.MethodGet, "/mia?agent_config_id="+configID, nil, &existing); err != nil {
					return apiError(err)
				}
				cfg := existing
				if inner, ok := existing["agent_config"].(map[string]any); ok {
					cfg = inner
				}
				cfg["agent_config_id"] = configID
				cfg["model"] = body["model"]
				b, _ := json.Marshal(cfg)
				err = c.Do(cmd.Context(), http.MethodPut, "/mia", bytes.NewReader(b), &resp)
			} else {
				b, _ := json.Marshal(body)
				err = c.Do(cmd.Context(), http.MethodPost, "/mia", bytes.NewReader(b), &resp)
			}
			if err != nil {
				var ae *api.Error
				if errors.As(err, &ae) && strings.Contains(ae.Detail(), "relay") {
					return &ExitError{Code: 1, Message: ae.Detail(), Hint: "relay agents are on for test stacks first; point MEETSTREAM_API_URL at one"}
				}
				return apiError(err)
			}
			id := configID
			if m, ok := resp.(map[string]any); ok && id == "" {
				id = first(m, "agent_config_id", "id")
			}

			saved := kitconfig.Setup{Agent: spec.Name, Workdir: workdir, MaxAccess: maxAccess, Tunnel: "relay"}
			if err := saved.Save(""); err != nil {
				return err
			}
			out := map[string]any{"agent": spec.Name, "device_id": creds.DeviceID, "agent_config_id": id, "access": maxAccess, "workdir": workdir}
			if err := emit(out, func() {
				ui.OK("MIA agent " + ui.BoldStyle.Render(name) + "  " + ui.DimStyle.Render(id))
				fmt.Fprintln(ui.Out)
				ui.Box("Ready", [][2]string{{"agent", spec.Label}, {"access", maxAccess}, {"folder", ui.ShortPath(workdir)}, {"mia agent", id}})
				ui.Note("Start your agent, then send MIA to a meeting:")
				ui.Command("meetstream agent serve")
				ui.Command("meetstream join <meeting-url> --agent-config " + id)
			}); err != nil {
				return err
			}
			if interactive && !noStart {
				fmt.Fprint(ui.Out, "\n  Start serving now? [Y/n] ")
				var ans string
				_, _ = fmt.Fscanln(os.Stdin, &ans)
				if a := strings.ToLower(strings.TrimSpace(ans)); a == "" || a == "y" || a == "yes" {
					root := cmd.Root()
					root.SetArgs([]string{"agent", "serve"})
					return root.Execute()
				}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&agentName, "agent", "", "agent CLI: "+strings.Join(agentOrder, ", ")+" (default: pick, or the first installed)")
	f.StringVar(&maxAccess, "access", "readonly", "what MIA's turns may do: readonly, confirm, full")
	f.StringVar(&workdir, "workdir", "", "folder the agent works in (default: here)")
	f.StringVar(&configID, "agent-config", "", "update this MIA agent instead of creating one (keeps its voice and prompts)")
	f.StringVar(&name, "name", "", "name of the new MIA agent")
	f.BoolVar(&dryRun, "dry-run", false, "print the MIA agent request without sending it")
	f.BoolVar(&noStart, "no-start", false, "don't offer to start serving")
	return cmd
}

// newSetupShortcut is `meetstream setup`, the kit's entry point, as a short form of `agent setup`.
func newSetupShortcut(info BuildInfo) *cobra.Command {
	cmd := newAgentSetupCmd(info)
	cmd.GroupID = "agent"
	return cmd
}
