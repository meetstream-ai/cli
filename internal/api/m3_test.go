package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

type testTransport func(*http.Request) (*http.Response, error)

func (tr testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return tr(r) }
func fakeClient(handler http.HandlerFunc) *Client {
	c := New("http://meetstream.test", "test-key", "test")
	c.HTTP.Transport = testTransport(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		handler(rec, r)
		return rec.Result(), nil
	})
	return c
}
func TestCreatePayloadNodeCases(t *testing.T) {
	loose := false
	strict := true
	floor := 10.0
	storage := ""
	own := false
	for _, tc := range []struct {
		name string
		opts CreateOpts
		want map[string]any
		bad  bool
	}{
		{"audio defaults", CreateOpts{}, map[string]any{"video_required": false, "bot_name": "MeetStream Bot", "recording_config": nil, "video_separate_streams": nil}, false},
		{"video defaults", CreateOpts{Video: true}, map[string]any{"recording_config": map[string]any{"video_layout": "speaker_view"}}, false},
		{"layout normalized", CreateOpts{Video: true, VideoLayout: "GRID_VIEW_DEFAULT"}, map[string]any{"recording_config": map[string]any{"video_layout": "grid_view_default"}}, false},
		{"layout needs video", CreateOpts{VideoLayout: "speaker_view"}, nil, true},
		{"invalid layout", CreateOpts{Video: true, VideoLayout: "gallery"}, nil, true},
		{"removed obf", CreateOpts{ZoomObf: true}, nil, true},
		{"zoom own app conflict", CreateOpts{ZoomZakURL: "https://example.invalid/zak", ZoomUseOwnApp: &own}, nil, true},
		{"empty storage rejected", CreateOpts{StorageID: &storage}, nil, true},
		{"strict default ignored", CreateOpts{StrictEmail: &strict}, map[string]any{"teams": nil, "google_meet": nil}, false},
		{"teams", CreateOpts{TeamsLoginDomain: "example.invalid"}, map[string]any{"teams": map[string]any{"login_required": true, "teams_login_domain": "example.invalid"}}, false},
		{"google strict", CreateOpts{GoogleLoginDomain: "example.invalid", SignInEmail: "bot@example.invalid"}, map[string]any{"google_meet": map[string]any{"login_required": true, "google_login_domain": "example.invalid", "sign_in_email": "bot@example.invalid", "strict_email": true}}, false},
		{"google loose", CreateOpts{GoogleLoginDomain: "example.invalid", SignInEmail: "bot@example.invalid", StrictEmail: &loose}, map[string]any{"google_meet": map[string]any{"login_required": true, "google_login_domain": "example.invalid", "sign_in_email": "bot@example.invalid", "strict_email": false}}, false},
		{"email needs domain", CreateOpts{SignInEmail: "bot@example.invalid"}, nil, true},
		{"two domains", CreateOpts{GoogleLoginDomain: "a", TeamsLoginDomain: "b"}, nil, true},
		{"loose needs email", CreateOpts{GoogleLoginDomain: "a", StrictEmail: &loose}, nil, true},
		{"loose needs domain", CreateOpts{StrictEmail: &loose}, nil, true},
		{"fractional timeouts", CreateOpts{EveryoneLeftTimeout: floatPtr(1.5), WaitingRoomTimeout: floatPtr(2.5), MaxRecordingSeconds: floatPtr(3.5)}, map[string]any{"automatic_leave": map[string]any{"waiting_room_timeout": 2.5, "everyone_left_timeout": 1.5, "in_call_recording_timeout": 3.5, "recording_permission_denied_timeout": 60}}, false},
		{"permission floor", CreateOpts{PermissionDeniedTimeout: &floor}, map[string]any{"automatic_leave": map[string]any{"waiting_room_timeout": 300, "everyone_left_timeout": 60, "in_call_recording_timeout": 14400, "recording_permission_denied_timeout": 60}}, false},
		{"agent no bridge", CreateOpts{AgentConfigID: "a1", AgentConfigParams: map[string]string{"customer": "example"}}, map[string]any{"agent_config_id": "a1", "agent_config_params": map[string]any{"customer": "example"}, "socket_connection_url": nil, "live_audio_required": nil}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := BuildCreateBotPayload(tc.opts)
			if (err != nil) != tc.bad {
				t.Fatalf("err=%v", err)
			}
			if err != nil {
				return
			}
			b, _ := json.Marshal(p)
			var got map[string]any
			_ = json.Unmarshal(b, &got)
			b, _ = json.Marshal(tc.want)
			var want map[string]any
			_ = json.Unmarshal(b, &want)
			for k, v := range want {
				if !reflect.DeepEqual(got[k], v) {
					t.Errorf("%s: got %#v want %#v", k, got[k], v)
				}
			}
		})
	}
}
func TestProviderNodeCases(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		value       any
	}{
		{"deepgram", "model", "nova-3"}, {"assemblyai", "speech_models", []any{"best"}}, {"sarvam", "mode", "batch"},
		{"meetstream", "translate", false}, {"jigsawstack", "translate", false}, {"smallest", "model", "pulse"},
		{"meeting_captions", "missing", nil}, {"deepgram_streaming", "model", "nova-3"}, {"assemblyai_streaming", "missing", nil}, {"smallest_streaming", "model", "pulse"}, {"custom", "missing", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := BuildCreateBotPayload(CreateOpts{Transcript: tc.name})
			if err != nil {
				t.Fatal(err)
			}
			rc := p["recording_config"].(map[string]any)
			prov := rc["transcript"].(map[string]any)["provider"].(map[string]any)[tc.name].(map[string]any)
			if !reflect.DeepEqual(prov[tc.field], tc.value) {
				t.Fatalf("provider mismatch: %v", prov)
			}
		})
	}
}
func TestSignedWarningsNodeCases(t *testing.T) {
	for _, tc := range []struct {
		link, google, teams, contains string
		count                         int
	}{
		{"https://teams.microsoft.com/x", "", "example.invalid", "display name and picture", 1},
		{"https://meet.google.com/x", "", "example.invalid", "not a Microsoft Teams host", 2},
		{"https://teams.live.com/x", "", "example.invalid", "personal Teams", 2},
		{"https://meet.google.com/x", "example.invalid", "", "", 0},
		{"https://teams.microsoft.com/x", "example.invalid", "", "not meet.google.com", 1},
	} {
		warnings := SignedInWarnings(tc.link, tc.google, tc.teams)
		if len(warnings) != tc.count || !strings.Contains(strings.Join(warnings, " "), tc.contains) {
			t.Errorf("warnings=%v", warnings)
		}
	}
}
func TestTimelineDownloadNoAuth(t *testing.T) {
	c := fakeClient(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("authorization leaked to download")
		}
		_, _ = w.Write([]byte(`[{"speaker":"A","start":0,"end":1}]`))
	})
	v, err := c.DownloadJSON(context.Background(), "https://download.test/timeline")
	if err != nil || len(v.([]any)) != 1 {
		t.Fatalf("download err=%v", err)
	}
}

