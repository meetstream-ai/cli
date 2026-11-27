package mock

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Scenario is one scripted bot run.
type Scenario struct {
	Name        string
	Description string
	// End is how the meeting ends: a bot_event (bot.stopped, bot.kicked, bot.notallowed).
	End string
	// TranscriptFails makes the post-call transcript fail (422 on get_transcript).
	TranscriptFails bool
	// ZoomNeedsSetup rejects Zoom links at create_bot, as an account with no Zoom app.
	ZoomNeedsSetup bool
}

// Scenarios are the runs `meetstream mock --scenario` offers.
var Scenarios = map[string]Scenario{
	"notetaker-happy":      {Name: "notetaker-happy", Description: "join → waiting room → recording → everyone left → transcript ready", End: "bot.stopped"},
	"waiting-room-timeout": {Name: "waiting-room-timeout", Description: "nobody admits the bot; it leaves with bot.notallowed", End: "bot.notallowed"},
	"transcript-failed":    {Name: "transcript-failed", Description: "the meeting records, then the transcript fails (exit 4 path)", End: "bot.stopped", TranscriptFails: true},
	"bot-removed":          {Name: "bot-removed", Description: "a participant removes the bot (bot.kicked)", End: "bot.kicked"},
	"zoom-needs-setup":     {Name: "zoom-needs-setup", Description: "Zoom links are rejected: the account has no Zoom app set up", End: "bot.stopped", ZoomNeedsSetup: true},
}

// ScenarioNames is the sorted scenario list.
func ScenarioNames() []string {
	var out []string
	for k := range Scenarios {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// streamingOnly providers send live text and make no post-call transcript.
var streamingOnly = map[string]bool{"meetstream_streaming": true, "deepgram_streaming": true, "assemblyai_streaming": true, "jigsawstack_streaming": true, "smallest_streaming": true, "sarvam_streaming": true, "meeting_captions": true}

type bot struct {
	ID, Name, Link, Status, Provider, Callback, LiveURL string
	TranscriptID                                        string
	TranscriptState                                     string // "", processing, completed, failed
	AudioStatus                                         string // "", Success (as /detail reports it)
	Created                                             time.Time
}

// Server is the mock API. Step is the time between lifecycle events.
type Server struct {
	Scenario Scenario
	Step     time.Duration
	// Log gets one line per request and per webhook sent (may be nil).
	Log func(format string, args ...any)

	mu     sync.Mutex
	bots   map[string]*bot
	order  []string
	seq    int
	client *http.Client
	ctx    context.Context
}

// New returns a server for the scenario. ctx stops the scripted runs.
func New(ctx context.Context, sc Scenario, step time.Duration) *Server {
	if step <= 0 {
		step = 2 * time.Second
	}
	return &Server{Scenario: sc, Step: step, bots: map[string]*bot{}, client: &http.Client{Timeout: 10 * time.Second}, ctx: ctx}
}

func (s *Server) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log(format, args...)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ServeHTTP routes the subset of the API the CLI and the templates use.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Token ") {
		writeJSON(w, 401, map[string]any{"detail": "Authentication credentials were not provided."})
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	s.logf("%s %s", r.Method, path)
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case r.Method == http.MethodPost && path == "/bots/create_bot":
		s.createBot(w, r)
	case r.Method == http.MethodGet && path == "/bots":
		s.listBots(w)
	case r.Method == http.MethodGet && path == "/billing/balance":
		writeJSON(w, 200, map[string]any{"balance": 25.0, "currency": "USD", "note": "mock balance"})
	case len(parts) == 3 && parts[0] == "bots":
		s.botRoute(w, r, parts[1], parts[2])
	case len(parts) == 3 && parts[0] == "transcript" && parts[2] == "get_transcript":
		s.getTranscript(w, parts[1])
	default:
		writeJSON(w, 404, map[string]any{"detail": "not in the mock: " + r.Method + " " + path})
	}
}

func providerOf(payload map[string]any) string {
	rc, _ := payload["recording_config"].(map[string]any)
	tr, _ := rc["transcript"].(map[string]any)
	p, _ := tr["provider"].(map[string]any)
	for k := range p {
		return k
	}
	return ""
}

