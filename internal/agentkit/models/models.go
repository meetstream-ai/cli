// Package models lists which models and effort levels an agent CLI offers, and
// which it uses by default. Ported from the kit's models.py.
//
// Each list comes from the CLI itself where it can tell us:
//
//   - ACP agents (Antigravity, Copilot, Muse, OpenClaw): session/new returns
//     "models" and, for some, a thought_level option. Antigravity falls back to
//     "agy models".
//   - Hermes: read from ~/.hermes/config.yaml. Its ACP session/new probes every
//     provider and takes over 30 s, so it is never started for listing.
//   - Codex: "codex app-server" answers model/list with each model's efforts.
//   - Claude Code has no listing command; its --help names the aliases and effort levels.
//
// The preselected default is the CLI's own setting (CLIDefaults), then what the
// CLI itself reports as current. Every list is de-duplicated (Copilot lists
// "auto" twice).
package models

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/meetstream-ai/cli/internal/agentkit/agents"
)

// ListTimeout is how long a CLI gets to list its models.
const ListTimeout = 30 * time.Second

// MenuSize is the most models a picker shows (the default and the last pick always in).
const MenuSize = 12

// SettingsSource names the list when only the CLI's settings file answered.
const SettingsSource = "your CLI's settings"

// Choice is one model a CLI offers.
type Choice struct {
	ID           string
	Label        string
	Efforts      []string // effort levels this model takes, when the CLI lists them per model
	Vision       *bool    // nil: unknown
	VisionSource string
}

// Catalog is what a CLI offers.
type Catalog struct {
	Models        []Choice
	DefaultModel  string
	Efforts       []string
	DefaultEffort string
	Source        string // where the list came from ("agy acp", "codex app-server", "from claude --help")
	Error         string // why the listing failed, when it did
	Vision        *bool
	VisionSource  string
	Note          string // something the user should know about this list, shown under it
}

// EffortsFor is the model's own efforts when listed, else the catalog's.
func (c Catalog) EffortsFor(model string) []string {
	for _, m := range c.Models {
		if m.ID == model && len(m.Efforts) > 0 {
			return append([]string(nil), m.Efforts...)
		}
	}
	return append([]string(nil), c.Efforts...)
}

// SourceLabel is Source, or SettingsSource when it is blank: never "1 model from ".
func (c Catalog) SourceLabel() string {
	if strings.TrimSpace(c.Source) == "" {
		return SettingsSource
	}
	return c.Source
}

// Summary is the line shown above the menu: "3 models from agy acp", or
// "<label> did not list its models (<error>)".
func (c Catalog) Summary(agentLabel string) string {
	if n := len(c.Models); n > 0 {
		s := "s"
		if n == 1 {
			s = ""
		}
		return fmt.Sprintf("%d model%s from %s", n, s, c.SourceLabel())
	}
	if c.Error != "" {
		return fmt.Sprintf("%s did not list its models (%s)", agentLabel, c.Error)
	}
	return agentLabel + " did not list its models"
}

// AutoOnly is the single "auto" model when that is all the CLI offers (Copilot
// accounts that allow only Auto over ACP). Setup then says AutoLine instead of a
// menu. Only for "auto": a CLI that lists just its default (Hermes) still gets the
// menu, since it takes other ids.
func (c Catalog) AutoOnly() (Choice, bool) {
	if len(c.Models) == 1 && c.Models[0].ID == "auto" {
		return c.Models[0], true
	}
	return Choice{}, false
}

// AutoLine is what setup says for an AutoOnly catalog.
func AutoLine(agentLabel string) string { return agentLabel + " picks the model itself (Auto)" }

// Unique keeps one row per model id, the first one (Copilot lists "auto" twice).
func Unique(models []Choice) []Choice {
	seen := map[string]bool{}
	out := make([]Choice, 0, len(models))
	for _, m := range models {
		if !seen[m.ID] {
			seen[m.ID] = true
			out = append(out, m)
		}
	}
	return out
}

// MenuModels is at most MenuSize models, with the default and keep always in, each once.
func MenuModels(c Catalog, keep string) []Choice {
	models := Unique(c.Models)
	if len(models) <= MenuSize {
		return models
	}
	var must, rest []Choice
	for _, m := range models {
		if m.ID == c.DefaultModel || m.ID == keep {
			must = append(must, m)
		} else {
			rest = append(rest, m)
		}
	}
	return append(must, rest...)[:MenuSize]
}

