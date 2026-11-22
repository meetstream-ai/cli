// Package usage reads the token usage the agent CLIs report and projects one
// measured turn to an hour of meeting. Ported from the kit's usage.py.
//
// Each CLI reports usage in its own words (read from real runs, 2026-11-18):
//
//   - Claude Code "result": input_tokens + cache_creation_input_tokens +
//     cache_read_input_tokens, output_tokens (thinking in output_tokens_details),
//     and total_cost_usd.
//   - Codex "turn.completed": input_tokens (cached included), cached_input_tokens,
//     output_tokens, reasoning_output_tokens.
//   - Antigravity "result.usage": input_tokens, output_tokens, thinking_tokens,
//     cache_read_tokens.
//   - Hermes --usage-file: input_tokens, output_tokens, reasoning_tokens,
//     cache_read_tokens, estimated_cost_usd.
//   - GitHub Copilot CLI, Muse and OpenClaw report none. That is not a failure:
//     the answer still counts, only the usage is unknown (see ReportsUsage).
//
// Thinking tokens are counted inside output by every CLI above.
package usage

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// Usage is one turn's token counts.
type Usage struct {
	Input    int64    `json:"input"`    // all input tokens of the turn, cached ones included
	Cached   int64    `json:"cached"`   // cache reads
	Output   int64    `json:"output"`   // output tokens, thinking included
	Thinking int64    `json:"thinking"` // thinking/reasoning tokens
	CostUSD  *float64 `json:"cost_usd"` // the CLI's own price for the turn, nil when it reports none
}

// Total is input plus output.
func (u Usage) Total() int64 { return u.Input + u.Output }

// UsageFileFlag is the one-run flag that writes a JSON usage report to a file, per agent.
var UsageFileFlag = map[string]string{"hermes": "--usage-file"}

var reportsUsage = map[string]bool{"claude": true, "codex": true, "agy": true, "hermes": true}

// ReportsUsage says whether the agent's one-run output (or usage file) carries
// token counts, so its models can be measured. A CLI that does not (Copilot) still
// answers; no usage is not a failure.
func ReportsUsage(agent string) bool { return reportsUsage[agent] }

var (
	thinkingKeys = []string{"thinking_tokens", "reasoning_output_tokens", "reasoning_tokens"}
	cachedKeys   = []string{"cache_read_input_tokens", "cached_input_tokens", "cache_read_tokens"}
)

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func toInt(v any) int64 {
	var n int64
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0
		}
		n = int64(x)
	case int:
		n = int64(x)
	case int64:
		n = x
	case json.Number:
		if i, err := x.Int64(); err == nil {
			n = i
		} else if f, err := x.Float64(); err == nil {
			n = int64(f)
		}
	case string:
		var i int64
		if _, err := fmt.Sscan(strings.TrimSpace(x), &i); err == nil {
			n = i
		}
	}
	if n < 0 {
		return 0
	}
	return n
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

func firstPresent(block map[string]any, keys []string) int64 {
	for _, k := range keys {
		if v, ok := block[k]; ok {
			return toInt(v)
		}
	}
	return 0
}

// From reads usage in one decoded JSON object: "usage", "result.usage", or a
// top-level usage report (Hermes' --usage-file). Nil when there is none.
func From(event map[string]any) *Usage {
	block := asMap(event["usage"])
	if len(block) == 0 {
		block = asMap(asMap(event["result"])["usage"])
	}
	if len(block) == 0 {
		_, in := event["input_tokens"]
		_, out := event["output_tokens"]
		if in && out {
			block = event
		}
	}
	if len(block) == 0 {
		return nil
	}
	if _, ok := block["input_tokens"]; !ok {
		return nil
	}
	input := toInt(block["input_tokens"])
	if _, ok := block["cache_creation_input_tokens"]; ok { // Anthropic: cache counts sit beside input_tokens
		input += toInt(block["cache_creation_input_tokens"]) + toInt(block["cache_read_input_tokens"])
	}
	thinking := firstPresent(block, thinkingKeys)
	if thinking == 0 {
		thinking = toInt(asMap(block["output_tokens_details"])["thinking_tokens"])
	}
	var costRaw any
	if v, ok := event["total_cost_usd"]; ok {
		costRaw = v
	} else if v, ok := event["estimated_cost_usd"]; ok {
		costRaw = v
	} else {
		costRaw = block["estimated_cost_usd"]
	}
	u := &Usage{
		Input:    input,
		Cached:   firstPresent(block, cachedKeys),
		Output:   toInt(block["output_tokens"]),
		Thinking: thinking,
	}
	if f, ok := toFloat(costRaw); ok {
		u.CostUSD = &f
	}
	return u
}

