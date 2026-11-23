package estimates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"github.com/meetstream-ai/cli/internal/agentkit/models"
	"github.com/meetstream-ai/cli/internal/agentkit/pricing"
	"github.com/meetstream-ai/cli/internal/agentkit/usage"
)

// isolate gives the test an empty kit home and user home (plan detection reads ~/.claude.json).
func isolate(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	kit := filepath.Join(root, "home")
	user := filepath.Join(root, "user")
	_ = os.MkdirAll(user, 0o700)
	t.Setenv("MEETSTREAM_AGENT_KIT_HOME", kit)
	t.Setenv("HOME", user)
	t.Setenv("USERPROFILE", user)
	t.Setenv("ANTHROPIC_API_KEY", "")
	return kit
}

func f(v float64) *float64 { return &v }

func offline(context.Context, string) ([]byte, error) {
	return nil, errors.New("tests never reach the network")
}

func entry(model string, output int64) Entry {
	return NewEntry("agy", model, usage.Usage{Input: 9800, Output: output}, f(3800), "2026-11-18T21:28:33+00:00")
}

func doc(models ...string) []byte {
	var es []Entry
	for _, m := range models {
		es = append(es, entry(m, 20))
	}
	data, _ := json.Marshal(Document(es, nil))
	return data
}

func modelsOf(es []Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Model)
	}
	return out
}

// ── Ported from test_shared_estimates.py ───────────────────────────────────

func TestExportWritesAndMerges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model-estimates.json")
	if n, err := Export(path, []Entry{entry("a", 20), entry("b", 20)}); err != nil || n != 2 {
		t.Fatalf("first export: %d %v", n, err)
	}
	if n, err := Export(path, []Entry{entry("b", 999), entry("c", 20)}); err != nil || n != 3 {
		t.Fatalf("merge: %d %v", n, err)
	}
	var data map[string]any
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if data["schema"] != float64(1) || data["assumptions"].(map[string]any)["turns_per_hour"] != float64(20) || data["kit_version"] == "" {
		t.Fatalf("header %v", data)
	}
	byModel := map[string]map[string]any{}
	for _, e := range data["estimates"].([]any) {
		byModel[e.(map[string]any)["model"].(string)] = e.(map[string]any)
	}
	if byModel["b"]["turn"].(map[string]any)["output"] != float64(999) {
		t.Fatal("the newer measurement must replace the older one")
	}
	for _, k := range []string{"turn", "per_hour", "first_output_ms", "measured_at"} {
		if _, ok := byModel["a"][k]; !ok {
			t.Errorf("missing %s", k)
		}
	}
}

func TestOddEntriesAreSkipped(t *testing.T) {
	good := `{"agent":"agy","model":"ok","measured_at":"2026-11-18T21:28:33+00:00","turn":{"input":9800,"cached":0,"output":20,"thinking":0,"cost_usd":null},"first_output_ms":3800}`
	bad := []string{
		`{"agent":3,"model":"x","measured_at":"t","turn":{"input":1,"cached":0,"output":1,"thinking":0}}`,
		`{"agent":"agy","model":"x","measured_at":"t","turn":{"input":-1,"cached":0,"output":1,"thinking":0}}`,
		`{"agent":"agy","model":"x","measured_at":"t","turn":{"input":1,"cached":0,"output":1,"thinking":0,"cost_usd":"free"}}`,
		`{"agent":"agy","model":"x","measured_at":null,"turn":{"input":1,"cached":0,"output":1,"thinking":0}}`,
	}
	got := ValidEntries(decodeAny([]byte(`{"schema":1,"estimates":[` + good + "," + strings.Join(bad, ",") + `]}`)))
	if len(got) != 1 || got[0].Model != "ok" || got[0].FirstOutputMS == nil || *got[0].FirstOutputMS != 3800 {
		t.Fatalf("got %+v", got)
	}
	if ValidEntries(decodeAny([]byte(`{"schema":2,"estimates":[`+good+`]}`))) != nil {
		t.Fatal("another schema is not trusted")
	}
}