// ── Features ────────────────────────────────────────────────────────────────

// Feature is one meeting capability chip; Available nil means unknown.
type Feature struct {
	Name      string // talk, tools, vision, local
	Available *bool
	Source    string
}

func ptr(b bool) *bool { return &b }

func documentedVision(agent agents.Spec, model string) *bool {
	lower := strings.ToLower(model)
	if strings.Contains(lower, "gpt-oss") {
		return ptr(false)
	}
	if agent.Name == "claude" || strings.Contains(lower, "claude") {
		return ptr(true)
	}
	return nil
}

// Features are the meeting capabilities, with the source of the vision claim.
// Per-model facts win (Codex inputModalities), then the documented per-model rules
// (gpt-oss is text-only), and only then an agent-wide ACP capability.
func Features(agent agents.Spec, model Choice) []Feature {
	vision, source := model.Vision, model.VisionSource
	documented := documentedVision(agent, model.ID)
	agentWide := strings.HasSuffix(source, " acp") // ACP promptCapabilities describe the agent
	if documented != nil && (vision == nil || agentWide) {
		vision, source = documented, "from docs"
	}
	tools := agent.Warm == "claude_stream_json" || agent.Warm == "codex_app_server" || (agent.Warm == "acp" && agent.ACPMCP)
	var local *bool
	if _, ok := agents.All[agent.Name]; ok {
		local = ptr(true)
	}
	return []Feature{
		{Name: "talk", Available: ptr(true)},
		{Name: "tools", Available: ptr(tools)},
		{Name: "vision", Available: vision, Source: source},
		{Name: "local", Available: local, Source: "per access level"},
	}
}

// ── The CLI's own defaults ──────────────────────────────────────────────────

func userHome(home string) string {
	if home != "" {
		return home
	}
	h, _ := os.UserHomeDir()
	return h
}

func readText(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(data), true
}

// TOMLValue is a top-level `key = "value"` from a TOML file (before the first table).
func TOMLValue(text, key string) string {
	top, _, _ := strings.Cut(text, "\n[")
	re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(key) + `\s*=\s*"([^"]*)"`)
	if m := re.FindStringSubmatch(top); m != nil {
		return m[1]
	}
	return ""
}

// YAMLBlockValue is `key: value` inside a top-level `block:` of a simple YAML file.
func YAMLBlockValue(text, block, key string) string {
	blockRe := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(block) + `:\s*\n((?:[ \t]+.*\n?|\s*\n)*)`)
	m := blockRe.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	keyRe := regexp.MustCompile(`(?m)^[ \t]+` + regexp.QuoteMeta(key) + `:\s*["']?([^"'\n#]+)`)
	if found := keyRe.FindStringSubmatch(m[1]); found != nil {
		return strings.TrimSpace(found[1])
	}
	return ""
}

// CLIDefaults is the (model, effort) the CLI uses when none is given, from its own
// settings file under home ("" = os.UserHomeDir).
func CLIDefaults(agent agents.Spec, home string) (string, string) {
	root := userHome(home)
	switch agent.Name {
	case "agy":
		d := readJSONObject(filepath.Join(root, ".gemini", "antigravity-cli", "settings.json"))
		return str(d["model"]), str(d["effort"])
	case "claude":
		d := readJSONObject(filepath.Join(root, ".claude", "settings.json"))
		return str(d["model"]), str(d["effortLevel"])
	case "codex":
		text, ok := readText(filepath.Join(root, ".codex", "config.toml"))
		if !ok {
			return "", ""
		}
		return TOMLValue(text, "model"), TOMLValue(text, "model_reasoning_effort")
	case "hermes":
		text, ok := readText(filepath.Join(root, ".hermes", "config.yaml"))
		if !ok {
			return "", ""
		}
		return YAMLBlockValue(text, "model", "default"), YAMLBlockValue(text, "agent", "reasoning_effort")
	}
	return "", ""
}

// ── Listing ─────────────────────────────────────────────────────────────────

// Runner runs a short command (argv[0] is the binary) and returns its output.
// The context carries the listing timeout. Tests pass a fake; they never run real CLIs.
type Runner func(ctx context.Context, argv []string) (stdout, stderr string, err error)

