package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"github.com/meetstream-ai/cli/internal/agentkit/bench"
	"github.com/meetstream-ai/cli/internal/agentkit/bridge"
	kitconfig "github.com/meetstream-ai/cli/internal/agentkit/config"
	agentlogin "github.com/meetstream-ai/cli/internal/agentkit/login"
	"github.com/meetstream-ai/cli/internal/api"
	"github.com/meetstream-ai/cli/internal/testutil/fakecli"
)

// Signed out at startup: the sign-in check notifies on serve's own goroutine
// before the header is drawn. It must not wait for that header (deadlock); the
// line shows right after it.
func TestServeSignedOutNoticeBeforeHeader(t *testing.T) {
	buf := captureUI(t)
	o := newServeOutput()
	checker := &agentlogin.Checker{Agent: agents.All["claude"], Binary: filepath.Join(t.TempDir(), "missing"),
		Notify: func(title, text string) { o.queue(title + ": " + text) }}
	done := make(chan agentlogin.Status, 1)
	go func() { done <- checker.Check(true) }()
	select {
	case st := <-done:
		if st.Usable() {
			t.Fatalf("state %q", st.State)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the sign-in notice waited for the header (deadlock)")
	}
	if buf.Len() != 0 {
		t.Fatalf("printed before the header: %q", buf)
	}
	fmt.Fprintln(buf, "HEADER")
	o.headerShown()
	if !strings.HasPrefix(buf.String(), "HEADER\nClaude Code is not installed") {
		t.Fatal(buf.String())
	}
	o.queue("after")
	if !strings.HasSuffix(buf.String(), "after\n") {
		t.Fatal(buf.String())
	}
}

// The turn line keeps the kit's "[turn] speaker: text" on the terminal with
// secrets hidden; the home app's background bridge logs only who spoke; and
// agent errors are redacted too.
func TestServeTurnLineRedactedAndHidden(t *testing.T) {
	echo := fakecli.Echo(t, t.TempDir(), "echo")
	spec, err := agents.Custom("'" + echo + "' ready")
	if err != nil {
		t.Fatal(err)
	}
	for _, hide := range []bool{false, true} {
		var mu sync.Mutex
		var lines []string
		c := kitconfig.Config{Agent: spec, Binary: echo, Workdir: t.TempDir(), RuntimeDir: t.TempDir(), HideTurnText: hide,
			Log: func(l string) { mu.Lock(); lines = append(lines, l); mu.Unlock() }}
		srv := bridge.New(c)
		srv.SetInternalToken("tok")
		post := func(path, body string) {
			r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer tok")
			srv.ServeHTTP(httptest.NewRecorder(), r)
		}
		post("/v1/session/start", `{"session_id":"s1"}`)
		post("/v1/turn", `{"session_id":"s1","messages":[{"speaker":"Sam","text":"my key is sk-ant-api03-abcdefghijklmnop, ok?"}]}`)
		srv.Close()
		mu.Lock()
		got := strings.Join(lines, "\n")
		mu.Unlock()
		if strings.Contains(got, "abcdefghijklmnop") {
			t.Fatalf("secret printed:\n%s", got)
		}
		want := "[turn] Sam: my key is [redacted], ok?"
		if hide {
			want = "[turn] Sam\n"
		}
		if !strings.Contains(got+"\n", want) {
			t.Fatalf("hide=%v: missing %q in\n%s", hide, want, got)
		}
		if hide && strings.Contains(got, "my key") {
			t.Fatalf("turn text in a hidden log:\n%s", got)
		}
	}
	// The home app runs the bridge with its turn text hidden, and keeps its
	// activity with secrets hidden.
	if args := strings.Join(homeAgentCommand().Args, " "); !strings.HasSuffix(args, "agent serve --hide-turn-text") {
		t.Fatal(args)
	}
	a := &bgAgent{changed: make(chan struct{}, 1)}
	a.add("[error] failed: Authorization: Bearer abcdefghijklmnop0123")
	if strings.Contains(a.log[0], "abcdefghijklmnop") {
		t.Fatal(a.log[0])
	}
}

// Background serving cannot ask "Allow …?" (the app holds the terminal):
// Ask-first access is refused with the foreground command.
func TestHomeRefusesAskFirstInTheBackground(t *testing.T) {
	m := homeFor(t, nil)
	if err := (kitconfig.Setup{Agent: "claude", Workdir: t.TempDir(), Tunnel: "relay", MaxAccess: "confirm"}).Save(""); err != nil {
		t.Fatal(err)
	}
	old := homeAgentCommand
	homeAgentCommand = func() *exec.Cmd { t.Fatal("started with confirm access"); return nil }
	t.Cleanup(func() { homeAgentCommand = old })
	m = moveTo(t, m, "Run my agent for meetings")
	m = homePress(t, m, "enter", "enter")
	if m.agent != nil || m.agentErr != "Ask-first access needs the foreground: run meetstream agent serve" || !strings.Contains(m.pane, "meetstream agent serve") {
		t.Fatalf("agent=%v err=%q\n%s", m.agent, m.agentErr, m.pane)
	}
}

// Re-running setup updates only setup's part of the model block: the agent's
// own model fields (system_prompt …) stay.
func TestRerunKeepsOtherModelFields(t *testing.T) {
	home := relayCreds(t)
	t.Setenv("PATH", fakeBin(t, "claude"))
	swap(t, &setupLoginCheck, func(agents.Spec, string) agentlogin.Status { return agentlogin.Status{State: "ok"} })
	if err := (kitconfig.Setup{Agent: "claude", Workdir: t.TempDir(), Tunnel: "relay", MIAAgentID: "cfg_1"}).Save(home); err != nil {
		t.Fatal(err)
	}
	s := &miaServer{get: `{"agent_config":{"agent_name":"Mine","voice":{"provider":"elevenlabs"},
		"model":{"provider":"openai","model":"gpt-4o","system_prompt":"Be terse.","temperature":0.2}}}`}
	env := map[string]string{"MEETSTREAM_API_URL": s.start(t), "MEETSTREAM_API_KEY": "ms_test_key"}
	if out, code := run(t, env, "setup", "--agent", "claude", "--no-input", "--no-start", "--json"); code != 0 {
		t.Fatalf("exit %d %s", code, out)
	}
	model, _ := s.put["model"].(map[string]any)
	if model["provider"] != "local_agent" || model["local_agent"] == nil || model["system_prompt"] != "Be terse." || model["temperature"] != 0.2 {
		t.Fatalf("model %+v", model)
	}
	if _, ok := model["model"]; ok { // the CLI's default: no hosted model id passed to the agent CLI
		t.Fatalf("stale model id kept: %+v", model)
	}
	if mergeModelBlock(map[string]any{"system_prompt": "x"}, map[string]any{"provider": "local_agent", "model": "m2"}).(map[string]any)["model"] != "m2" {
		t.Fatal("picked model not set")
	}
}

// --no-input re-run against a saved agent on a server without MeetStream ears
// and voice: the update keeps the agent's own, so it is not refused (exit 3
// stays for creating a new one).
func TestRerunWithoutHostedKeysUpdates(t *testing.T) {
	home := relayCreds(t)
	t.Setenv("PATH", fakeBin(t, "claude"))
	swap(t, &setupLoginCheck, func(agents.Spec, string) agentlogin.Status { return agentlogin.Status{State: "ok"} })
	s := &miaServer{catalog: disabledCatalog}
	env := map[string]string{"MEETSTREAM_API_URL": s.start(t), "MEETSTREAM_API_KEY": "ms_test_key"}
	if ee := setupExit(t, env, "setup", "--agent", "claude", "--no-input", "--no-start"); ee == nil || ee.Code != 3 {
		t.Fatalf("new agent: %+v", ee)
	}
	if err := (kitconfig.Setup{Agent: "claude", Workdir: t.TempDir(), Tunnel: "relay", MIAAgentID: "cfg_1"}).Save(home); err != nil {
		t.Fatal(err)
	}
	s.calls = nil
	if out, code := run(t, env, "setup", "--agent", "claude", "--no-input", "--no-start", "--json"); code != 0 {
		t.Fatalf("update: exit %d %s", code, out)
	}
	if strings.Join(s.calls, ",") != "GET cfg_1,PUT " {
		t.Fatalf("calls %v", s.calls)
	}
}

// A failed save is not reported as "Saved your answers".
func TestQuitAfterTestSaysWhenNotSaved(t *testing.T) {
	w := localWizard(t, "claude")
	captureUI(t)
	file := filepath.Join(t.TempDir(), "not-a-folder")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MEETSTREAM_AGENT_KIT_HOME", file)
	var ee *ExitError
	if err := w.quitAfterTest("boom"); !errors.As(err, &ee) || strings.Contains(ee.Hint, "Saved your answers") || !strings.Contains(ee.Hint, "not saved") {
		t.Fatalf("%v", err)
	}
	if err := w.saveAnswers(); err == nil {
		t.Fatal("save error swallowed")
	}
}

// Calendar connect from setup sends the MIA agent it made as the calendar
// bot's agent, on top of the auto-join bot already saved.
func TestSetupCalendarUsesTheNewAgent(t *testing.T) {
	var mu sync.Mutex
	var start map[string]any
	settings := `{"auto_schedule_enabled":true,"default_bot_config":{"bot_name":"Notetaker","video_required":true}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method + " " + r.URL.Path {
		case "GET /calendar/auto-schedule/settings":
			_, _ = w.Write([]byte(settings))
		case "POST /calendar/oauth/start":
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &start)
			w.WriteHeader(404) // no one-click connect here: setup leaves a note
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MEETSTREAM_API_URL", srv.URL)
	t.Setenv("MEETSTREAM_API_KEY", "ms_test_key")
	c := api.New(srv.URL, "ms_test_key", "test")
	cfg := calendarBotConfig(context.Background(), c, "cfg_9")
	if cfg["agent_config_id"] != "cfg_9" || cfg["bot_name"] != "Notetaker" || cfg["video_required"] != true {
		t.Fatalf("%+v", cfg)
	}
	settings = `{"auto_schedule_enabled":false}`
	if cfg := calendarBotConfig(context.Background(), c, "cfg_9"); cfg["bot_name"] != "MIA" || cfg["agent_config_id"] != "cfg_9" {
		t.Fatalf("%+v", cfg)
	}
	if calendarBotConfig(context.Background(), c, "") != nil {
		t.Fatal("no agent: the server's default")
	}

	w := testWizard()
	w.cmd = NewRoot(BuildInfo{Version: "test"})
	w.cmd.SetContext(context.Background())
	w.calendar = "google"
	out := captureUI(t)
	if err := w.connectCalendar(map[string]any{"bot_name": "MIA", "agent_config_id": "cfg_9"}, "Claude Code in the meeting"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	bot, _ := start["default_bot_config"].(map[string]any)
	if bot["agent_config_id"] != "cfg_9" {
		t.Fatalf("oauth start %+v", start)
	}
	if !strings.Contains(out.String(), "Bots that join your calendar meetings will run MIA agent Claude Code in the meeting (cfg_9).") {
		t.Fatal(out.String())
	}
}

// A failed bench with --json is one JSON document, the error inside it.
func TestAgentBenchFailureIsOneJSONDocument(t *testing.T) {
	t.Setenv("MEETSTREAM_AGENT_KIT_HOME", t.TempDir())
	t.Setenv("PATH", fakeBin(t, "claude"))
	swap(t, &benchExec, func(context.Context, []string, string, string, []string, func(string)) (string, error) {
		return "", errors.New("agent CLI not found: claude")
	})
	out, code := run(t, nil, "agent", "bench", "--agent", "claude", "--models", "x", "--json", "--no-input")
	dec := json.NewDecoder(strings.NewReader(out))
	var doc struct {
		Results []map[string]any `json:"results"`
		Error   struct {
			Message  string `json:"message"`
			ExitCode int    `json:"exit_code"`
		} `json:"error"`
	}
	if err := dec.Decode(&doc); err != nil || code != 1 {
		t.Fatalf("code=%d err=%v %s", code, err, out)
	}
	if dec.More() {
		t.Fatalf("more than one JSON document:\n%s", out)
	}
	if len(doc.Results) != 1 || doc.Error.ExitCode != 1 || doc.Error.Message != "no model reported its token usage" {
		t.Fatalf("%s", out)
	}
}

// Ctrl-C in the live table exits 130 with what finished shown.
func TestAgentBenchLiveCtrlCExits130(t *testing.T) {
	t.Setenv("MEETSTREAM_AGENT_KIT_HOME", t.TempDir())
	t.Setenv("PATH", fakeBin(t, "claude"))
	swap(t, &benchExec, fakeClaudeExec(nil))
	jobs := func(ctx context.Context, q *bench.Queue) ([]bench.Job, bool) { return q.Run(ctx), true }
	var ee *ExitError
	out, got := runBenchLiveCmd(t, jobs)
	if !errors.As(got, &ee) || ee.Code != 130 || ee.Silent {
		t.Fatalf("%v", got)
	}
	if !strings.Contains(out, "opus") || !strings.Contains(out, "/h") {
		t.Fatalf("finished results not shown:\n%s", out)
	}
	// Esc is a stop, not an interrupt: exit 0.
	if _, err := runBenchLiveCmd(t, func(ctx context.Context, q *bench.Queue) ([]bench.Job, bool) { return q.Run(ctx), false }); err != nil {
		t.Fatal(err)
	}
}

// runBenchLiveCmd runs `agent bench` with live as the live table (as at a
// terminal); it returns what it printed and its error.
func runBenchLiveCmd(t *testing.T, live func(context.Context, *bench.Queue) ([]bench.Job, bool)) (string, error) {
	t.Helper()
	swap(t, &runBenchLive, live)
	swap(t, &benchLiveAllowed, func() bool { return true })
	g = globals{}
	cmd := NewRoot(BuildInfo{Version: "test"})
	cmd.SetArgs([]string{"agent", "bench", "--agent", "claude", "--models", "opus", "--no-input"})
	cmd.SetOut(io.Discard)
	var err error
	out := withStderr(t, func() { err = cmd.Execute() })
	return out, err
}
