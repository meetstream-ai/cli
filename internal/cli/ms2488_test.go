package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// G3: --forward-to keeps the signature headers and the User-Agent.
func TestListenForwardCopiesMeetStreamHeaders(t *testing.T) {
	got := make(chan http.Header, 1)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Clone()
	}))
	defer app.Close()
	var out, diagnostics bytes.Buffer
	handler := webhookHandler("/webhook", app.URL, true, &out, &diagnostics)
	req := httptest.NewRequest("POST", "/webhook", strings.NewReader(`{"event":"bot.done"}`))
	req.Header.Set("X-MeetStream-Signature", "v1=abc")
	req.Header.Set("X-MeetStream-Timestamp", "1700000000")
	req.Header.Set("X-MeetStream-Event-Id", "evt-1")
	req.Header.Set("User-Agent", "MeetStream-Webhooks/1.0")
	req.Header.Set("Authorization", "Token secret")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	var h http.Header
	select {
	case h = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("event not forwarded")
	}
	if h.Get("X-MeetStream-Signature") != "v1=abc" || h.Get("X-MeetStream-Timestamp") != "1700000000" || h.Get("X-MeetStream-Event-Id") != "evt-1" {
		t.Fatalf("X-MeetStream-* headers lost: %v", h)
	}
	if h.Get("User-Agent") != "MeetStream-Webhooks/1.0" || h.Get("Content-Type") != "application/json" {
		t.Fatalf("User-Agent or Content-Type wrong: %v", h)
	}
	if h.Get("Authorization") != "" {
		t.Fatal("only X-MeetStream-* and User-Agent are copied")
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("forward failed: %s", diagnostics.String())
	}
}

// G4: the human output skips raw interim POSTs; --json keeps every event.
func TestListenSkipsInterimInHumanOutputOnly(t *testing.T) {
	interim := `{"bot_id":"b1","speakerName":"Jane","transcript":"Can every","is_final":false}`
	final := `{"bot_id":"b1","speakerName":"Jane","transcript":"Can everyone hear me?","is_final":true}`
	sentence := `{"bot_id":"b1","speakerName":"Jane","transcript":"Next line."}`
	var human, js, diag bytes.Buffer
	hh := webhookHandler("/webhook", "", false, &human, &diag)
	jh := webhookHandler("/webhook", "", true, &js, &diag)
	for _, body := range []string{interim, final, sentence} {
		hh.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/webhook", strings.NewReader(body)))
		jh.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/webhook", strings.NewReader(body)))
	}
	if strings.Contains(human.String(), "Can every\n") || strings.Count(human.String(), "Live transcript") != 2 ||
		!strings.Contains(human.String(), "Can everyone hear me?") || !strings.Contains(human.String(), "Next line.") {
		t.Fatalf("human output: %q", human.String())
	}
	if n := strings.Count(strings.TrimSpace(js.String()), "\n") + 1; n != 3 {
		t.Fatalf("--json must print all 3 events, got %d: %q", n, js.String())
	}
}

