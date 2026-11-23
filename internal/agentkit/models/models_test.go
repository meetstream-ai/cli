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

func ids(models []Choice) []string {
	out := []string{}
	for _, m := range models {
		out = append(out, m.ID)
	}
	return out
}

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// noRun fails the test if a real or fake command is run when none should be.
func noRun(t *testing.T) Runner {
	return func(_ context.Context, argv []string) (string, string, error) {
		t.Fatalf("unexpected command %v", argv)
		return "", "", nil
	}
}

func noACP(t *testing.T) func(agents.Spec, string, time.Duration) (Catalog, error) {
	return func(agents.Spec, string, time.Duration) (Catalog, error) {
		t.Fatal("ACP listing must not run here")
		return Catalog{}, nil
	}
}

const agySessionNew = `{
  "sessionId": "s",
  "models": {
    "availableModels": [
      {"modelId": "gemini-3.8-flash-high", "name": "Gemini 3.8 Flash (High)"},
      {"modelId": "gemini-3.8-flash-low", "name": "Gemini 3.8 Flash (Low)"},
      {"modelId": "claude-sonnet-4-6", "name": "Claude Sonnet 4.6 (Thinking)"}
    ],
    "currentModelId": "gemini-3.8-flash-high"
  },
  "configOptions": [
    {"id": "thought_level", "category": "thought_level", "currentValue": "high",
     "options": [{"value": "low"}, {"value": "medium"}, {"value": "high"}]}
  ]
}`

const codexModelList = `{"data": [
  {"id": "gpt-6.1-sol", "displayName": "GPT-6.1-Sol", "isDefault": true, "hidden": false,
   "defaultReasoningEffort": "medium",
   "supportedReasoningEfforts": [{"reasoningEffort": "low"}, {"reasoningEffort": "medium"}, {"reasoningEffort": "high"}, {"reasoningEffort": "xhigh"}]},
  {"id": "gpt-6-luna", "displayName": "GPT-6 Luna", "hidden": false, "supportedReasoningEfforts": []},
  {"id": "internal", "hidden": true}
]}`

const claudeHelp = `
  --effort <level>                      Effort level for the current session
                                        (low, medium, high, xhigh, max)
  --model <model>                       Model for the current session. Provide
                                        an alias for the latest model (e.g.
                                        'fable', 'opus', or 'sonnet') or a
                                        model's full name.
`

// ── Ported from test_models.py (listings) ──────────────────────────────────

func TestACPSessionNewListsModelsAndThinkingLevels(t *testing.T) {
	c := ACPCatalogFrom(decode(t, agySessionNew), "agy acp", nil)
	if got := ids(c.Models); !reflect.DeepEqual(got, []string{"gemini-3.8-flash-high", "gemini-3.8-flash-low", "claude-sonnet-4-6"}) {
		t.Errorf("models %v", got)
	}
	if c.DefaultModel != "gemini-3.8-flash-high" || c.DefaultEffort != "high" {
		t.Errorf("defaults %q %q", c.DefaultModel, c.DefaultEffort)
	}
	if !reflect.DeepEqual(c.Efforts, []string{"low", "medium", "high"}) {
		t.Errorf("efforts %v", c.Efforts)
	}
}

func TestCodexModelListKeepsPerModelEffortsAndSkipsHidden(t *testing.T) {
	c := CodexCatalogFrom(decode(t, codexModelList))
	if got := ids(c.Models); !reflect.DeepEqual(got, []string{"gpt-6.1-sol", "gpt-6-luna"}) {
		t.Errorf("models %v", got)
	}
	if c.DefaultModel != "gpt-6.1-sol" || c.DefaultEffort != "medium" {
		t.Errorf("defaults %q %q", c.DefaultModel, c.DefaultEffort)
	}
	if got := c.EffortsFor("gpt-6.1-sol"); !reflect.DeepEqual(got, []string{"low", "medium", "high", "xhigh"}) {
		t.Errorf("efforts %v", got)
	}
}

