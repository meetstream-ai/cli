package cli

// Human views: every renderer returns a string (so the home app can show it in
// a pane) and never prints raw JSON, cursors or secrets. --json never comes here.

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/meetstream-ai/cli/internal/config"
	"github.com/meetstream-ai/cli/internal/ui"
)

// miaIntro introduces MIA once, in plain words.
const miaIntro = "MIA is MeetStream's AI meeting agent: it joins your call, listens and answers out loud."

// pick is the first non-empty value under any of keys (the API mixes
// snake_case and DynamoDB's CamelCase).
func pick(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil && v != "" {
			return v
		}
	}
	return nil
}

// plain is a scalar as text: numbers without exponents, booleans as yes/no.
func plain(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(x)
	case bool:
		if x {
			return "yes"
		}
		return "no"
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	}
	return fmt.Sprint(v)
}

func pickStr(m map[string]any, keys ...string) string { return plain(pick(m, keys...)) }

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func dash(s string) string {
	if s == "" {
		return ui.DimStyle.Render("—")
	}
	return s
}

// ── words ────────────────────────────────────────────────────────────────────

// labels replaces internal field names with plain words.
var labels = map[string]string{
	"agent_config_id": "Agent ID", "agentconfigid": "Agent ID", "agent_config": "Agent", "agent_configs": "Agents",
	"bot_id": "Bot ID", "botid": "Bot ID", "bot_username": "Bot name", "botusername": "Bot name", "bot_name": "Bot name",
	"transcript_id": "Transcript ID", "meeting_url": "Meeting", "meeting_link": "Meeting", "meetinglink": "Meeting",
	"key_source": "Keys", "local_agent": "Your agent", "custom_attributes": "Tags", "customer_type": "Billing",
	"balance_usd": "Balance", "created_at": "Created", "createdat": "Created", "updated_at": "Updated", "updatedat": "Updated",
	"start_time": "Started", "end_time": "Ended", "scheduled_join_time": "Joins", "is_active": "Active",
	"sso_workspace_domain": "Workspace domain", "login_mode": "Signs in", "audio_url": "Audio", "video_url": "Video",
	"speaker_timeline_url": "Download", "size_bytes": "Size", "auto_schedule_enabled": "Auto-join",
	"default_bot_config": "Bot for calendar meetings", "platform_capabilities": "Capabilities", "url": "Link",
}

// humanize turns "agent_config_id" or "BotUsername" into plain words.
func humanize(key string) string {
	if l, ok := labels[key]; ok {
		return l
	}
	if l, ok := labels[strings.ToLower(key)]; ok {
		return l
	}
	var words []string
	var cur []rune
	rs := []rune(key)
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = nil
		}
	}
	for i, r := range rs {
		switch {
		case r == '_' || r == '-' || r == ' ' || r == '.':
			flush()
		case unicode.IsUpper(r) && i > 0 && (unicode.IsLower(rs[i-1]) || i+1 < len(rs) && unicode.IsLower(rs[i+1])):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	if len(words) == 0 {
		return key
	}
	for i, w := range words {
		if w == "id" || w == "url" || w == "ai" {
			words[i] = strings.ToUpper(w)
		}
	}
	s := strings.Join(words, " ")
	return strings.ToUpper(s[:1]) + s[1:]
}

var providerNames = map[string]string{
	"openai": "OpenAI", "elevenlabs": "ElevenLabs", "google": "Google", "xai": "xAI", "smallest": "Smallest",
	"sarvam": "Sarvam", "cartesia": "Cartesia", "deepgram": "Deepgram", "anthropic": "Anthropic", "assemblyai": "AssemblyAI",
	"azure": "Azure", "groq": "Groq", "gemini": "Gemini", "outlook": "Microsoft", "microsoft": "Microsoft",
	"meetstream": "MeetStream", "jigsawstack": "Interfaze", "meeting_captions": "Meeting captions", "rime": "Rime",
	"deepgram_streaming": "Deepgram (live)", "assemblyai_streaming": "AssemblyAI (live)", "smallest_streaming": "Smallest (live)",
}

func providerName(p string) string {
	if n, ok := providerNames[strings.ToLower(p)]; ok {
		return n
	}
	if p == "" {
		return ""
	}
	return humanize(p)
}

// platformName reads the platform field, else the meeting link's host.
func platformName(platform, link string) string {
	key := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, platform)
	switch key {
	case "gmeet", "googlemeet", "meet", "google":
		return "Google Meet"
	case "zoom", "zoomrtms":
		return "Zoom"
	case "teams", "microsoftteams", "msteams":
		return "Teams"
	case "whatsapp", "whatsappweb":
		return "WhatsApp"
	}
	if u, err := url.Parse(link); err == nil {
		h := strings.ToLower(u.Hostname())
		switch {
		case h == "meet.google.com":
			return "Google Meet"
		case h == "zoom.us" || strings.HasSuffix(h, ".zoom.us"):
			return "Zoom"
		case strings.HasPrefix(h, "teams.") || strings.HasSuffix(h, ".teams.microsoft.com"):
			return "Teams"
		}
	}
	return platform
}

