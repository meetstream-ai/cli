package models

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/meetstream-ai/cli/internal/agentkit/agents"
)

// ── Ported from test_hermes_models.py ──────────────────────────────────────

// Shaped like a real Hermes config (comments, quotes, aliases, list items, unrelated blocks).
const hermesConfig = `_config_version: 49
model:
  # Default model to use (can be overridden with --model flag)
  default: "upstage/solar-mini4:free"
  provider: "nous"   # Nous Portal

  aliases:
    go-planner:
      provider: opencode-go
      model: gpt-6-luna
    codex-review:
      provider: openai-codex
      model: gpt-6.1-sol
  base_url: https://inference-api.nousresearch.com/v1
stt:
  provider: local
  local:
    model: "base"              # tiny | base | small
  openai:
    provider: openai
    model: "whisper-1"
delegation:
  max_iterations: 80
  # comments do not end a block
  provider: opencode-go
  model: deepseek-v4.1-flash
  fallback_providers:
    - provider: opencode-go
      model: glm-5.3
fallback_providers:
  - provider: opencode-go
    model: gpt-6-luna
moa:
  reference_models:
    - provider: opencode-go
      model: glm-5.3
      enabled: true
  aggregator:
    provider: openai-codex
    model: gpt-6.1-sol
agent:
  reasoning_effort: high
`

func hermesHome(t *testing.T, config string, cache map[string]any) string {
	t.Helper()
	home := t.TempDir()
	write(t, filepath.Join(home, ".hermes", "config.yaml"), config)
	if cache != nil {
		data, _ := json.Marshal(cache)
		write(t, filepath.Join(home, ".hermes", "provider_models_cache.json"), string(data))
	}
	return home
}

func TestConfigPairsBecomeProviderModelIDs(t *testing.T) {
	var got [][2]string
	for _, m := range HermesModelsFrom(hermesConfig) {
		got = append(got, [2]string{m.ID, m.Label})
	}
	want := [][2]string{
		{"opencode-go:gpt-6-luna", "alias go-planner"},
		{"openai-codex:gpt-6.1-sol", "alias codex-review"},
		{"opencode-go:deepseek-v4.1-flash", "delegation"},
		{"opencode-go:glm-5.3", "delegation"},
	} // each pair once; speech-to-text models (stt) are not chat models
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestHermesListsItsDefaultAndConfiguredModelsWithoutStartingACP(t *testing.T) {
	// Written as raw JSON to keep the file's key order, as Hermes writes it.
	home := hermesHome(t, hermesConfig, nil)
	write(t, filepath.Join(home, ".hermes", "provider_models_cache.json"), `{
		"nous": {"fp": "x", "at": 1, "models": ["anthropic/claude-opus-4.6", "upstage/solar-mini4:free"]},
		"copilot-acp": {"fp": "x", "at": 1, "models": ["auto"]},
		"broken": "not a dict"}`)
	c := CatalogFor(agents.All["hermes"], "hermes", Options{Home: home, ACP: noACP(t), Run: noRun(t)})
	if c.Source != "~/.hermes/config.yaml" || c.DefaultModel != "upstage/solar-mini4:free" {
		t.Fatalf("source %q default %q", c.Source, c.DefaultModel)
	}
	want := []string{"upstage/solar-mini4:free", "opencode-go:gpt-6-luna", "openai-codex:gpt-6.1-sol", "opencode-go:deepseek-v4.1-flash", "opencode-go:glm-5.3"}
	if got := ids(c.Models); !reflect.DeepEqual(got, want) {
		t.Fatalf("models %v", got)
	}
	if c.DefaultEffort != "high" {
		t.Errorf("effort %q", c.DefaultEffort)
	}
	if !strings.Contains(c.Note, "2 more models") { // the cached ones not listed, never "auto"
		t.Errorf("note %q", c.Note)
	}
	if !strings.Contains(c.Note, "nous:anthropic/claude-opus-4.6") { // the main provider's first
		t.Errorf("note %q", c.Note)
	}
}

func TestMainProviderExtrasComeFirstInTheNote(t *testing.T) {
	home := hermesHome(t, hermesConfig, nil)
	write(t, filepath.Join(home, ".hermes", "provider_models_cache.json"),
		`{"other": {"models": ["z-model"]}, "nous": {"models": ["anthropic/claude-opus-4.6"]}}`)
	c := HermesCatalog(home)
	if !strings.Contains(c.Note, "for example nous:anthropic/claude-opus-4.6.") {
		t.Fatalf("note %q", c.Note)
	}
}

func TestWithoutAConfigTheSourceIsStillNamed(t *testing.T) {
	c := CatalogFor(agents.All["hermes"], "hermes", Options{Home: t.TempDir(), ACP: noACP(t), Run: noRun(t)})
	if len(c.Models) != 0 || c.Error == "" {
		t.Fatalf("no config: %+v", c)
	}
	onlyDefault := hermesHome(t, "model:\n  default: \"upstage/solar-mini4:free\"\n", nil)
	c = CatalogFor(agents.All["hermes"], "hermes", Options{Home: onlyDefault, ACP: noACP(t), Run: noRun(t)})
	if got := ids(c.Models); !reflect.DeepEqual(got, []string{"upstage/solar-mini4:free"}) {
		t.Fatalf("models %v", got)
	}
	if c.Source == "" { // was blank: "✓ 1 model from "
		t.Fatal("blank source")
	}
	if c.Summary("Hermes Agent") != "1 model from ~/.hermes/config.yaml" {
		t.Fatalf("summary %q", c.Summary("Hermes Agent"))
	}
}

func TestAFailedACPListWithASettingsDefaultNamesItsSource(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"), `{"model": "m1"}`)
	broken := func(agents.Spec, string, time.Duration) (Catalog, error) {
		return Catalog{}, errors.New("session/new did not answer within 30 s")
	}
	noModels := func(context.Context, []string) (string, string, error) {
		return "", "", errors.New("agy models failed")
	}
	c := CatalogFor(agents.All["agy"], "agy", Options{Home: home, ACP: broken, Run: noModels})
	if got := ids(c.Models); !reflect.DeepEqual(got, []string{"m1"}) || c.Source != "your CLI's settings" {
		t.Fatalf("models %v from %q", got, c.Source)
	}
	if (Catalog{Models: []Choice{{ID: "x"}}}).Summary("X") != "1 model from your CLI's settings" {
		t.Fatal("blank source must fall back")
	}
}

// Only Copilot's Auto skips the menu: Hermes takes other ids even when it lists one.
// (The kit's test drives setup's _pick_model; the decision itself is AutoOnly.)
func TestHermesWithOnlyItsDefaultStillGetsAMenu(t *testing.T) {
	c := Catalog{Models: []Choice{{ID: "upstage/solar-mini4:free"}}, DefaultModel: "upstage/solar-mini4:free", Source: "test"}
	if _, only := c.AutoOnly(); only {
		t.Fatal("a single non-auto model must still get the menu")
	}
}

// ── Ported from test_copilot_models.py ─────────────────────────────────────

// `copilot --acp --stdio` session/new, GitHub Copilot CLI 1.0.114: "auto" twice, nothing else.
func recorded(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "copilot_acp_session_new.json"))
	if err != nil {
		t.Fatal(err)
	}
	return decode(t, string(data))
}

