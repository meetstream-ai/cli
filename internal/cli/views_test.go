package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/meetstream-ai/cli/internal/ui"
)

// Demo data for the human views: realistic shapes (from the API handlers),
// many bots, missing fields, long names. No real names, emails or ids.

var viewNow = time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC)

func ago(d time.Duration) string { return viewNow.Add(-d).Format(time.RFC3339) }

func demoBots(n, offset int) []any {
	names := []string{"Weekly product sync", "MeetStream Bot", "", "Customer onboarding call with the very long name that will not fit", "Notetaker", "Design review"}
	platforms := []string{"GMeet", "Zoom", "Teams", "", "ZoomRTMS"}
	statuses := []string{"Done", "InMeeting", "Scheduled", "Cancelled", "Failed", "Stopped", "Joining", "Denied"}
	links := []string{"https://meet.google.com/abc-defg-hij", "https://us02web.zoom.us/j/123456789", "https://teams.microsoft.com/l/meetup-join/x", "https://meet.google.com/xyz-abcd-efg", ""}
	out := make([]any, 0, n)
	for i := offset; i < offset+n; i++ {
		b := map[string]any{
			"bot_id":       fmt.Sprintf("%08x-1111-4222-8333-%012d", 0xa1b2c300+i, i),
			"platform":     platforms[i%len(platforms)],
			"status":       statuses[i%len(statuses)],
			"bot_username": names[i%len(names)],
			"created_at":   ago(time.Duration(i*5+3) * time.Hour),
			"meeting_url":  links[i%len(links)],
		}
		if b["status"] == "Scheduled" {
			b["start_time"] = viewNow.Add(19 * time.Hour).Format(time.RFC3339)
		}
		if i%3 != 2 {
			b["duration"] = float64(600 + i*317)
		}
		if b["bot_username"] == "" {
			delete(b, "bot_username")
		}
		out = append(out, b)
	}
	return out
}

func botsPageJSON(bots []any, next string, total int) string {
	page := map[string]any{"bots": bots, "hasNextPage": next != "", "nextCursor": nil, "summary": map[string]any{"total_bots": total, "total_duration_seconds": 1234567, "active_now": 1, "scheduled_bots": 2}}
	if next != "" {
		page["nextCursor"] = next
	}
	b, _ := json.Marshal(page)
	return string(b)
}

const demoCursor = "eyJlayI6IHsiQm90SUQiOiAiZGVtbyJ9fQ=="

const demoMia = `{"agent_configs":[
 {"AgentConfigID":"5f1c2d3e-aaaa-4bbb-8ccc-000000000001","AgentName":"Sales assistant","Mode":"pipeline","UpdatedAt":"2026-11-20T13:00:00Z",
  "Model":{"provider":"openai","model":"gpt-5-mini","key_source":"meetstream","system_prompt":"You are MIA, a helpful assistant.","temperature":0.8},
  "Voice":{"provider":"elevenlabs","voice_id":"rachel","model":"eleven_turbo_v2_5","key_source":"meetstream","api_key":"sk-••••"},
  "Transcriber":{"provider":"deepgram","model":"nova-3","language":"en","key_source":"meetstream"},
  "Agent":{"response_modality":"audio","interruptible":true},"WakeWord":{"words":["hey mia","mia"]}},
 {"AgentConfigID":"9a07be41-aaaa-4bbb-8ccc-000000000002","AgentName":"Engineering standup helper with a long name","Mode":"pipeline","UpdatedAt":"2026-11-18T09:30:00Z",
  "Model":{"provider":"anthropic","model":"claude-sonnet-4-5"},"Voice":{"provider":"cartesia","voice_id":"sonic-english"},"Transcriber":{"provider":"assemblyai"}},
 {"AgentConfigID":"c3d84f20-aaaa-4bbb-8ccc-000000000003","AgentName":"My Claude Code","Mode":"pipeline","UpdatedAt":"2026-11-20T14:20:00Z",
  "Model":{"provider":"local_agent","model":"sonnet","local_agent":{"transport":"relay","device_id":"dev_demo01","label":"Claude Code","access":"readonly","require_code":true}},
  "Voice":{"provider":"openai","voice_id":"alloy","key_source":"meetstream"},"Transcriber":{"provider":"deepgram","key_source":"meetstream"}}
],"count":3}`

