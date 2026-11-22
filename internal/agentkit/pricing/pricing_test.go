package pricing

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/meetstream-ai/cli/internal/agentkit/usage"
)

// Ported from test_pricing.py::test_models_match_the_bundled_table.
func TestModelsMatchTheBundledTable(t *testing.T) {
	t.Setenv("MEETSTREAM_AGENT_KIT_HOME", t.TempDir()) // no cached shared file: the embedded table
	prices := Load()
	cases := []struct{ model, match string }{
		{"gemini-3.8-flash-high", "gemini-3-8-flash"}, // Antigravity's effort suffix
		{"gemini-3.8-flash-lite", ""},                 // a different model, not "flash" with a suffix
		{"claude-opus-4-6-thinking", "claude-opus-4-6"},
		{"claude-opus-4-1", "claude-opus-4-1"}, // the longest match, not claude-opus-4
		{"claude-sonnet-4-6[1m]", "claude-sonnet-4-6"},
		{"nous:anthropic/claude-sonnet-4.6", "claude-sonnet-4-6"}, // Hermes provider prefixes
		{"sonnet", "sonnet"},                                      // Claude Code's alias
		{"gpt-6.2-astra", "gpt-6-2-astra"},
		{"gpt-6-astra", "gpt-6-astra"},
		{"gpt-oss-120b-medium", ""},
		{"upstage/solar-mini4:free", ""},
		{"openai-codex:gpt-6.2-astra", "gpt-6-2-astra"}, // Hermes' provider:model
		{"opencode-go:claude-sonnet-4.6:thinking", "claude-sonnet-4-6"},
		{"", ""}, // the CLI's default
	}
	for _, c := range cases {
		got := ""
		if p := PriceFor(c.model, prices); p != nil {
			got = p.Match
		}
		if got != c.match {
			t.Errorf("PriceFor(%q) = %q, want %q", c.model, got, c.match)
		}
	}
}

// Ported from test_pricing.py::test_the_bundled_table_is_dated_and_covers_three_vendors.
func TestTheBundledTableIsDatedAndCoversThreeVendors(t *testing.T) {
	t.Setenv("MEETSTREAM_AGENT_KIT_HOME", t.TempDir())
	prices := Load()
	vendors, dates := map[string]bool{}, map[string]bool{}
	for _, p := range prices {
		vendors[p.Vendor], dates[p.AsOf] = true, true
	}
	if len(vendors) != 3 || !vendors["Anthropic"] || !vendors["Google"] || !vendors["OpenAI"] {
		t.Errorf("vendors %v", vendors)
	}
	if len(dates) != 1 || !dates["2026-11-21"] {
		t.Errorf("dates %v", dates)
	}
	flash := PriceFor("gemini-3.8-flash", prices)
	if flash == nil || flash.Input != 0.75 || flash.Cached != 0.075 || flash.Output != 3.75 {
		t.Errorf("flash %+v", flash)
	}
}

// Ported from test_pricing.py::test_an_hour_uses_the_measured_cached_share.
func TestAnHourUsesTheMeasuredCachedShare(t *testing.T) {
	price := Price{Match: "m", Vendor: "V", Input: 1.0, Cached: 0.1, Output: 10.0, AsOf: "2026-11-21"}
	hour := usage.HourEstimate{Input: 1_000_000, Output: 100_000}
	if got := HourCost(hour, usage.Usage{Input: 1000, Output: 10}, price); math.Abs(got-2.0) > 1e-9 {
		t.Errorf("no cache: %v", got)
	}
	if got := HourCost(hour, usage.Usage{Input: 1000, Cached: 500, Output: 10}, price); math.Abs(got-(0.5+0.05+1.0)) > 1e-9 {
		t.Errorf("half cached: %v", got)
	}
}