func copilotACP(created map[string]any) func(agents.Spec, string, time.Duration) (Catalog, error) {
	return func(agents.Spec, string, time.Duration) (Catalog, error) {
		return ACPCatalogFrom(created, "copilot", nil), nil
	}
}

func TestRecordedCopilotListHasAutoTwiceAndWeKeepOne(t *testing.T) {
	created := recorded(t)
	var raw []string
	for _, m := range created["models"].(map[string]any)["availableModels"].([]any) {
		raw = append(raw, m.(map[string]any)["modelId"].(string))
	}
	if !reflect.DeepEqual(raw, []string{"auto", "auto"}) {
		t.Fatalf("fixture %v", raw)
	}
	c := ACPCatalogFrom(created, "copilot --acp --stdio", nil)
	if got := ids(c.Models); !reflect.DeepEqual(got, []string{"auto"}) || c.DefaultModel != "auto" {
		t.Fatalf("models %v default %q", got, c.DefaultModel)
	}
}

func TestCopilotWithOnlyAutoExplainsHowToGetMore(t *testing.T) {
	c := CatalogFor(agents.All["copilot"], "copilot", Options{Home: t.TempDir(), ACP: copilotACP(recorded(t)), Run: noRun(t)})
	if got := ids(c.Models); !reflect.DeepEqual(got, []string{"auto"}) {
		t.Fatalf("models %v", got)
	}
	if !strings.Contains(c.Note, "github.com/settings/copilot") {
		t.Fatalf("note %q", c.Note)
	}
	// What setup shows instead of a menu (test_setup_says_copilot_picks_the_model_itself).
	only, ok := c.AutoOnly()
	if !ok || only.ID != "auto" {
		t.Fatal("AutoOnly")
	}
	if got := AutoLine(agents.All["copilot"].Label); got != "GitHub Copilot CLI picks the model itself (Auto)" {
		t.Fatalf("line %q", got)
	}
}

func TestACopilotAccountWithMoreModelsShowsThemAllOnce(t *testing.T) {
	created := recorded(t)
	models := created["models"].(map[string]any)
	models["availableModels"] = append(models["availableModels"].([]any),
		map[string]any{"modelId": "claude-sonnet-5.5", "name": "Claude Sonnet 5.5"},
		map[string]any{"modelId": "gpt-6.1-sol", "name": "GPT-6.1 Sol"},
		map[string]any{"modelId": "claude-sonnet-5.5", "name": "Claude Sonnet 5.5"},
	)
	c := CatalogFor(agents.All["copilot"], "copilot", Options{Home: t.TempDir(), ACP: copilotACP(created), Run: noRun(t)})
	if got := ids(c.Models); !reflect.DeepEqual(got, []string{"auto", "claude-sonnet-5.5", "gpt-6.1-sol"}) {
		t.Fatalf("models %v", got)
	}
	if c.Note != "" {
		t.Fatalf("note %q", c.Note)
	}
	if _, only := c.AutoOnly(); only {
		t.Fatal("more than Auto: show the menu")
	}
}

func TestThePickerNeverShowsAModelTwice(t *testing.T) {
	twice := Catalog{Models: []Choice{{ID: "auto"}, {ID: "auto"}, {ID: "x"}}, DefaultModel: "auto"}
	if got := ids(MenuModels(twice, "")); !reflect.DeepEqual(got, []string{"auto", "x"}) {
		t.Fatalf("menu %v", got)
	}
	if got := ids(Unique([]Choice{{ID: "a"}, {ID: "b"}, {ID: "a"}})); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("unique %v", got)
	}
}

func TestMenuKeepsTheDefaultAndLastPickWhenLong(t *testing.T) {
	var many []Choice
	for i := 0; i < 20; i++ {
		many = append(many, Choice{ID: string(rune('a' + i))})
	}
	menu := MenuModels(Catalog{Models: many, DefaultModel: "s"}, "t")
	if len(menu) != MenuSize || menu[0].ID != "s" || menu[1].ID != "t" {
		t.Fatalf("menu %v", ids(menu))
	}
}