func (s *Server) createBot(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		writeJSON(w, 400, map[string]any{"detail": "body must be JSON"})
		return
	}
	link, _ := payload["meeting_link"].(string)
	if link == "" {
		writeJSON(w, 400, map[string]any{"meeting_link": []string{"This field is required."}})
		return
	}
	if s.Scenario.ZoomNeedsSetup && strings.Contains(link, "zoom.us") {
		writeJSON(w, 400, map[string]any{"detail": "Zoom needs setup: bots join Zoom through your own Zoom app. Set it up in the MeetStream dashboard (Integrations > Zoom), or pass zoom.obf_url / zoom.zak_url for meetings outside that app's account."})
		return
	}
	provider := providerOf(payload)
	live := ""
	if lt, ok := payload["live_transcription_required"].(map[string]any); ok {
		live, _ = lt["webhook_url"].(string)
	}
	if streamingOnly[provider] && live == "" {
		writeJSON(w, 400, map[string]any{"detail": "live_transcription_required.webhook_url is mandatory when using streaming providers: " + provider})
		return
	}
	cb, _ := payload["callback_url"].(string)
	name, _ := payload["bot_name"].(string)
	s.mu.Lock()
	s.seq++
	id := fmt.Sprintf("mock-%04d-%s", s.seq, time.Now().Format("150405"))
	b := &bot{ID: id, Name: name, Link: link, Status: "Joining", Provider: provider, Callback: cb, LiveURL: live, Created: time.Now()}
	s.bots[id] = b
	s.order = append(s.order, id)
	s.mu.Unlock()
	go s.run(b)
	writeJSON(w, 201, map[string]any{"bot_id": id, "bot_name": name, "meeting_link": link, "status": "Joining", "message": "Bot created successfully (mock)"})
}

func (s *Server) listBots(w http.ResponseWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := []any{}
	for i := len(s.order) - 1; i >= 0; i-- {
		b := s.bots[s.order[i]]
		rows = append(rows, map[string]any{"bot_id": b.ID, "bot_name": b.Name, "meeting_link": b.Link, "status": b.Status, "created_at": b.Created.UTC().Format(time.RFC3339)})
	}
	writeJSON(w, 200, map[string]any{"bots": rows, "hasNextPage": false})
}

func (s *Server) get(id string) *bot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bots[id]
}

func (s *Server) botRoute(w http.ResponseWriter, r *http.Request, id, action string) {
	b := s.get(id)
	if b == nil {
		writeJSON(w, 404, map[string]any{"detail": "Bot not found"})
		return
	}
	s.mu.Lock()
	snap := *b
	s.mu.Unlock()
	switch action {
	case "status":
		writeJSON(w, 200, map[string]any{"bot_id": id, "status": snap.Status})
	case "detail":
		ts := map[string]string{"completed": "Success", "failed": "Failed", "processing": "Processing"}[snap.TranscriptState]
		writeJSON(w, 200, map[string]any{"bot_details": map[string]any{"bot_id": id, "bot_name": snap.Name, "meeting_link": snap.Link,
			"status": snap.Status, "Status": snap.Status, "transcript_id": snap.TranscriptID,
			"AudioStatus": snap.AudioStatus, "TranscriptStatus": ts}})
	case "transcriptions":
		rows := []any{}
		if snap.TranscriptID != "" {
			rows = append(rows, map[string]any{"transcript_id": snap.TranscriptID, "status": snap.TranscriptState, "provider": snap.Provider, "created_at": snap.Created.UTC().Format(time.RFC3339)})
		}
		writeJSON(w, 200, map[string]any{"transcriptions": rows})
	case "transcribe":
		if r.Method != http.MethodPost {
			writeJSON(w, 405, map[string]any{"detail": "use POST"})
			return
		}
		var body map[string]any
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
		prov := ""
		if p, ok := body["provider"].(map[string]any); ok {
			for k := range p {
				prov = k
			}
		}
		s.mu.Lock()
		b.Provider, b.TranscriptID, b.TranscriptState = prov, "tr-"+id+"-"+prov, "processing"
		s.mu.Unlock()
		go func() {
			s.sleep(s.Step)
			s.mu.Lock()
			b.TranscriptState = "completed"
			s.mu.Unlock()
		}()
		writeJSON(w, 202, map[string]any{"message": "Transcription started (mock)", "transcript_id": b.TranscriptID, "provider": prov})
	case "remove_bot":
		s.mu.Lock()
		b.Status = "Stopped"
		s.mu.Unlock()
		writeJSON(w, 200, map[string]any{"message": "Bot removed (mock)"})
	case "get_audio", "get_video":
		writeJSON(w, 200, map[string]any{"bot_id": id, "url": "https://example.invalid/mock/" + id + "/" + action})
	default:
		writeJSON(w, 404, map[string]any{"detail": "not in the mock: " + action})
	}
}

func (s *Server) getTranscript(w http.ResponseWriter, tid string) {
	s.mu.Lock()
	var b *bot
	for _, x := range s.bots {
		if x.TranscriptID == tid {
			b = x
		}
	}
	var state string
	if b != nil {
		state = b.TranscriptState
	}
	s.mu.Unlock()
	switch {
	case b == nil:
		writeJSON(w, 404, map[string]any{"detail": "Transcript not found"})
	case state == "failed":
		writeJSON(w, 422, map[string]any{"message": "Transcript processing failed: audio too short", "transcript_status": "failed", "transcript_id": tid})
	case state != "completed":
		writeJSON(w, 404, map[string]any{"detail": "Transcript is still processing"})
	default:
		writeJSON(w, 200, sampleTranscript())
	}
}

