package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// run executes the CLI with args and returns stdout and the exit code.
func run(t *testing.T, env map[string]string, args ...string) (string, int) {
	t.Helper()
	t.Setenv("MEETSTREAM_CONFIG_DIR", t.TempDir())
	t.Setenv("MEETSTREAM_API_KEY", "")
	isolateAgentLookup(t)
	for k, v := range env {
		t.Setenv(k, v)
	}
	g = globals{}
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	root := NewRoot(BuildInfo{Version: "1.2.3", Commit: "abc", Date: "today"})
	root.SetArgs(args)
	code := 0
	if err := root.Execute(); err != nil {
		code = 1
		if ee, ok := err.(*ExitError); ok {
			code = ee.Code
		}
	}
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String(), code
}

func TestVersionJSON(t *testing.T) {
	out, code := run(t, nil, "version", "--json")
	var v map[string]string
	if code != 0 || json.Unmarshal([]byte(out), &v) != nil || v["version"] != "1.2.3" {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestAuthStatusEnvWinsAndIsMasked(t *testing.T) {
	key := "ms_live_supersecret_1234"
	out, code := run(t, map[string]string{"MEETSTREAM_API_KEY": key}, "auth", "status", "--offline", "--json")
	if code != 0 || strings.Contains(out, "supersecret") || !strings.Contains(out, `"source": "env"`) || !strings.Contains(out, "1234") {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestNotSignedInExits2(t *testing.T) {
	_, code := run(t, nil, "auth", "status", "--json", "--no-input")
	if code != 2 {
		t.Fatalf("want exit 2, got %d", code)
	}
}

func TestPlannedCommandsExit2(t *testing.T) {
	for _, c := range [][]string{{"bots", "tail"}} {
		if _, code := run(t, nil, append(c, "--json")...); code != 2 {
			t.Errorf("%v: want exit 2, got %d", c, code)
		}
	}
}

func TestEveryCommandAcceptsGlobalFlags(t *testing.T) {
	for _, c := range NewRoot(BuildInfo{Version: "x"}).Commands() {
		if c.DisableFlagParsing {
			continue // planned stubs: checked by TestPlannedCommandsExit2
		}
		for _, f := range []string{"--json", "--no-input"} {
			if _, code := run(t, nil, c.Name(), f, "--help"); code != 0 {
				t.Errorf("%s %s --help: exit %d", c.Name(), f, code)
			}
		}
	}
}

// TestHomeView renders the home screen; MEETSTREAM_DUMP_HOME=<file> also writes
// it with colour, for screenshots (scripts/screenshot.sh).
func TestHomeView(t *testing.T) {
	if os.Getenv("MEETSTREAM_DUMP_HOME") != "" {
		lipgloss.SetColorProfile(termenv.TrueColor)
	}
	m := homeModel{version: "0.1.0", signedIn: "Signed in · key ms_…a1b2 (~/.meetstream)", cursor: 2}
	v := m.View()
	for _, want := range []string{"MeetStream", "Send a bot to a meeting", "coming in M4", "enter choose"} {
		if !strings.Contains(v, want) {
			t.Errorf("home view missing %q", want)
		}
	}
	if p := os.Getenv("MEETSTREAM_DUMP_HOME"); p != "" {
		if err := os.WriteFile(p, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoginWithKeyFromStdinStoresMaskedKey(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token ms_piped_key_9876" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`{"bots":[{},{}],"hasNextPage":false}`))
	}))
	defer api.Close()
	dir := t.TempDir()
	in, w, _ := os.Pipe()
	_, _ = w.WriteString("ms_piped_key_9876\n")
	w.Close()
	oldIn := os.Stdin
	os.Stdin = in
	defer func() { os.Stdin = oldIn }()
	out, code := run(t, map[string]string{"MEETSTREAM_API_URL": api.URL, "MEETSTREAM_CONFIG_DIR": dir}, "login", "--with-key", "--json")
	if code != 0 || strings.Contains(out, "piped_key") || !strings.Contains(out, `"bots_on_account": "2"`) {
		t.Fatalf("code=%d out=%s", code, out)
	}
	b, _ := os.ReadFile(dir + "/config.json")
	if !strings.Contains(string(b), "ms_piped_key_9876") {
		t.Fatal("key not stored")
	}
	if fi, _ := os.Stat(dir + "/config.json"); fi.Mode().Perm() != 0o600 {
		t.Fatalf("config mode %v", fi.Mode().Perm())
	}
}

func TestAuthSetKeyFromStdin(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token ms_set_key_4321" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`{"bots":[],"hasNextPage":false}`))
	}))
	defer fake.Close()
	dir := t.TempDir()
	in, w, _ := os.Pipe()
	_, _ = w.WriteString("ms_set_key_4321\n")
	w.Close()
	oldIn := os.Stdin
	os.Stdin = in
	defer func() { os.Stdin = oldIn }()
	out, code := run(t, map[string]string{"MEETSTREAM_API_URL": fake.URL, "MEETSTREAM_CONFIG_DIR": dir}, "auth", "set-key", "--json")
	if code != 0 || strings.Contains(out, "set_key_4321") || !strings.Contains(out, `"validated": true`) {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if b, _ := os.ReadFile(dir + "/config.json"); !strings.Contains(string(b), "ms_set_key_4321") {
		t.Fatal("key not stored")
	}
}

// agent setup --dry-run builds the relay MIA agent (no tunnel, no pairing key)
// from the computer's stored sign-in, without sending anything.
func TestAgentSetupDryRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MEETSTREAM_AGENT_KIT_HOME", home)
	t.Setenv("MEETSTREAM_AGENT_KIT_CREDENTIALS", "file")
	if err := os.WriteFile(home+"/credentials.json", []byte(`{"device_id":"dev_test1","refresh_token":"rt","auth_url":"http://a.invalid","relay_url":"wss://r.invalid/v1/connect"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(bin+"/claude", []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	out, code := run(t, nil, "agent", "setup", "--agent", "claude", "--access", "confirm", "--dry-run", "--json", "--no-input")
	var m struct {
		DryRun bool `json:"dry_run"`
		Method string
		Body   struct {
			Model struct {
				Provider   string         `json:"provider"`
				LocalAgent map[string]any `json:"local_agent"`
			} `json:"model"`
		} `json:"body"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &m) != nil || !m.DryRun || m.Method != "POST" {
		t.Fatalf("code=%d out=%s", code, out)
	}
	la := m.Body.Model.LocalAgent
	if m.Body.Model.Provider != "local_agent" || la["transport"] != "relay" || la["device_id"] != "dev_test1" || la["access"] != "confirm" || la["url"] != nil {
		t.Fatalf("model block %+v", m.Body.Model)
	}
}
