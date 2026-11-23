// Package pricing says what an hour of meeting would cost at the vendors' API
// rates, and whether the CLI is on a plan. Ported from the kit's pricing.py.
//
// Most agent CLIs run on a subscription or a free plan (Antigravity with a Google
// account, Claude Code with a Claude plan, Codex with ChatGPT, Copilot); nothing is
// billed per token. The picker still shows an API-equivalent price so models can be
// compared: the vendors' published per-token list prices (standard tier, USD per
// million tokens) from a small dated table.
//
// The table lives in MeetStream's shared estimates file ("prices"), so the weekly
// refresh can update it without a release; the copy embedded here is the fallback.
// Prices are estimates: the hour is projected from one measured turn, the measured
// turn's cached share is assumed for the hour, and cache writes, tiers and
// discounts are left out.
package pricing

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/meetstream-ai/cli/internal/agentkit/pairing"
	"github.com/meetstream-ai/cli/internal/agentkit/usage"
)

// Billing kinds returned by PlanKind.
const (
	Plan = "plan" // signed in on a subscription or free plan: no per-token bill
	Free = "free" // a free model (an OpenRouter ":free" id)
	API  = "api"  // billed per token with an API key
)

// CacheFile is the shared estimates file's name, both in the kit home (the cached
// download) and embedded here (the bundled copy).
const CacheFile = "model-estimates.json"

//go:embed data/model-estimates.json
var bundled []byte

// Bundled is the embedded copy of MeetStream's shared estimates file (schema 1,
// "estimates" plus the dated "prices" block), as raw JSON.
func Bundled() []byte { return append([]byte(nil), bundled...) }

// BundledDocument is the embedded estimates file, decoded.
func BundledDocument() map[string]any {
	var doc map[string]any
	if err := json.Unmarshal(bundled, &doc); err != nil {
		return map[string]any{}
	}
	return doc
}

// a suffix that names another, cheaper model ("flash-lite"), not an effort level ("flash-high")
var variants = map[string]bool{"lite": true, "mini": true, "nano": true, "tts": true, "image": true, "live": true, "audio": true, "embedding": true}

// trailing ":tag"s that are not part of the model name
var tags = map[string]bool{"free": true, "thinking": true, "beta": true, "nitro": true, "floor": true, "online": true, "extended": true, "exacto": true}

// Price is one row of the table: USD per million tokens.
type Price struct {
	Match  string
	Vendor string
	Input  float64
	Cached float64
	Output float64
	AsOf   string
	Note   string
}

// Normalize maps "nous:anthropic/claude-sonnet-4.6" or "claude-sonnet-4.6:free" to "claude-sonnet-4-6".
func Normalize(model string) string {
	s := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	parts := strings.Split(s, ":")
	for len(parts) > 1 && tags[parts[len(parts)-1]] {
		parts = parts[:len(parts)-1] // ":free", ":thinking"
	}
	name := parts[0]
	if len(parts) > 1 {
		name = parts[1] // "opencode-go:gpt-6-luna": Hermes' provider:model
	}
	return strings.NewReplacer(".", "-", "_", "-").Replace(name)
}

// number is a non-negative JSON number (float64, or json.Number from a UseNumber decoder).
func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, x >= 0
	case json.Number:
		f, err := x.Float64()
		return f, err == nil && f >= 0
	}
	return 0, false
}

// Table is the well-formed rows of a decoded document's "prices" block; anything
// odd is skipped, and a block without an as_of date is not trusted.
func Table(document any) []Price {
	doc, _ := document.(map[string]any)
	block, _ := doc["prices"].(map[string]any)
	asOf, ok := block["as_of"].(string)
	if block == nil || !ok {
		return nil
	}
	rows, _ := block["models"].([]any)
	var found []Price
	for _, r := range rows {
		row, _ := r.(map[string]any)
		match, _ := row["match"].(string)
		if row == nil || strings.TrimSpace(match) == "" {
			continue
		}
		in, ok1 := number(row["input"])
		cached, ok2 := number(row["cached"])
		out, ok3 := number(row["output"])
		if !ok1 || !ok2 || !ok3 {
			continue
		}
		vendor, _ := row["vendor"].(string)
		note, _ := row["note"].(string)
		found = append(found, Price{Normalize(match), vendor, in, cached, out, asOf, note})
	}
	return found
}

