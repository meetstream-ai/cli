package cli

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meetstream-ai/cli/internal/api"
	"github.com/meetstream-ai/cli/internal/mock"
)

// startMock runs the mock API on a real loopback port (join --follow polls it).
func startMock(t *testing.T, scenario string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv := mock.New(ctx, mock.Scenarios[scenario], 20*time.Millisecond)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts.URL + "/api/v1"
}

func ndjson(t *testing.T, out string) []map[string]any {
	t.Helper()
	var rows []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("not NDJSON: %q", line)
		}
		rows = append(rows, m)
	}
	return rows
}

func TestJoinFollowHappyPathNDJSON(t *testing.T) {
	env := map[string]string{"MEETSTREAM_API_KEY": "k", "MEETSTREAM_API_URL": startMock(t, "notetaker-happy")}
	out, code := run(t, env, "join", "https://meet.google.com/abc-defg-hij", "--transcription", "meetstream", "--follow", "--json")
	if code != 0 {
		t.Fatalf("code=%d out=%s", code, out)
	}
	rows := ndjson(t, out)
	if rows[0]["event"] != "sent" || rows[len(rows)-1]["event"] != "transcript_ready" {
		t.Fatalf("rows=%v", rows)
	}
	if rows[len(rows)-1]["segments"].(float64) != 4 {
		t.Fatalf("segments: %v", rows[len(rows)-1])
	}
}

func TestJoinFollowExitCodes(t *testing.T) {
	cases := []struct {
		scenario, provider string
		code               int
	}{
		{"waiting-room-timeout", "meetstream", 1},
		{"transcript-failed", "assemblyai", 4},
		{"bot-removed", "meetstream", 0},
	}
	for _, c := range cases {
		env := map[string]string{"MEETSTREAM_API_KEY": "k", "MEETSTREAM_API_URL": startMock(t, c.scenario)}
		if _, code := run(t, env, "join", "https://meet.google.com/x", "--transcription", c.provider, "--follow", "--json"); code != c.code {
			t.Errorf("%s: code=%d want %d", c.scenario, code, c.code)
		}
	}
}

func TestTranscriptionFlagsMapToPayload(t *testing.T) {
	t.Setenv("MEETSTREAM_CONFIG_DIR", t.TempDir())
	o := api.CreateOpts{}
	if err := applyTranscriptionFlags(&o, "meetstream", ""); err != nil || o.Transcript != "meetstream" {
		t.Fatalf("post-call: %v %q", err, o.Transcript)
	}
	o = api.CreateOpts{LiveTranscriptWebhook: "https://hooks.example/live"}
	if err := applyTranscriptionFlags(&o, "", "meetstream"); err != nil || o.Transcript != "meetstream_streaming" {
		t.Fatalf("live: %v %q", err, o.Transcript)
	}
	o = api.CreateOpts{}
	if err := applyTranscriptionFlags(&o, "meetstream", "meetstream"); err == nil {
		t.Fatal("both flags must be refused")
	}
	o = api.CreateOpts{}
	if err := applyTranscriptionFlags(&o, "", "meetstream"); err == nil {
		t.Fatal("live with no webhook and no listen session must be refused")
	}
	o = api.CreateOpts{Transcript: "deepgram"}
	if err := applyTranscriptionFlags(&o, "assemblyai", ""); err == nil {
		t.Fatal("-t and --transcription that disagree must be refused")
	}
	o = api.CreateOpts{}
	if err := applyTranscriptionFlags(&o, "deepgram_streaming", ""); err == nil {
		t.Fatal("a streaming provider in --transcription must point to --live-transcription")
	}
}

func TestOldTranscriptFlagUnchanged(t *testing.T) {
	t.Setenv("MEETSTREAM_CONFIG_DIR", t.TempDir())
	o := api.CreateOpts{Transcript: "deepgram"}
	if err := applyTranscriptionFlags(&o, "", ""); err != nil || o.Transcript != "deepgram" || o.Callback != "" {
		t.Fatalf("-t alone must pass through: %v %+v", err, o)
	}
}

