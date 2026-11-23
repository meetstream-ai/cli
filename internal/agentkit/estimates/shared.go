// Package estimates holds the model picker's usage estimates: your own
// measurements (model-usage.json in the kit home, never shared) and MeetStream's
// shared estimates file. Ported from the kit's estimates.py and shared_estimates.py.
//
// For models you have not measured, the picker uses MeetStream's file:
//
//  1. SharedURL, fetched at most once a day and cached in the kit home;
//  2. when offline or missing, the copy embedded in the pricing package.
//
// The file is schema 1 plus the additive "prices" block (the dated API price
// table, see package pricing); Export keeps that block so the weekly refresh can
// edit it.
package estimates

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/meetstream-ai/cli/internal/agentkit/pairing"
	"github.com/meetstream-ai/cli/internal/agentkit/pricing"
	"github.com/meetstream-ai/cli/internal/agentkit/usage"
)

const (
	// SharedURL is MeetStream's published estimates file (same as the kit's).
	SharedURL = "https://meetstream-public-images.s3.amazonaws.com/agent-kit/model-estimates.json"
	// CacheFile is the cached copy's name in the kit home.
	CacheFile = pricing.CacheFile
	// CacheTTL is how long a fetched copy is used before fetching again.
	CacheTTL = 24 * time.Hour
	// FetchTimeout bounds the one network request.
	FetchTimeout = 3 * time.Second
	// Schema is the only shared-file schema this code trusts.
	Schema = 1
	// maxDownload caps the fetched body; the real file is ~10 KB.
	maxDownload = 4 << 20
)

// KitVersion is written into exported files; the wiring step may set it to the CLI's version.
var KitVersion = "go"

// Fetch downloads url within ctx's deadline. Tests pass a fake: they never reach the network.
type Fetch func(ctx context.Context, url string) ([]byte, error)

// HTTPFetch is the real Fetch: one GET, 200 only, body capped.
func HTTPFetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxDownload))
}

// Turn is the measured turn in a shared entry.
type Turn struct {
	Input    int64    `json:"input"`
	Cached   int64    `json:"cached"`
	Output   int64    `json:"output"`
	Thinking int64    `json:"thinking"`
	CostUSD  *float64 `json:"cost_usd"`
}

// PerHour is the entry's one-hour projection.
type PerHour struct {
	Input   int64    `json:"input"`
	Output  int64    `json:"output"`
	Total   int64    `json:"total"`
	CostUSD *float64 `json:"cost_usd"`
}

// Entry is one model's line in the shared file.
type Entry struct {
	Agent           string   `json:"agent"`
	Model           string   `json:"model"`
	MeasuredAt      string   `json:"measured_at"`
	Turn            Turn     `json:"turn"`
	PerHour         PerHour  `json:"per_hour"`
	FirstOutputMS   *float64 `json:"first_output_ms"`
	FirstOutputMode string   `json:"first_output_mode"`
}

// Usage is the entry's measured turn as usage.
func (e Entry) Usage() usage.Usage {
	return usage.Usage{Input: e.Turn.Input, Cached: e.Turn.Cached, Output: e.Turn.Output, Thinking: e.Turn.Thinking, CostUSD: e.Turn.CostUSD}
}

// NewEntry is one model's line: the measured turn and its one-hour projection.
func NewEntry(agent, model string, turn usage.Usage, firstOutputMS *float64, measuredAt string) Entry {
	hour := usage.ProjectHour(turn, nil)
	var first *float64
	if firstOutputMS != nil {
		r := math.RoundToEven(*firstOutputMS)
		first = &r
	}
	return Entry{
		Agent: agent, Model: model, MeasuredAt: measuredAt,
		Turn:          Turn{turn.Input, turn.Cached, turn.Output, turn.Thinking, turn.CostUSD},
		PerHour:       PerHour{hour.Input, hour.Output, hour.Total(), hour.CostUSD},
		FirstOutputMS: first, FirstOutputMode: "cold",
	}
}

// Document is a whole shared file. prices nil keeps the embedded price table.
func Document(entries []Entry, prices map[string]any) map[string]any {
	a := usage.DefaultAssumptions()
	if entries == nil {
		entries = []Entry{}
	}
	doc := map[string]any{
		"schema":       Schema,
		"kit_version":  KitVersion,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"assumptions": map[string]any{
			"turns_per_hour":          a.TurnsPerHour,
			"meeting_tokens_per_turn": a.MeetingTokensPerTurn,
			"session":                 "warm",
			"sample_turn":             "one short meeting turn, read-only, through the CLI's one-run mode",
		},
		"estimates": entries,
	}
	if prices == nil {
		prices, _ = pricing.BundledDocument()["prices"].(map[string]any)
	}
	if prices != nil {
		doc["prices"] = prices // the dated API price table; the refresh edits it here
	}
	return doc
}