// joinBody runs join against a fake API and returns the create_bot body it sent.
func joinBody(t *testing.T, args ...string) (map[string]any, int) {
	t.Helper()
	var body map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"bot_id":"b1","status":"Joining"}`))
	}))
	defer ts.Close()
	env := map[string]string{"MEETSTREAM_API_KEY": "k", "MEETSTREAM_API_URL": ts.URL + "/api/v1"}
	_, code := run(t, env, append([]string{"join", "https://meet.google.com/abc-defg-hij", "--json", "--no-input"}, args...)...)
	return body, code
}

func streamingConfig(body map[string]any, provider string) map[string]any {
	rc, _ := body["recording_config"].(map[string]any)
	tr, _ := rc["transcript"].(map[string]any)
	p, _ := tr["provider"].(map[string]any)
	cfg, _ := p[provider].(map[string]any)
	return cfg
}

func TestLiveTranscriptionSendsSentenceModeByDefault(t *testing.T) {
	hook := "--live-transcript-webhook=https://hooks.example/live"
	body, code := joinBody(t, "--live-transcription", "deepgram", hook)
	if code != 0 || streamingConfig(body, "deepgram_streaming")["transcription_mode"] != "sentence" {
		t.Fatalf("code=%d body=%v", code, body)
	}
	body, code = joinBody(t, "--live-transcription", "meetstream", "--transcription-mode", "raw", hook)
	if code != 0 || streamingConfig(body, "meetstream_streaming")["transcription_mode"] != "raw" {
		t.Fatalf("override: code=%d body=%v", code, body)
	}
	body, code = joinBody(t, "--live-transcription", "meetstream", "--transcription-mode", "WORD_LEVEL", hook)
	if code != 0 || streamingConfig(body, "meetstream_streaming")["transcription_mode"] != "word_level" {
		t.Fatalf("word_level: code=%d body=%v", code, body)
	}
	if _, code = joinBody(t, "--live-transcription", "meetstream", "--transcription-mode", "fast", hook); code != 2 {
		t.Fatalf("invalid mode: code=%d", code)
	}
	if _, code = joinBody(t, "--transcription", "meetstream", "--transcription-mode", "sentence"); code != 2 {
		t.Fatalf("mode without a live provider: code=%d", code)
	}
	// Unchanged: post-call providers and meeting_captions carry no transcription_mode.
	body, _ = joinBody(t, "--transcription", "meeting_captions")
	if _, ok := streamingConfig(body, "meeting_captions")["transcription_mode"]; ok {
		t.Fatalf("meeting_captions must not get transcription_mode: %v", body)
	}
	body, _ = joinBody(t, "--transcription", "deepgram")
	if _, ok := streamingConfig(body, "deepgram")["transcription_mode"]; ok {
		t.Fatalf("post-call provider must not get transcription_mode: %v", body)
	}
}

// G8: 202 on a non-transcript read is not data: exit 3, nothing on stdout.
func TestNotReady202ExitsThree(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/get_chats"), strings.HasSuffix(r.URL.Path, "/get_audio"), strings.HasSuffix(r.URL.Path, "/screenshots"):
			w.WriteHeader(202)
			_, _ = w.Write([]byte(`{"message":"Chats are still being processed"}`))
		case strings.HasSuffix(r.URL.Path, "/get_video"):
			w.WriteHeader(507)
			_, _ = w.Write([]byte(`{"url":"https://example.invalid/v.mp4"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer ts.Close()
	env := map[string]string{"MEETSTREAM_API_KEY": "k", "MEETSTREAM_API_URL": ts.URL + "/api/v1"}
	for _, args := range [][]string{
		{"bots", "get", "b1", "--part", "chats", "--json"},
		{"bots", "get", "b1", "--part", "screenshots", "--json"},
		{"recordings", "b1", "--json"},
	} {
		out, code := run(t, env, args...)
		if code != 3 || strings.Contains(out, "still being processed") {
			t.Errorf("%v: code=%d out=%q", args, code, out)
		}
	}
	out, code := run(t, env, "bots", "get", "b1", "--part", "video", "--json")
	if code != 0 || !strings.Contains(out, "v.mp4") {
		t.Fatalf("507 must stay a success: code=%d out=%q", code, out)
	}
}

// G9: whole status values only.
func TestTerminalStatusAnchored(t *testing.T) {
	for _, s := range []string{"Stopped", "kicked", "Done", "Error", "Failed", "NotAllowed", "Denied", "Cancelled", "MediaExpired", "STOPPED"} {
		if !terminalStatus.MatchString(s) {
			t.Errorf("%s must be terminal", s)
		}
	}
	for _, s := range []string{"RecordingPermissionDenied", "Joining", "InMeeting", "Recording", "MediaProcessing", "Stopping", "", "notdone"} {
		if terminalStatus.MatchString(s) {
			t.Errorf("%s must not be terminal", s)
		}
	}
}

