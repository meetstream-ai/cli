package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

const hostedMCPURL = "https://mcp.meetstream.ai/mcp"
const codexMCPSection = "[mcp_servers.meetstream]"

func newMCPCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "mcp", Short: "Configure the hosted OAuth MCP server", GroupID: "tool"}
	var write bool
	add := &cobra.Command{Use: "add <claude|codex|cursor|vscode>", Short: "Print or install the hosted MCP entry", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		agent := args[0]
		entry := map[string]any{"type": "http", "url": hostedMCPURL}
		var path, snippet string
		switch agent {
		case "claude":
			snippet = "claude mcp add --transport http meetstream " + hostedMCPURL
			// Claude's command owns its configuration; do not execute another CLI implicitly.
			if write {
				return usageErr("Claude configuration is managed by its CLI", snippet)
			}
		case "codex":
			path = filepath.Join(home, ".codex", "config.toml")
			snippet = codexMCPSection + "\nurl = \"" + hostedMCPURL + "\"\n"
		case "cursor":
			path = filepath.Join(home, ".cursor", "mcp.json")
		case "vscode":
			path = filepath.Join(".vscode", "mcp.json")
		default:
			return usageErr("unsupported agent", "choose claude, codex, cursor or vscode")
		}
		key := "mcpServers"
		if agent == "vscode" {
			key = "servers"
		}
		if snippet == "" {
			b, _ := json.MarshalIndent(map[string]any{key: map[string]any{"meetstream": entry}}, "", "  ")
			snippet = string(b) + "\n"
		}
		// Show only the new entry, never unrelated existing config (which may hold secrets).
		if write {
			fmt.Fprintf(os.Stderr, "Set MeetStream MCP entry in %s:\n%s\n", path, snippet)
		}
		if !write {
			return emit(map[string]any{"agent": agent, "path": path, "entry": entry, "snippet": snippet, "written": false}, func() { fmt.Fprintln(os.Stdout, snippet) })
		}
		if err := mergeMCPFile(agent, path, true); err != nil {
			return err
		}
		return emit(map[string]any{"agent": agent, "path": path, "entry": entry, "written": true}, func() { fmt.Fprintln(os.Stdout, "Configured MeetStream MCP in "+path) })
	}}
	add.Flags().BoolVar(&write, "write", false, "merge into the agent config, backing up an existing file to .bak")
	cmd.AddCommand(add)
	return cmd
}

// Preserve unrelated TOML verbatim. Refuse ambiguous target spellings instead of
// guessing at a TOML parse without an offline parser dependency.
func mergeCodexMCP(old []byte) ([]byte, error) {
	// Without a TOML parser, multiline strings and inline/dotted root tables can
	// make section boundaries ambiguous. Leave those files untouched.
	raw := string(old)
	if strings.Contains(raw, "\"\"\"") || strings.Contains(raw, "'''") {
		return nil, usageErr("cannot safely merge multiline TOML", "use the printed snippet to update it manually")
	}
	lines := strings.Split(raw, "\n")
	var out []string
	target := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "mcp_servers") && strings.Contains(trimmed, "=") {
			return nil, usageErr("cannot safely merge inline or dotted MCP TOML", "use the printed snippet to update it manually")
		}
		if strings.HasPrefix(trimmed, "[") {
			header := strings.TrimSpace(strings.SplitN(trimmed, "#", 2)[0])
			if strings.Contains(header, "meetstream") && header != codexMCPSection {
				return nil, usageErr("cannot safely merge this TOML MeetStream section", "use the printed snippet to update it manually")
			}
			target = header == codexMCPSection
		}
		if !target {
			out = append(out, line)
		}
	}
	return []byte(strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n\n" + codexMCPSection + "\nurl = \"" + hostedMCPURL + "\"\n"), nil
}

// mergeMCPFile puts the hosted MCP entry into an agent's config file. An existing
// file is backed up to .bak first. With strictBackup an existing .bak stops the
// write (the first backup is kept); otherwise the first backup is kept and the
// write goes on.
func mergeMCPFile(agent, path string, strictBackup bool) error {
	entry := map[string]any{"type": "http", "url": hostedMCPURL}
	key := "mcpServers"
	if agent == "vscode" {
		key = "servers"
	}
	old, err := os.ReadFile(path)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var updated []byte
	if agent == "codex" {
		updated, err = mergeCodexMCP(old)
		if err != nil {
			return err
		}
	} else {
		config := map[string]any{}
		if exists && (json.Unmarshal(old, &config) != nil || config == nil) {
			return usageErr("existing MCP config must be a JSON object", "JSON comments are not supported")
		}
		servers := map[string]any{}
		if value, ok := config[key]; ok {
			var valid bool
			servers, valid = value.(map[string]any)
			if !valid {
				return usageErr("existing "+key+" must be an object", "")
			}
		}
		servers["meetstream"] = entry
		config[key] = servers
		updated, err = json.MarshalIndent(config, "", "  ")
		if err != nil {
			return err
		}
		updated = append(updated, '\n')
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if exists {
		// Exclusive creation preserves the first backup instead of silently overwriting it.
		backup, err := os.OpenFile(path+".bak", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil && !strictBackup && errors.Is(err, os.ErrExist) {
			return os.WriteFile(path, updated, 0600)
		}
		if err != nil {
			return err
		}
		_, err = backup.Write(old)
		closeErr := backup.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if err := os.WriteFile(path, updated, 0600); err != nil {
		return err
	}
	return nil
}

// writeMCPEntry is mergeMCPFile for `meetstream ai install` (safe to run again).
func writeMCPEntry(agent, path string) error { return mergeMCPFile(agent, path, false) }
