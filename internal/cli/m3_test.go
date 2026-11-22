package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/meetstream-ai/cli/internal/api"
	"github.com/spf13/cobra"
)

type m3Transport struct{ handler http.Handler }

func (tr m3Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	tr.handler.ServeHTTP(rec, r)
	return rec.Result(), nil
}

type m3Route struct {
	method, path string
	body         any
	status       int
	response     string
}

func m3Server(t *testing.T, routes []m3Route) (map[string]string, *int) {
	t.Helper()
	calls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := calls
		calls++
		if i >= len(routes) {
			t.Error("unexpected request")
			w.WriteHeader(500)
			return
		}
		want := routes[i]
		if object(want.body)["bot_name"] == "CLI bot" && r.Header.Get("Idempotency-Key") != "retry-1" {
			t.Error("missing idempotency key")
		}
		if r.Method != want.method || r.URL.RequestURI() != want.path {
			t.Errorf("request %d: got %s %s, want %s %s", i, r.Method, r.URL.RequestURI(), want.method, want.path)
		}
		if r.Header.Get("Authorization") != "Token test-key" {
			t.Error("incorrect authorization")
		}
		if want.body != nil {
			var got any
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Error(err)
			}
			b, _ := json.Marshal(want.body)
			var expected any
			_ = json.Unmarshal(b, &expected)
			if !reflect.DeepEqual(got, expected) {
				t.Errorf("request body mismatch: got %#v, want %#v", got, expected)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		status := want.status
		if status == 0 {
			status = 200
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(want.response))
	})
	// Exercise the real client against httptest without opening sandbox-blocked sockets.
	old := http.DefaultTransport
	http.DefaultTransport = m3Transport{handler}
	t.Cleanup(func() { http.DefaultTransport = old })
	return map[string]string{"MEETSTREAM_API_KEY": "test-key", "MEETSTREAM_API_URL": "http://meetstream.test"}, &calls
}
func TestM3JoinManyFlags(t *testing.T) {
	payload := map[string]any{
		"meeting_link": "https://example.invalid/meeting", "bot_name": "CLI bot", "video_required": true,
		"bot_message": "hello", "bot_image_url": "https://example.invalid/avatar", "callback_url": "https://example.invalid/hook", "join_at": "2027-01-01T00:00:00Z",
		"audio_separate_streams": true, "video_separate_streams": true, "storage_id": "archive_1",
		"zoom":            map[string]any{"zak_url": "https://example.invalid/zak", "obf_url": "https://example.invalid/obf", "use_own_app": true},
		"agent_config_id": "a1", "agent_config_params": map[string]string{"prompt": "a=b"},
		"custom_attributes":           map[string]string{"tag": "1", "empty": ""},
		"live_transcription_required": map[string]string{"webhook_url": "https://example.invalid/live"},
		"live_audio_required":         map[string]string{"websocket_url": "wss://example.invalid/audio"},
		"live_video_required":         map[string]string{"websocket_url": "wss://example.invalid/video"},
		"socket_connection_url":       map[string]string{"websocket_url": "wss://example.invalid/socket"},
		"recording_config":            map[string]any{"video_layout": "grid_view", "retention": map[string]any{"type": "timed", "hours": 24}, "transcript": map[string]any{"provider": map[string]any{"assemblyai": map[string]any{"speech_models": []string{"best"}, "language_code": "fr", "speaker_labels": true}}}},
		"automatic_leave":             map[string]int{"everyone_left_timeout": 20, "waiting_room_timeout": 90, "in_call_recording_timeout": 1800, "recording_permission_denied_timeout": 60},
	}
	env, calls := m3Server(t, []m3Route{{"POST", "/bots/create_bot", payload, 507, `{"bot_id":"original"}`}})
	args := []string{"join", "https://example.invalid/meeting", "--name", "CLI bot", "--video", "--video-layout", "grid_view", "--transcript", "assemblyai", "--language", "fr", "--callback", "https://example.invalid/hook", "--join-at", "2027-01-01T00:00:00Z", "--bot-message", "hello", "--image-url", "https://example.invalid/avatar", "--retention-hours", "24", "--separate-audio", "--separate-video", "--zoom-zak-url", "https://example.invalid/zak", "--zoom-obf-url", "https://example.invalid/obf", "--zoom-use-own-app", "--storage-id", "archive_1", "--idempotency-key", "retry-1", "--agent-config", "a1", "--agent-param", "prompt=a=b", "--attr", "tag=1", "empty=", "--live-transcript-webhook", "https://example.invalid/live", "--live-audio-ws", "wss://example.invalid/audio", "--live-video-ws", "wss://example.invalid/video", "--socket-ws", "wss://example.invalid/socket", "--everyone-left-timeout", "20", "--waiting-room-timeout", "90", "--max-recording-seconds", "1800", "--json"}
	out, code := run(t, env, args...)
	if code != 0 || *calls != 1 || !strings.Contains(out, "original") {
		t.Fatalf("code=%d calls=%d out=%s", code, *calls, out)
	}
}

