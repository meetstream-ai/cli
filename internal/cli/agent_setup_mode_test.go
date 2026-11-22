package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/meetstream-ai/cli/internal/ui"
)

// demoCatalog is a made-up `mia_managed` for tests and screenshots: the shape
// of GET /mia, not live prices.
const demoCatalog = `{
  "enabled": true,
  "rates": {"base_price": 0.35, "agent": 0.6, "managed_agent": 0.75},
  "sections": {
    "transcriber": {"default_provider": "openai", "providers": {
      "openai": {"label": "OpenAI via MeetStream", "default_model": "gpt-4o-mini-transcribe", "models": {"gpt-4o-mini-transcribe": 0.2, "whisper-1": 0.4}},
      "elevenlabs": {"label": "ElevenLabs via MeetStream", "default_model": "scribe_v2_realtime", "models": {"scribe_v2_realtime": 0.45}}}},
    "model": {"default_provider": "openai", "providers": {
      "openai": {"label": "OpenAI via MeetStream", "default_model": "gpt-4o-mini", "models": {"gpt-4o-mini": 0.05, "gpt-4.1-mini": 0.1}}}},
    "voice": {"default_provider": "elevenlabs", "providers": {
      "elevenlabs": {"label": "ElevenLabs via MeetStream", "default_model": "eleven_turbo_v2_5", "models": {"eleven_turbo_v2_5": 0.25, "eleven_flash_v2_5": 0.25}}}}
  },
  "screen_vision": {}
}`

// disabledCatalog is a server whose MeetStream keys are off: rates only.
const disabledCatalog = `{"enabled": false, "rates": {"base_price": 0.35, "agent": 0.6, "managed_agent": 0.6}, "sections": {}}`

func catalogFrom(t *testing.T, raw string) miaCatalog {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	return parseCatalog(v)
}

// Price math: Meeting + MIA harness + each section on a MeetStream key, from
// the user's rates; your own keys add "your provider charges".
func TestModePrices(t *testing.T) {
	c := catalogFrom(t, demoCatalog)
	hosted := quoteFor(c, c.managedPicks(), pipelineSections)
	if got := hosted.breakdown(); got != "Meeting $0.35 + MIA harness $0.40 + ears $0.20 + brain $0.05 + voice $0.25 ≈ $1.25/h" {
		t.Fatalf("hosted: %q", got)
	}
	if hosted.total() != "≈$1.25/h" {
		t.Fatal(hosted.total())
	}
	byok := quoteFor(c, c.ownPicks(), pipelineSections)
	if byok.breakdown() != "Meeting $0.35 + MIA harness $0.25 + your provider charges" || byok.total() != "$0.60/h + your providers" {
		t.Fatalf("byok: %q %q", byok.breakdown(), byok.total())
	}
	local := quoteFor(c, c.managedPicks(), []string{secEars, secVoice})
	if local.breakdown() != "Meeting $0.35 + MIA harness $0.40 + ears $0.20 + voice $0.25 ≈ $1.20/h" {
		t.Fatalf("local: %q", local.breakdown())
	}
	// A model other than the default is priced from the catalog.
	picks := c.managedPicks()
	picks[secEars] = sectionPick{Provider: "openai", Model: "whisper-1", Managed: true}
	if q := quoteFor(c, picks, pipelineSections); *q.Total != 1.45 {
		t.Fatalf("whisper-1: %v", *q.Total)
	}
	// A user with their own agent rate: the API sends it as managed_agent too.
	user := catalogFrom(t, strings.Replace(demoCatalog, `"agent": 0.6, "managed_agent": 0.75`, `"agent": 0.5, "managed_agent": 0.5`, 1))
	if q := quoteFor(user, user.managedPicks(), pipelineSections); q.breakdown() != "Meeting $0.35 + MIA harness $0.15 + ears $0.20 + brain $0.05 + voice $0.25 ≈ $1.00/h" {
		t.Fatalf("user rates: %q", q.breakdown())
	}
	// An older backend without managed_agent: the agent rate.
	old := catalogFrom(t, strings.Replace(demoCatalog, `, "managed_agent": 0.75`, ``, 1))
	if q := quoteFor(old, old.managedPicks(), pipelineSections); *q.Total != 1.1 {
		t.Fatalf("old backend: %v", *q.Total)
	}
	// Not signed in: no numbers at all.
	if q := quoteFor(miaCatalog{}, miaCatalog{}.managedPicks(), pipelineSections); q.Total != nil || q.total() != "price after sign-in" {
		t.Fatalf("unknown: %+v", q)
	}
}