// FromJSON decodes one JSON object and reads its usage; nil when it is not an object or has none.
func FromJSON(data []byte) *Usage {
	var event map[string]any
	if err := json.Unmarshal(data, &event); err != nil || event == nil {
		return nil
	}
	return From(event)
}

// FromLines is the last usage a CLI printed (its final "result" or "turn.completed").
func FromLines(lines []string) *Usage {
	var found *Usage
	for _, line := range lines {
		if !strings.Contains(line, "usage") && !strings.Contains(line, "input_tokens") {
			continue
		}
		if u := FromJSON([]byte(line)); u != nil {
			found = u
		}
	}
	return found
}

// Assumptions behind the one-hour projection.
type Assumptions struct {
	TurnsPerHour         int64 // turns your agent answers in an hour: one every three minutes
	MeetingTokensPerTurn int64 // what MIA sends each turn: the new lines people said
}

// DefaultAssumptions is 20 turns an hour and ~120 tokens of meeting text per turn.
func DefaultAssumptions() Assumptions {
	return Assumptions{TurnsPerHour: 20, MeetingTokensPerTurn: 120}
}

// Describe states the assumptions in one sentence.
func (a Assumptions) Describe() string {
	return fmt.Sprintf("%d answered turns an hour, one warm session; each turn adds ~%d tokens of meeting text plus the answer to the context",
		a.TurnsPerHour, a.MeetingTokensPerTurn)
}

// HourEstimate is one projected hour of meeting.
type HourEstimate struct {
	Input   int64
	Output  int64
	CostUSD *float64 // the CLI's own price scaled to the hour, nil when it reports none
}

// Total is input plus output.
func (h HourEstimate) Total() int64 { return h.Input + h.Output }

// ProjectHour grows one measured turn to an hour: turn k re-reads the first
// turn's input plus k turns of history. Pass nil for the default assumptions.
func ProjectHour(turn Usage, a *Assumptions) HourEstimate {
	as := DefaultAssumptions()
	if a != nil {
		as = *a
	}
	turns := as.TurnsPerHour
	growth := as.MeetingTokensPerTurn + turn.Output
	in := turns*turn.Input + growth*turns*(turns-1)/2
	out := turns * turn.Output
	h := HourEstimate{Input: in, Output: out}
	if turn.CostUSD != nil && turn.Total() > 0 {
		c := *turn.CostUSD * float64(in+out) / float64(turn.Total())
		h.CostUSD = &c
	}
	return h
}

// HumanTokens is a short count: 950, 9.8k, 223k, 1.2M.
func HumanTokens(count float64) string {
	switch {
	case count >= 1_000_000:
		return fmt.Sprintf("%.1fM", count/1_000_000)
	case count >= 10_000:
		return fmt.Sprintf("%.0fk", count/1_000)
	case count >= 1_000:
		return fmt.Sprintf("%.1fk", count/1_000)
	}
	return fmt.Sprintf("%d", int64(count))
}

var eighths = []string{"", "▏", "▎", "▍", "▌", "▋", "▊", "▉"}

// Bar is a horizontal bar in eighths of a cell.
func Bar(value, largest float64, width int) string {
	if largest <= 0 || value <= 0 {
		return ""
	}
	n := int(math.RoundToEven(value / largest * float64(width) * 8))
	if n < 1 {
		n = 1
	}
	full, rest := n/8, n%8
	return strings.Repeat("█", full) + eighths[rest]
}