func TestLoadPrefersAFreshCacheThenTheURLThenAStaleCacheThenTheBundle(t *testing.T) {
	kit := t.TempDir()
	cache := filepath.Join(kit, CacheFile)
	var calls []string
	fetchOK := func(_ context.Context, url string) ([]byte, error) {
		calls = append(calls, url)
		return doc("from-url"), nil
	}
	opts := SharedOptions{Fetch: fetchOK, KitHome: kit}
	if got := modelsOf(LoadShared(opts)); len(got) != 1 || got[0] != "from-url" {
		t.Fatalf("url: %v", got)
	}
	if len(calls) != 1 || calls[0] != SharedURL {
		t.Fatalf("calls %v", calls)
	}
	if _, err := os.Stat(cache); err != nil {
		t.Fatal("not cached for a day")
	}
	if got := modelsOf(LoadShared(opts)); got[0] != "from-url" || len(calls) != 1 {
		t.Fatal("the fresh cache should have answered")
	}
	old := time.Now().Add(-CacheTTL - time.Minute)
	_ = os.Chtimes(cache, old, old)
	if got := modelsOf(LoadShared(SharedOptions{Fetch: offline, KitHome: kit})); len(got) != 1 || got[0] != "from-url" {
		t.Fatalf("stale: %v", got)
	}
	_ = os.Remove(cache)
	bundled := LoadShared(SharedOptions{Fetch: offline, KitHome: kit})
	have := map[string]bool{}
	for _, e := range bundled {
		have[e.Agent+"/"+e.Model] = true
	}
	if !have["agy/gemini-3.8-flash-low"] || !have["claude/sonnet"] {
		t.Fatalf("bundled %v", have)
	}
	// A garbage download is not cached and not trusted.
	garbage := func(context.Context, string) ([]byte, error) { return []byte(`{"schema":1,"estimates":"x"}`), nil }
	if got := LoadShared(SharedOptions{Fetch: garbage, KitHome: kit}); len(got) != len(bundled) {
		t.Fatal("garbage download")
	}
	if _, err := os.Stat(cache); err == nil {
		t.Fatal("garbage was cached")
	}
}

func TestPickerLabelsSharedEstimatesAndYourOwnRunsWin(t *testing.T) {
	isolate(t)
	shared := []Entry{entry("gemini-3.8-flash-low", 20), entry("gemini-3.8-flash-high", 1100)}
	if err := Save("agy", "gemini-3.8-flash-high", Measurement{Usage: &usage.Usage{Input: 9000, Output: 5}, Seconds: 4}); err != nil {
		t.Fatal(err)
	}
	found := LoadWithShared("agy", shared)
	if high := found["gemini-3.8-flash-high"]; high.Source != MeasuredHere || high.Usage.Output != 5 {
		t.Fatalf("high %+v", high)
	}
	low := found["gemini-3.8-flash-low"]
	if low.Source != "measured by MeetStream 2026-11-18" {
		t.Fatalf("low source %q", low.Source)
	}
	agy := agents.All["agy"]
	if d := DetailText(agy, models.Choice{ID: "gemini-3.8-flash-low"}, &low); !strings.Contains(d, "measured by MeetStream 2026-11-18") {
		t.Fatalf("detail %q", d)
	}
	row := RowFor(agy, models.Choice{ID: "gemini-3.8-flash-low"}, &low, 500_000)
	plain := row.Plain()
	if strings.Contains(plain, "MS 11-18") || !strings.Contains(plain, "≈$") || !strings.HasSuffix(plain, "cold") || !strings.HasPrefix(plain, "gemini-3.8-flash-low") {
		t.Fatalf("row %q", plain)
	}
	if row.Kind != pricing.Plan || !strings.Contains(plain, "plan") {
		t.Fatalf("agy is always on a plan: %q", plain)
	}
	unknown := DetailText(agy, models.Choice{ID: "brand-new"}, nil)
	if !strings.HasPrefix(unknown, "not measured yet") || !strings.Contains(unknown, "Measure live") {
		t.Fatalf("unknown %q", unknown)
	}
}

// ── Ported from test_pricing.py::test_export_keeps_the_price_table ─────────