func TestAgyModelsAndClaudeHelpAreRead(t *testing.T) {
	text := "Fetching available models...\ngemini-3.8-flash-low\tGemini 3.8 Flash (Low)\nclaude-sonnet-4-6\tClaude Sonnet 4.6\n"
	want := []Choice{{ID: "gemini-3.8-flash-low", Label: "Gemini 3.8 Flash (Low)"}, {ID: "claude-sonnet-4-6", Label: "Claude Sonnet 4.6"}}
	if got := AgyModelsFrom(text); !reflect.DeepEqual(got, want) {
		t.Errorf("agy models %+v", got)
	}
	claude := ClaudeCatalogFrom(claudeHelp)
	if got := ids(claude.Models); !reflect.DeepEqual(got, []string{"fable", "opus", "sonnet"}) {
		t.Errorf("claude models %v", got)
	}
	if claude.Models[1].Label != "latest Opus" {
		t.Errorf("label %q", claude.Models[1].Label)
	}
	if !reflect.DeepEqual(claude.Efforts, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Errorf("claude efforts %v", claude.Efforts)
	}
	if claude.Source != "from claude --help" {
		t.Errorf("source %q", claude.Source)
	}
}

func TestClaudeIsListedThroughTheRunner(t *testing.T) {
	var asked []string
	run := func(_ context.Context, argv []string) (string, string, error) {
		asked = argv
		return claudeHelp, "", nil
	}
	c := CatalogFor(agents.All["claude"], "/bin/claude", Options{Home: t.TempDir(), Run: run})
	if !reflect.DeepEqual(asked, []string{"/bin/claude", "--help"}) || len(c.Models) != 3 {
		t.Fatalf("asked %v, got %v", asked, ids(c.Models))
	}
}

func TestEachCLIsOwnDefaultIsRead(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"), `{"model": "gemini-3.8-flash-low", "effort": "low"}`)
	write(t, filepath.Join(home, ".codex", "config.toml"), "model = \"gpt-6-astra\"\nmodel_reasoning_effort = \"high\"\n\n[profiles.x]\nmodel = \"other\"\n")
	write(t, filepath.Join(home, ".claude", "settings.json"), `{"model": "opus[1m]"}`)
	write(t, filepath.Join(home, ".hermes", "config.yaml"), "model:\n  provider: \"nous\"\n  default: \"upstage/solar-mini4:free\"\nagent:\n  reasoning_effort: \"high\"\n")
	cases := map[string][2]string{
		"agy":     {"gemini-3.8-flash-low", "low"},
		"codex":   {"gpt-6-astra", "high"},
		"claude":  {"opus[1m]", ""},
		"hermes":  {"upstage/solar-mini4:free", "high"},
		"copilot": {"", ""},
	}
	for name, want := range cases {
		m, e := CLIDefaults(agents.All[name], home)
		if [2]string{m, e} != want {
			t.Errorf("%s: (%q, %q), want %v", name, m, e, want)
		}
	}
}

func TestCLIDefaultsUseTheUserHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	write(t, filepath.Join(home, ".claude", "settings.json"), `{"model": "sonnet", "effortLevel": "high"}`)
	if m, e := CLIDefaults(agents.All["claude"], ""); m != "sonnet" || e != "high" {
		t.Fatalf("(%q, %q)", m, e)
	}
}

func TestCatalogPreselectsTheCLIsOwnDefault(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"), `{"model": "gemini-3.8-flash-low", "effort": "low"}`)
	c := CatalogFor(agents.All["agy"], "agy", Options{Home: home, Run: noRun(t), ACP: func(agents.Spec, string, time.Duration) (Catalog, error) {
		return ACPCatalogFrom(decode(t, agySessionNew), "agy acp", nil), nil
	}})
	if c.DefaultModel != "gemini-3.8-flash-low" || c.DefaultEffort != "low" { // settings.json, not ACP's "current"
		t.Fatalf("defaults %q %q", c.DefaultModel, c.DefaultEffort)
	}
}