// Step 1: hosted rows are dimmed and cannot be picked when the server offers no
// MeetStream keys; BYOK and your own agent (on your keys) stay available; all
// local is never available yet.
func TestModeMenuRows(t *testing.T) {
	on := modeMenu(catalogFrom(t, demoCatalog), modeLocal)
	if fmtBools(on.disabled) != "0001" || on.cursor != 2 || on.rows() != 4 {
		t.Fatalf("on: %v cursor %d rows %d", on.disabled, on.cursor, on.rows())
	}
	v := stripANSI(on.View())
	for _, want := range []string{modeQuestion, "MeetStream hosted", "≈$1.25/h", "MIA with your keys (BYOK)", "$0.60/h + your providers",
		"Your agent on your computer", "≈$1.20/h", "All local (preview, coming)", "not available yet", backLabel} {
		if want == backLabel {
			if strings.Contains(v, want) {
				t.Errorf("step 1 has no Back row:\n%s", v)
			}
			continue
		}
		if !strings.Contains(v, want) {
			t.Errorf("missing %q in\n%s", want, v)
		}
	}
	if d := on.detail(3); !strings.Contains(d, "Meeting fee only") || !strings.Contains(d, "performance isn't guaranteed") {
		t.Fatal(d)
	}
	if d := on.detail(2); !strings.Contains(d, "With your keys: Meeting $0.35 + MIA harness $0.25 + your provider charges") {
		t.Fatal(d)
	}

	off := modeMenu(catalogFrom(t, disabledCatalog), modeHosted)
	if fmtBools(off.disabled) != "1001" || off.hints[0] != notOffered || off.hints[2] != "$0.60/h + your providers" {
		t.Fatalf("off: %v %v", off.disabled, off.hints)
	}
	// Enter on a disabled row does nothing; on an available one it chooses.
	m, cmd := off.Update(key("enter"))
	if isQuit(cmd) || m.(stepMenuModel).result != menuOpen {
		t.Fatal("chose a disabled row")
	}
	final, _ := press(m, "down", "enter")
	if r := final.(stepMenuModel); r.result != menuChosen || r.chosen != 1 {
		t.Fatalf("%+v", r)
	}
	final, _ = press(off, "esc")
	if final.(stepMenuModel).result != menuBack {
		t.Fatal("esc at step 1 should leave (setup cancels)")
	}
}

func fmtBools(b []bool) string {
	s := ""
	for _, v := range b {
		s += map[bool]string{true: "1", false: "0"}[v]
	}
	return s
}

// fakeMIA serves GET /mia with a catalog and records POST /mia bodies.
type fakeMIA struct {
	mu      sync.Mutex
	catalog string
	created map[string]any
	reply   string
}