func TestExportKeepsThePriceTable(t *testing.T) {
	out := filepath.Join(t.TempDir(), "model-estimates.json")
	if _, err := Export(out, nil); err != nil {
		t.Fatal(err)
	}
	if len(pricing.Table(readJSONf(t, out))) == 0 {
		t.Fatal("the embedded table must be carried over")
	}
	edited := readJSONf(t, out).(map[string]any)
	edited["prices"].(map[string]any)["as_of"] = "2026-12-01"
	data, _ := json.Marshal(edited)
	_ = os.WriteFile(out, data, 0o644)
	if _, err := Export(out, nil); err != nil {
		t.Fatal(err)
	}
	if got := readJSONf(t, out).(map[string]any)["prices"].(map[string]any)["as_of"]; got != "2026-12-01" {
		t.Fatalf("the refresh's edit must stay: %v", got)
	}
}

func readJSONf(t *testing.T, path string) any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// ── Ported from test_picker.py (local cache) ───────────────────────────────

func TestCacheRoundTripAndDefaultKey(t *testing.T) {
	kit := isolate(t)
	if len(Load("claude")) != 0 {
		t.Fatal("empty home")
	}
	m := Measurement{Usage: &usage.Usage{Input: 9800, Cached: 500, Output: 20, Thinking: 5, CostUSD: f(0.02)}, Seconds: 6, FirstOutputMS: f(4800)}
	if err := Save("claude", "", m); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(kit, UsageFile)
	// 0600 is a Unix mode; Windows reports 0666 for any writable file (its ACLs keep the profile private).
	if info, _ := os.Stat(path); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	raw := readJSONf(t, path).(map[string]any)["claude:"].(map[string]any)
	if raw["first_output_ms"] != float64(4800) || raw["input"] != float64(9800) {
		t.Fatalf("stored %v", raw)
	}
	loaded := Load("claude")[""]
	if loaded.Usage.Input != 9800 || *loaded.Usage.CostUSD != 0.02 || loaded.Seconds != 6 || loaded.MeasuredAt == "" {
		t.Fatalf("loaded %+v", loaded)
	}
	_ = os.Chmod(path, 0o644)
	_ = Save("hermes", "provider:model", m)
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode after resave %v", info.Mode().Perm())
	}
	if again := Load("claude")[""]; again.MeasuredAt != loaded.MeasuredAt {
		t.Fatal("another agent's save changed claude's entry")
	}
	if h := Load("hermes"); len(h) != 1 {
		t.Fatalf("hermes %v", h)
	} else if _, ok := h["provider:model"]; !ok {
		t.Fatalf("hermes %v", h)
	}
}

func TestCorruptCacheIsEmpty(t *testing.T) {
	for _, contents := range []string{"broken", "[]", `{"claude:x":null}`, `{"claude:x":{"input":"bad"}}`} {
		kit := isolate(t)
		_ = os.MkdirAll(kit, 0o700)
		_ = os.WriteFile(filepath.Join(kit, UsageFile), []byte(contents), 0o600)
		if len(Load("claude")) != 0 {
			t.Fatalf("%s: not empty", contents)
		}
		_ = Save("claude", "x", Measurement{Usage: &usage.Usage{Input: 20, Output: 1}, Seconds: 1})
		if _, ok := Load("claude")["x"]; !ok {
			t.Fatalf("%s: save did not recover", contents)
		}
	}
}

func TestInvalidMeasurementIsIgnored(t *testing.T) {
	cases := map[string]string{
		"seconds":         "-1",
		"first_output_ms": `"NaN"`,
		"cost_usd":        `"bad"`,
		"input":           "true",
		"thinking":        "-1",
		"measured_at":     `"not a date"`,
	}
	for field, value := range cases {
		kit := isolate(t)
		_ = Save("claude", "x", Measurement{Usage: &usage.Usage{Input: 20, Output: 1}, Seconds: 1})
		path := filepath.Join(kit, UsageFile)
		data := readJSONf(t, path).(map[string]any)
		var v any
		_ = json.Unmarshal([]byte(value), &v)
		data["claude:x"].(map[string]any)[field] = v
		body, _ := json.Marshal(data)
		_ = os.WriteFile(path, body, 0o600)
		if len(Load("claude")) != 0 {
			t.Errorf("%s=%s was accepted", field, value)
		}
	}
	// A non-integer count (1.5) is not a token count either.
	kit := isolate(t)
	_ = os.MkdirAll(kit, 0o700)
	_ = os.WriteFile(filepath.Join(kit, UsageFile), []byte(`{"claude:x":{"input":1.5,"cached":0,"output":1,"thinking":0,"seconds":1,"measured_at":"2026-11-18T00:00:00+00:00"}}`), 0o600)
	if len(Load("claude")) != 0 {
		t.Error("1.5 tokens accepted")
	}
}