// Ported from test_pricing.py::test_a_newer_shared_file_overrides_the_bundled_prices.
func TestANewerSharedFileOverridesTheBundledPrices(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MEETSTREAM_AGENT_KIT_HOME", home)
	doc := BundledDocument()
	doc["prices"] = map[string]any{
		"as_of":  "2026-12-01",
		"models": []any{map[string]any{"match": "gemini-3.8-flash", "vendor": "Google", "input": 1.5, "cached": 0.15, "output": 7.5}},
	}
	data, _ := json.Marshal(doc)
	if err := os.WriteFile(filepath.Join(home, CacheFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
	found := PriceFor("gemini-3.8-flash-low", Load())
	if found == nil || found.Input != 1.5 || found.AsOf != "2026-12-01" {
		t.Fatalf("got %+v", found)
	}
}

// Ported from test_pricing.py::test_odd_price_rows_are_skipped.
func TestOddPriceRowsAreSkipped(t *testing.T) {
	var doc any
	_ = json.Unmarshal([]byte(`{"prices":{"as_of":"2026-11-21","models":[
		{"match":"ok","input":1,"cached":0,"output":2},
		{"match":"negative","input":-1,"cached":0,"output":2},
		{"match":"text","input":"1","cached":0,"output":2},
		{"match":"","input":1,"cached":0,"output":2},
		{"match":"bool","input":true,"cached":0,"output":2},
		"not a row"]}}`), &doc)
	got := Table(doc)
	if len(got) != 1 || got[0].Match != "ok" {
		t.Errorf("got %+v", got)
	}
	var undated any
	_ = json.Unmarshal([]byte(`{"prices":{"models":[]}}`), &undated)
	if Table(undated) != nil {
		t.Error("no date: not trusted")
	}
	if Table(nil) != nil || Table("x") != nil {
		t.Error("non-documents")
	}
}

// Ported from test_pricing.py::test_plan_detection_reads_sign_in_files_only.
func TestPlanDetectionReadsSignInFilesOnly(t *testing.T) {
	home := filepath.Join(t.TempDir(), "user")
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	none := func(string) string { return "" }
	opts := PlanOptions{Getenv: none, Home: home}
	write := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(home, rel), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	check := func(agent, model string, o PlanOptions, want string) {
		t.Helper()
		if got := PlanKind(agent, model, o); got != want {
			t.Errorf("PlanKind(%s, %s) = %q, want %q", agent, model, got, want)
		}
	}
	check("agy", "gemini-3.8-flash", opts, Plan)
	check("copilot", "auto", opts, Plan)
	check("hermes", "upstage/solar-mini4:free", opts, Free)
	check("hermes", "nous:anthropic/claude-sonnet-4.6", opts, "")
	check("claude", "sonnet", opts, "") // not signed in
	write(".claude.json", `{"oauthAccount": {"billingType": "x"}}`)
	check("claude", "sonnet", opts, Plan)
	withKey := PlanOptions{Getenv: func(k string) string {
		if k == "ANTHROPIC_API_KEY" {
			return "set"
		}
		return ""
	}, Home: home}
	check("claude", "sonnet", withKey, API)
	write(".codex/auth.json", `{"OPENAI_API_KEY": null, "tokens": {}}`)
	check("codex", "", opts, Plan)
	write(".codex/auth.json", `{"OPENAI_API_KEY": "set"}`)
	check("codex", "", opts, API)
}

// Home defaults to os.UserHomeDir, so HOME in a test redirects it.
func TestPlanKindDefaultsToUserHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("ANTHROPIC_API_KEY", "")
	if got := PlanKind("claude", "sonnet", PlanOptions{}); got != "" {
		t.Fatalf("empty home: %q", got)
	}
	_ = os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"oauthAccount":{}}`), 0o600)
	if got := PlanKind("claude", "sonnet", PlanOptions{}); got != Plan {
		t.Fatalf("signed in: %q", got)
	}
}

func TestMoney(t *testing.T) {
	for v, want := range map[float64]string{0: "$0.00", 0.004: "<$0.01", 0.449: "$0.45", 12.3: "$12.30"} {
		if got := Money(v); got != want {
			t.Errorf("Money(%v) = %q, want %q", v, got, want)
		}
	}
}