// G7: a bot that was not admitted has no transcript; transcript --wait stops at once.
func TestTranscriptWaitStopsOn409(t *testing.T) {
	env := map[string]string{"MEETSTREAM_API_KEY": "k", "MEETSTREAM_API_URL": startMock(t, "waiting-room-timeout")}
	out, code := run(t, env, "join", "https://meet.google.com/abc-defg-hij", "--transcription", "meetstream", "--json", "--no-input")
	if code != 0 {
		t.Fatalf("join: code=%d out=%s", code, out)
	}
	var created map[string]any
	_ = json.Unmarshal([]byte(out), &created)
	id, _ := created["bot_id"].(string)
	if id == "" {
		t.Fatalf("no bot_id: %s", out)
	}
	start := time.Now()
	for time.Since(start) < 5*time.Second { // let the mock bot reach NotAllowed
		o, _ := run(t, env, "bots", "status", id, "--json")
		if strings.Contains(o, "NotAllowed") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	start = time.Now()
	_, code = run(t, env, "transcript", id, "--wait", "--timeout", "30", "--json")
	if code != 1 || time.Since(start) > 10*time.Second {
		t.Fatalf("code=%d after %s; want exit 1 at once", code, time.Since(start))
	}
}

// G17: a saved `mia get --json` answer applies as the snake_case create body.
func TestMiaApplyMapsGetShape(t *testing.T) {
	saved := `{"agent_config":{"AgentConfigID":"ag-1","UserID":"u","AgentName":"Helper","Mode":"pipeline",
"Model":{"provider":"openai","model":"gpt-4o-mini","system_prompt":"hi"},"Voice":null,"Transcriber":{"provider":"deepgram"},
"Audio":{"sample_rate":24000},"WakeWord":"hey","CreatedAt":"x","UpdatedAt":"y"}}`
	got := miaRequestBody(mustObject(t, saved))
	want := map[string]any{"agent_config_id": "ag-1", "agent_name": "Helper", "mode": "pipeline", "wake_word": "hey"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s=%v want %v (body %v)", k, got[k], v, got)
		}
	}
	if _, ok := got["model"].(map[string]any); !ok {
		t.Errorf("model block missing: %v", got)
	}
	for _, k := range []string{"agent_config", "AgentName", "user_id", "created_at", "updated_at", "audio", "voice"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s must not be sent: %v", k, got)
		}
	}
	plain := `{"agent_name":"A","mode":"realtime","model":{"provider":"openai","system_prompt":"x"},"AgentName":"B"}`
	if got := miaRequestBody(mustObject(t, plain)); got["agent_name"] != "A" || got["AgentName"] != nil {
		t.Fatalf("snake_case body must pass through and win: %v", got)
	}
	if snakeCase("AgentConfigID") != "agent_config_id" || snakeCase("WakeWord") != "wake_word" {
		t.Fatal("snakeCase")
	}
}

func TestMiaApplyRoundTripAgainstMock(t *testing.T) {
	env := map[string]string{"MEETSTREAM_API_KEY": "k", "MEETSTREAM_API_URL": startMock(t, "notetaker-happy")}
	dir := t.TempDir()
	create := filepath.Join(dir, "create.json")
	writeFile(t, create, `{"agent_name":"Helper","mode":"realtime","model":{"provider":"openai","system_prompt":"Be brief."}}`)
	out, code := run(t, env, "mia", "apply", "-f", create, "--json")
	if code != 0 {
		t.Fatalf("create: code=%d out=%s", code, out)
	}
	id, _ := mustObject(t, out)["agent_config_id"].(string)
	saved, code := run(t, env, "mia", "get", id, "--json")
	if code != 0 {
		t.Fatalf("get: code=%d", code)
	}
	path := filepath.Join(dir, "saved.json")
	writeFile(t, path, saved)
	out, code = run(t, env, "mia", "apply", "-f", path, "--json")
	rec, _ := mustObject(t, out)["agent_config"].(map[string]any)
	if code != 0 || rec["AgentConfigID"] != id || !strings.Contains(out, "updated") {
		t.Fatalf("round trip must update the same agent: code=%d out=%s", code, out)
	}
}

func mustObject(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not a JSON object: %q", s)
	}
	return m
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