// statusPill is a bot or transcript status as a coloured label.
func statusPill(s string) string {
	key := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, s)
	switch key {
	case "":
		return ui.DimStyle.Render("—")
	case "done", "stopped", "completed", "complete", "ended", "finished", "left", "success", "succeeded", "ok":
		return ui.Pill("Done", ui.Green)
	case "inmeeting", "joined", "recording", "incall", "active", "live", "running":
		return ui.Pill("In meeting", ui.Cyan)
	case "joining", "waiting", "inwaitingroom", "waitingroom", "starting", "created", "queued", "pending":
		return ui.Pill("Joining", ui.Yellow)
	case "processing", "inprogress", "transcribing":
		return ui.Pill("Processing", ui.Yellow)
	case "scheduled":
		return ui.Pill("Scheduled", ui.Blue)
	case "cancelled", "canceled":
		return ui.DimPill("Cancelled")
	case "denied", "notallowed":
		return ui.DimPill("Not let in")
	case "removed", "kicked":
		return ui.DimPill("Removed")
	case "expired":
		return ui.DimPill("Expired")
	case "failed", "error", "fatal":
		return ui.Pill("Failed", ui.Red)
	case "offline", "disconnected":
		return ui.Pill("Offline", ui.Red)
	}
	return ui.DimPill(humanize(s))
}

// ── generic data (any API answer without its own view) ────────────────────────

func secretKey(k string) bool {
	k = strings.ToLower(k)
	for _, s := range []string{"password", "secret", "token", "private_key", "api_key", "apikey", "pem"} {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

func timeKey(k string) bool {
	k = strings.ToLower(k)
	return strings.HasSuffix(k, "_at") || strings.HasSuffix(k, "at") && len(k) > 2 && (strings.HasSuffix(k, "createdat") || strings.HasSuffix(k, "updatedat")) ||
		strings.HasSuffix(k, "time") || strings.HasSuffix(k, "date") || k == "timestamp" || k == "expires"
}

func idKey(k string) bool {
	k = strings.ToLower(k)
	return k == "id" || strings.HasSuffix(k, "_id") || strings.HasSuffix(k, "id") && len(k) > 2 && unicode.IsLetter(rune(k[len(k)-3]))
}

// fmtValue formats one scalar for a human: statuses as pills, times relative,
// durations in minutes. Maps and lists never reach here.
func fmtValue(key string, v any) string {
	k := strings.ToLower(key)
	switch {
	case strings.Contains(k, "status") || k == "state":
		if s, ok := v.(string); ok {
			return statusPill(s)
		}
	case timeKey(k):
		if w := ui.When(v); w != "" {
			return w
		}
	case strings.Contains(k, "duration"):
		if d := ui.Duration(v); d != "" {
			return d
		}
	case k == "provider" || strings.HasSuffix(k, "_provider"):
		if s, ok := v.(string); ok {
			return providerName(s)
		}
	case strings.HasSuffix(k, "mode") || strings.HasSuffix(k, "type") || strings.HasSuffix(k, "role") || strings.HasSuffix(k, "modality"):
		if s, ok := v.(string); ok && !strings.ContainsAny(s, " ./:@") {
			return humanize(s)
		}
	case k == "platform" || k == "meeting_platform":
		if s, ok := v.(string); ok {
			return platformName(s, "")
		}
	}
	return plain(v)
}

func scalar(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return false
	}
	return true
}

func empty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	case map[string]any:
		return len(x) == 0
	case []any:
		return len(x) == 0
	}
	return false
}

// sortedKeys puts plain fields first, ids last, nested blocks after.
func viewKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	rank := func(k string) int {
		switch {
		case !scalar(m[k]):
			return 2
		case idKey(k):
			return 1
		}
		return 0
	}
	sort.SliceStable(keys, func(i, j int) bool {
		if ri, rj := rank(keys[i]), rank(keys[j]); ri != rj {
			return ri < rj
		}
		return keys[i] < keys[j]
	})
	return keys
}

// flatRows turns a map into label/value rows; nested maps become "Parent · child".
func flatRows(prefix string, m map[string]any, depth int) [][2]string {
	var rows [][2]string
	for _, k := range viewKeys(m) {
		v := m[k]
		if empty(v) || secretKey(k) || k == "message" && prefix == "" {
			continue
		}
		label := humanize(k)
		if prefix != "" {
			label = prefix + " · " + strings.ToLower(label[:1]) + label[1:]
		}
		switch x := v.(type) {
		case map[string]any:
			if depth < 2 {
				rows = append(rows, flatRows(label, x, depth+1)...)
			}
		case []any:
			if allScalar(x) {
				parts := make([]string, 0, len(x))
				for _, it := range x {
					parts = append(parts, plain(it))
				}
				rows = append(rows, [2]string{label, strings.Join(parts, ", ")})
			} else {
				rows = append(rows, [2]string{label, fmt.Sprintf("%d items", len(x))})
			}
		default:
			val := fmtValue(k, v)
			if idKey(k) {
				val = ui.DimStyle.Render(val)
			}
			rows = append(rows, [2]string{label, val})
		}
	}
	return rows
}

func allScalar(xs []any) bool {
	for _, x := range xs {
		if !scalar(x) {
			return false
		}
	}
	return true
}