func TestListenSessionFillsCallbackOnlyWhenReachable(t *testing.T) {
	t.Setenv("MEETSTREAM_CONFIG_DIR", t.TempDir())
	if err := saveListenSession(&listenSession{LocalURL: "http://127.0.0.1:3333/webhook", PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MEETSTREAM_API_URL", "https://api.meetstream.ai/api/v1")
	o := api.CreateOpts{}
	_ = applyTranscriptionFlags(&o, "", "")
	if o.Callback != "" {
		t.Fatalf("production API cannot reach a local URL; got callback %q", o.Callback)
	}
	t.Setenv("MEETSTREAM_API_URL", "http://127.0.0.1:4010/api/v1")
	_ = applyTranscriptionFlags(&o, "", "")
	if o.Callback != "http://127.0.0.1:3333/webhook" {
		t.Fatalf("mock API: callback %q", o.Callback)
	}
	o = api.CreateOpts{Callback: "https://mine.example/hook"}
	_ = applyTranscriptionFlags(&o, "", "")
	if o.Callback != "https://mine.example/hook" {
		t.Fatal("-c must win over the listen session")
	}
}

func TestDeadListenSessionIgnored(t *testing.T) {
	t.Setenv("MEETSTREAM_CONFIG_DIR", t.TempDir())
	_ = saveListenSession(&listenSession{LocalURL: "http://127.0.0.1:1/webhook", PublicURL: "https://x.example/webhook", PID: 999999})
	if loadListenSession() != nil {
		t.Fatal("a session whose process ended must be ignored")
	}
}

func TestInitNonInteractiveWritesProject(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nt")
	env := map[string]string{"MEETSTREAM_API_KEY": "ms_secret_value"}
	out, code := run(t, env, "init", "notetaker", "--llm", "openai", "--email", "smtp", "--dir", dir, "--yes", "--json")
	if code != 0 {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if strings.Contains(out, "ms_secret_value") {
		t.Fatal("init must never print the key")
	}
	var res map[string]any
	_ = json.Unmarshal([]byte(out), &res)
	if res["use_case"] != "notetaker" || res["lang"] != "python" {
		t.Fatalf("res=%v", res)
	}
	for _, f := range []string{"app.py", "summarize.py", "deliver.py", "AGENTS.md", ".gitignore", ".env.example", ".env"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing %s", f)
		}
	}
	env2, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if !strings.Contains(string(env2), "MEETSTREAM_API_KEY=ms_secret_value") || !strings.Contains(string(env2), "OPENAI_MODEL=") || !strings.Contains(string(env2), "SMTP_HOST=") {
		t.Fatalf(".env=%s", env2)
	}
	if st, _ := os.Stat(filepath.Join(dir, ".env")); st != nil && st.Mode().Perm() != 0600 && os.PathSeparator == '/' {
		t.Fatalf(".env mode %v", st.Mode().Perm())
	}
	if _, code := run(t, env, "init", "notetaker", "--dir", dir, "--yes"); code != 2 {
		t.Fatalf("non-empty dir: code=%d", code)
	}
}

func TestInitErrors(t *testing.T) {
	cases := map[string][]string{
		"missing use case": {"init", "--no-input", "--json"},
		"unknown lang":     {"init", "notetaker", "--lang", "node", "--yes"},
		"unknown provider": {"init", "send-a-bot", "--transcription", "nope", "--yes"},
	}
	for name, args := range cases {
		if _, code := run(t, nil, append(args, "--dir", t.TempDir()+"/x")...); code != 2 {
			t.Errorf("%s: code=%d", name, code)
		}
	}
	if _, code := run(t, nil, "init", "voice-bridge", "--yes", "--dir", t.TempDir()+"/v"); code != 3 {
		t.Errorf("coming-soon template: code=%d", code)
	}
}

func TestEveryTemplateRenders(t *testing.T) {
	for _, u := range []struct{ id, lang string }{{"notetaker", "python"}, {"send-a-bot", "python"}, {"send-a-bot", "node"}, {"live-captions", "python"}, {"webhook-receiver", "python"}, {"webhook-receiver", "node"}} {
		dir := filepath.Join(t.TempDir(), u.id)
		if out, code := run(t, nil, "init", u.id, "--lang", u.lang, "--dir", dir, "--yes", "--no-env"); code != 0 {
			t.Errorf("%s/%s: code=%d %s", u.id, u.lang, code, out)
		}
	}
}

func TestEnvCheckAndWrite(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, ".env.example"), []byte("MEETSTREAM_API_KEY=\nPORT=8000\nRESEND_API_KEY=\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, ".env"), []byte("RESEND_API_KEY=re_x\nEXTRA=1\n"), 0600)
	env := map[string]string{"MEETSTREAM_API_KEY": "ms_k"}
	out, code := run(t, env, "env", "--dir", dir, "--check", "--json")
	if code != 0 || strings.Contains(out, "ms_k") || strings.Contains(out, "re_x") {
		t.Fatalf("check: code=%d out=%s", code, out)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".env")); strings.Contains(string(b), "ms_k") {
		t.Fatal("--check must not write")
	}
	if _, code := run(t, env, "env", "--dir", dir); code != 0 {
		t.Fatalf("write: code=%d", code)
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".env"))
	for _, want := range []string{"MEETSTREAM_API_KEY=ms_k", "PORT=8000", "RESEND_API_KEY=re_x", "EXTRA=1"} {
		if !strings.Contains(string(b), want) {
			t.Errorf(".env lacks %s: %s", want, b)
		}
	}
	_ = os.WriteFile(filepath.Join(dir, ".env.example"), []byte("ANTHROPIC_API_KEY=\n"), 0644)
	if _, code := run(t, env, "env", "--dir", dir, "--check"); code != 3 {
		t.Fatalf("missing key: code=%d", code)
	}
}

