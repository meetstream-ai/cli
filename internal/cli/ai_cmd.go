package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	meetstream "github.com/meetstream-ai/cli"
	"github.com/meetstream-ai/cli/internal/ui"
)

// aiTarget is where one coding agent keeps skills and MCP config.
type aiTarget struct {
	Name     string
	Binary   string // to detect it
	SkillDir string // relative to home; "" = no skill folder
	MCP      string // "codex", "cursor", or "claude" (claude's own CLI owns its config)
}

var aiTargets = []aiTarget{
	{Name: "claude", Binary: "claude", SkillDir: ".claude/skills/meetstream-cli", MCP: "claude"},
	{Name: "codex", Binary: "codex", SkillDir: ".codex/skills/meetstream-cli", MCP: "codex"},
	{Name: "cursor", Binary: "cursor-agent", MCP: "cursor"},
}

// aiDetect is swapped in tests.
var aiDetect = func(binary string) bool { _, err := exec.LookPath(binary); return err == nil }

type aiResult struct {
	Agent   string   `json:"agent"`
	Skill   string   `json:"skill,omitempty"`
	MCP     string   `json:"mcp"`
	Changed []string `json:"changed"`
	Todo    string   `json:"todo,omitempty"`
}

func newAICmd() *cobra.Command {
	cmd := &cobra.Command{Use: "ai", Short: "Set up coding agents (Claude Code, Codex, Cursor) for MeetStream", GroupID: "tool"}
	var all, dry bool
	install := &cobra.Command{
		Use: "install [claude|codex|cursor ...]", Short: "Install the MeetStream skill and the hosted MCP into your coding agents",
		Long: `Install the meetstream CLI skill (SKILL.md) and the hosted MCP server
(https://mcp.meetstream.ai, OAuth on first use) into coding agents.

With no names it uses every agent it finds. Existing config files are backed
up to .bak before a change. Claude Code's MCP entry is added with its own
command, which this prints for you to run.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			var targets []aiTarget
			for _, t := range aiTargets {
				if len(args) > 0 {
					if contains(args, t.Name) {
						targets = append(targets, t)
					}
				} else if all || aiDetect(t.Binary) {
					targets = append(targets, t)
				}
			}
			for _, a := range args {
				found := false
				for _, t := range aiTargets {
					found = found || t.Name == a
				}
				if !found {
					return usageErr("unknown agent "+a, "choose claude, codex or cursor")
				}
			}
			if len(targets) == 0 {
				return &ExitError{Code: 3, Message: "no coding agent found", Hint: "name one: meetstream ai install codex"}
			}
			var results []aiResult
			for _, t := range targets {
				r := aiResult{Agent: t.Name, Changed: []string{}}
				if t.SkillDir != "" {
					p := filepath.Join(home, filepath.FromSlash(t.SkillDir), "SKILL.md")
					r.Skill = p
					if !dry {
						if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
							return err
						}
						if err := os.WriteFile(p, []byte(meetstream.Skill), 0644); err != nil {
							return err
						}
					}
					r.Changed = append(r.Changed, p)
				}
				switch t.MCP {
				case "claude":
					r.MCP = "run: claude mcp add --transport http meetstream " + hostedMCPURL
					r.Todo = "claude mcp add --transport http meetstream " + hostedMCPURL
				default:
					p, err := mcpConfigPath(home, t.MCP)
					if err != nil {
						return err
					}
					r.MCP = hostedMCPURL
					if !dry {
						if err := writeMCPEntry(t.MCP, p); err != nil {
							return err
						}
					}
					r.Changed = append(r.Changed, p)
				}
				results = append(results, r)
			}
			return emit(map[string]any{"dry_run": dry, "agents": results}, func() {
				if dry {
					ui.Note("Dry run: nothing written.")
				}
				for _, r := range results {
					var bits []string
					if r.Skill != "" {
						bits = append(bits, "skill → "+ui.ShortPath(r.Skill))
					}
					if r.Todo == "" {
						bits = append(bits, "hosted MCP → "+ui.ShortPath(r.Changed[len(r.Changed)-1]))
					}
					ui.OK(fmt.Sprintf("%-7s %s", r.Agent, strings.Join(bits, " · ")))
					if r.Todo != "" {
						ui.Note("MCP: run " + r.Todo)
					}
				}
				ui.Note("Restart each agent to load the changes. The MCP asks you to sign in on first use.")
			})
		},
	}
	install.Flags().BoolVar(&all, "all", false, "every supported agent, installed or not")
	install.Flags().BoolVar(&dry, "dry-run", false, "show what would change; write nothing")
	cmd.AddCommand(install)
	return cmd
}

func mcpConfigPath(home, agent string) (string, error) {
	switch agent {
	case "codex":
		return filepath.Join(home, ".codex", "config.toml"), nil
	case "cursor":
		return filepath.Join(home, ".cursor", "mcp.json"), nil
	}
	return "", usageErr("unsupported agent "+agent, "")
}