func TestM3Requests(t *testing.T) {
	defaultCreate := map[string]any{"meeting_link": "https://example.invalid/meeting", "bot_name": "MeetStream Bot", "video_required": false, "automatic_leave": map[string]int{"waiting_room_timeout": 300, "everyone_left_timeout": 60, "in_call_recording_timeout": 14400, "recording_permission_denied_timeout": 60}}
	for _, tc := range []struct {
		name     string
		args     []string
		routes   []m3Route
		code     int
		contains string
	}{
		{"list", []string{"bots", "list", "--json"}, []m3Route{{"GET", "/bots", nil, 200, `{"bots":[{"bot_id":"b1"}]}`}}, 0, "b1"},
		{"delete refused", []string{"bots", "delete", "b1", "--no-input"}, nil, 2, ""},
		{"delete yes", []string{"bots", "delete", "b1", "--yes", "--json"}, []m3Route{{"DELETE", "/bots/b1/delete", nil, 200, `{"deleted":true}`}}, 0, "true"},
		{"legacy create", []string{"bot", "create", "https://example.invalid/meeting", "--json"}, []m3Route{{"POST", "/bots/create_bot", defaultCreate, 200, `{"bot_id":"b1"}`}}, 0, "b1"},
		{"ready", []string{"transcript", "b1", "--json"}, []m3Route{{"GET", "/bots/b1/detail", nil, 200, `{"bot_details":{"transcript_id":"t1"}}`}, {"GET", "/transcript/t1/get_transcript?raw=false", nil, 200, `{"message":[{"speaker":"A","transcript":"hello"}]}`}}, 0, "hello"},
		{"raw", []string{"transcript", "b1", "--raw"}, []m3Route{{"GET", "/bots/b1/detail", nil, 200, `{"transcript_id":"t1"}`}, {"GET", "/transcript/t1/get_transcript?raw=true", nil, 200, `{"provider":"raw-data"}`}}, 0, "raw-data"},
		{"not ready", []string{"transcript", "b1", "--json"}, []m3Route{{"GET", "/bots/b1/detail", nil, 200, `{}`}, {"GET", "/bots/b1/transcriptions", nil, 200, `{"transcriptions":[]}`}}, 3, ""},
		{"failed never polled", []string{"transcript", "b1", "--wait", "--json"}, []m3Route{{"GET", "/bots/b1/detail", nil, 200, `{"transcript_id":"t1"}`}, {"GET", "/transcript/t1/get_transcript?raw=false", nil, 422, `{"message":"Transcript processing failed: quota","transcript_status":"Failed","transcript_id":"t1"}`}}, 4, `"error": "transcript_failed"`},
		{"balance unavailable", []string{"balance", "--json"}, []m3Route{{"GET", "/billing/balance", nil, 404, `{"detail":"not enabled"}`}}, 3, ""},
		{"mia check", []string{"mia", "check", "a1", "--json"}, []m3Route{{"GET", "/mia?agent_config_id=a1&check=local_agent", nil, 200, `{"local_agent":{"status":"offline"}}`}}, 3, "offline"},
		{"mia check missing", []string{"agents", "check", "a1", "--json"}, []m3Route{{"GET", "/mia?agent_config_id=a1&check=local_agent", nil, 200, `{}`}}, 3, "{}"},
		{"say", []string{"bots", "say", "b1", "hello", "world", "--json"}, []m3Route{{"POST", "/bots/b1/send_message", map[string]string{"message": "hello world"}, 200, `{}`}}, 0, ""},
		{"hand", []string{"bots", "hand", "b1", "up", "--json"}, []m3Route{{"POST", "/bots/b1/send_command", map[string]string{"command": "raise_hand"}, 200, `{}`}}, 0, ""},
		{"legacy hand", []string{"bot", "lower-hand", "b1", "--json"}, []m3Route{{"POST", "/bots/b1/send_command", map[string]string{"command": "lower_hand"}, 200, `{}`}}, 0, ""},
		{"screenshots", []string{"bots", "get", "b1", "--part", "screenshots", "--json"}, []m3Route{{"GET", "/bots/b1/screenshots", nil, 200, `[]`}}, 0, ""},
		{"transcribe", []string{"transcribe", "b1", "-p", "smallest", "-l", "fr", "-c", "https://example.invalid/callback", "--json"}, []m3Route{{"POST", "/bots/b1/transcribe", map[string]any{"provider": map[string]any{"smallest": map[string]string{"model": "pulse", "language": "fr"}}, "callback_url": "https://example.invalid/callback"}, 200, `{}`}}, 0, ""},
		{"auth rejected", []string{"bots", "list", "--json"}, []m3Route{{"GET", "/bots", nil, 403, `{"detail":"invalid"}`}}, 2, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, calls := m3Server(t, tc.routes)
			out, code := run(t, env, tc.args...)
			if code != tc.code || *calls != len(tc.routes) || !strings.Contains(out, tc.contains) {
				t.Fatalf("code=%d want=%d calls=%d want=%d out=%s", code, tc.code, *calls, len(tc.routes), out)
			}
		})
	}
}
func TestM3MiaUpdate(t *testing.T) {
	file := t.TempDir() + "/agent.json"
	if err := os.WriteFile(file, []byte(`{"agent_config_id":"old","model":{"provider":"local_agent","config":{"temperature":0.2}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"agent_config_id": "a1", "model": map[string]any{"provider": "local_agent", "config": map[string]any{"temperature": 0.2}}}
	env, calls := m3Server(t, []m3Route{{"PUT", "/mia", body, 200, `{"agent_config_id":"a1"}`}})
	out, code := run(t, env, "mia", "update", "a1", "-f", file, "--json")
	if code != 0 || *calls != 1 || !strings.Contains(out, "a1") {
		t.Fatalf("code=%d calls=%d", code, *calls)
	}
}
func TestM3NoKey(t *testing.T) {
	for _, args := range [][]string{{"join", "https://example.invalid/meeting"}, {"bots", "list"}, {"transcript", "b1"}, {"balance"}, {"mia", "list"}} {
		_, code := run(t, nil, args...)
		if code != 2 {
			t.Errorf("%v: code=%d", args, code)
		}
	}
}
func TestM3TranscriptText(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{`[{"participant":{"name":" A "},"speaker":"ignored","words":[{"text":" hello "},{"punctuated_word":"world!"},{"word":"yes"}]}]`, "A: hello world! yes"},
		{`[{"speaker":"B","transcript":" flat "},{"speaker":"C","words":[]}]`, "B: flat"},
		{`[{"participant":{"name":" "},"speaker":" B ","words":[{"text":""}],"text":" fallback "},{"text":"alone"}]`, "B: fallback\nUnknown: alone"},
	} {
		var v any
		_ = json.Unmarshal([]byte(tc.input), &v)
		if got := transcriptText(v); got != tc.want {
			t.Errorf("got %q want %q", got, tc.want)
		}
	}
}
func TestM3Confirmation(t *testing.T) {
	for _, answer := range []string{"y\n", "yes\n", "N\n", ""} {
		g = globals{}
		cmd := &cobra.Command{}
		cmd.SetIn(strings.NewReader(answer))
		ok, err := confirmDelete(cmd, false)
		if strings.HasPrefix(answer, "y") {
			if !ok || err != nil {
				t.Fatal("yes refused")
			}
		} else if ok {
			t.Fatal("deletion accepted")
		}
	}
}
func TestM3ErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code int
		hint string
	}{
		{&api.Error{Status: 401, Body: `{"message":"rejected"}`}, 2, "run: meetstream login"},
		{&api.Error{Status: 403, Body: `{"detail":"rejected"}`}, 2, "run: meetstream login"},
		{&api.Error{Status: 500, Body: `{"error":"server"}`}, 1, ""},
		{&api.UsageError{Msg: "invalid"}, 2, ""},
	} {
		err, ok := apiError(tc.err).(*ExitError)
		if !ok || err.Code != tc.code || err.Hint != tc.hint {
			t.Errorf("incorrect mapping: %v", err)
		}
	}
}

func TestM3CreateFlagErrorsAndHelp(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"join", "--help"}, 0},
		{[]string{"bot", "create", "--help"}, 0},
		{[]string{"join", "https://example.invalid/meeting", "--attr", "bad", "--json"}, 2},
		{[]string{"join", "https://example.invalid/meeting", "--agent-param", "bad", "--json"}, 2},
		{[]string{"join", "https://example.invalid/meeting", "--storage-id", "", "--json"}, 2},
		{[]string{"join", "https://example.invalid/meeting", "--sign-in-email", "bot@example.invalid", "--json"}, 2},
		{[]string{"join", "https://example.invalid/meeting", "--video-layout", "grid_view", "--json"}, 2},
	} {
		env, calls := m3Server(t, nil)
		_, code := run(t, env, tc.args...)
		if code != tc.code || *calls != 0 {
			t.Errorf("args=%v code=%d calls=%d", tc.args, code, *calls)
		}
	}
}
func TestM3CreateSignedInAndZoomFlags(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		field string
		value any
	}{
		{[]string{"--google-login-domain", "example.invalid", "--sign-in-email", "bot@example.invalid", "--no-strict-email"}, "google_meet", map[string]any{"login_required": true, "google_login_domain": "example.invalid", "sign_in_email": "bot@example.invalid", "strict_email": false}},
		{[]string{"--teams-login-domain", "example.invalid"}, "teams", map[string]any{"login_required": true, "teams_login_domain": "example.invalid"}},
		{[]string{"--no-zoom-use-own-app"}, "zoom", map[string]any{"use_own_app": false}},
		{[]string{"--no-zoom-use-own-app", "--zoom-use-own-app"}, "zoom", map[string]any{"use_own_app": true}},
		{[]string{"--zoom-use-own-app", "--no-zoom-use-own-app"}, "zoom", map[string]any{"use_own_app": false}},
	} {
		body := map[string]any{"meeting_link": "https://example.invalid/meeting", "bot_name": "MeetStream Bot", "video_required": false, "automatic_leave": map[string]int{"waiting_room_timeout": 300, "everyone_left_timeout": 60, "in_call_recording_timeout": 14400, "recording_permission_denied_timeout": 60}, tc.field: tc.value}
		env, calls := m3Server(t, []m3Route{{"POST", "/bots/create_bot", body, 200, `{}`}})
		args := append([]string{"join", "https://example.invalid/meeting", "--json"}, tc.args...)
		_, code := run(t, env, args...)
		if code != 0 || *calls != 1 {
			t.Fatalf("code=%d calls=%d", code, *calls)
		}
	}
}
func TestM3Hints(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		routes []m3Route
		code   int
		hint   string
	}{
		{[]string{"bots", "delete", "b1", "--no-input"}, nil, 2, "pass --yes"},
		{[]string{"mia", "delete", "a1", "--no-input"}, nil, 2, "pass --yes"},
		{[]string{"transcript", "b1"}, []m3Route{{"GET", "/bots/b1/detail", nil, 200, `{}`}, {"GET", "/bots/b1/transcriptions", nil, 200, `{}`}}, 3, "meetstream transcript b1 --wait"},
		{[]string{"balance"}, []m3Route{{"GET", "/billing/balance", nil, 404, `{}`}}, 3, "https://app.meetstream.ai/billing"},
	} {
		env, _ := m3Server(t, tc.routes)
		for k, v := range env {
			t.Setenv(k, v)
		}
		g = globals{}
		root := NewRoot(BuildInfo{})
		root.SetArgs(tc.args)
		err, ok := root.Execute().(*ExitError)
		if !ok || err.Code != tc.code || err.Hint != tc.hint {
			t.Fatalf("error=%v want code=%d hint=%s", err, tc.code, tc.hint)
		}
	}
}
func TestM3HomeAndAlias(t *testing.T) {
	for _, item := range homeItems {
		if item.args[0] == "join" || item.args[0] == "bots" {
			if item.soon != "" {
				t.Errorf("%s still marked soon", item.args[0])
			}
		}
	}
	root := NewRoot(BuildInfo{})
	bot, _, err := root.Find([]string{"bot"})
	if err != nil || !bot.Hidden {
		t.Fatal("legacy bot must be hidden")
	}
}
func TestM3Recordings(t *testing.T) {
	for _, streams := range []bool{true, false} {
		status, response := 200, `[{"participant":"A","url":"https://example.invalid/stream"}]`
		if !streams {
			status, response = 404, `{}`
		}
		env, calls := m3Server(t, []m3Route{
			{"GET", "/bots/b1/get_audio", nil, 200, `{"audio_url":"https://example.invalid/audio"}`},
			{"GET", "/bots/b1/get_video", nil, 200, `{"video_url":"https://example.invalid/video"}`},
			{"GET", "/bots/b1/get_recording_streams", nil, status, response},
			{"GET", "/bots/b1/get_audio_streams", nil, status, response},
		})
		out, code := run(t, env, "recordings", "b1", "--json")
		var data map[string]any
		if code != 0 || *calls != 4 || json.Unmarshal([]byte(out), &data) != nil {
			t.Fatalf("code=%d calls=%d out=%s", code, *calls, out)
		}
		if data["bot_id"] != "b1" || data["audio"] == nil || data["video"] == nil {
			t.Fatal("missing recording fields")
		}
		if _, ok := data["streams"]; ok != streams {
			t.Fatal("optional streams mismatch")
		}
		if _, ok := data["audio_streams"]; ok != streams {
			t.Fatal("optional audio streams mismatch")
		}
	}
}
