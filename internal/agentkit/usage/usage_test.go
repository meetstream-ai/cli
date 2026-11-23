package usage

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func f(v float64) *float64 { return &v }

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

const claudeResult = `{"type":"result","usage":{"input_tokens":10,"cache_creation_input_tokens":36964,"cache_read_input_tokens":100,"output_tokens":33,"output_tokens_details":{"thinking_tokens":26}},"total_cost_usd":0.0741}`

// Ported from test_models.py::test_usage_in_each_clis_own_words.
func TestUsageInEachCLIsOwnWords(t *testing.T) {
	cases := []struct {
		name, event string
		want        Usage
	}{
		{"agy", `{"event":"result","result":{"status":"SUCCESS","usage":{"input_tokens":9568,"output_tokens":27,"thinking_tokens":26,"cache_read_tokens":0}}}`, Usage{9568, 0, 27, 26, nil}},
		{"claude", claudeResult, Usage{37074, 100, 33, 26, f(0.0741)}},
		{"codex", `{"type":"turn.completed","usage":{"input_tokens":16555,"cached_input_tokens":13056,"output_tokens":5,"reasoning_output_tokens":0}}`, Usage{16555, 13056, 5, 0, nil}},
		{"hermes", `{"estimated_cost_usd":0.0,"input_tokens":14989,"output_tokens":21,"reasoning_tokens":18,"cache_read_tokens":0}`, Usage{14989, 0, 21, 18, f(0)}},
	}
	for _, c := range cases {
		got := From(decode(t, c.event))
		if got == nil || !reflect.DeepEqual(*got, c.want) {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
	lines := []string{
		"not json",
		`{"type":"assistant","usage":{"input_tokens":1,"output_tokens":1}}`,
		claudeResult,
	}
	last, claude := FromLines(lines), From(decode(t, claudeResult))
	if !reflect.DeepEqual(last, claude) {
		t.Errorf("FromLines kept %+v, want the final result %+v", last, claude)
	}
}

// Copilot prints no usage: nothing is found, and that is not an error.
func TestNoUsageIsNilNotAFailure(t *testing.T) {
	if u := FromLines([]string{"Hello from Copilot", `{"type":"message","text":"hi"}`}); u != nil {
		t.Fatalf("got %+v", u)
	}
	if From(decode(t, `{"usage":{"output_tokens":3}}`)) != nil {
		t.Fatal("a block without input_tokens is not usage")
	}
	if ReportsUsage("copilot") || !ReportsUsage("claude") || !ReportsUsage("codex") || !ReportsUsage("agy") || !ReportsUsage("hermes") {
		t.Fatal("ReportsUsage does not match the kit (claude, codex, agy, hermes)")
	}
	if UsageFileFlag["hermes"] != "--usage-file" {
		t.Fatal("hermes usage file flag")
	}
}

// Ported from test_models.py::test_one_hour_projection_states_its_arithmetic.
func TestOneHourProjectionStatesItsArithmetic(t *testing.T) {
	turn := Usage{Input: 10_000, Output: 50, CostUSD: f(0.02)}
	hour := ProjectHour(turn, &Assumptions{TurnsPerHour: 20, MeetingTokensPerTurn: 120})
	if hour.Input != 20*10_000+170*20*19/2 {
		t.Errorf("input %d", hour.Input)
	}
	if hour.Output != 20*50 {
		t.Errorf("output %d", hour.Output)
	}
	want := 0.02 * float64(hour.Total()) / float64(turn.Total())
	if hour.CostUSD == nil || math.Abs(*hour.CostUSD-want) > 1e-12 {
		t.Errorf("cost %v, want %v", hour.CostUSD, want)
	}
	if ProjectHour(Usage{Input: 100, Output: 10}, nil).CostUSD != nil {
		t.Error("no CLI price: no hour price")
	}
	if got := DefaultAssumptions().Describe(); got == "" {
		t.Error("describe")
	}
}

// Ported from the bar asserts in test_models.py::test_chart_bars_scale_and_say_what_is_estimated.
func TestBarAndHumanTokens(t *testing.T) {
	if Bar(1, 8, 1) != "▏" || Bar(8, 8, 2) != "██" || Bar(0, 8, 4) != "" {
		t.Errorf("bars: %q %q %q", Bar(1, 8, 1), Bar(8, 8, 2), Bar(0, 8, 4))
	}
	for n, want := range map[float64]string{950: "950", 9800: "9.8k", 223_000: "223k", 1_250_000: "1.2M"} {
		if got := HumanTokens(n); got != want {
			t.Errorf("HumanTokens(%v) = %q, want %q", n, got, want)
		}
	}
}