// Options are CatalogFor's injectable parts; zero values use the real ones.
type Options struct {
	Run     Runner        // short commands: claude --help, agy models
	Timeout time.Duration // default ListTimeout
	Home    string        // the user's home for settings files; default os.UserHomeDir
	// ACP lists an ACP agent's models (initialize + session/new). Default: start it over stdio.
	ACP func(agent agents.Spec, binary string, timeout time.Duration) (Catalog, error)
	// Codex lists Codex's models over "codex app-server". Default: start it over stdio.
	Codex func(binary string, timeout time.Duration) (Catalog, error)
	// Hermes reads Hermes' models from its files. Default: HermesCatalog.
	Hermes func(home string) Catalog
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// CatalogFor asks the CLI. It never fails: a CLI that cannot list leaves an empty
// catalog with Error, and the settings-file default is still offered.
func CatalogFor(agent agents.Spec, binary string, opts Options) Catalog {
	if opts.Run == nil {
		opts.Run = ExecRunner
	}
	if opts.Timeout <= 0 {
		opts.Timeout = ListTimeout
	}
	if opts.ACP == nil {
		opts.ACP = ACPCatalog
	}
	if opts.Codex == nil {
		opts.Codex = CodexCatalog
	}
	if opts.Hermes == nil {
		opts.Hermes = HermesCatalog
	}
	run := func(argv ...string) (string, string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
		defer cancel()
		return opts.Run(ctx, argv)
	}

	var catalog Catalog
	var err error
	switch {
	case agent.Name == "hermes":
		catalog = opts.Hermes(opts.Home)
	case agent.Warm == "acp":
		catalog, err = opts.ACP(agent, binary, opts.Timeout)
	case agent.Warm == "codex_app_server":
		catalog, err = opts.Codex(binary, opts.Timeout)
	case agent.Name == "claude":
		var out, errOut string
		out, errOut, err = run(binary, "--help")
		if err == nil || out != "" || errOut != "" {
			catalog, err = ClaudeCatalogFrom(out+"\n"+errOut), nil
		}
	}
	if err != nil {
		catalog = Catalog{Error: clip(err.Error(), 200)}
	}
	catalog.Models = Unique(catalog.Models)
	if agent.Name == "copilot" {
		if _, only := catalog.AutoOnly(); only {
			// Checked 2026-11-21 (Copilot CLI 1.0.114): over ACP, which meetings use, such an
			// account accepts only "auto" (--model is ignored, session/set_model rejects others).
			catalog.Note = "Copilot offers only Auto to apps on this account. To use other models, enable them in " +
				"your Copilot settings (github.com/settings/copilot), then run setup again."
		}
	}
	if agent.Name == "agy" && len(catalog.Models) == 0 {
		out, _, err := run(binary, "models")
		if err != nil && out == "" {
			if catalog.Error == "" {
				catalog.Error = clip(err.Error(), 200)
			}
		} else {
			catalog.Models, catalog.Source = Unique(AgyModelsFrom(out)), "agy models"
		}
	}
	if agent.Name == "agy" && len(catalog.Efforts) == 0 {
		catalog.Efforts = []string{"low", "medium", "high"}
	}
	if agent.Name == "hermes" && len(catalog.Efforts) == 0 {
		catalog.Efforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}
	}
	model, effort := CLIDefaults(agent, opts.Home)
	if model != "" {
		catalog.DefaultModel = model
		listed := false
		for _, m := range catalog.Models {
			if m.ID == model {
				listed = true
				break
			}
		}
		if !listed {
			if catalog.Source == "" {
				catalog.Source = SettingsSource // the list itself failed
			}
			first := Choice{ID: model, Label: "your CLI's default", Vision: catalog.Vision, VisionSource: catalog.VisionSource}
			catalog.Models = append([]Choice{first}, catalog.Models...)
		}
	}
	if effort != "" && (len(catalog.Efforts) == 0 || contains(catalog.Efforts, effort)) {
		catalog.DefaultEffort = effort
	}
	return catalog
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ── Parsers for each CLI's answer ───────────────────────────────────────────

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	case bool:
		if !x {
			return ""
		}
	case float64:
		if x == 0 {
			return ""
		}
	}
	return fmt.Sprint(v)
}