func sampleTranscript() []any {
	lines := [][3]any{
		{"Jane Smith", 2.0, "Can everyone hear me? Let's start with the launch plan."},
		{"Ravi Kumar", 9.5, "Yes. The CLI release candidate is ready for testing on Thursday."},
		{"Jane Smith", 17.2, "Good. Who owns the harness run?"},
		{"Ana Lopez", 21.8, "I do. I will send the scores on Friday."},
	}
	out := []any{}
	for _, l := range lines {
		out = append(out, map[string]any{"speaker": l[0], "start_time": l[1], "transcript": l[2]})
	}
	return out
}

func (s *Server) sleep(d time.Duration) bool {
	select {
	case <-s.ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func (s *Server) set(b *bot, status string) {
	s.mu.Lock()
	b.Status = status
	s.mu.Unlock()
}

func (s *Server) send(url string, payload map[string]any) {
	if url == "" {
		return
	}
	code, err := Post(s.ctx, s.client, url, payload)
	name, _ := payload["event"].(string)
	if name == "" {
		name = "live transcript"
	}
	if err != nil {
		s.logf("webhook %s → %s failed: %v", name, url, err)
		return
	}
	s.logf("webhook %s → %d", name, code)
}

var liveLines = [][2]string{
	{"Jane Smith", "Can everyone hear me? Let's start with the launch plan."},
	{"Ravi Kumar", "Yes. The CLI release candidate is ready for testing on Thursday."},
	{"Jane Smith", "Good. Who owns the harness run?"},
	{"Ana Lopez", "I do. I will send the scores on Friday."},
}

// run plays the scenario for one bot: status changes and webhooks in production order.
func (s *Server) run(b *bot) {
	sc := s.Scenario
	ev := func(event, botEvent, status, msg string, code int) {
		if status != "" {
			s.set(b, status)
		}
		s.send(b.Callback, Event(event, botEvent, b.ID, status, msg, code))
	}
	ev("bot.joining", "bot.joining", "Joining", "Bot is joining the meeting", 200)
	if !s.sleep(s.Step) {
		return
	}
	ev("bot.in_waiting_room", "bot.in_waiting_room", "InWaitingRoom", "Waiting to be admitted", 200)
	if !s.sleep(s.Step) {
		return
	}
	if sc.End == "bot.notallowed" {
		if !s.sleep(s.Step) {
			return
		}
		ev("bot.stopped", "bot.notallowed", "NotAllowed", "Failed: Not admitted to meeting", 500)
		s.sleep(s.Step / 2)
		ev("bot.done", "", "", "Post-call processing finished", 200)
		return
	}
	ev("bot.inmeeting", "bot.inmeeting", "InMeeting", "Bot is in the meeting", 200)
	if !s.sleep(s.Step) {
		return
	}
	ev("bot.recording", "bot.recording", "Recording", "Recording started", 200)
	for i, l := range liveLines {
		if b.LiveURL != "" {
			s.send(b.LiveURL, LiveSegment(b.ID, l[0], fmt.Sprint(16778240+i%3), l[1], i+1))
		}
		if !s.sleep(s.Step / 2) {
			return
		}
	}
	if sc.End == "bot.kicked" {
		ev("bot.stopped", "bot.kicked", "Stopped", "Removed by a participant", 200)
	} else {
		ev("bot.leaving", "bot.leaving", "Leaving", "Everyone left", 200)
		s.sleep(s.Step / 2)
		ev("bot.stopped", "bot.stopped", "Stopped", "Everyone left", 200)
	}
	if !s.sleep(s.Step / 2) {
		return
	}
	// Production order: Stopped, then MediaProcessing while media is processed, then Done.
	s.set(b, "MediaProcessing")
	s.mu.Lock()
	b.AudioStatus = "Success"
	s.mu.Unlock()
	ev("audio.processed", "", "", "Audio ready", 200)
	if b.Provider != "" && !streamingOnly[b.Provider] {
		s.mu.Lock()
		b.TranscriptID, b.TranscriptState = "tr-"+b.ID, "processing"
		s.mu.Unlock()
		if !s.sleep(s.Step) {
			return
		}
		if sc.TranscriptFails {
			s.mu.Lock()
			b.TranscriptState = "failed"
			s.mu.Unlock()
			ev("transcription.failed", "", "", "Transcript processing failed: audio too short", 500)
		} else {
			s.mu.Lock()
			b.TranscriptState = "completed"
			s.mu.Unlock()
			ev("transcription.processed", "", "", "Transcript ready", 200)
		}
	}
	s.sleep(s.Step / 2)
	s.set(b, "Done")
	ev("bot.done", "", "", "Post-call processing finished", 200)
}