type brokenBody struct{}

func (brokenBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (brokenBody) Close() error             { return nil }
func TestReadErrorsPropagate(t *testing.T) {
	c := New("http://meetstream.test", "test-key", "test")
	c.HTTP.Transport = testTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: brokenBody{}, Header: make(http.Header)}, nil
	})
	if _, err := c.ListBots(context.Background()); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("API read error lost: %v", err)
	}
	if _, err := c.DownloadJSON(context.Background(), "http://download.test/data"); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("download read error lost: %v", err)
	}
}
func TestImageDurationNodeCases(t *testing.T) {
	for _, duration := range []float64{0, 1.5, -1} {
		c := fakeClient(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "POST" || r.URL.Path != "/bots/b1/send_image" {
				t.Error("wrong route")
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["img_url"] != "https://example.invalid/image" {
				t.Error("wrong URL")
			}
			if duration == 0 {
				if _, ok := body["display_duration"]; ok {
					t.Error("zero duration must be omitted")
				}
			} else if body["display_duration"] != duration {
				t.Error("duration lost")
			}
			_, _ = w.Write([]byte("{}"))
		})
		if _, err := c.SendImage(context.Background(), "b1", "https://example.invalid/image", duration); err != nil {
			t.Fatal(err)
		}
	}
}
func TestFailedTranscriptNodeCases(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		reason string
	}{
		{422, `{"message":"Transcript processing failed: quota","transcript_status":"Failed","transcript_id":"t1"}`, "quota"},
		{422, `{"transcript_status":"Failed"}`, "no reason given"},
		{422, `{"message":"x"}`, ""},
		{500, `{"transcript_status":"Failed"}`, ""},
	} {
		f := FailedTranscript(&Error{Status: tc.status, Body: tc.body})
		if tc.reason == "" {
			if f != nil {
				t.Fatal("not a final transcript failure")
			}
		} else if f == nil || f.Reason != tc.reason {
			t.Fatalf("failure=%v", f)
		}
	}
}
func TestTranscriptPolling(t *testing.T) {
	for _, status := range []int{400, 404, 422, 401, 500} {
		calls := 0
		c := fakeClient(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/detail") {
				_, _ = w.Write([]byte(`{"transcript_id":"t1"}`))
				return
			}
			calls++
			if calls == 1 {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"transcript_status":"Failed","message":"failed"}`))
				return
			}
			_, _ = w.Write([]byte(`[{"speaker":"A","transcript":"ready"}]`))
		})
		res, err := c.GetTranscript(context.Background(), "b1", TranscriptOpt{Wait: true, Interval: time.Millisecond, Timeout: time.Second})
		if status == 400 || status == 404 {
			if err != nil || res.Transcript == nil || calls != 2 {
				t.Errorf("status=%d calls=%d err=%v", status, calls, err)
			}
		} else if err == nil || calls != 1 {
			t.Errorf("terminal status=%d polled %d times err=%v", status, calls, err)
		}
	}
}
func TestErrorDetailNodeCases(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"message":"first","detail":"second","error":"third"}`, "first"}, {`{"detail":"second"}`, "second"}, {`{"error":"third"}`, "third"}, {`"bare string"`, "bare string"},
	} {
		if got := (&Error{Body: tc.body}).Detail(); got != tc.want {
			t.Errorf("got %s want %s", got, tc.want)
		}
	}
}

func floatPtr(v float64) *float64 { return &v }