const demoCalendars = `{"calendars":[
 {"id":"primary","summary":"demo@example.com","isPrimary":true,"accessRole":"owner","provider":"google","account_id":"demo@example.com"},
 {"id":"team","summary":"Team calendar","isPrimary":false,"accessRole":"reader","provider":"google","account_id":"demo@example.com"},
 {"id":"holidays","summary":"Holidays","accessRole":"reader","provider":"google","account_id":"demo@example.com"}],
 "connections":[{"provider":"google","account_id":"demo@example.com","calendars_count":3,"is_legacy":false}],"total":3,"user_id":"u-demo"}`

const demoTranscript = `{"message":[
 {"speaker":"Alex Rivera","start_time":0.4,"transcript":"Good morning, everyone. Thanks for joining the weekly product sync. Let's start with the launch checklist and then go through open questions."},
 {"speaker":"Alex Rivera","start_time":14.2,"transcript":"Sam, can you give us the status on the calendar integration?"},
 {"speaker":"Sam Chen","start_time":21.9,"words":[{"word":"Sure.","start":21.9},{"word":"Google","start":22.3},{"word":"is","start":22.6},{"word":"done","start":22.8},{"word":"and","start":23.0},{"word":"Microsoft","start":23.2},{"word":"ships","start":23.6},{"word":"next","start":23.9},{"word":"week.","start":24.1}]},
 {"participant":{"name":"Jordan Lee"},"start_time":75,"transcript":"Great. One question from support: can customers choose which meetings the bot joins?"},
 {"speaker":"Sam Chen","start_time":83.5,"transcript":"Yes. The join rule is either meetings you host or accepted, or every meeting with a link."}]}`

// viewServer answers by "METHOD /path?query" and records what was asked.
func viewServer(t *testing.T, routes map[string]string) (map[string]string, *[]string) {
	t.Helper()
	var seen []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.RequestURI()
		seen = append(seen, key)
		body, ok := routes[key]
		if !ok {
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"detail":"no route"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	})
	old := http.DefaultTransport
	http.DefaultTransport = m3Transport{handler}
	t.Cleanup(func() { http.DefaultTransport = old })
	return map[string]string{"MEETSTREAM_API_KEY": "test-key", "MEETSTREAM_API_URL": "http://meetstream.test"}, &seen
}

// runBoth runs the CLI and returns stdout, stderr (where human output goes) and the exit code.
func runBoth(t *testing.T, env map[string]string, args ...string) (string, string, int) {
	t.Helper()
	r, w, _ := os.Pipe()
	old := os.Stderr
	os.Stderr = w
	done := make(chan []byte)
	go func() { b, _ := io.ReadAll(r); done <- b }()
	out, code := run(t, env, args...)
	w.Close()
	os.Stderr = old
	return out, string(<-done), code
}

// sameJSON is what emit wrote before this change: the API answer re-encoded
// with two-space indent. --json must stay byte-for-byte this.
func sameJSON(t *testing.T, raw string) string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
	return b.String()
}

func pinViews(t *testing.T) {
	t.Helper()
	old := ui.Now
	ui.Now = func() time.Time { return viewNow }
	t.Cleanup(func() { ui.Now = old })
	t.Setenv("COLUMNS", "110")
}

func assertHuman(t *testing.T, name, out string, want []string) {
	t.Helper()
	if os.Getenv("FORCE_COLOR") == "" && strings.Contains(out, "\x1b[") {
		t.Errorf("%s: colour codes with no terminal", name)
	}
	out = stripANSI(out)
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("%s: missing %q in:\n%s", name, w, out)
		}
	}
	for _, bad := range []string{`{"`, `":"`, `[{`, demoCursor, "nextCursor", "agent_config", "key_source", "local_agent"} {
		if strings.Contains(out, bad) {
			t.Errorf("%s: human output contains %q:\n%s", name, bad, out)
		}
	}
	for _, l := range strings.Split(out, "\n") {
		if w := len([]rune(l)); w > 110 {
			t.Errorf("%s: line wider than the terminal (%d): %q", name, w, l)
		}
	}
}