func TestCacheWriteFailureDoesNotInterruptSetup(t *testing.T) {
	kit := isolate(t)
	_ = os.WriteFile(kit, []byte("not a directory"), 0o600)
	if err := Save("claude", "x", Measurement{Usage: &usage.Usage{Input: 20}, Seconds: 1}); err == nil {
		t.Fatal("expected an error to report (callers may ignore it)")
	}
	if len(Load("claude")) != 0 {
		t.Fatal("nothing should load")
	}
}

func TestFailedMeasurementDoesNotReplaceGoodCache(t *testing.T) {
	isolate(t)
	_ = Save("claude", "x", Measurement{Usage: &usage.Usage{Input: 100}, Seconds: 1})
	_ = Save("claude", "x", Measurement{Usage: &usage.Usage{Input: 9000}, Seconds: 1, Error: "failed"})
	_ = Save("claude", "x", Measurement{Seconds: 1}) // answered without usage (Copilot-style)
	if got := Load("claude")["x"].Usage.Input; got != 100 {
		t.Fatalf("input %d", got)
	}
}

// Parallel saves used to lose entries: every one must survive.
func TestParallelSavesKeepEveryEntry(t *testing.T) {
	isolate(t)
	const n = 40
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := Save("agy", fmt.Sprintf("model-%02d", i), Measurement{Usage: &usage.Usage{Input: int64(1000 + i), Output: 10}, Seconds: 1}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	got := Load("agy")
	if len(got) != n {
		t.Fatalf("%d of %d entries survived", len(got), n)
	}
	for i := 0; i < n; i++ {
		if got[fmt.Sprintf("model-%02d", i)].Usage.Input != int64(1000+i) {
			t.Fatalf("entry %d wrong", i)
		}
	}
	matches, _ := filepath.Glob(filepath.Join(os.Getenv("MEETSTREAM_AGENT_KIT_HOME"), ".*"))
	if len(matches) != 0 {
		t.Fatalf("temp files left behind: %v", matches)
	}
}

// ── Ported from test_picker.py (rows and details) ──────────────────────────

func TestRowsAndDetailsShowRealUsageAndFirstWords(t *testing.T) {
	isolate(t)
	claude := agents.All["claude"]
	sonnet := models.Choice{ID: "sonnet"}
	for _, cost := range []*float64{nil, f(0), f(0.02)} {
		e := Estimate{Usage: usage.Usage{Input: 9800, Output: 20, CostUSD: cost}, Seconds: 20, FirstOutputMS: f(4700), MeasuredAt: "2026-11-18T00:00:00+00:00", Source: MeasuredHere}
		hour := usage.ProjectHour(e.Usage, nil)
		row := RowFor(claude, sonnet, &e, hour.Total())
		plain, detail := row.Plain(), DetailText(claude, sonnet, &e)
		if row.Bar != "██████████" || !strings.Contains(plain, "223k/h") || row.FirstWords != "~5 s cold" || row.TokensPerHour != hour.Total() {
			t.Fatalf("row %q", plain)
		}
		if cost == nil { // no price from the CLI: the dated table's Anthropic price for "sonnet"
			if !strings.Contains(plain, "223k/h ≈$0.45") || !strings.Contains(detail, "≈ $0.45/h at API rates (estimate; Anthropic list prices, 2026-11-21)") {
				t.Fatalf("table price: %q / %q", plain, detail)
			}
		} else { // the CLI's own price wins
			if !strings.Contains(plain, "≈"+pricing.Money(*hour.CostUSD)) || !strings.Contains(detail, "Claude Code's own estimate") {
				t.Fatalf("CLI price: %q / %q", plain, detail)
			}
		}
		noCLIPrice := e
		noCLIPrice.Usage.CostUSD = nil
		unpriced := RowFor(claude, models.Choice{ID: "some-new-model"}, &noCLIPrice, hour.Total())
		if !strings.Contains(unpriced.Plain(), "223k/h —") || unpriced.Cost != nil {
			t.Fatalf("unpriced %q", unpriced.Plain())
		}
		if !strings.Contains(DetailText(claude, models.Choice{ID: "some-new-model"}, &noCLIPrice), "API price unknown") {
			t.Fatal("API price unknown")
		}
		if !strings.Contains(detail, "first words ~5 s (cold)") || !strings.Contains(detail, "vision (from docs), local actions per access level") {
			t.Fatalf("detail %q", detail)
		}
		noFirst := e
		noFirst.FirstOutputMS = nil
		if !strings.Contains(DetailText(claude, sonnet, &noFirst), "first words —") {
			t.Fatal("first words —")
		}
	}
	empty := RowFor(agents.All["openclaw"], models.Choice{ID: "unknown"}, nil, 0).Plain()
	if !strings.Contains(empty, "[          ] usage n/a") || !strings.Contains(empty, "no-tools vision?") || !strings.Contains(empty, "—") {
		t.Fatalf("openclaw %q", empty) // OpenClaw does not report token usage
	}
	if !strings.Contains(RowFor(agents.All["agy"], models.Choice{ID: "unknown"}, nil, 0).Plain(), "[          ] not measured") {
		t.Fatal("agy not measured")
	}
	if !strings.Contains(DetailText(claude, sonnet, nil), `pick "Measure live"`) {
		t.Fatal("measure live hint")
	}
	if got := RowFor(claude, models.Choice{}, nil, 0).Model; got != "The CLI's default" {
		t.Fatalf("default row %q", got)
	}
}

// Copilot reports no usage: "usage n/a", the plan tag, and no Measure live hint.
func TestCopilotRowsSayUsageNotAvailable(t *testing.T) {
	isolate(t)
	copilot := agents.All["copilot"]
	row := RowFor(copilot, models.Choice{ID: "auto"}, nil, 0)
	if row.Usage != "usage n/a" || row.Kind != pricing.Plan {
		t.Fatalf("row %+v", row)
	}
	d := DetailText(copilot, models.Choice{ID: "auto"}, nil)
	if !strings.HasPrefix(d, "usage not available: GitHub Copilot CLI doesn't report token usage") || strings.Contains(d, "Measure live") || !strings.Contains(d, "on your plan") {
		t.Fatalf("detail %q", d)
	}
}

func TestFreeModelsSayFree(t *testing.T) {
	isolate(t)
	e := Estimate{Usage: usage.Usage{Input: 9800, Output: 20}, Source: MeasuredHere}
	row := RowFor(agents.All["hermes"], models.Choice{ID: "upstage/solar-mini4:free"}, &e, 0)
	if row.CostText != "free" || row.Kind != pricing.Free || strings.Contains(row.Plain(), "free free") {
		t.Fatalf("row %q", row.Plain())
	}
	if !strings.Contains(DetailText(agents.All["hermes"], models.Choice{ID: "upstage/solar-mini4:free"}, &e), "free model") {
		t.Fatal("detail")
	}
}

func TestHourCostPrefersTheCLIsOwnPrice(t *testing.T) {
	isolate(t)
	if c, s := HourCost(agents.All["claude"], "sonnet", nil); c != nil || s != "" {
		t.Fatal("no turn: no cost")
	}
	turn := usage.Usage{Input: 1000, Output: 10, CostUSD: f(0.01)}
	c, s := HourCost(agents.All["claude"], "sonnet", &turn)
	want := 0.01 * float64(usage.ProjectHour(turn, nil).Total()) / float64(turn.Total())
	if c == nil || math.Abs(*c-want) > 1e-12 || s != "Claude Code's own estimate" {
		t.Fatalf("%v %q", c, s)
	}
}