// ACPCatalogFrom is the models and thinking levels an ACP session/new result
// advertises; initialized is the initialize result (for promptCapabilities.image).
func ACPCatalogFrom(created map[string]any, source string, initialized map[string]any) Catalog {
	raw := asMap(created["models"])
	capabilities := asMap(asMap(initialized)["agentCapabilities"])
	var vision *bool
	visionSource := ""
	if img, ok := asMap(capabilities["promptCapabilities"])["image"].(bool); ok {
		vision, visionSource = ptr(img), source
	}
	var models []Choice
	for _, m := range asList(raw["availableModels"]) {
		mm := asMap(m)
		if id := str(mm["modelId"]); id != "" {
			models = append(models, Choice{ID: id, Label: str(mm["name"]), Vision: vision, VisionSource: visionSource})
		}
	}
	var efforts []string
	current := ""
	for _, o := range asList(created["configOptions"]) {
		option := asMap(o)
		if option["category"] != "thought_level" {
			continue
		}
		efforts = nil
		for _, v := range asList(option["options"]) {
			if value := str(asMap(v)["value"]); value != "" {
				efforts = append(efforts, value)
			}
		}
		current = str(option["currentValue"])
	}
	return Catalog{
		Models: Unique(models), DefaultModel: str(raw["currentModelId"]), Efforts: efforts,
		DefaultEffort: current, Source: source, Vision: vision, VisionSource: visionSource,
	}
}

// CodexCatalogFrom reads Codex app-server's model/list result: per-model efforts,
// hidden models skipped, vision from inputModalities.
func CodexCatalogFrom(listed map[string]any) Catalog {
	var models []Choice
	def, defEffort := "", ""
	var all []string
	for _, it := range asList(listed["data"]) {
		item := asMap(it)
		id := str(item["id"])
		if item == nil || id == "" || item["hidden"] == true {
			continue
		}
		var efforts []string
		for _, e := range asList(item["supportedReasoningEfforts"]) {
			if v := str(asMap(e)["reasoningEffort"]); v != "" {
				efforts = append(efforts, v)
				if !contains(all, v) {
					all = append(all, v)
				}
			}
		}
		c := Choice{ID: id, Label: str(item["displayName"]), Efforts: efforts}
		if mods, ok := item["inputModalities"].([]any); ok {
			has := false
			for _, m := range mods {
				if m == "image" {
					has = true
				}
			}
			c.Vision, c.VisionSource = ptr(has), "codex app-server"
		}
		models = append(models, c)
		if item["isDefault"] == true {
			def, defEffort = id, str(item["defaultReasoningEffort"])
		}
	}
	return Catalog{Models: Unique(models), DefaultModel: def, Efforts: all, DefaultEffort: defEffort, Source: "codex app-server"}
}

var agySlug = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-_/:]*$`)

// AgyModelsFrom reads "agy models": "slug<TAB>label" per line after a status line.
func AgyModelsFrom(text string) []Choice {
	var found []Choice
	for _, line := range strings.Split(text, "\n") {
		slug, label, _ := strings.Cut(strings.TrimSpace(line), "\t")
		if agySlug.MatchString(slug) {
			found = append(found, Choice{ID: slug, Label: strings.TrimSpace(label)})
		}
	}
	return found
}

var (
	quotedAlias = regexp.MustCompile(`'([a-z]+)'`)
	parenList   = regexp.MustCompile(`\(([a-z, ]+)\)`)
	colonList   = regexp.MustCompile(`:\s*([a-z]+(?:,\s*(?:or\s+)?[a-z]+)+)`)
)

// helpList is the values a --help text lists for flag in parentheses or after a colon.
func helpList(text, flag string) []string {
	re := regexp.MustCompile(regexp.QuoteMeta(flag) + `[^\n]*\n?(?:[^\n]*\n){0,3}`)
	block := re.FindString(text)
	if block == "" {
		return nil
	}
	inner := parenList.FindStringSubmatch(block)
	if inner == nil {
		inner = colonList.FindStringSubmatch(block)
	}
	if inner == nil {
		return nil
	}
	var out []string
	for _, part := range strings.Split(inner[1], ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, strings.TrimSpace(strings.TrimPrefix(p, "or ")))
		}
	}
	return out
}

// ClaudeCatalogFrom reads Claude Code's --help: the model aliases and effort levels.
func ClaudeCatalogFrom(help string) Catalog {
	after := help
	if _, rest, ok := strings.Cut(help, "--model"); ok {
		after = rest
	}
	if len(after) > 400 {
		after = after[:400]
	}
	var models []Choice
	for _, m := range quotedAlias.FindAllStringSubmatch(after, -1) {
		alias := m[1]
		models = append(models, Choice{ID: alias, Label: "latest " + strings.ToUpper(alias[:1]) + alias[1:]})
	}
	return Catalog{Models: Unique(models), Efforts: helpList(help, "--effort"), Source: "from claude --help"}
}
