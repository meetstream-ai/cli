package cli

import (
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/meetstream-ai/cli/internal/api"
	"github.com/meetstream-ai/cli/internal/config"
	"github.com/meetstream-ai/cli/internal/ui"
)

// Setup's step 1: how MIA runs in meetings, and what it costs per hour.
// Every price comes from the API's `mia_managed` catalog for this user (GET
// /mia); nothing here hard-codes a rate.

const (
	modeHosted = "hosted"
	modeBYOK   = "byok"
	modeLocal  = "local-agent"

	keysMeetStream = "meetstream"
	keysOwn        = "own"

	secEars  = "transcriber"
	secBrain = "model"
	secVoice = "voice"

	// keysURL is where provider keys are added (the dashboard's Integrations page).
	keysURL = "https://app.meetstream.ai/integrations"

	modeQuestion = "How should MIA run in your meetings?"
)

var pipelineSections = []string{secEars, secBrain, secVoice}

// sectionWord is how a section reads in a price breakdown.
var sectionWord = map[string]string{secEars: "ears", secBrain: "brain", secVoice: "voice"}

// ── the managed catalog ───────────────────────────────────────────────────────

type catalogProvider struct {
	Label, DefaultModel string
	Models              map[string]float64 // USD per hour
}

type catalogSection struct {
	DefaultProvider string
	Providers       map[string]catalogProvider
}

// miaCatalog is `mia_managed` from GET /mia. Known is false when it could not
// be read (no API key): prices are then unknown, not zero.
type miaCatalog struct {
	Known, Enabled bool
	Base, Agent    *float64 // core bot rate; all-in MIA rate on your own keys
	Managed        *float64 // all-in MIA rate with any section on a MeetStream key
	Sections       map[string]catalogSection
}

func asPrice(v any) (float64, bool) {
	f, ok := v.(float64)
	return f, ok && f >= 0 && !math.IsInf(f, 0) && !math.IsNaN(f)
}