func readJSON(path string) any {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var v any
	if json.Unmarshal(data, &v) != nil {
		return nil
	}
	return v
}

// Load is the cached shared file's prices (the picker fetched it moments ago, any
// age), else the embedded ones. The cache is <kit home>/model-estimates.json.
func Load() []Price { return LoadFrom(pairing.Home()) }

// LoadFrom is Load with an explicit kit home.
func LoadFrom(kitHome string) []Price {
	if found := Table(readJSON(filepath.Join(kitHome, CacheFile))); len(found) > 0 {
		return found
	}
	return Table(BundledDocument())
}

// PriceFor is the longest table entry that model starts with, at a word boundary,
// skipping variant suffixes (lite, mini, nano, …). Nil when none fits.
func PriceFor(model string, prices []Price) *Price {
	name := Normalize(model)
	var best *Price
	for i := range prices {
		key := prices[i].Match
		if !strings.HasPrefix(name, key) {
			continue
		}
		rest := name[len(key):]
		fits := rest == ""
		if !fits && (rest[0] == '-' || rest[0] == '[') {
			word, _, _ := strings.Cut(strings.TrimLeft(rest, "-["), "-")
			fits = !variants[word]
		}
		if fits && (best == nil || len(key) > len(best.Match)) {
			best = &prices[i]
		}
	}
	return best
}

// HourCost is the hour at price; the measured turn's cached share of input applies to the hour.
func HourCost(hour usage.HourEstimate, turn usage.Usage, price Price) float64 {
	share := 0.0
	if turn.Input > 0 {
		share = min(1.0, float64(turn.Cached)/float64(turn.Input))
	}
	fresh := float64(hour.Input) * (1 - share)
	cached := float64(hour.Input) * share
	return (fresh*price.Input + cached*price.Cached + float64(hour.Output)*price.Output) / 1_000_000
}

// PlanOptions overrides where PlanKind looks; zero values use the real environment and home.
type PlanOptions struct {
	Getenv func(string) string // default os.Getenv
	Home   string              // default os.UserHomeDir()
}

// PlanKind is Plan, Free, API or "" (unknown), from local sign-in files only.
// Secrets are only checked for presence: never returned, logged or compared.
func PlanKind(agent, model string, opts PlanOptions) string {
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	home := opts.Home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if strings.HasSuffix(strings.ToLower(model), ":free") {
		return Free
	}
	switch agent {
	case "agy", "copilot":
		return Plan // a Google account / a Copilot subscription; neither bills per token
	case "claude":
		if getenv("ANTHROPIC_API_KEY") != "" {
			return API
		}
		if isObject(readKeys(filepath.Join(home, ".claude.json"))["oauthAccount"]) {
			return Plan
		}
	case "codex":
		auth := readKeys(filepath.Join(home, ".codex", "auth.json"))
		if present(auth["OPENAI_API_KEY"]) {
			return API
		}
		if isObject(auth["tokens"]) {
			return Plan
		}
	}
	return ""
}

// readKeys decodes only the top level, leaving every value as raw bytes that are
// inspected for shape (object / non-empty), never decoded into strings.
func readKeys(path string) map[string]json.RawMessage {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(data, &m) != nil {
		return nil
	}
	return m
}

func isObject(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return strings.HasPrefix(s, "{")
}

// present: set to anything but null, false, 0 or "" (Python truthiness, as the kit checks it).
func present(raw json.RawMessage) bool {
	switch s := strings.TrimSpace(string(raw)); s {
	case "", "null", "false", "0", `""`, "[]", "{}":
		return false
	}
	return true
}

// Money is "$0.45", "$0.00" or "<$0.01".
func Money(value float64) string {
	if value >= 0.01 || value == 0 {
		return fmt.Sprintf("$%.2f", value)
	}
	return "<$0.01"
}