func TestACLIThatCannotListFallsBackWithoutRaising(t *testing.T) {
	home := t.TempDir()
	broken := func(agents.Spec, string, time.Duration) (Catalog, error) {
		return Catalog{}, errors.New("session/new did not answer within 30 s")
	}
	agyModels := func(_ context.Context, argv []string) (string, string, error) {
		if !reflect.DeepEqual(argv[1:], []string{"models"}) {
			t.Fatalf("argv %v", argv)
		}
		return "gemini-3.8-flash-low\tFlash\n", "", nil
	}
	c := CatalogFor(agents.All["agy"], "agy", Options{Home: home, ACP: broken, Run: agyModels})
	if got := ids(c.Models); !reflect.DeepEqual(got, []string{"gemini-3.8-flash-low"}) || c.Source != "agy models" {
		t.Fatalf("models %v from %q", got, c.Source)
	}
	if !strings.Contains(c.Error, "did not answer") {
		t.Fatalf("error %q", c.Error)
	}
	if !reflect.DeepEqual(c.Efforts, []string{"low", "medium", "high"}) {
		t.Fatalf("agy efforts %v", c.Efforts)
	}
	empty := CatalogFor(agents.All["copilot"], "copilot", Options{Home: home, ACP: broken, Run: noRun(t)})
	if len(empty.Models) != 0 || empty.Error == "" {
		t.Fatalf("copilot %+v", empty)
	}
}

// ── Ported from test_picker.py (vision and features) ───────────────────────

func TestCodexVisionComesFromModalities(t *testing.T) {
	c := CodexCatalogFrom(decode(t, `{"data":[{"id":"image","inputModalities":["text","image"]},{"id":"text","inputModalities":["text"]},{"id":"unknown"}]}`))
	got := []any{}
	for _, m := range c.Models {
		if m.Vision == nil {
			got = append(got, nil)
		} else {
			got = append(got, *m.Vision)
		}
	}
	if !reflect.DeepEqual(got, []any{true, false, nil}) || c.Models[0].VisionSource != "codex app-server" {
		t.Fatalf("vision %v", got)
	}
}

func TestACPAgentVisionIsKeptForModelsAndUnlistedDefault(t *testing.T) {
	for _, vision := range []any{true, false, nil} {
		home := t.TempDir()
		write(t, filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"), `{"model": "unlisted"}`)
		initialized := map[string]any{"agentCapabilities": map[string]any{"promptCapabilities": map[string]any{"image": vision}}}
		c := CatalogFor(agents.All["agy"], "agy", Options{Home: home, Run: noRun(t), ACP: func(agents.Spec, string, time.Duration) (Catalog, error) {
			return ACPCatalogFrom(decode(t, agySessionNew), "agy acp", initialized), nil
		}})
		for _, m := range c.Models {
			if (m.Vision == nil) != (vision == nil) || (m.Vision != nil && *m.Vision != vision) {
				t.Errorf("vision %v: model %s has %v", vision, m.ID, m.Vision)
			}
		}
		if c.Models[0].ID != "unlisted" {
			t.Errorf("unlisted default not first: %v", ids(c.Models))
		}
		wantSource := ""
		if vision != nil {
			wantSource = "agy acp"
		}
		if c.VisionSource != wantSource {
			t.Errorf("vision source %q", c.VisionSource)
		}
	}
	if ACPCatalogFrom(decode(t, agySessionNew), "agy acp", nil).Vision != nil {
		t.Error("no initialize result: vision unknown")
	}
}

func TestCuratedVisionRules(t *testing.T) {
	cases := []struct {
		agent, model string
		vision       *bool
	}{
		{"claude", "sonnet", ptr(true)},
		{"hermes", "anthropic/claude-sonnet", ptr(true)},
		{"hermes", "gpt-oss-120b", ptr(false)},
		{"agy", "unknown", nil},
	}
	for _, c := range cases {
		f := Features(agents.All[c.agent], Choice{ID: c.model})[2]
		if !reflect.DeepEqual(f.Available, c.vision) {
			t.Errorf("%s/%s: %v", c.agent, c.model, f.Available)
		}
		want := ""
		if c.vision != nil {
			want = "from docs"
		}
		if f.Source != want {
			t.Errorf("%s/%s: source %q", c.agent, c.model, f.Source)
		}
	}
	for _, m := range ClaudeCatalogFrom(claudeHelp).Models {
		if v := Features(agents.All["claude"], m)[2].Available; v == nil || !*v {
			t.Errorf("claude alias %s without vision", m.ID)
		}
	}
	// A documented text-only model beats the agent's image capability.
	acp := Choice{ID: "gpt-oss-120b", Vision: ptr(true), VisionSource: "hermes acp"}
	if v := Features(agents.All["hermes"], acp)[2]; v.Available == nil || *v.Available || v.Source != "from docs" {
		t.Errorf("gpt-oss over ACP: %+v", v)
	}
}

func TestFeaturesMatchMeetingToolSupport(t *testing.T) {
	for name, agent := range agents.All {
		found := map[string]Feature{}
		for _, f := range Features(agent, Choice{ID: "x"}) {
			found[f.Name] = f
		}
		if tools := *found["tools"].Available; tools != (name != "openclaw") {
			t.Errorf("%s tools %v", name, tools)
		}
		if !*found["talk"].Available || !*found["local"].Available {
			t.Errorf("%s talk/local", name)
		}
	}
	custom, err := agents.Custom("mytool")
	if err != nil {
		t.Fatal(err)
	}
	fs := Features(custom, Choice{ID: "x"})
	if *fs[1].Available || fs[3].Available != nil {
		t.Errorf("custom: tools %v local %v", *fs[1].Available, fs[3].Available)
	}
	explicit := Choice{ID: "claude-test", Vision: ptr(false), VisionSource: "test CLI"}
	if v := Features(agents.All["claude"], explicit)[2].Available; *v {
		t.Error("a CLI's own per-model answer wins")
	}
}

// ── The real JSON-RPC session, against this test binary as a stand-in CLI ──

// TestHelperRPC is not a test: it is the stand-in CLI that rpcSession talks to.
func TestHelperRPC(t *testing.T) {
	mode := os.Getenv("MODELS_HELPER_RPC")
	if mode == "" {
		t.Skip("helper process")
	}
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		var msg map[string]any
		if dec.Decode(&msg) != nil {
			os.Exit(0)
		}
		id, hasID := msg["id"]
		if !hasID {
			continue
		}
		switch msg["method"] {
		case "initialize":
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"agentCapabilities": map[string]any{"promptCapabilities": map[string]any{"image": true}}}})
		case "session/new":
			if mode == "error" {
				_ = enc.Encode(map[string]any{"id": id, "error": map[string]any{"code": -32602, "message": "not signed in"}})
				continue
			}
			if mode == "silent" {
				continue
			}
			_ = enc.Encode(map[string]any{"method": "session/update", "params": map[string]any{}}) // a call, not a reply
			var created map[string]any
			_ = json.Unmarshal([]byte(agySessionNew), &created)
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": created})
		case "model/list":
			var listed map[string]any
			_ = json.Unmarshal([]byte(codexModelList), &listed)
			_ = enc.Encode(map[string]any{"id": id, "result": listed})
		}
	}
}