func (f *fakeMIA) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/mia"):
			_, _ = w.Write([]byte(`{"agent_configs":[],"count":0,"mia_managed":` + f.catalog + `}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/mia"):
			b, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			_ = json.Unmarshal(b, &f.created)
			f.mu.Unlock()
			reply := f.reply
			if reply == "" {
				reply = `{"agent_config_id":"cfg_test"}`
			}
			_, _ = w.Write([]byte(reply))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type dryRunOut struct {
	Mode   string                    `json:"mode"`
	Body   map[string]map[string]any `json:"-"`
	Raw    map[string]any            `json:"body"`
	Price  *float64                  `json:"price_per_hour"`
	Plus   bool                      `json:"plus_your_providers"`
	Detail []priceLine               `json:"breakdown"`
}

func dryRun(t *testing.T, env map[string]string, args ...string) (dryRunOut, int, string) {
	t.Helper()
	out, code := run(t, env, append([]string{"setup", "--dry-run", "--json", "--no-input"}, args...)...)
	var d dryRunOut
	if code == 0 {
		if err := json.Unmarshal([]byte(out), &d); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		d.Body = map[string]map[string]any{}
		for k, v := range d.Raw {
			if m, ok := v.(map[string]any); ok {
				d.Body[k] = m
			}
		}
	}
	return d, code, out
}

// Each mode's request body and price, from the catalog GET /mia sends.
func TestSetupDryRunBodyPerMode(t *testing.T) {
	f := &fakeMIA{catalog: demoCatalog}
	env := map[string]string{"MEETSTREAM_API_URL": f.server(t).URL, "MEETSTREAM_API_KEY": "ms_test_key"}

	d, code, out := dryRun(t, env, "--mode", "hosted")
	if code != 0 || d.Mode != "hosted" || d.Price == nil || *d.Price != 1.25 || len(d.Detail) != 5 || d.Detail[1].Label != "MIA harness" {
		t.Fatalf("hosted: %d %s", code, out)
	}
	for sec, want := range map[string]string{"transcriber": "openai/gpt-4o-mini-transcribe", "model": "openai/gpt-4o-mini", "voice": "elevenlabs/eleven_turbo_v2_5"} {
		b := d.Body[sec]
		if b["provider"].(string)+"/"+b["model"].(string) != want || b["key_source"] != "meetstream" {
			t.Errorf("hosted %s: %v", sec, b)
		}
	}
	if d.Body["voice"]["voice_id"] == "" || d.Body["model"]["system_prompt"] == "" {
		t.Fatalf("hosted needs a voice id and a prompt: %s", out)
	}

	d, code, out = dryRun(t, env, "--mode", "hosted", "--ears", "elevenlabs:scribe_v2_realtime")
	if code != 0 || *d.Price != 1.5 || d.Body["transcriber"]["provider"] != "elevenlabs" {
		t.Fatalf("hosted --ears: %s", out)
	}
	if _, code, _ := dryRun(t, env, "--mode", "hosted", "--ears", "deepgram:nova-3"); code != 2 {
		t.Fatalf("hosted with a model MeetStream keys do not offer: exit %d", code)
	}

	d, code, out = dryRun(t, env, "--mode", "byok", "--brain", "anthropic:claude-haiku-5-5")
	if code != 0 || *d.Price != 0.6 || !d.Plus || d.Detail[2].Label != "your provider charges" || d.Detail[2].PerHour != nil {
		t.Fatalf("byok: %s", out)
	}
	for _, sec := range pipelineSections {
		if _, ok := d.Body[sec]["key_source"]; ok {
			t.Errorf("byok %s has a key_source: %v", sec, d.Body[sec])
		}
	}
	if d.Body["model"]["provider"] != "anthropic" || d.Body["transcriber"]["provider"] != "openai" || d.Body["voice"]["provider"] != "elevenlabs" {
		t.Fatalf("byok defaults to the catalog's providers: %s", out)
	}

	relayCreds(t)
	t.Setenv("PATH", fakeBin(t, "claude"))
	d, code, out = dryRun(t, env, "--agent", "claude")
	if code != 0 || d.Mode != "local-agent" || *d.Price != 1.2 || d.Body["model"]["provider"] != "local_agent" || d.Body["voice"]["key_source"] != "meetstream" {
		t.Fatalf("local-agent: %s", out)
	}
	if strings.Contains(out, "deepgram") || strings.Contains(out, "coral") {
		t.Fatalf("hard-coded stack: %s", out)
	}
	d, code, out = dryRun(t, env, "--agent", "claude", "--voice-keys", "own")
	if code != 0 || *d.Price != 0.6 || !d.Plus || d.Body["voice"]["key_source"] != nil {
		t.Fatalf("local-agent own keys: %s", out)
	}
	for _, args := range [][]string{{"--agent", "claude", "--brain", "openai:gpt-4o-mini"}, {"--mode", "byok", "--voice-keys", "own"}, {"--mode", "nope"}} {
		if _, code, _ := dryRun(t, env, args...); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}

// A server with MeetStream keys off: hosted and MeetStream ears/voice stop with
// exit 3; your own keys still work.
func TestSetupWithCatalogOff(t *testing.T) {
	f := &fakeMIA{catalog: disabledCatalog}
	env := map[string]string{"MEETSTREAM_API_URL": f.server(t).URL, "MEETSTREAM_API_KEY": "ms_test_key"}
	if _, code, _ := dryRun(t, env, "--mode", "hosted"); code != 3 {
		t.Fatalf("hosted: exit %d", code)
	}
	d, code, out := dryRun(t, env, "--mode", "byok")
	if code != 0 || *d.Price != 0.6 || d.Body["voice"]["voice_id"] == nil {
		t.Fatalf("byok: %s", out)
	}
	relayCreds(t)
	t.Setenv("PATH", fakeBin(t, "claude"))
	if _, code, _ := dryRun(t, env, "--agent", "claude"); code != 3 {
		t.Fatalf("local-agent on MeetStream keys: exit %d", code)
	}
	if _, code, out := dryRun(t, env, "--agent", "claude", "--voice-keys", "own"); code != 0 {
		t.Fatalf("local-agent own keys: %s", out)
	}
}

// The API put sections the user has no key for on MeetStream keys: setup says
// which, and the price it bills at.
func TestSetupWarnsOnManagedKeyFallback(t *testing.T) {
	f := &fakeMIA{catalog: demoCatalog, reply: `{"agent_config_id":"cfg_byok","managed_key_sections":["transcriber","voice"],"mia_managed_rates":{"base_price":0.35,"agent":0.6,"managed_agent":0.75}}`}
	env := map[string]string{"MEETSTREAM_API_URL": f.server(t).URL, "MEETSTREAM_API_KEY": "ms_test_key"}
	out, code := run(t, env, "setup", "--mode", "byok", "--json", "--no-input")
	var got struct {
		ID       string   `json:"agent_config_id"`
		Sections []string `json:"managed_key_sections"`
		Price    float64  `json:"price_per_hour"`
		Plus     bool     `json:"plus_your_providers"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || got.ID != "cfg_byok" || len(got.Sections) != 2 || got.Price != 1.2 || !got.Plus {
		t.Fatalf("%d %s", code, out)
	}
	var human string
	human = withStderr(t, func() { _, code = run(t, env, "setup", "--mode", "byok", "--no-input") })
	for _, want := range []string{"No saved key for the ears and voice", "MeetStream keys instead", "Meeting $0.35 + MIA harness $0.40 + ears $0.20 + voice $0.25 + your provider charges", keysURL()} {
		if !strings.Contains(human, want) {
			t.Errorf("missing %q in\n%s", want, human)
		}
	}
}

// Back works on every step of the hosted and BYOK branches, back to step 1 too,
// and setup still creates the agent.
func TestEveryStepGoesBackInEveryMode(t *testing.T) {
	for _, tc := range []struct {
		mode  string
		asked []string
		check func(map[string]any) bool
	}{
		{"hosted", []string{"Name your MIA agent", "Create this MIA agent?"}, func(b map[string]any) bool {
			return b["voice"].(map[string]any)["key_source"] == "meetstream" && b["agent_name"] == "MIA"
		}},
		{"byok", []string{"Ears (speech to text)?", "Brain (LLM)?", "Voice (text to speech)?", "Name your MIA agent", "Create this MIA agent?"}, func(b map[string]any) bool {
			return b["voice"].(map[string]any)["key_source"] == nil && b["model"].(map[string]any)["provider"] == "openai"
		}},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			f := &fakeMIA{catalog: demoCatalog}
			env := map[string]string{"MEETSTREAM_API_URL": f.server(t).URL, "MEETSTREAM_API_KEY": "ms_test_key"}
			swap(t, &setupInteractive, func() bool { return true })
			drv := &backOnce{t: t, backed: map[string]bool{}}
			useDriver(t, drv.run)
			var code int
			out := withStderr(t, func() { _, code = run(t, env, "setup", "--mode", tc.mode, "--no-start") })
			if code != 0 {
				t.Fatalf("exit %d\n%s\nasked %v", code, out, drv.asked)
			}
			for _, q := range append(tc.asked, "Connect your calendar so MIA can join your meetings?") {
				if !drv.backed[q] {
					t.Errorf("Back never pressed at %q (asked %v)", q, drv.asked)
				}
			}
			if !contains(drv.asked, modeQuestion) {
				t.Errorf("Back from the first question did not return to step 1: %v", drv.asked)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.created == nil || !tc.check(f.created) {
				t.Fatalf("created %+v", f.created)
			}
			total := map[string]string{"hosted": "/3", "byok": "/4"}[tc.mode]
			if !strings.Contains(out, "2"+total) || !strings.Contains(out, "≈$") && tc.mode == "hosted" {
				t.Errorf("step counter %s or price missing:\n%s", total, out)
			}
		})
	}
}

// TestSetupModeView renders step 1 from the demo catalog;
// MEETSTREAM_DUMP_SETUP_MODE=<file> writes it in colour for screenshots.
func TestSetupModeView(t *testing.T) {
	if os.Getenv("MEETSTREAM_DUMP_SETUP_MODE") != "" {
		lipgloss.SetColorProfile(termenv.TrueColor)
	}
	out := captureUI(t)
	g = globals{}
	ui.Step(1, 7, "How MIA runs")
	view := out.String() + modeMenu(catalogFrom(t, demoCatalog), modeHosted).View()
	for _, want := range []string{modeQuestion, "≈$1.25/h", "Meeting $0.35 + MIA harness $0.40"} {
		if !strings.Contains(stripANSI(view), want) {
			t.Errorf("missing %q in\n%s", want, view)
		}
	}
	dumpView(t, "MEETSTREAM_DUMP_SETUP_MODE", view)
}
