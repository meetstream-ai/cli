package cli

import (
	"gopkg.in/yaml.v3"

	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode"

	"github.com/meetstream-ai/cli/internal/api"
	"github.com/spf13/cobra"
)

func newMiaCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "mia", Aliases: []string{"agents"}, Short: "Manage MIA agent configs", GroupID: "meet"}
	cmd.AddCommand(newMiaApplyCmd())
	cmd.AddCommand(botAction("list", 0, func(cmd *cobra.Command, c *api.Client, _ []string) (any, error) { return c.MiaList(cmd.Context()) }))
	cmd.AddCommand(botAction("get <id>", 1, func(cmd *cobra.Command, c *api.Client, args []string) (any, error) {
		return c.MiaGet(cmd.Context(), args[0])
	}))
	for _, name := range []string{"create", "update"} {
		var file string
		n, use := 0, name
		if name == "update" {
			n = 1
			use += " <id>"
		}
		sub := botAction(use, n, func(cmd *cobra.Command, c *api.Client, args []string) (any, error) {
			b, err := os.ReadFile(file)
			if err != nil {
				return nil, usageErr("Could not read JSON from "+file, "")
			}
			var body map[string]any
			if json.Unmarshal(b, &body) != nil || body == nil {
				return nil, usageErr("Could not read JSON object from "+file, "")
			}
			if name == "update" {
				body["agent_config_id"] = args[0]
				return c.MiaUpdate(cmd.Context(), body)
			}
			return c.MiaCreate(cmd.Context(), body)
		})
		sub.Flags().StringVarP(&file, "file", "f", "", "JSON config file (send whole blocks)")
		_ = sub.MarkFlagRequired("file")
		cmd.AddCommand(sub)
	}
	var yes bool
	del := botAction("delete <id>", 1, func(cmd *cobra.Command, c *api.Client, args []string) (any, error) {
		ok, err := confirmDelete(cmd, yes)
		if err != nil {
			return nil, err
		}
		if !ok {
			return map[string]any{"deleted": false}, nil
		}
		return c.MiaDelete(cmd.Context(), args[0])
	})
	del.Flags().BoolVar(&yes, "yes", false, "skip confirmation")
	cmd.AddCommand(del)
	cmd.AddCommand(&cobra.Command{Use: "check <id>", Short: "Check local agent connection", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, err := apiClient(cmd)
		if err != nil {
			return err
		}
		v, err := c.MiaCheckLocalAgent(cmd.Context(), args[0])
		if err := result("Local agent bridge", v, err); err != nil {
			return err
		}
		if object(object(v)["local_agent"])["status"] != "ok" {
			return &ExitError{Code: 3, Message: "local agent bridge is not ready", Silent: true}
		}
		return nil
	}})
	return cmd
}

func newMiaApplyCmd() *cobra.Command {
	var file string
	var dry bool
	cmd := &cobra.Command{Use: "apply", Short: "Create or update an agent config from a file", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		b, err := os.ReadFile(file)
		if err != nil {
			return usageErr("could not read config file", "")
		}
		var body map[string]any
		// YAML is a superset of JSON, so one parser reads both mia.yaml and mia.json.
		if yaml.Unmarshal(b, &body) != nil || body == nil {
			return usageErr("the config file must be a YAML or JSON object", "write the create body (agent_name, mode, model, voice, transcriber), or save: meetstream mia get <id> --json")
		}
		body = miaRequestBody(body)
		id := first(body, "agent_config_id", "id")
		if id != "" {
			body["agent_config_id"] = id
			delete(body, "id")
		}
		if dry {
			return json.NewEncoder(os.Stdout).Encode(body)
		}
		c, err := apiClient(cmd)
		if err != nil {
			return err
		}
		var v any
		if id != "" {
			v, err = c.MiaUpdate(cmd.Context(), body)
		} else {
			v, err = c.MiaCreate(cmd.Context(), body)
		}
		if err != nil {
			return apiError(err)
		}
		return emit(v, func() { fmt.Fprintln(os.Stdout, first(object(v), "agent_config_id", "id")) })
	}}
	cmd.Flags().StringVarP(&file, "file", "f", "", "agent config file (mia.yaml or JSON)")
	_ = cmd.MarkFlagRequired("file")
	cmd.Flags().BoolVar(&dry, "dry-run", false, "print request body without sending")
	return cmd
}

// miaReadOnly are record fields that GET /mia returns and POST/PUT do not take.
var miaReadOnly = map[string]bool{"user_id": true, "created_at": true, "updated_at": true, "audio": true}

// miaRequestBody turns a saved `mia get --json` answer ({"agent_config": {...}}
// with PascalCase keys such as AgentName and Model) into the snake_case body
// that POST/PUT /mia read. A body already in that shape passes through. Only
// top-level keys change: the blocks inside (model, voice, ...) are snake_case
// in both.
func miaRequestBody(body map[string]any) map[string]any {
	if inner, ok := body["agent_config"].(map[string]any); ok && len(body) == 1 {
		body = inner
	}
	out := map[string]any{}
	for k, v := range body {
		if k == "" || !unicode.IsUpper(rune(k[0])) {
			out[k] = v
		}
	}
	for k, v := range body {
		if k == "" || !unicode.IsUpper(rune(k[0])) {
			continue
		}
		sk := snakeCase(k)
		if _, set := out[sk]; set || miaReadOnly[sk] || v == nil {
			continue // a snake_case key in the file wins; read-only and null fields are dropped
		}
		out[sk] = v
	}
	return out
}

// snakeCase is AgentConfigID -> agent_config_id, WakeWord -> wake_word.
func snakeCase(s string) string {
	r := []rune(s)
	var b strings.Builder
	for i, c := range r {
		if unicode.IsUpper(c) && i > 0 && (unicode.IsLower(r[i-1]) || (i+1 < len(r) && unicode.IsLower(r[i+1]) && unicode.IsUpper(r[i-1]))) {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToLower(c))
	}
	return b.String()
}
