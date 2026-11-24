package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/meetstream-ai/cli/internal/ui"
)

type flagInfo struct {
	Name      string   `json:"name"`
	Shorthand string   `json:"shorthand,omitempty"`
	Type      string   `json:"type"`
	Default   string   `json:"default,omitempty"`
	Usage     string   `json:"usage"`
	Enum      []string `json:"enum,omitempty"`
}

type commandInfo struct {
	Path    string     `json:"path"`
	Use     string     `json:"use"`
	Short   string     `json:"short"`
	Group   string     `json:"group,omitempty"`
	Planned bool       `json:"planned,omitempty"`
	Flags   []flagInfo `json:"flags,omitempty"`
}

// flagEnums are the choices for flags that take a fixed set, so agents read
// them instead of guessing.
func flagEnums() map[string][]string {
	return map[string][]string{
		"join --transcription":       providerIDs(postCallProviders),
		"join --live-transcription":  providerIDs(liveProviders),
		"transcribe --transcription": providerIDs(postCallProviders),
		"init --use-case":            {"notetaker", "send-a-bot", "live-captions", "webhook-receiver"},
		"init --lang":                {"python", "node"},
		"init --transcription":       providerIDs(postCallProviders),
		"init --live-transcription":  providerIDs(liveProviders),
		"init --llm":                 llmChoices,
		"init --email":               emailChoices,
		"listen --tunnel":            {"cloudflared", "ngrok", "https://<your-url>"},
		"join --video-layout":        {"speaker_view", "grid_view"},
		"setup --mode":               {"hosted", "byok", "local-agent"},
		"setup --access":             {"readonly", "confirm", "full"},
	}
}

func collectCommands(c *cobra.Command, out *[]commandInfo) {
	for _, sub := range c.Commands() {
		if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
			continue
		}
		path := strings.TrimSpace(strings.TrimPrefix(sub.CommandPath(), sub.Root().Name()))
		info := commandInfo{Path: path, Use: sub.Use, Short: sub.Short, Group: sub.GroupID, Planned: strings.HasPrefix(sub.Short, "Planned")}
		enums := flagEnums()
		sub.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) {
			if f.Hidden {
				return
			}
			fi := flagInfo{Name: f.Name, Shorthand: f.Shorthand, Type: f.Value.Type(), Usage: f.Usage, Enum: enums[path+" --"+f.Name]}
			if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "[]" && f.DefValue != "0" {
				fi.Default = f.DefValue
			}
			info.Flags = append(info.Flags, fi)
		})
		if sub.Runnable() {
			*out = append(*out, info)
		}
		collectCommands(sub, out)
	}
}

func newCommandsCmd() *cobra.Command {
	return &cobra.Command{
		Use: "commands", Short: "Every command, flag and choice (use --json; skills are built from it)", GroupID: "tool", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var all []commandInfo
			collectCommands(cmd.Root(), &all)
			out := map[string]any{
				"version":      cmd.Root().Version,
				"global_flags": []string{"--json", "--no-input", "--no-color"},
				"exit_codes":   map[string]string{"0": "ok", "1": "runtime or API error", "2": "usage error or not signed in", "3": "not ready or not available", "4": "transcript failed", "130": "interrupted"},
				"error_shape":  `{"error": {"message", "hint", "exit_code"}}`,
				"commands":     all,
			}
			return emit(out, func() {
				for _, c := range all {
					fmt.Fprintf(ui.Out, "  %-28s %s\n", c.Path, ui.DimStyle.Render(c.Short))
				}
				ui.Note(fmt.Sprintf("%d commands. Add --json for flags, choices and exit codes.", len(all)))
			})
		},
	}
}