func TestHumanViews(t *testing.T) {
	pinViews(t)
	page1 := botsPageJSON(demoBots(10, 0), demoCursor, 7334)
	page2 := botsPageJSON(demoBots(10, 10), demoCursor, 7334)
	bigPage := botsPageJSON(demoBots(50, 0), demoCursor, 7334)
	routes := map[string]string{
		"GET /bots":          bigPage,
		"GET /bots?limit=10": page1,
		"GET /bots?cursor=" + strings.ReplaceAll(demoCursor, "=", "%3D") + "&limit=10": page2,
		"GET /mia":                                    demoMia,
		"GET /calendar/calendars":                     demoCalendars,
		"GET /calendar/auto-schedule/settings":        `{"auto_schedule_enabled":true,"default_bot_config":{"bot_name":"Notetaker"}}`,
		"GET /calendar/events":                        `{"next":"Y3Vyc29y","previous":null,"has_more":true,"results":[{"id":"ev1","start_time":"2026-11-21T10:00:00Z","meeting_platform":"google_meet","meeting_url":"https://meet.google.com/abc-defg-hij","raw":{"summary":"Weekly product sync"},"bots":[{"id":"b1","status":"Scheduled"}]},{"id":"ev2","start_time":"2026-11-20T15:30:00Z","meeting_url":"https://us02web.zoom.us/j/1","raw":{"subject":"Customer call"},"bots":[]}]}`,
		"GET /bots/b1/detail":                         `{"transcript_id":"t1"}`,
		"GET /transcript/t1/get_transcript?raw=false": demoTranscript,
		"GET /bots/b1/transcriptions":                 `{"transcriptions":[{"transcript_id":"t1","status":"completed","provider":{"deepgram":{"model":"nova-3"}},"created_at":"2026-11-20T12:00:00Z"}]}`,
		"GET /bots/b1/get_audio":                      `{"audio_url":"https://files.example.com/audio.mp3"}`,
		"GET /bots/b1/get_video":                      `{"video_url":"https://files.example.com/video.mp4"}`,
		"GET /bots/b1/get_recording_streams":          `[{"participant":{"name":"Alex Rivera"},"url":"https://files.example.com/alex.mp4"}]`,
		"GET /bots/b1/get_audio_streams":              `{"detail":"none"}`,
		"GET /billing/balance":                        `{"balance_usd":42.5,"currency":"USD","customer_type":"prepaid"}`,
		"GET /bots/b1/status":                         `{"status":"InMeeting","bot_id":"b1"}`,
		"GET /teams-login-domains":                    `[{"domain":"example.com","name":"Example","login_mode":"always","created_at":"2026-11-01T00:00:00Z"}]`,
		"GET /bots/b1/get_speaker_timeline":           `{"speaker_timeline":[{"speaker":"Alex Rivera","start":0.4,"end":14},{"speaker":"Sam Chen","start":21.9,"end":30}]}`,
	}
	routes["GET /mia?agent_config_id=a3"] = `{"agent_config":{"AgentConfigID":"a3","AgentName":"My Claude Code","Mode":"pipeline","Model":{"provider":"local_agent","model":"sonnet","local_agent":{"label":"Claude Code","access":"readonly","require_code":true},"system_prompt":"You are MIA."},"Voice":{"provider":"openai","voice_id":"alloy","key_source":"meetstream"},"Transcriber":{"provider":"deepgram","model":"nova-3","language":"en","key_source":"meetstream"},"Agent":{"response_modality":"audio"},"WakeWord":{"words":["hey mia"]},"UpdatedAt":"2026-11-20T14:20:00Z"}}`
	env, seen := viewServer(t, routes)

	for _, tc := range []struct {
		name string
		args []string
		want []string
		dump string
	}{
		{"bots list", []string{"bots", "list"}, []string{"Weekly product sync", "Google Meet", "Zoom", "Teams", "● Done", "● In meeting", "● Scheduled", "○ Cancelled", "● Failed", "3 h ago", "tomorrow 10:00", "Showing 10 of 7,334", "meetstream bots list --page 2", "a1b2c300", "…"}, "MEETSTREAM_DUMP_BOTS"},
		{"bots list page 2", []string{"bots", "list", "--page", "2"}, []string{"Showing 11–20 of 7,334", "--page 3"}, ""},
		{"mia list", []string{"mia", "list"}, []string{"Sales assistant", "MeetStream hosted", "Your keys", "Your agent · Claude Code", "ElevenLabs · rachel", "Cartesia", "2 h ago", "3 agents"}, "MEETSTREAM_DUMP_MIA"},
		{"mia get", []string{"mia", "get", "a3"}, []string{"My Claude Code", "MIA is MeetStream's AI meeting agent", "How it runs", "Ears", "Brain", "Voice", "Behaviour", "Read only", "Asks for a code", "Out loud", "hey mia", "MeetStream's"}, ""},
		{"calendar list", []string{"calendar", "list"}, []string{"Connected accounts", "Google", "demo@example.com", "Calendars", "Team calendar", "primary", "Owner", "On"}, "MEETSTREAM_DUMP_CALENDAR"},
		{"calendar events", []string{"calendar", "events"}, []string{"Weekly product sync", "Customer call", "tomorrow 10:00", "in 30 min", "● Scheduled", "not scheduled", "Google Meet"}, ""},
		{"transcript", []string{"transcript", "b1"}, []string{"00:00", "Alex Rivera", "Sam Chen", "Jordan Lee", "01:15", "Google is done and Microsoft ships next week."}, "MEETSTREAM_DUMP_TRANSCRIPT"},
		{"transcriptions", []string{"transcriptions", "b1"}, []string{"Deepgram", "● Done", "3 h ago", "meetstream transcript b1"}, ""},
		{"recordings", []string{"recordings", "b1"}, []string{"Audio", "https://files.example.com/audio.mp3", "Video", "Video per person", "Alex Rivera"}, ""},
		{"balance", []string{"balance"}, []string{"$42.50", "Prepaid"}, ""},
		{"status", []string{"bots", "status", "b1"}, []string{"● In meeting"}, ""},
		{"logins", []string{"logins", "teams", "domains"}, []string{"Teams sign-in domains", "example.com", "Always"}, ""},
		{"timeline", []string{"bots", "timeline", "b1"}, []string{"Speaker timeline", "Alex Rivera", "Sam Chen"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, human, code := runBoth(t, env, tc.args...)
			if code != 0 {
				t.Fatalf("code=%d\n%s", code, human)
			}
			assertHuman(t, tc.name, human, tc.want)
			if p := os.Getenv(tc.dump); tc.dump != "" && p != "" {
				t.Setenv("FORCE_COLOR", "1")
				_, colour, _ := runBoth(t, env, tc.args...)
				if err := os.WriteFile(p, []byte(colour), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	if strings.Contains(strings.Join(*seen, "\n"), "GET /bots\n") {
		t.Error("human bots list fetched the full default page instead of 10")
	}
}

// TestJSONUnchanged: --json on every command with a new human view prints
// exactly what it printed before (the API answer, indented), and asks the
// same requests (no extra calls, no new query parameters).
func TestJSONUnchanged(t *testing.T) {
	pinViews(t)
	bigPage := botsPageJSON(demoBots(50, 0), demoCursor, 7334)
	for _, tc := range []struct {
		args  []string
		route string
		body  string
	}{
		{[]string{"bots", "list", "--json"}, "GET /bots", bigPage},
		{[]string{"mia", "list", "--json"}, "GET /mia", demoMia},
		{[]string{"agents", "list", "--json"}, "GET /mia", demoMia},
		{[]string{"mia", "get", "a1", "--json"}, "GET /mia?agent_config_id=a1", `{"agent_config":{"AgentConfigID":"a1","Model":{"provider":"openai"}}}`},
		{[]string{"calendar", "list", "--json"}, "GET /calendar/calendars", demoCalendars},
		{[]string{"calendar", "events", "--json"}, "GET /calendar/events", `{"next":"Y3Vyc29y","previous":null,"has_more":true,"results":[{"id":"ev1"}]}`},
		{[]string{"transcriptions", "b1", "--json"}, "GET /bots/b1/transcriptions", `{"transcriptions":[{"transcript_id":"t1","status":"completed"}]}`},
		{[]string{"balance", "--json"}, "GET /billing/balance", `{"balance_usd":42.5,"currency":"USD","customer_type":"prepaid"}`},
		{[]string{"bots", "status", "b1", "--json"}, "GET /bots/b1/status", `{"status":"InMeeting"}`},
		{[]string{"bots", "get", "b1", "--json"}, "GET /bots/b1/detail", `{"bot_details":{"transcript_id":"t1","status":"Done"}}`},
		{[]string{"logins", "teams", "domains", "--json"}, "GET /teams-login-domains", `[{"domain":"example.com"}]`},
		{[]string{"join", "https://meet.google.com/abc-defg-hij", "--json"}, "POST /bots/create_bot", `{"bot_id":"b1","transcript_id":"t1","message":"Bot created"}`},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			env, seen := viewServer(t, map[string]string{tc.route: tc.body})
			out, human, code := runBoth(t, env, tc.args...)
			if code != 0 {
				t.Fatalf("code=%d stderr=%s", code, human)
			}
			if out != sameJSON(t, tc.body) {
				t.Fatalf("--json changed:\n got %s\nwant %s", out, sameJSON(t, tc.body))
			}
			if len(*seen) != 1 || (*seen)[0] != tc.route {
				t.Fatalf("requests = %v, want only %s", *seen, tc.route)
			}
		})
	}
	// Transcript: the segments, as before.
	env, _ := viewServer(t, map[string]string{"GET /bots/b1/detail": `{"transcript_id":"t1"}`, "GET /transcript/t1/get_transcript?raw=false": demoTranscript})
	out, _, code := runBoth(t, env, "transcript", "b1", "--json")
	var full map[string]any
	_ = json.Unmarshal([]byte(demoTranscript), &full)
	b, _ := json.Marshal(full["message"])
	if code != 0 || out != sameJSON(t, string(b)) {
		t.Fatalf("transcript --json changed: %s", out)
	}
}

func TestBotsListPagingAndEmpty(t *testing.T) {
	pinViews(t)
	env, seen := viewServer(t, map[string]string{
		"GET /bots?limit=10":           botsPageJSON(nil, "", 0),
		"GET /bots?limit=5":            botsPageJSON(demoBots(3, 0), "", 3),
		"GET /bots?limit=50":           botsPageJSON(demoBots(50, 0), "c2", 60),
		"GET /bots?cursor=c2&limit=50": botsPageJSON(demoBots(10, 50), "", 60),
	})
	_, human, code := runBoth(t, env, "bots", "list")
	if code != 0 || !strings.Contains(human, "No bots yet. Send one: meetstream join <meeting link>") {
		t.Fatalf("empty: code=%d %s", code, human)
	}
	_, human, _ = runBoth(t, env, "bots", "list", "--limit", "5")
	assertHuman(t, "short", human, []string{"Showing all 3"})
	out, _, code := runBoth(t, env, "bots", "list", "--all", "--json")
	var all map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &all) != nil || len(all["bots"].([]any)) != 60 || all["nextCursor"] != nil {
		t.Fatalf("--all --json: code=%d %.200s", code, out)
	}
	_, human, _ = runBoth(t, env, "bots", "list", "--all")
	assertHuman(t, "all", human, []string{"Showing all 60"})
	if _, _, code := runBoth(t, env, "bots", "list", "--limit", "80"); code != 2 {
		t.Fatalf("--limit 80: code=%d", code)
	}
	if strings.Count(strings.Join(*seen, "\n"), "cursor=c2") != 2 {
		t.Fatalf("cursor not followed: %v", *seen)
	}
}

func TestNarrowAndPlainTables(t *testing.T) {
	pinViews(t)
	t.Setenv("COLUMNS", "60")
	env, _ := viewServer(t, map[string]string{"GET /bots?limit=10": botsPageJSON(demoBots(10, 0), demoCursor, 7334), "GET /mia": demoMia})
	for _, args := range [][]string{{"bots", "list"}, {"mia", "list"}, {"bots", "list", "--no-color"}} {
		_, human, _ := runBoth(t, env, args...)
		for _, l := range strings.Split(human, "\n") {
			if w := len([]rune(l)); w > 60 {
				t.Errorf("%v: line wider than 60 (%d): %q", args, w, l)
			}
		}
		if strings.Contains(human, "\x1b[") {
			t.Errorf("%v: colour codes with no terminal", args)
		}
	}
}

func TestEmptyStates(t *testing.T) {
	pinViews(t)
	env, _ := viewServer(t, map[string]string{
		"GET /mia":                    `{"agent_configs":[],"count":0}`,
		"GET /calendar/calendars":     `{"calendars":[],"connections":[],"total":0}`,
		"GET /calendar/events":        `{"results":[],"has_more":false}`,
		"GET /bots/b1/transcriptions": `{"transcriptions":[]}`,
	})
	for args, want := range map[string]string{
		"mia list":          "meetstream setup",
		"calendar list":     "meetstream calendar connect",
		"calendar events":   "No upcoming meetings",
		"transcriptions b1": "meetstream transcribe b1",
	} {
		_, human, code := runBoth(t, env, strings.Fields(args)...)
		if code != 0 || !strings.Contains(human, want) {
			t.Errorf("%s: code=%d %s", args, code, human)
		}
	}
}

func TestRenderDataNeverJSON(t *testing.T) {
	var v any
	_ = json.Unmarshal([]byte(`{"message":"Agent configuration created successfully.","agent_config_id":"a1","agent_config":{"AgentName":"X","Model":{"provider":"openai","api_key":"sk-secret","config":{"deep":{"deeper":1}}},"Tags":["a","b"]},"items":[{"id":"1","status":"Done","created_at":"2026-11-20T12:00:00Z"}],"password":"hunter2"}`), &v)
	out := renderData("Agent created", v, 100)
	for _, bad := range []string{`{"`, "sk-secret", "hunter2", "agent_config"} {
		if strings.Contains(out, bad) {
			t.Errorf("generic view shows %q:\n%s", bad, out)
		}
	}
	for _, want := range []string{"Agent created", "Agent configuration created successfully.", "Agent ID", "a, b", "● Done"} {
		if !strings.Contains(out, want) {
			t.Errorf("generic view missing %q:\n%s", want, out)
		}
	}
}

func TestDashboardLinkOnlyOnProd(t *testing.T) {
	t.Setenv("MEETSTREAM_API_URL", "")
	if dashboardURL("/api-keys") != "https://app.meetstream.ai/api-keys" || serverLabel() != "MeetStream" {
		t.Fatal("prod link missing")
	}
	t.Setenv("MEETSTREAM_API_URL", "https://api-test.example.com/api/v1")
	if dashboardURL("/api-keys") != "" || serverLabel() != "test server: api-test.example.com" || strings.Contains(keysWhere(), "app.meetstream.ai") {
		t.Fatal("non-prod server shows the prod dashboard")
	}
}

func TestWebhookEventLine(t *testing.T) {
	out := renderWebhookEvent(map[string]any{"event": "transcription.processed", "bot_id": "b1234567890", "bot_status": "Stopped"}, "15:04:05")
	for _, want := range []string{"Transcript ready", "● Done", "b1234567", "meetstream transcript b1234567890"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q: %s", want, out)
		}
	}
	out = renderWebhookEvent(map[string]any{"event": "bot.stopped", "bot_status": "denied"}, "15:04:05")
	if !strings.Contains(out, "Ended early: bot was not let in") {
		t.Errorf("abnormal end: %s", out)
	}
}

func TestJoinCard(t *testing.T) {
	out := renderJoinResult(map[string]any{"bot_id": "b1"}, "Notetaker", "https://meet.google.com/abc-defg-hij", false)
	for _, want := range []string{"Bot sent", "Notetaker", "Google Meet", "meetstream bots status b1 --watch"} {
		if !strings.Contains(out, want) {
			t.Errorf("join card missing %q:\n%s", want, out)
		}
	}
}

func TestHeaderOncePerRun(t *testing.T) {
	r, w, _ := os.Pipe()
	old := ui.Out
	ui.Out = w
	ui.Setup(true, false)
	ui.Out = w
	ui.Header("1.0.0", "Signing in")
	ui.Header("1.0.0", "Connect your calendar")
	w.Close()
	ui.Out = old
	b, _ := io.ReadAll(r)
	if strings.Count(string(b), "◆ MeetStream") != 1 || !strings.Contains(string(b), "Connect your calendar") {
		t.Fatalf("want one banner then a step header:\n%s", b)
	}
}
