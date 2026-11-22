package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	kitconfig "github.com/meetstream-ai/cli/internal/agentkit/config"
	"github.com/meetstream-ai/cli/internal/agentkit/models"
	"github.com/meetstream-ai/cli/internal/agentkit/warmcheck"
)

type benchJSON struct {
	Agent   string `json:"agent"`
	Effort  string `json:"effort"`
	Stopped int    `json:"stopped"`
	Results []struct {
		Model         string   `json:"model"`
		State         string   `json:"state"`
		InputTokens   *int     `json:"input_tokens"`
		OutputTokens  *int     `json:"output_tokens"`
		TokensPerHour *int     `json:"tokens_per_hour"`
		CostPerHour   *float64 `json:"cost_per_hour_usd"`
		Seconds       float64  `json:"seconds"`
		Error         string   `json:"error"`
	} `json:"results"`
	Export map[string]any `json:"export"`
}

// agent bench with a fake CLI: --json results, plain lines without a terminal,
// --export, and the effort passed to every run.
func TestAgentBenchWithFakeRunner(t *testing.T) {
	t.Setenv("MEETSTREAM_AGENT_KIT_HOME", t.TempDir())
	var seen []string
	swap(t, &benchExec, fakeClaudeExec(&seen))
	export := filepath.Join(t.TempDir(), "model-estimates.json")
	t.Setenv("PATH", fakeBin(t, "claude"))
	out, code := run(t, nil, "agent", "bench", "--agent", "claude", "--models", "opus, sonnet", "--effort", "low", "--export", export, "--json", "--no-input")
	var b benchJSON
	if code != 0 || json.Unmarshal([]byte(out), &b) != nil {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if b.Agent != "claude" || b.Effort != "low" || len(b.Results) != 2 || b.Export["written"] != float64(2) {
		t.Fatalf("%s", out)
	}
	for _, r := range b.Results {
		if r.State != "done" || r.InputTokens == nil || *r.InputTokens != 12000 || r.TokensPerHour == nil || *r.TokensPerHour == 0 || r.Error != "" {
			t.Fatalf("%+v", r)
		}
	}
	if len(seen) != 2 || !strings.Contains(strings.Join(seen, "\n"), "--model opus") || !strings.Contains(seen[0], "--effort low") {
		t.Fatalf("argv %v", seen)
	}
	if data, err := os.ReadFile(export); err != nil || !strings.Contains(string(data), `"model": "sonnet"`) {
		t.Fatalf("export: %v %s", err, data)
	}

	// Without --json (no terminal): one plain line per model, then the chart.
	var human string
	human = withStderr(t, func() { _, code = run(t, nil, "agent", "bench", "--agent", "claude", "--models", "opus", "--no-input") })
	if code != 0 || !strings.Contains(human, "Measuring 1 model(s), 1 at a time.") || !strings.Contains(human, "✓ opus") || !strings.Contains(human, "/h") {
		t.Fatalf("code=%d\n%s", code, human)
	}
}

// --compare measures the CLI's own list (default first); a run that fails is
// reported, and no usage at all exits 1.
func TestAgentBenchCompareAndFailures(t *testing.T) {
	t.Setenv("MEETSTREAM_AGENT_KIT_HOME", t.TempDir())
	t.Setenv("PATH", fakeBin(t, "claude", "copilot"))
	swap(t, &setupCatalog, catalogOf(models.Catalog{Models: []models.Choice{{ID: "a"}, {ID: "b"}, {ID: "c"}}, DefaultModel: "b", Source: "test"}))
	var seen []string
	swap(t, &benchExec, fakeClaudeExec(&seen))
	out, code := run(t, nil, "agent", "bench", "--agent", "claude", "--compare", "--max-models", "2", "--json", "--no-input")
	var b benchJSON
	if code != 0 || json.Unmarshal([]byte(out), &b) != nil || len(b.Results) != 2 || b.Results[0].Model != "b" || b.Results[1].Model != "a" {
		t.Fatalf("code=%d %s", code, out)
	}

	swap(t, &benchExec, func(context.Context, []string, string, string, []string, func(string)) (string, error) {
		return "", errors.New("agent CLI not found: claude")
	})
	out, code = run(t, nil, "agent", "bench", "--agent", "claude", "--models", "x", "--json", "--no-input")
	if code != 1 || !strings.Contains(out, `"state": "failed"`) || !strings.Contains(out, "agent CLI not found") {
		t.Fatalf("code=%d %s", code, out)
	}

	// Only offered for CLIs that report their token usage.
	if _, code := run(t, nil, "agent", "bench", "--agent", "copilot", "--json", "--no-input"); code != 2 {
		t.Fatalf("copilot: exit %d", code)
	}
}

type doctorJSON struct {
	OK      bool   `json:"ok"`
	Version string `json:"version"`
	Verdict string `json:"verdict"`
	Checks  []struct {
		Name   string `json:"name"`
		OK     bool   `json:"ok"`
		Status string `json:"status"`
		Detail string `json:"detail"`
		Fix    string `json:"fix"`
	} `json:"checks"`
}

func (d doctorJSON) check(name string) (ok bool, status string) {
	for _, c := range d.Checks {
		if c.Name == name {
			return c.OK, c.Status
		}
	}
	return false, "missing"
}

// doctor --json: the API key lines first, then the agent kit's checks; exit 1
// when one fails, 2 without a key.
func TestDoctorJSON(t *testing.T) {
	t.Setenv("MEETSTREAM_AGENT_KIT_HOME", t.TempDir())
	t.Setenv("MEETSTREAM_AGENT_KIT_CREDENTIALS", "file")
	swap(t, &doctorWarm, func(context.Context, kitconfig.Config) warmcheck.Result {
		t.Fatal("warm check started")
		return warmcheck.Result{}
	})
	t.Setenv("PATH", fakeBin(t, "claude"))
	key := map[string]string{"MEETSTREAM_API_KEY": "ms_test_key_1234"}
	out, code := run(t, key, "doctor", "--json", "--no-warm")
	var d doctorJSON
	if code != 0 || json.Unmarshal([]byte(out), &d) != nil || !d.OK || d.Verdict != "All good." || d.Version != "1.2.3" {
		t.Fatalf("code=%d %s", code, out)
	}
	if d.Checks[0].Name != "API key" || d.Checks[1].Name != "API URL" || strings.Contains(out, "gemini") || strings.Contains(out, "test_key") {
		t.Fatalf("%s", out)
	}
	for _, name := range []string{"config", "workdir", "agent", "login", "context"} {
		if ok, status := d.check(name); !ok || status != "ok" && status != "warn" {
			t.Errorf("%s: %v %s\n%s", name, ok, status, out)
		}
	}

	// A saved relay setup but this computer not connected: one problem, exit 1.
	if err := (kitconfig.Setup{Agent: "claude", Tunnel: "relay", MaxAccess: "readonly"}).Save(""); err != nil {
		t.Fatal(err)
	}
	out, code = run(t, key, "doctor", "--json", "--no-warm")
	d = doctorJSON{}
	if code != 1 || json.Unmarshal([]byte(out), &d) != nil || d.OK || d.Verdict != "1 problem(s) to fix, listed below." {
		t.Fatalf("code=%d %s", code, out)
	}
	if ok, status := d.check("relay"); ok || status != "fail" {
		t.Fatalf("relay: %s", out)
	}

	// No key: exit 2 as before.
	if _, code := run(t, nil, "doctor", "--json", "--no-warm"); code != 2 {
		t.Fatalf("no key: exit %d", code)
	}
}

// No agent CLI at all is a warning, not a failure: your own agent is optional.
func TestDoctorWithoutAgentCLI(t *testing.T) {
	t.Setenv("MEETSTREAM_AGENT_KIT_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	out, code := run(t, map[string]string{"MEETSTREAM_API_KEY": "ms_test_key_1234"}, "doctor", "--json")
	var d doctorJSON
	if code != 0 || json.Unmarshal([]byte(out), &d) != nil || len(d.Checks) != 3 {
		t.Fatalf("code=%d %s", code, out)
	}
	if ok, status := d.check("agent"); !ok || status != "warn" {
		t.Fatalf("%s", out)
	}
}
