package estimates

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/meetstream-ai/cli/internal/agentkit/pairing"
	"github.com/meetstream-ai/cli/internal/agentkit/usage"
)

// UsageFile is your own measurements' file in the kit home. It never leaves your computer.
const UsageFile = "model-usage.json"

// Estimate is one model's measured turn, from your runs or the shared file.
type Estimate struct {
	Usage         usage.Usage
	Seconds       float64
	FirstOutputMS *float64
	MeasuredAt    string
	// Source is "measured here" (your own runs) or "measured by MeetStream <date>" (the shared file).
	Source string
}

// MeasuredHere is the Source of your own runs.
const MeasuredHere = "measured here"

// Measurement is one finished run to keep. Nothing is kept when Usage is nil or Error is set.
type Measurement struct {
	Usage         *usage.Usage
	Seconds       float64
	FirstOutputMS *float64
	Error         string
}

type stored struct {
	Input         int64    `json:"input"`
	Cached        int64    `json:"cached"`
	Output        int64    `json:"output"`
	Thinking      int64    `json:"thinking"`
	CostUSD       *float64 `json:"cost_usd"`
	Seconds       float64  `json:"seconds"`
	FirstOutputMS *float64 `json:"first_output_ms"`
	MeasuredAt    string   `json:"measured_at"`
}

var timeLayouts = []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999", "2006-01-02T15:04:05", "2006-01-02"}

func validTime(s string) bool {
	for _, layout := range timeLayouts {
		if _, err := time.Parse(layout, s); err == nil {
			return true
		}
	}
	return false
}

// count is a non-negative JSON integer (1.0, true and "1" are not).
func count(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	return i, err == nil && i >= 0
}

func usagePath() string { return filepath.Join(pairing.Home(), UsageFile) }

// Load is this agent's valid measurements by model id; a broken file is harmless.
func Load(agent string) map[string]Estimate {
	out := map[string]Estimate{}
	data, _ := readJSON(usagePath()).(map[string]any)
	prefix := agent + ":"
	for key, raw := range data {
		item, _ := raw.(map[string]any)
		if !strings.HasPrefix(key, prefix) || item == nil {
			continue
		}
		var counts [4]int64
		bad := false
		for i, k := range []string{"input", "cached", "output", "thinking"} {
			v, ok := count(item[k])
			bad = bad || !ok
			counts[i] = v
		}
		seconds, ok := num(item["seconds"])
		if bad || !ok {
			continue
		}
		var optional [2]*float64
		for i, k := range []string{"cost_usd", "first_output_ms"} {
			if item[k] == nil {
				continue
			}
			v, ok := num(item[k])
			if !ok {
				bad = true
			}
			optional[i] = &v
		}
		stamp, ok := item["measured_at"].(string)
		if bad || !ok || !validTime(stamp) {
			continue
		}
		out[key[len(prefix):]] = Estimate{
			Usage:   usage.Usage{Input: counts[0], Cached: counts[1], Output: counts[2], Thinking: counts[3], CostUSD: optional[0]},
			Seconds: seconds, FirstOutputMS: optional[1], MeasuredAt: stamp, Source: MeasuredHere,
		}
	}
	return out
}

// LoadWithShared is your own measurements, then the shared entries (from
// LoadShared) for models you have not measured. It never fetches by itself.
func LoadWithShared(agent string, shared []Entry) map[string]Estimate {
	found := Load(agent)
	for model, e := range ForAgent(agent, shared) {
		if _, mine := found[model]; mine {
			continue
		}
		date := e.MeasuredAt
		if len(date) > 10 {
			date = date[:10]
		}
		found[model] = Estimate{Usage: e.Usage(), FirstOutputMS: e.FirstOutputMS, MeasuredAt: e.MeasuredAt,
			Source: "measured by MeetStream " + date}
	}
	return found
}

// saving serializes saves: measurements finish on several goroutines at once, and
// each save reads, merges and replaces the file (parallel saves used to lose entries).
var saving sync.Mutex

// Save keeps a successful measurement under "<agent>:<model>", atomically and
// owner-only (0600). A failed run or one without usage is not kept. Callers may
// ignore the error: a cache that cannot be written must not interrupt setup.
func Save(agent, model string, m Measurement) error {
	if m.Usage == nil || m.Error != "" {
		return nil
	}
	saving.Lock()
	defer saving.Unlock()
	path := usagePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data := map[string]any{}
	if existing, ok := readJSON(path).(map[string]any); ok {
		data = existing
	}
	data[agent+":"+model] = stored{
		Input: m.Usage.Input, Cached: m.Usage.Cached, Output: m.Usage.Output, Thinking: m.Usage.Thinking,
		CostUSD: m.Usage.CostUSD, Seconds: m.Seconds, FirstOutputMS: m.FirstOutputMS,
		MeasuredAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return writeAtomic(path, body, 0o600)
}

// writeAtomic writes a temp file beside path, sets perm, and renames it over path.
func writeAtomic(path string, body []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // a no-op after the rename
	if err := tmp.Chmod(perm); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