// listTable renders a list of objects as a table of their plain fields.
func listTable(items []any, width int) string {
	var cols []string
	seen := map[string]bool{}
	for _, it := range items {
		m, _ := it.(map[string]any)
		for _, k := range viewKeys(m) {
			if !seen[k] && scalar(m[k]) && !secretKey(k) && !empty(m[k]) {
				seen[k] = true
				cols = append(cols, k)
			}
		}
	}
	sort.SliceStable(cols, func(i, j int) bool { return !idKey(cols[i]) && idKey(cols[j]) })
	if len(cols) > 6 {
		cols = append(cols[:5], cols[len(cols)-1])
	}
	if len(cols) == 0 {
		return ui.DimStyle.Render(fmt.Sprintf("%d items", len(items)))
	}
	head := make([]ui.Column, len(cols))
	for i, k := range cols {
		head[i] = ui.Column{Title: humanize(k), Shrink: true, Dim: idKey(k)}
	}
	rows := make([][]string, 0, len(items))
	for _, it := range items {
		m, _ := it.(map[string]any)
		row := make([]string, len(cols))
		for i, k := range cols {
			if !empty(m[k]) {
				row[i] = fmtValue(k, m[k])
			}
		}
		rows = append(rows, row)
	}
	return ui.Table(head, rows, width)
}