func helperAgent(t *testing.T, mode string) agents.Spec {
	t.Setenv("MODELS_HELPER_RPC", mode)
	spec := agents.All["agy"]
	spec.ACPArgs = []string{"-test.run=^TestHelperRPC$"}
	return spec
}

func TestACPCatalogOverStdio(t *testing.T) {
	c, err := ACPCatalog(helperAgent(t, "ok"), os.Args[0], 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Models) != 3 || c.Vision == nil || !*c.Vision || c.DefaultEffort != "high" {
		t.Fatalf("catalog %+v", c)
	}
	if _, err := ACPCatalog(helperAgent(t, "error"), os.Args[0], 10*time.Second); err == nil || err.Error() != "not signed in" {
		t.Fatalf("error reply: %v", err)
	}
	start := time.Now()
	if _, err := ACPCatalog(helperAgent(t, "silent"), os.Args[0], 500*time.Millisecond); err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("timeout: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the timeout was not kept")
	}
}

func TestCodexCatalogOverStdio(t *testing.T) {
	t.Setenv("MODELS_HELPER_RPC", "ok")
	// "codex app-server" becomes "<test binary> app-server": the helper ignores the extra argument.
	c, err := rpcSession([]string{os.Args[0], "-test.run=^TestHelperRPC$"}, []rpcRequest{
		{"initialize", map[string]any{}}, {"initialized", map[string]any{}}, {"model/list", map[string]any{}},
	}, 10*time.Second, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(CodexCatalogFrom(c[2]).Models); !reflect.DeepEqual(got, []string{"gpt-6.1-sol", "gpt-6-luna"}) {
		t.Fatalf("models %v", got)
	}
}