func priceRef(v any) *float64 {
	if f, ok := asPrice(v); ok {
		return &f
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// parseCatalog reads `mia_managed` the way the dashboard does: anything
// malformed is left out, and a missing catalog reads as disabled.
func parseCatalog(raw any) miaCatalog {
	c := miaCatalog{Known: true, Sections: map[string]catalogSection{}}
	m, ok := raw.(map[string]any)
	if !ok {
		return c
	}
	c.Enabled = m["enabled"] == true
	if r, ok := m["rates"].(map[string]any); ok {
		c.Base, c.Agent, c.Managed = priceRef(r["base_price"]), priceRef(r["agent"]), priceRef(r["managed_agent"])
	}
	secs, _ := m["sections"].(map[string]any)
	for _, name := range pipelineSections {
		s, _ := secs[name].(map[string]any)
		provs, _ := s["providers"].(map[string]any)
		out := catalogSection{Providers: map[string]catalogProvider{}}
		for _, id := range sortedKeys(provs) {
			p, _ := provs[id].(map[string]any)
			ms, _ := p["models"].(map[string]any)
			cp := catalogProvider{Models: map[string]float64{}}
			cp.Label, _ = p["label"].(string)
			for _, model := range sortedKeys(ms) {
				if f, ok := asPrice(ms[model]); ok {
					cp.Models[model] = f
				}
			}
			if len(cp.Models) == 0 {
				continue
			}
			cp.DefaultModel, _ = p["default_model"].(string)
			if _, ok := cp.Models[cp.DefaultModel]; !ok {
				cp.DefaultModel = sortedKeys(cp.Models)[0]
			}
			out.Providers[strings.ToLower(id)] = cp
		}
		if len(out.Providers) == 0 {
			continue
		}
		out.DefaultProvider, _ = s["default_provider"].(string)
		if _, ok := out.Providers[out.DefaultProvider]; !ok {
			out.DefaultProvider = sortedKeys(out.Providers)[0]
		}
		c.Sections[name] = out
	}
	return c
}

// offers is whether MeetStream keys run this section on this server.
func (c miaCatalog) offers(section string) bool {
	_, ok := c.Sections[section]
	return c.Known && c.Enabled && ok
}

// hostedOffered is whether every section can run on MeetStream keys. An
// unknown catalog (not signed in) is not ruled out.
func (c miaCatalog) hostedOffered(sections ...string) bool {
	if !c.Known {
		return true
	}
	for _, s := range sections {
		if !c.offers(s) {
			return false
		}
	}
	return true
}

func (c miaCatalog) managedPrice(section, provider, model string) (float64, bool) {
	p, ok := c.Sections[section].Providers[provider]
	if !ok {
		return 0, false
	}
	if model == "" {
		model = p.DefaultModel
	}
	f, ok := p.Models[model]
	return f, ok
}

// fetchCatalog reads the catalog with the saved API key. No key: unknown. A
// failed read, or a server that sends no catalog, reads as disabled.
func fetchCatalog(w *setupWizard) miaCatalog {
	key, _ := config.ResolveKey()
	if key == "" {
		return miaCatalog{}
	}
	var resp map[string]any
	if err := api.New(config.APIURL(), key, w.info.Version).Do(w.cmd.Context(), http.MethodGet, "/mia", nil, &resp); err != nil {
		if !g.JSON {
			ui.Warn("Could not read MeetStream's hosted catalog, so hosted ears, brain and voice are off for this setup: " + apiError(err).Error())
		}
		return parseCatalog(nil)
	}
	return parseCatalog(resp["mia_managed"])
}

// ── what each section runs on ─────────────────────────────────────────────────

// sectionPick is one pipeline section: provider and model, and whether it runs
// on a MeetStream key. Provider "" means the server's default (catalog unknown).
type sectionPick struct {
	Provider, Model string
	Managed         bool
}

func (p sectionPick) String() string {
	if p.Provider == "" {
		return "MeetStream's default"
	}
	return p.Provider + " " + p.Model
}

// byokChoices are the providers and models offered for your own keys, besides
// the catalog's. Providers are the ones MIA supports per section; voices need a
// default voice id (below), so only those providers are listed for the voice.
var byokChoices = map[string][]sectionPick{
	secEars: {
		{Provider: "openai", Model: "gpt-4o-mini-transcribe"}, {Provider: "openai", Model: "gpt-4o-transcribe"}, {Provider: "openai", Model: "whisper-1"},
		{Provider: "deepgram", Model: "nova-3"}, {Provider: "deepgram", Model: "flux-general-en"},
		{Provider: "elevenlabs", Model: "scribe_v2_realtime"},
	},
	secBrain: {
		{Provider: "openai", Model: "gpt-4.1-mini"}, {Provider: "openai", Model: "gpt-4o-mini"}, {Provider: "openai", Model: "gpt-4.1"}, {Provider: "openai", Model: "gpt-5-mini"},
		{Provider: "anthropic", Model: "claude-haiku-5-5"}, {Provider: "anthropic", Model: "claude-sonnet-5-5"},
		{Provider: "google", Model: "gemini-2.5-flash"}, {Provider: "xai", Model: "grok-3-mini"},
	},
	secVoice: {
		{Provider: "openai", Model: "tts-1"}, {Provider: "openai", Model: "tts-1-hd"},
		{Provider: "elevenlabs", Model: "eleven_turbo_v2_5"}, {Provider: "elevenlabs", Model: "eleven_flash_v2_5"},
		{Provider: "deepgram", Model: "aura-2"},
	},
}

// supportedProviders are the providers the MIA API accepts per section (agent
// config validation), for --ears/--brain/--voice with your own keys.
var supportedProviders = map[string][]string{
	secEars:  {"openai", "deepgram", "assemblyai", "xai", "google", "smallest", "elevenlabs", "cartesia", "sarvam"},
	secBrain: {"openai", "anthropic", "xai", "google"},
	secVoice: {"openai", "elevenlabs", "deepgram"},
}

// defaultVoiceID is the voice each voice provider speaks with: the API needs a
// voice id and the catalog lists none. Change it later in the dashboard.
var defaultVoiceID = map[string]string{"openai": "coral", "elevenlabs": "Xb7hH8MSUJpSbSDYk0k2", "deepgram": "apollo"}

// defaultSystemPrompt is the brain's prompt for hosted and BYOK agents (the API
// needs one; the local agent has its own). Edit it later in the dashboard.
const defaultSystemPrompt = "You are MIA, a helpful assistant in this meeting. Answer briefly, in plain spoken sentences, and only when you are asked."

var modelIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

// defaultPick is the catalog's default for a section on MeetStream keys, or
// the first own-key choice the catalog does not cover.
func (c miaCatalog) defaultPick(section string, managed bool) sectionPick {
	if s, ok := c.Sections[section]; ok && c.Enabled {
		p := sectionPick{Provider: s.DefaultProvider, Model: s.Providers[s.DefaultProvider].DefaultModel, Managed: managed}
		if managed || section != secVoice || defaultVoiceID[p.Provider] != "" {
			return p
		}
	}
	if managed {
		return sectionPick{Managed: true} // catalog unknown: the server picks
	}
	return byokChoices[section][0]
}

// choicesFor lists a section's own-key rows: the catalog's first, then the rest.
func (c miaCatalog) choicesFor(section string) []sectionPick {
	var out []sectionPick
	seen := map[string]bool{}
	add := func(p sectionPick) {
		if k := p.Provider + ":" + p.Model; !seen[k] && (section != secVoice || defaultVoiceID[p.Provider] != "") {
			seen[k] = true
			out = append(out, p)
		}
	}
	if s, ok := c.Sections[section]; ok && c.Enabled {
		add(sectionPick{Provider: s.DefaultProvider, Model: s.Providers[s.DefaultProvider].DefaultModel})
		for _, id := range sortedKeys(s.Providers) {
			for _, m := range sortedKeys(s.Providers[id].Models) {
				add(sectionPick{Provider: id, Model: m})
			}
		}
	}
	for _, p := range byokChoices[section] {
		add(p)
	}
	return out
}

// parsePickFlag reads --ears/--brain/--voice ("provider:model").
func parsePickFlag(flag, section, v string, managed bool, c miaCatalog) (sectionPick, error) {
	provider, model, ok := strings.Cut(strings.TrimSpace(v), ":")
	provider = strings.ToLower(provider)
	if !ok || provider == "" || !modelIDPattern.MatchString(model) {
		return sectionPick{}, usageErr("invalid --"+flag+" "+v, "use provider:model, for example openai:gpt-4o-mini")
	}
	p := sectionPick{Provider: provider, Model: model, Managed: managed}
	if managed {
		if !c.Known {
			return p, nil // checked by the server; prices need a sign-in
		}
		if _, ok := c.managedPrice(section, provider, model); !ok || !c.Enabled {
			var offered []string
			for _, id := range sortedKeys(c.Sections[section].Providers) {
				for _, m := range sortedKeys(c.Sections[section].Providers[id].Models) {
					offered = append(offered, id+":"+m)
				}
			}
			hint := "MeetStream keys offer: " + strings.Join(offered, ", ")
			if len(offered) == 0 {
				hint = "MeetStream keys are not offered for this on this server; use --mode byok"
			}
			return p, usageErr("--"+flag+" "+v+" is not offered on MeetStream keys", hint)
		}
		return p, nil
	}
	if !contains(supportedProviders[section], provider) {
		return p, usageErr("--"+flag+" provider "+provider+" is not supported", "choose: "+strings.Join(supportedProviders[section], ", "))
	}
	return p, nil
}

// ── prices ────────────────────────────────────────────────────────────────────

// priceLine is one part of a per-hour price; PerHour nil is "your provider charges".
type priceLine struct {
	Label   string   `json:"label"`
	PerHour *float64 `json:"price_per_hour"`
}

// quote is a per-hour price and its parts. Total is nil when a rate is unknown
// or the price also has your own providers' charges (Plus).
type quote struct {
	Lines   []priceLine
	Total   *float64
	Plus    bool // + your provider charges
	Unknown bool // rates not read (not signed in, or an old server)
	FeeOnly bool // the meeting fee alone, rate not exposed by the API
	Note    string
}

func roundUSD(v float64) float64 { return math.Round(v*1e6) / 1e6 }

func usd(v float64) string { return fmt.Sprintf("$%.2f", v) }

// quoteFor prices the picks: Meeting + MIA harness + each section on a
// MeetStream key (+ your provider charges for the rest). A bot with any section
// on a MeetStream key bills the managed MIA rate, else the BYOK rate.
func quoteFor(c miaCatalog, picks map[string]sectionPick, sections []string) quote {
	managed := false
	for _, s := range sections {
		managed = managed || picks[s].Managed
	}
	rate := c.Agent
	if managed && c.Managed != nil {
		rate = c.Managed
	}
	q := quote{}
	if !c.Known || c.Base == nil || rate == nil {
		q.Unknown = true
	}
	var total float64
	if !q.Unknown {
		harness := math.Max(0, roundUSD(*rate-*c.Base))
		q.Lines = append(q.Lines, priceLine{"Meeting", c.Base}, priceLine{"MIA harness", &harness})
		total = *c.Base + harness
	}
	for _, s := range sections {
		p := picks[s]
		if !p.Managed {
			q.Plus = true
			continue
		}
		f, ok := c.managedPrice(s, p.Provider, p.Model)
		if !ok {
			q.Unknown = true
			continue
		}
		q.Lines = append(q.Lines, priceLine{sectionWord[s], &f})
		total += f
	}
	if q.Plus {
		q.Lines = append(q.Lines, priceLine{Label: "your provider charges"})
	}
	if !q.Unknown && !q.Plus {
		t := roundUSD(total)
		q.Total = &t
	}
	if !q.Unknown && q.Plus {
		t := roundUSD(total)
		q.Total = &t // MeetStream's part; your providers bill the rest
	}
	return q
}

// breakdown is "Meeting $0.35 + MIA harness $0.40 + ears $0.20 ≈ $0.95/h".
func (q quote) breakdown() string {
	switch {
	case q.Note != "":
		return q.Note
	case q.FeeOnly:
		return "Meeting fee only"
	case q.Unknown:
		return "Prices show once you are signed in (meetstream login)."
	}
	parts := make([]string, len(q.Lines))
	for i, l := range q.Lines {
		parts[i] = l.Label
		if l.PerHour != nil {
			parts[i] += " " + usd(*l.PerHour)
		}
	}
	s := strings.Join(parts, " + ")
	if !q.Plus {
		s += " ≈ " + usd(*q.Total) + "/h"
	}
	return s
}

// total is the short price beside a row: "≈$1.25/h" or "$0.60/h + your providers".
func (q quote) total() string {
	switch {
	case q.Note != "":
		return "price unchanged"
	case q.FeeOnly:
		return "meeting fee only"
	case q.Unknown || q.Total == nil:
		return "price after sign-in"
	case q.Plus:
		return usd(*q.Total) + "/h + your providers"
	}
	return "≈" + usd(*q.Total) + "/h"
}

// jsonFields are the price fields of --json and --dry-run output.
func (q quote) jsonFields(out map[string]any) {
	var total any
	if q.Total != nil {
		total = *q.Total
	}
	lines := q.Lines
	if lines == nil {
		lines = []priceLine{}
	}
	out["price_per_hour"], out["breakdown"] = total, lines
	if q.Plus {
		out["plus_your_providers"] = true
	}
}

// ── step 1 ────────────────────────────────────────────────────────────────────

// modeRow is one option of step 1.
type modeRow struct {
	id, label, detail string
	quote             quote
	disabled          string // why it cannot be picked; "" when it can
	extra             string // more detail lines (other prices, cons)
}

const (
	hostedDetail = "MIA, our meeting harness, on a stack we run for you (e.g. OpenAI + ElevenLabs). Pre-paid per hour; fixed stack."
	byokDetail   = "MIA, our meeting harness built for meetings, with your own provider keys: pick any models, voices and parameters."
	localDetail  = "Our meeting harness, your meeting agent. Your agent (Claude Code, Codex, Hermes…) and its files stay on your computer. For ears and voice, use MeetStream hosted or bring your own keys."
	allLocalDesc = "Ears, brain and voice all on your computer. You pay only the meeting fee."
	allLocalCons = "MIA's meeting harness (turn-taking, interruptions, meeting tools) doesn't apply here, and performance isn't guaranteed."
	notOffered   = "not offered on this server"
)

// managedPicks is every section on MeetStream keys at the catalog's defaults.
func (c miaCatalog) managedPicks() map[string]sectionPick {
	m := map[string]sectionPick{}
	for _, s := range pipelineSections {
		m[s] = c.defaultPick(s, true)
	}
	return m
}

func (c miaCatalog) ownPicks() map[string]sectionPick {
	m := map[string]sectionPick{}
	for _, s := range pipelineSections {
		m[s] = c.defaultPick(s, false)
	}
	return m
}

// modeRows are step 1's options with this user's prices.
func modeRows(c miaCatalog) []modeRow {
	hosted := modeRow{id: modeHosted, label: "MeetStream hosted", detail: hostedDetail,
		quote: quoteFor(c, c.managedPicks(), pipelineSections)}
	if !c.hostedOffered(pipelineSections...) {
		hosted.disabled = notOffered
	}
	byok := modeRow{id: modeBYOK, label: "MIA with your keys (BYOK)", detail: byokDetail,
		quote: quoteFor(c, c.ownPicks(), pipelineSections)}
	local := modeRow{id: modeLocal, label: "Your agent on your computer", detail: localDetail}
	localSections := []string{secEars, secVoice}
	own := quoteFor(c, c.ownPicks(), localSections)
	if c.hostedOffered(localSections...) {
		local.quote = quoteFor(c, c.managedPicks(), localSections)
		local.extra = "With MeetStream ears and voice: " + local.quote.breakdown() + "\nWith your keys: " + own.breakdown()
	} else {
		local.quote = own
		local.extra = "With your keys: " + own.breakdown() + "\nMeetStream ears and voice are " + notOffered + "."
	}
	// Bridge bots bill a meeting rate the agent list does not send, so no number.
	all := modeRow{id: "all-local", label: "All local (preview, coming)", detail: allLocalDesc,
		quote: quote{FeeOnly: true}, disabled: "not available yet", extra: allLocalCons}
	return []modeRow{hosted, byok, local, all}
}

// modeMenu is step 1's menu: the total beside each row, the breakdown under
// the highlighted one. Rows that cannot be picked are dimmed and say why.
func modeMenu(c miaCatalog, current string) stepMenuModel {
	rows := modeRows(c)
	var labels, hints, details []string
	disabled := make([]bool, len(rows))
	cursor := 0
	for i, r := range rows {
		labels = append(labels, r.label)
		hint := r.quote.total()
		if r.disabled != "" {
			hint = r.disabled
			if r.quote.FeeOnly {
				hint = r.quote.total() + " · " + r.disabled
			}
			disabled[i] = true
		}
		hints = append(hints, hint)
		d := r.detail
		if r.extra != "" && r.id == modeLocal {
			d += "\n" + r.extra
		} else {
			d += "\n" + r.quote.breakdown()
			if r.extra != "" {
				d += "\n" + r.extra
			}
		}
		if r.disabled == notOffered {
			d += "\nNot offered on this server: MeetStream keys are off here."
		}
		details = append(details, d)
		if r.id == current {
			cursor = i
		}
	}
	m := newStepMenu(modeQuestion, labels...).withHints(hints).withDetails(details).at(cursor)
	m.disabled, m.noBack = disabled, true
	return m
}