func TestTriggerPostsEnvelope(t *testing.T) {
	got := make(chan map[string]any, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &m)
		got <- m
	}))
	defer ts.Close()
	if _, code := run(t, nil, "trigger", "bot.notallowed", "--forward-to", ts.URL, "--json"); code != 0 {
		t.Fatalf("code=%d", code)
	}
	m := <-got
	if m["event"] != "bot.stopped" || m["bot_event"] != "bot.notallowed" || m["status_code"].(float64) != 500 {
		t.Fatalf("envelope=%v", m)
	}
	if _, code := run(t, nil, "trigger", "nope", "--forward-to", ts.URL); code != 2 {
		t.Fatalf("unknown event: code=%d", code)
	}
	if _, code := run(t, nil, "trigger", "bot.joining"); code != 2 {
		t.Fatalf("no target: code=%d", code)
	}
}

func TestTunnelURLParsingAndMissingBinary(t *testing.T) {
	if u := tunnelURLFrom("cloudflared", "INF |  https://quiet-river-1234.trycloudflare.com  |"); u != "https://quiet-river-1234.trycloudflare.com" {
		t.Fatalf("cloudflared: %q", u)
	}
	if u := tunnelURLFrom("ngrok", `{"msg":"started tunnel","url":"https://ab12.ngrok-free.app"}`); u != "https://ab12.ngrok-free.app" {
		t.Fatalf("ngrok: %q", u)
	}
	old := lookTunnel
	lookTunnel = func(string) (string, error) { return "", os.ErrNotExist }
	defer func() { lookTunnel = old }()
	_, err := startTunnel(context.Background(), "cloudflared", 3333, "/webhook")
	if ee, ok := err.(*ExitError); !ok || ee.Code != 3 {
		t.Fatalf("missing binary: %v", err)
	}
	tp, err := startTunnel(context.Background(), "https://mine.example", 3333, "/webhook")
	if err != nil || tp.URL != "https://mine.example/webhook" {
		t.Fatalf("own URL: %v %+v", err, tp)
	}
}

func TestListenWritesAndRemovesSession(t *testing.T) {
	t.Setenv("MEETSTREAM_CONFIG_DIR", t.TempDir())
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	ln.Close()
	_ = saveListenSession(&listenSession{LocalURL: "http://" + ln.Addr().String() + "/webhook", PID: os.Getpid()})
	if s := loadListenSession(); s == nil || s.callbackURL("http://127.0.0.1:4010") == "" {
		t.Fatal("session not loaded")
	}
	removeListenSession()
	if loadListenSession() != nil {
		t.Fatal("session not removed")
	}
}

func TestAIInstallWritesSkillAndMCP(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	out, code := run(t, nil, "ai", "install", "codex", "cursor", "--json")
	if code != 0 {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if b, err := os.ReadFile(filepath.Join(home, ".codex", "skills", "meetstream-cli", "SKILL.md")); err != nil || len(b) == 0 {
		t.Fatal("codex skill not written")
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".cursor", "mcp.json")); !strings.Contains(string(b), hostedMCPURL) {
		t.Fatalf("cursor mcp: %s", b)
	}
	if _, code := run(t, nil, "ai", "install", "codex", "cursor", "--json"); code != 0 {
		t.Fatalf("second run must succeed: code=%d", code)
	}
	if _, code := run(t, nil, "ai", "install", "vim"); code != 2 {
		t.Fatalf("unknown agent: code=%d", code)
	}
}

func TestCommandsCatalogHasEnums(t *testing.T) {
	out, code := run(t, nil, "commands", "--json")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	var cat struct {
		Commands []commandInfo `json:"commands"`
	}
	_ = json.Unmarshal([]byte(out), &cat)
	found := false
	for _, c := range cat.Commands {
		if c.Path == "join" {
			for _, f := range c.Flags {
				if f.Name == "transcription" && len(f.Enum) > 0 && f.Enum[0] == "meetstream" {
					found = true
				}
			}
		}
	}
	if !found || len(cat.Commands) < 50 {
		t.Fatalf("join --transcription enum missing (%d commands)", len(cat.Commands))
	}
}