// renderData is the view for any answer without its own: a titled list of
// plain rows, nested lists as tables. Never JSON.
func renderData(title string, v any, width int) string {
	var b strings.Builder
	if title != "" {
		b.WriteString(ui.Section(title) + "\n")
	}
	switch x := v.(type) {
	case map[string]any:
		if msg := plain(x["message"]); msg != "" && scalar(x["message"]) {
			b.WriteString("  " + msg + "\n")
		}
		rows := flatRows("", x, 0)
		if len(rows) > 0 {
			b.WriteString(ui.KV(rows, 2) + "\n")
		}
		for _, k := range viewKeys(x) {
			if items, ok := x[k].([]any); ok && len(items) > 0 && !allScalar(items) && !secretKey(k) {
				b.WriteString("\n" + ui.Section(humanize(k)) + "\n" + listTable(items, width) + "\n")
			}
		}
		if len(rows) == 0 && b.Len() == len(ui.Section(title))+1 {
			b.WriteString("  " + ui.DimStyle.Render("Nothing to show.") + "\n")
		}
	case []any:
		if len(x) == 0 {
			b.WriteString("  " + ui.DimStyle.Render("Nothing to show.") + "\n")
		} else if allScalar(x) {
			for _, it := range x {
				b.WriteString("  " + plain(it) + "\n")
			}
		} else {
			b.WriteString(listTable(x, width) + "\n")
		}
	case nil:
		b.WriteString("  " + ui.DimStyle.Render("Done.") + "\n")
	default:
		b.WriteString("  " + plain(x) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// ── bots ─────────────────────────────────────────────────────────────────────

// botsListView is the human bots list: one page plus what is around it.
type botsListView struct {
	Bots    []any
	Total   int // summary.total_bots; 0 when unknown
	Start   int // 1-based index of the first row
	Page    int
	Limit   int
	HasMore bool
	All     bool
	Pick    int // in the home app: the highlighted row, else -1
}

func renderBotsList(v botsListView, width int) string {
	if len(v.Bots) == 0 {
		if v.Page > 1 {
			return ui.DimStyle.Render(fmt.Sprintf("No bots on page %d.", v.Page)) + " First page: " + ui.CmdStyle.Render("meetstream bots list")
		}
		return "No bots yet. Send one: " + ui.CmdStyle.Render("meetstream join <meeting link>")
	}
	cols := []ui.Column{{Title: "Bot", Shrink: true, Min: 16}, {Title: "Platform", Drop: 1}, {Title: "Status"}, {Title: "When"}, {Title: "Duration", Drop: 2}, {Title: "ID", Dim: true, Drop: 3}}
	rows := make([][]string, 0, len(v.Bots))
	for _, it := range v.Bots {
		m := object(it)
		status := pickStr(m, "status", "Status")
		when := pick(m, "created_at", "CreatedAt", "start_time", "StartTime")
		if strings.EqualFold(status, "scheduled") {
			when = pick(m, "start_time", "scheduled_join_time", "StartTime", "created_at", "CreatedAt")
		}
		rows = append(rows, []string{
			dash(pickStr(m, "bot_username", "BotUsername", "bot_name", "name")),
			dash(platformName(pickStr(m, "platform", "Platform"), pickStr(m, "meeting_url", "meeting_link", "MeetingLink"))),
			statusPill(status),
			dash(ui.When(when)),
			dash(ui.Duration(pick(m, "duration", "Duration"))),
			shortID(pickStr(m, "bot_id", "BotID", "id")),
		})
	}
	if v.Pick >= 0 && v.Pick < len(rows) {
		for i := range rows {
			if i == v.Pick {
				rows[i][0] = selStyle.Render("❯ ") + rows[i][0]
			} else {
				rows[i][0] = "  " + rows[i][0]
			}
		}
	}
	out := ui.Table(cols, rows, width)
	end := v.Start + len(v.Bots) - 1
	var foot string
	switch {
	case v.All || !v.HasMore && v.Start == 1:
		foot = "Showing all " + ui.Count(len(v.Bots))
	case v.Total > 0:
		foot = fmt.Sprintf("Showing %s–%s of %s", ui.Count(v.Start), ui.Count(end), ui.Count(v.Total))
	default:
		foot = fmt.Sprintf("Showing %s–%s", ui.Count(v.Start), ui.Count(end))
	}
	if v.Start == 1 && !v.All && v.HasMore {
		foot = strings.Replace(foot, "Showing 1–"+ui.Count(end), "Showing "+ui.Count(len(v.Bots)), 1)
	}
	if v.HasMore && !v.All {
		next := fmt.Sprintf("meetstream bots list --page %d", v.Page+1)
		if v.Limit != defaultBotsLimit {
			next += fmt.Sprintf(" --limit %d", v.Limit)
		}
		foot += " · more: " + ui.CmdStyle.Render(next)
	}
	return out + "\n" + ui.DimStyle.Render(foot) + "\n" + ui.DimStyle.Render("Details: meetstream bots get <id>")
}

// renderBotStatus is one status line: "15:04:05  ● In meeting".
func renderBotStatus(v any, clock string) string {
	m := object(v)
	line := ui.DimStyle.Render(clock) + "  " + statusPill(pickStr(m, "status", "Status"))
	if msg := pickStr(m, "message", "detail"); msg != "" {
		line += "  " + ui.DimStyle.Render(msg)
	}
	return line
}

// renderJoinResult is the success card after join.
func renderJoinResult(v any, name, link string, replay bool) string {
	m := object(v)
	id := pickStr(m, "bot_id", "BotID", "id")
	title := ui.Pill("Bot sent", ui.Green)
	if replay {
		title = ui.Pill("Already sent", ui.Yellow) + ui.DimStyle.Render("  (same idempotency key: no new bot)")
	}
	meeting := platformName("", link)
	if meeting == "" {
		meeting = "Meeting"
	}
	rows := [][2]string{
		{"Bot", orDefault(pickStr(m, "bot_name", "bot_username"), name)},
		{"Meeting", meeting + "  " + ui.DimStyle.Render(ui.Truncate(link, 60))},
		{"Status", statusPill(orDefault(pickStr(m, "status", "Status"), "joining"))},
	}
	if id != "" {
		rows = append(rows, [2]string{"ID", ui.DimStyle.Render(id)}, [2]string{"Watch", ui.CmdStyle.Render("meetstream bots status " + id + " --watch")})
	}
	if msg := pickStr(m, "message"); msg != "" && !strings.Contains(strings.ToLower(msg), "success") {
		rows = append(rows, [2]string{"Note", msg})
	}
	return ui.Card(ui.BoldStyle.Render(title) + "\n" + ui.KV(rows, 0))
}

// ── transcripts ──────────────────────────────────────────────────────────────

var speakerColors = []lipgloss.Color{"#22d3ee", "208", "#a78bfa", "#22c55e", "#f472b6", "#eab308"}

func segmentStart(m map[string]any) (float64, bool) {
	for _, k := range []string{"start_time", "start", "timestamp", "offset"} {
		if f, ok := num(m[k]); ok {
			return f, true
		}
	}
	if words, _ := m["words"].([]any); len(words) > 0 {
		for _, k := range []string{"start", "start_time"} {
			if f, ok := num(object(words[0])[k]); ok {
				return f, true
			}
		}
	}
	return 0, false
}

func num(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}

// renderTranscript shows who said what, with time from the start of the
// meeting, each speaker in their own colour, wrapped to width.
func renderTranscript(v any, width int) string {
	width = min(width, 100) // long lines are hard to read
	items, ok := v.([]any)
	if !ok {
		if s, ok := v.(string); ok {
			return ui.Wrap(s, width-2, 0)
		}
		return renderData("Transcript", v, width)
	}
	colors := map[string]lipgloss.Color{}
	var b strings.Builder
	last := ""
	for _, it := range items {
		m := object(it)
		line := transcriptText([]any{it})
		speaker, said, ok := strings.Cut(line, ": ")
		if !ok || said == "" {
			continue
		}
		start, hasTime := segmentStart(m)
		gutter := ""
		if hasTime {
			gutter = ui.DimStyle.Render(fmt.Sprintf("%-6s", ui.Clock(start))) + " "
		}
		hang := 2
		if hasTime {
			hang = 7
		}
		if speaker != last {
			c, ok := colors[speaker]
			if !ok {
				c = speakerColors[len(colors)%len(speakerColors)]
				colors[speaker] = c
			}
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(gutter + lipgloss.NewStyle().Foreground(c).Bold(true).Render(speaker) + "\n")
			b.WriteString(strings.Repeat(" ", hang) + ui.Wrap(said, width-hang-1, hang) + "\n")
			last = speaker
			continue
		}
		if !hasTime {
			gutter = "  "
		}
		b.WriteString(gutter + ui.Wrap(said, width-hang-1, hang) + "\n")
	}
	if b.Len() == 0 {
		return ui.DimStyle.Render("The transcript is empty.")
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderTranscriptions(v any, botID string, width int) string {
	items, _ := object(v)["transcriptions"].([]any)
	if items == nil {
		items, _ = v.([]any)
	}
	if len(items) == 0 {
		return "No transcripts for this bot yet. Start one: " + ui.CmdStyle.Render("meetstream transcribe "+botID)
	}
	rows := [][]string{}
	for _, it := range items {
		m := object(it)
		prov := pick(m, "provider", "Provider")
		name := plain(prov)
		if pm, ok := prov.(map[string]any); ok {
			for k := range pm {
				name = k
			}
		}
		rows = append(rows, []string{dash(providerName(name)), statusPill(pickStr(m, "status", "Status")), dash(ui.When(pick(m, "created_at", "CreatedAt", "updated_at"))), pickStr(m, "transcript_id", "TranscriptID", "id")})
	}
	cols := []ui.Column{{Title: "Provider"}, {Title: "Status"}, {Title: "Created"}, {Title: "Transcript ID", Dim: true, Shrink: true}}
	return ui.Table(cols, rows, width) + "\n" + ui.DimStyle.Render("Read it: meetstream transcript "+botID)
}

// ── recordings and balance ───────────────────────────────────────────────────

// firstURL is the first http(s) link in v (a recording answer is a small object).
func firstURL(v any) string {
	switch x := v.(type) {
	case string:
		if strings.HasPrefix(x, "http://") || strings.HasPrefix(x, "https://") {
			return x
		}
	case map[string]any:
		for _, k := range []string{"url", "audio_url", "video_url", "download_url", "link"} {
			if u := firstURL(x[k]); u != "" {
				return u
			}
		}
		for _, k := range viewKeys(x) {
			if u := firstURL(x[k]); u != "" {
				return u
			}
		}
	case []any:
		for _, it := range x {
			if u := firstURL(it); u != "" {
				return u
			}
		}
	}
	return ""
}

func renderRecordings(out map[string]any) string {
	var b strings.Builder
	b.WriteString(ui.Section("Recordings") + "  " + ui.DimStyle.Render("bot "+shortID(plain(out["bot_id"]))) + "\n")
	link := func(label, u string) {
		if u == "" {
			b.WriteString("  " + ui.DimStyle.Render(fmt.Sprintf("%-6s", label)) + "  " + ui.DimStyle.Render("not available") + "\n")
			return
		}
		b.WriteString("  " + ui.DimStyle.Render(fmt.Sprintf("%-6s", label)) + "  " + ui.CmdStyle.Render(u) + "\n")
	}
	link("Audio", firstURL(out["audio"]))
	link("Video", firstURL(out["video"]))
	for _, part := range []struct{ key, title string }{{"streams", "Video per person"}, {"audio_streams", "Audio per person"}} {
		items, _ := out[part.key].([]any)
		if m, ok := out[part.key].(map[string]any); ok && items == nil {
			for _, k := range viewKeys(m) {
				if l, ok := m[k].([]any); ok {
					items = l
					break
				}
			}
		}
		if len(items) == 0 {
			continue
		}
		b.WriteString("\n" + ui.Section(part.title) + "\n")
		for _, it := range items {
			m := object(it)
			who := pickStr(m, "participant_name", "name", "speaker", "participant")
			if pm, ok := m["participant"].(map[string]any); ok {
				who = pickStr(pm, "name", "id")
			}
			b.WriteString("  " + orDefault(who, "Someone") + "\n    " + ui.CmdStyle.Render(firstURL(it)) + "\n")
		}
	}
	b.WriteString(ui.DimStyle.Render("Links are signed and expire; run this again for fresh ones."))
	return b.String()
}

func renderBalance(v any) string {
	m := object(v)
	amount := "No prepaid balance"
	sub := "Billed by invoice"
	if m["balance_usd"] != nil {
		cur := orDefault(plain(m["currency"]), "USD")
		f, _ := num(m["balance_usd"])
		amount = fmt.Sprintf("%.2f %s", f, cur)
		if cur == "USD" {
			amount = fmt.Sprintf("$%.2f", f)
		}
		sub = "Prepaid"
	}
	if t := plain(m["customer_type"]); t != "" {
		sub = map[string]string{"prepaid": "Prepaid", "invoice": "Billed by invoice", "postpaid": "Billed by invoice", "enterprise": "Enterprise"}[strings.ToLower(t)]
		if sub == "" {
			sub = humanize(t)
		}
	}
	body := ui.DimStyle.Render("Balance") + "\n" + ui.TitleStyle.Render(amount) + "\n" + ui.DimStyle.Render(sub)
	if u := dashboardURL("/billing"); u != "" {
		body += "\n\nTop up: " + ui.CmdStyle.Render(u)
	}
	return ui.Card(body)
}

// ── MIA agents ───────────────────────────────────────────────────────────────

func miaBlock(m map[string]any, name string) map[string]any {
	b := object(pick(m, strings.ToLower(name), name))
	if b == nil {
		b = map[string]any{}
	}
	return b
}

// miaHowItRuns says who runs the agent's brain and whose keys it uses.
func miaHowItRuns(m map[string]any) string {
	model := miaBlock(m, "Model")
	if strings.EqualFold(pickStr(model, "provider"), "local_agent") {
		local := object(pick(model, "local_agent", "config"))
		label := pickStr(local, "label", "agent")
		s := "Your agent"
		if label != "" {
			s += " · " + label
		}
		if dev := pickStr(local, "device_name", "device_label"); dev != "" {
			s += " on " + dev
		}
		return s
	}
	sections := []map[string]any{model}
	if !strings.EqualFold(pickStr(m, "Mode", "mode"), "realtime") {
		sections = append(sections, miaBlock(m, "Transcriber"), miaBlock(m, "Voice"))
	}
	hosted := 0
	for _, s := range sections {
		if strings.EqualFold(pickStr(s, "key_source"), keysMeetStream) {
			hosted++
		}
	}
	switch hosted {
	case len(sections):
		return "MeetStream hosted"
	case 0:
		return "Your keys"
	}
	return "MeetStream + your keys"
}

func miaVoice(m map[string]any) string {
	voice := miaBlock(m, "Voice")
	if len(voice) == 0 {
		model := miaBlock(m, "Model")
		if strings.EqualFold(pickStr(m, "Mode", "mode"), "realtime") {
			return providerName(pickStr(model, "provider")) + " · built in"
		}
		return ""
	}
	s := providerName(pickStr(voice, "provider"))
	if name := pickStr(voice, "voice_name", "voice", "voice_id", "name"); name != "" {
		s += " · " + voiceDisplay(name)
	}
	return s
}

// voiceDisplay shows a readable voice name; provider voice IDs (long opaque
// strings such as ElevenLabs IDs) read as "custom voice".
func voiceDisplay(name string) string {
	if opaqueVoiceID.MatchString(name) {
		return "custom voice"
	}
	return name
}

var opaqueVoiceID = regexp.MustCompile(`^[A-Za-z0-9]{16,}$`)

func renderMiaList(v any, width int) string {
	m := object(v)
	items, _ := m["agent_configs"].([]any)
	if items == nil {
		items, _ = v.([]any)
	}
	if len(items) == 0 {
		return "No AI agents yet. Bring one into your meetings: " + ui.CmdStyle.Render("meetstream setup")
	}
	sort.SliceStable(items, func(i, j int) bool {
		ti, _ := ui.ParseTime(pick(object(items[i]), "UpdatedAt", "updated_at", "CreatedAt"))
		tj, _ := ui.ParseTime(pick(object(items[j]), "UpdatedAt", "updated_at", "CreatedAt"))
		return ti.After(tj)
	})
	rows := [][]string{}
	for _, it := range items {
		a := object(it)
		rows = append(rows, []string{
			dash(pickStr(a, "AgentName", "agent_name", "name")),
			miaHowItRuns(a),
			dash(miaVoice(a)),
			dash(ui.When(pick(a, "UpdatedAt", "updated_at", "CreatedAt", "created_at"))),
			shortID(pickStr(a, "AgentConfigID", "agent_config_id", "id")),
		})
	}
	cols := []ui.Column{{Title: "Name", Shrink: true, Min: 12}, {Title: "How it runs", Shrink: true, Min: 12}, {Title: "Voice", Shrink: true, Min: 10, Drop: 2}, {Title: "Updated", Drop: 1}, {Title: "ID", Dim: true, Drop: 3}}
	foot := fmt.Sprintf("%d agent", len(items))
	if len(items) != 1 {
		foot += "s"
	}
	return ui.Table(cols, rows, width) + "\n" + ui.DimStyle.Render(foot+" · details: meetstream mia get <id>")
}

var accessLabels = map[string]string{"readonly": "Read only", "confirm": "Asks before changes", "full": "Full access"}

func renderMiaGet(v any, width int) string {
	a := object(v)
	if inner := object(a["agent_config"]); inner != nil {
		a = inner
	}
	if len(a) == 0 {
		return ui.DimStyle.Render("No agent found.")
	}
	name := orDefault(pickStr(a, "AgentName", "agent_name", "name"), "Agent")
	id := pickStr(a, "AgentConfigID", "agent_config_id", "id")
	var b strings.Builder
	b.WriteString(ui.TitleStyle.Render(name) + "  " + ui.DimStyle.Render(id) + "\n")
	b.WriteString(ui.DimStyle.Render(miaIntro) + "\n")
	section := func(title string, rows [][2]string) {
		kept := rows[:0]
		for _, r := range rows {
			if r[1] != "" {
				kept = append(kept, r)
			}
		}
		if len(kept) == 0 {
			return
		}
		b.WriteString("\n" + ui.Section(title) + "\n" + ui.KV(kept, 2) + "\n")
	}
	keys := func(s map[string]any) string {
		if len(s) == 0 {
			return ""
		}
		if strings.EqualFold(pickStr(s, "key_source"), keysMeetStream) {
			return "MeetStream's"
		}
		return "Yours"
	}
	model := miaBlock(a, "Model")
	ears := miaBlock(a, "Transcriber")
	voice := miaBlock(a, "Voice")
	how := "Listens, thinks, then speaks (speech to text → model → voice)"
	if strings.EqualFold(pickStr(a, "Mode", "mode"), "realtime") {
		how = "One realtime speech model listens and speaks"
	}
	section("How it runs", [][2]string{{"Runs", miaHowItRuns(a)}, {"Pipeline", how}, {"Updated", ui.When(pick(a, "UpdatedAt", "updated_at"))}})
	section("Ears", [][2]string{{"Provider", providerName(pickStr(ears, "provider"))}, {"Model", pickStr(ears, "model")}, {"Language", pickStr(ears, "language", "language_code")}, {"Keys", keys(ears)}})
	brain := [][2]string{}
	if strings.EqualFold(pickStr(model, "provider"), "local_agent") {
		local := object(pick(model, "local_agent", "config"))
		brain = append(brain, [2]string{"Agent", pickStr(local, "label", "agent")}, [2]string{"Model", orDefault(pickStr(model, "model"), "the agent's default")},
			[2]string{"Access", orDefault(accessLabels[pickStr(local, "access")], pickStr(local, "access"))})
		if local["require_code"] == true {
			brain = append(brain, [2]string{"Safety", "Asks for a code before acting"})
		}
	} else {
		brain = append(brain, [2]string{"Provider", providerName(pickStr(model, "provider"))}, [2]string{"Model", pickStr(model, "model")}, [2]string{"Keys", keys(model)})
		if e := pickStr(model, "reasoning_effort"); e != "" {
			brain = append(brain, [2]string{"Effort", e})
		}
	}
	if p := pickStr(model, "system_prompt", "prompt"); p != "" {
		brain = append(brain, [2]string{"Instructions", ui.Truncate(strings.Join(strings.Fields(p), " "), max(20, width-20))})
	}
	if p := pickStr(model, "first_message"); p != "" {
		brain = append(brain, [2]string{"Opens with", ui.Truncate(p, max(20, width-20))})
	}
	section("Brain", brain)
	section("Voice", [][2]string{{"Provider", providerName(pickStr(voice, "provider"))}, {"Voice", pickStr(voice, "voice_name", "voice", "voice_id")}, {"Model", pickStr(voice, "model")}, {"Speed", pickStr(voice, "speed")}, {"Keys", keys(voice)}})
	behaviour := [][2]string{}
	if words, _ := object(pick(a, "WakeWord", "wake_word"))["words"].([]any); len(words) > 0 {
		parts := []string{}
		for _, w := range words {
			parts = append(parts, plain(w))
		}
		behaviour = append(behaviour, [2]string{"Wakes on", strings.Join(parts, ", ")})
	}
	agent := miaBlock(a, "Agent")
	if mod := pickStr(agent, "response_modality"); mod != "" {
		behaviour = append(behaviour, [2]string{"Answers", map[string]string{"audio": "Out loud", "text": "In the chat"}[mod]})
	}
	for _, k := range viewKeys(agent) {
		if k == "response_modality" || !scalar(agent[k]) || empty(agent[k]) || secretKey(k) || idKey(k) {
			continue
		}
		behaviour = append(behaviour, [2]string{humanize(k), fmtValue(k, agent[k])})
	}
	section("Behaviour", behaviour)
	return strings.TrimRight(b.String(), "\n")
}

// ── calendar ─────────────────────────────────────────────────────────────────

func renderCalendarList(v any, settings map[string]any, width int) string {
	m := object(v)
	var b strings.Builder
	conns, _ := m["connections"].([]any)
	cals, _ := m["calendars"].([]any)
	if len(conns) == 0 && len(cals) == 0 {
		return "No calendar connected. Connect one: " + ui.CmdStyle.Render("meetstream calendar connect")
	}
	auto := ""
	if settings != nil {
		auto = "Off"
		if settings["auto_schedule_enabled"] == true {
			auto = "On"
		}
	}
	if len(conns) > 0 {
		b.WriteString(ui.Section("Connected accounts") + "\n")
		rows := [][]string{}
		for _, it := range conns {
			c := object(it)
			account := pickStr(c, "account_id", "account_email", "email")
			if account == "" {
				account = ui.DimStyle.Render("(first connection)")
			}
			join := auto
			if rule := pickStr(c, "join_rule"); rule != "" && join != "Off" {
				join = strings.TrimSpace(join + " · " + map[string]string{"accepted": "meetings you host or accepted", "all": "every meeting with a link"}[rule])
			}
			state := ""
			if e := pickStr(c, "error"); e != "" {
				state = ui.Pill("Needs reconnecting", ui.Red)
			}
			rows = append(rows, []string{providerName(pickStr(c, "provider")), account, plain(pick(c, "calendars_count")), dash(join), state})
		}
		cols := []ui.Column{{Title: "Provider"}, {Title: "Account", Shrink: true, Min: 12}, {Title: "Calendars"}, {Title: "Auto-join", Shrink: true}, {Title: ""}}
		if !anyNonEmpty(rows, 4) {
			cols = cols[:4]
			for i := range rows {
				rows[i] = rows[i][:4]
			}
		}
		b.WriteString(ui.Table(cols, rows, width) + "\n")
	}
	if len(cals) > 0 {
		b.WriteString("\n" + ui.Section("Calendars") + "\n")
		rows := [][]string{}
		for _, it := range cals {
			c := object(it)
			primary := ""
			if c["isPrimary"] == true || c["primary"] == true {
				primary = ui.Pill("primary", ui.Green)
			}
			rows = append(rows, []string{dash(pickStr(c, "summary", "name")), dash(humanize(pickStr(c, "accessRole", "access_role", "access"))), primary, dash(pickStr(c, "account_id"))})
		}
		cols := []ui.Column{{Title: "Calendar", Shrink: true, Min: 12}, {Title: "Access"}, {Title: ""}, {Title: "Account", Dim: true, Shrink: true, Min: 12, Drop: 1}}
		if len(conns) <= 1 { // one account: the column would repeat it on every row
			cols = cols[:3]
			for i := range rows {
				rows[i] = rows[i][:3]
			}
		}
		b.WriteString(ui.Table(cols, rows, width) + "\n")
	}
	hint := "Upcoming meetings: meetstream calendar events"
	if auto == "Off" {
		hint += " · turn on auto-join: meetstream calendar auto-join on"
	}
	b.WriteString(ui.DimStyle.Render(hint))
	return b.String()
}

func anyNonEmpty(rows [][]string, col int) bool {
	for _, r := range rows {
		if r[col] != "" {
			return true
		}
	}
	return false
}

func renderEvents(v any, width int) string {
	m := object(v)
	items, _ := m["results"].([]any)
	if items == nil {
		items, _ = m["events"].([]any)
	}
	if items == nil {
		items, _ = v.([]any)
	}
	if len(items) == 0 {
		return "No upcoming meetings with a link. " + ui.DimStyle.Render("Connected calendars: meetstream calendar list")
	}
	rows := [][]string{}
	for _, it := range items {
		e := object(it)
		raw := object(e["raw"])
		title := pickStr(e, "summary", "title", "event_summary")
		if title == "" {
			title = pickStr(raw, "summary", "subject")
		}
		bot := ui.DimStyle.Render("not scheduled")
		if bots, _ := e["bots"].([]any); len(bots) > 0 {
			bot = statusPill(pickStr(object(bots[0]), "status", "Status"))
		}
		rows = append(rows, []string{dash(ui.When(pick(e, "start_time", "start"))), dash(title), dash(platformName(pickStr(e, "meeting_platform"), pickStr(e, "meeting_url", "meeting_link"))), bot, pickStr(e, "id")})
	}
	cols := []ui.Column{{Title: "When"}, {Title: "Meeting", Shrink: true, Min: 14}, {Title: "Platform"}, {Title: "Bot"}, {Title: "Event ID", Dim: true, Shrink: true, Min: 8}}
	foot := fmt.Sprintf("%d meetings", len(items))
	if m["has_more"] == true {
		foot = fmt.Sprintf("Showing the next %d meetings", len(items))
	}
	return ui.Table(cols, rows, width) + "\n" + ui.DimStyle.Render(foot+" · send a bot to one: meetstream calendar schedule <event id>")
}

// ── links ────────────────────────────────────────────────────────────────────

// dashboardURL is a page on the MeetStream dashboard, only when the CLI talks
// to the production API; a test server has its own dashboard we cannot guess.
func dashboardURL(path string) string {
	if !prodAPI() {
		return ""
	}
	return "https://app.meetstream.ai" + path
}

func prodAPI() bool {
	return strings.TrimRight(config.APIURL(), "/") == strings.TrimRight(config.DefaultAPIURL, "/")
}

// serverLabel is "MeetStream", or "test server: <host>" for any other API URL.
func serverLabel() string {
	if prodAPI() {
		return "MeetStream"
	}
	u, err := url.Parse(config.APIURL())
	if err != nil || u.Host == "" {
		return "test server: " + config.APIURL()
	}
	return "test server: " + u.Host
}

// ── which view for which command ─────────────────────────────────────────────

// viewTitles name the generic view of commands without their own.
var viewTitles = map[string]string{
	"bots get": "Bot", "bot detail": "Bot", "bots timeline": "Speaker timeline", "bot timeline": "Speaker timeline",
	"bots remove": "Leaving the meeting", "bot remove": "Leaving the meeting", "bots delete": "Bot data", "bot delete": "Bot data",
	"bots say": "Message sent", "bot send-message": "Message sent", "bots show-image": "Image shown", "bot send-image": "Image shown",
	"bots hand": "Hand", "bot raise-hand": "Hand raised", "bot lower-hand": "Hand lowered", "transcribe": "Transcription started",
	"mia create": "Agent created", "mia update": "Agent updated", "mia delete": "Agent deleted", "calendar schedule": "Bot scheduled",
	"calendar unschedule": "Bot unscheduled", "calendar reschedule": "Bot rescheduled", "calendar cancel": "Scheduled bot cancelled",
	"calendar auto-join": "Auto-join", "calendar connect": "Calendar connected",
}

// humanView picks the view for a command's answer by its path ("mia list").
func humanView(cmd *cobra.Command, args []string, v any) string {
	path := strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()+" ")
	path = strings.Replace(path, "agents ", "mia ", 1)
	w := ui.Width()
	switch path {
	case "mia list":
		return renderMiaList(v, w)
	case "mia get":
		return renderMiaGet(v, w)
	case "calendar events":
		return renderEvents(v, w)
	case "transcriptions":
		return renderTranscriptions(v, args[0], w)
	case "recordings":
		return renderRecordings(object(v))
	}
	if strings.HasPrefix(path, "logins ") {
		return renderData(loginsTitle(path), v, w)
	}
	title, ok := viewTitles[path]
	if !ok {
		title = humanize(cmd.Name())
	}
	if len(args) > 0 && (strings.HasPrefix(path, "bots ") || strings.HasPrefix(path, "bot ")) {
		title += "  " + ui.DimStyle.Render(shortID(args[0]))
	}
	return renderData(title, v, w)
}

func loginsTitle(path string) string {
	parts := strings.Fields(path) // logins teams domains
	if len(parts) < 3 {
		return "Signed-in accounts"
	}
	platform := map[string]string{"teams": "Teams", "google": "Google"}[parts[1]]
	switch parts[2] {
	case "domains":
		return platform + " sign-in domains"
	case "list":
		return platform + " signed-in accounts"
	}
	return platform + ": " + strings.ReplaceAll(parts[2], "-", " ")
}