// Export writes or merges path: new measurements replace the same agent + model,
// and an existing well-formed "prices" block is kept. Returns the entry count.
func Export(path string, entries []Entry) (int, error) {
	existing := readJSON(path)
	var prices map[string]any
	if len(pricing.Table(existing)) > 0 {
		prices, _ = existing.(map[string]any)["prices"].(map[string]any)
	}
	type key struct{ agent, model string }
	merged := map[key]Entry{}
	for _, e := range ValidEntries(existing) {
		merged[key{e.Agent, e.Model}] = e
	}
	for _, e := range entries {
		merged[key{e.Agent, e.Model}] = e
	}
	all := make([]Entry, 0, len(merged))
	for _, e := range merged {
		all = append(all, e)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Agent != all[j].Agent {
			return all[i].Agent < all[j].Agent
		}
		return all[i].Model < all[j].Model
	})
	data, err := json.MarshalIndent(Document(all, prices), "", "  ")
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return 0, err
	}
	return len(all), nil
}

func readJSON(path string) any {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return decodeAny(data)
}

func decodeAny(data []byte) any {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return nil
	}
	return v
}

// num is a non-negative finite JSON number (never a bool or string).
func num(v any) (float64, bool) {
	var f float64
	switch x := v.(type) {
	case json.Number:
		var err error
		if f, err = x.Float64(); err != nil {
			return 0, false
		}
	case float64:
		f = x
	default:
		return 0, false
	}
	return f, f >= 0 && !math.IsInf(f, 0) && !math.IsNaN(f)
}

// ValidEntries is the well-formed entries of a decoded shared file; anything odd
// is skipped, never trusted, and another schema yields none.
func ValidEntries(data any) []Entry {
	doc, _ := data.(map[string]any)
	if s, ok := num(doc["schema"]); !ok || s != Schema {
		return nil
	}
	list, _ := doc["estimates"].([]any)
	var found []Entry
	for _, it := range list {
		item, _ := it.(map[string]any)
		agent, ok1 := item["agent"].(string)
		model, ok2 := item["model"].(string)
		measuredAt, ok3 := item["measured_at"].(string)
		turn, _ := item["turn"].(map[string]any)
		if !ok1 || !ok2 || !ok3 || turn == nil {
			continue
		}
		var counts [4]float64
		bad := false
		for i, k := range []string{"input", "cached", "output", "thinking"} {
			v, ok := num(turn[k])
			bad = bad || !ok
			counts[i] = v
		}
		cost, costOK := num(turn["cost_usd"])
		first, firstOK := num(item["first_output_ms"])
		if bad || (turn["cost_usd"] != nil && !costOK) || (item["first_output_ms"] != nil && !firstOK) {
			continue
		}
		e := Entry{Agent: agent, Model: model, MeasuredAt: measuredAt,
			Turn: Turn{int64(counts[0]), int64(counts[1]), int64(counts[2]), int64(counts[3]), nil}}
		if turn["cost_usd"] != nil {
			e.Turn.CostUSD = &cost
		}
		if item["first_output_ms"] != nil {
			e.FirstOutputMS = &first
		}
		if mode, ok := item["first_output_mode"].(string); ok {
			e.FirstOutputMode = mode
		}
		hour := usage.ProjectHour(e.Usage(), nil)
		e.PerHour = PerHour{hour.Input, hour.Output, hour.Total(), hour.CostUSD}
		found = append(found, e)
	}
	return found
}

// Bundled is the embedded file's entries.
func Bundled() []Entry { return ValidEntries(decodeAny(pricing.Bundled())) }

// CachedDocument is the last shared file fetched into kitHome (any age), or nil.
// kitHome "" is the kit home (pairing.Home).
func CachedDocument(kitHome string) any {
	return readJSON(filepath.Join(orKitHome(kitHome), CacheFile))
}

func orKitHome(kitHome string) string {
	if kitHome == "" {
		return pairing.Home()
	}
	return kitHome
}

// SharedOptions are LoadShared's injectable parts; zero values use the real ones.
type SharedOptions struct {
	Fetch   Fetch            // default HTTPFetch
	Now     func() time.Time // default time.Now
	KitHome string           // default pairing.Home()
}

// LoadShared is MeetStream's estimates: the cached copy (under a day old), else the
// URL (then cached), else a stale cached copy, else the embedded copy.
func LoadShared(opts SharedOptions) []Entry {
	fetch, now := opts.Fetch, opts.Now
	if fetch == nil {
		fetch = HTTPFetch
	}
	if now == nil {
		now = time.Now
	}
	cache := filepath.Join(orKitHome(opts.KitHome), CacheFile)
	if info, err := os.Stat(cache); err == nil && now().Sub(info.ModTime()) < CacheTTL {
		if cached := ValidEntries(readJSON(cache)); len(cached) > 0 {
			return cached
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), FetchTimeout)
	defer cancel()
	if raw, err := fetch(ctx, SharedURL); err == nil {
		if fetched := ValidEntries(decodeAny(raw)); len(fetched) > 0 {
			if os.MkdirAll(filepath.Dir(cache), 0o700) == nil {
				_ = writeAtomic(cache, raw, 0o644)
			}
			return fetched
		}
	}
	if stale := ValidEntries(readJSON(cache)); len(stale) > 0 {
		return stale
	}
	return Bundled()
}

// ForAgent is the agent's entries by model id.
func ForAgent(agent string, entries []Entry) map[string]Entry {
	out := map[string]Entry{}
	for _, e := range entries {
		if e.Agent == agent {
			out[e.Model] = e
		}
	}
	return out
}