func TestProvidersJSONKeepsDefaults(t *testing.T) {
	out, code := run(t, nil, "transcription", "providers", "--json")
	if code != 0 || !strings.Contains(out, `"transcribe": "deepgram"`) {
		t.Fatalf("defaults must be reported unchanged: %s", out)
	}
}

func TestMockRejectsNonLoopback(t *testing.T) {
	if _, code := run(t, nil, "mock", "--host", "0.0.0.0"); code != 2 {
		t.Fatalf("code=%d", code)
	}
}

func TestEnvKeyTravelsWithProfileURL(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, ".env.example"), []byte("MEETSTREAM_API_KEY=\nMEETSTREAM_API_URL=https://api.meetstream.ai/api/v1\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, ".env"), []byte("MEETSTREAM_API_URL=https://evil.example/collect\n"), 0600)
	env := map[string]string{"MEETSTREAM_API_KEY": "ms_k", "MEETSTREAM_API_URL": "https://api.meetstream.ai/api/v1"}
	if _, code := run(t, env, "env", "--dir", dir); code != 0 {
		t.Fatalf("code=%d", code)
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if strings.Contains(string(b), "evil.example") || !strings.Contains(string(b), "MEETSTREAM_API_URL=https://api.meetstream.ai/api/v1") {
		t.Fatalf("the key must be written with the profile URL: %s", b)
	}
}

func TestEnvRefusesSymlink(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, ".env.example"), []byte("MEETSTREAM_API_KEY=\n"), 0644)
	target := filepath.Join(t.TempDir(), "elsewhere")
	_ = os.Symlink(target, filepath.Join(dir, ".env"))
	if _, code := run(t, map[string]string{"MEETSTREAM_API_KEY": "ms_k"}, "env", "--dir", dir); code != 2 {
		t.Fatalf("code=%d", code)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("the key was written through the link")
	}
}

func TestWebhookLineStripsControlCharacters(t *testing.T) {
	line := renderWebhookEvent(map[string]any{"speakerName": "Eve\x1b]52;c;bad\x07", "transcript": "hi\x1b[2Jthere"}, "12:00:00")
	if strings.ContainsAny(line, "\x07") || strings.Contains(line, "\x1b]52") || strings.Contains(line, "\x1b[2J") {
		t.Fatalf("control characters kept: %q", line)
	}
	line = renderWebhookEvent(map[string]any{"event": "bot.joining\x1b[31m", "message": "x\x9b2J"}, "12:00:00")
	if strings.Contains(line, "\x1b[31m") || strings.ContainsRune(line, '\x9b') {
		t.Fatalf("control characters kept: %q", line)
	}
}

func TestEnvURLForcedWhenExampleOmitsIt(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, ".env.example"), []byte("MEETSTREAM_API_KEY=\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, ".env"), []byte("export MEETSTREAM_API_URL=https://evil.example/collect?t=1\n"), 0600)
	env := map[string]string{"MEETSTREAM_API_KEY": "ms_k", "MEETSTREAM_API_URL": "https://api.meetstream.ai/api/v1"}
	out, errOut, code := runBoth(t, env, "env", "--dir", dir)
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	if strings.Contains(out+errOut, "evil.example") {
		t.Fatal("the old URL must not be printed")
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if strings.Contains(string(b), "evil.example") || !strings.Contains(string(b), "MEETSTREAM_API_URL=https://api.meetstream.ai/api/v1") {
		t.Fatalf(".env=%s", b)
	}
}

func TestEnvLoaderVariantsCannotKeepAnotherURL(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, ".env.example"), []byte("MEETSTREAM_API_KEY=\nexport\tMEETSTREAM_API_URL=https://evil.example/a\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, ".env"), []byte("MEETSTREAM_API_URL: https://evil.example/b\nexport  meetstream_api_url=https://evil.example/c\nBAD KEY=https://evil.example/d\nKEEP_ME=1\n"), 0600)
	env := map[string]string{"MEETSTREAM_API_KEY": "ms_k", "MEETSTREAM_API_URL": "https://api.meetstream.ai/api/v1"}
	if _, code := run(t, env, "env", "--dir", dir); code != 0 {
		t.Fatalf("code=%d", code)
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".env"))
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if strings.Contains(string(b), "evil.example") || lines[len(lines)-1] != "MEETSTREAM_API_URL=https://api.meetstream.ai/api/v1" || !strings.Contains(string(b), "KEEP_ME=1") {
		t.Fatalf(".env=%s", b)
	}
}
