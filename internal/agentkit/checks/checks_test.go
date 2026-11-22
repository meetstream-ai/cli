package checks

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"github.com/meetstream-ai/cli/internal/agentkit/meetingctx"
	"github.com/meetstream-ai/cli/internal/agentkit/warmcheck"
)

func exe(t *testing.T, dir, name string) string {
	t.Helper()
	_ = os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// env with an empty PATH and the given install folders.
func env(t *testing.T, dirs ...string) Env {
	e := DefaultEnv()
	e.Home = t.TempDir()
	e.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	e.InstallDirs = dirs
	return e
}

func TestResolveBinaryFindsAnOffPathInstall(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, ".local", "bin")
	e := env(t, local)
	claude := agents.All["claude"]
	if p, _ := ResolveBinary(e, claude, ""); p != "" {
		t.Fatal("found a CLI that is not there")
	}
	if c := Binary(e, claude, ""); c.Status != Fail || c.Hint != claude.InstallHint {
		t.Fatalf("%+v", c)
	}
	installed := exe(t, local, "claude")
	p, off := ResolveBinary(e, claude, "")
	if p != installed || !off {
		t.Fatalf("p=%q off=%v", p, off)
	}
	c := Binary(e, claude, "")
	if c.Status != Warn || !strings.Contains(c.Hint, `export PATH="`+local+`:$PATH"`) {
		t.Fatalf("%+v", c)
	}
	e.LookPath = func(string) (string, error) { return "/usr/bin/claude", nil }
	if p, off := ResolveBinary(e, claude, ""); p != "/usr/bin/claude" || off {
		t.Fatal("PATH must win over the install folders")
	}
	if p, _ := ResolveBinary(e, claude, installed); p != installed {
		t.Fatal("an override path is used as given")
	}
	if p, _ := ResolveBinary(e, claude, filepath.Join(dir, "nope")); p != "" {
		t.Fatal("a missing override path is not installed")
	}
}

func TestMuseAppCheck(t *testing.T) {
	launcher := exe(t, t.TempDir(), "muse")
	c, ok := MuseApp(agents.All["muse"], launcher)
	if !ok || c.Status != Fail || !strings.Contains(c.Hint, "MUSE_LAUNCHER_INSTALL=1 "+launcher) {
		t.Fatalf("%+v", c)
	}
	if _, ok := MuseApp(agents.All["claude"], launcher); ok {
		t.Fatal("only Muse has an app check")
	}
}

func TestWorkdirAndHome(t *testing.T) {
	e := env(t)
	dir := t.TempDir()
	if c := Workdir(e, dir); c.Status != OK {
		t.Fatalf("%+v", c)
	}
	if c := Workdir(e, filepath.Join(dir, "missing")); c.Status != Fail || !strings.Contains(c.Detail, "does not exist") {
		t.Fatalf("%+v", c)
	}
	file := filepath.Join(dir, "f")
	_ = os.WriteFile(file, nil, 0o644)
	if c := Workdir(e, file); c.Status != Fail || !strings.Contains(c.Detail, "is not a folder") {
		t.Fatalf("%+v", c)
	}
	e.Readable = func(string) bool { return false }
	if c := Workdir(e, dir); c.Status != Fail || !strings.Contains(c.Detail, "cannot be read") {
		t.Fatalf("%+v", c)
	}
	if c := Home(e, filepath.Join(dir, "not", "yet")); c.Status != OK { // created later: its parent is writable
		t.Fatalf("%+v", c)
	}
	e.Writable = func(string) bool { return false }
	if c := Home(e, dir); c.Status != Fail || !strings.Contains(c.Hint, "MEETSTREAM_AGENT_KIT_HOME") {
		t.Fatalf("%+v", c)
	}
}

func TestPort(t *testing.T) {
	e := env(t)
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	busy := ln.Addr().(*net.TCPAddr).Port
	if c := Port(e, busy); c.Status != Fail || !strings.Contains(c.Detail, "used by another program") {
		t.Fatalf("%+v", c)
	}
	_ = ln.Close()
	if c := Port(e, busy); c.Status != OK {
		t.Fatalf("%+v", c)
	}
}

func TestLoginWarmRelayContext(t *testing.T) {
	agy := agents.All["agy"]
	if c := Login(agy, "ok", ""); c.Status != OK {
		t.Fatalf("%+v", c)
	}
	if c := Login(agy, "unknown", "no login check"); c.Status != Warn {
		t.Fatalf("%+v", c)
	}
	if c := Login(agy, "expired", ""); c.Status != Fail || c.Hint != "Run: agy acp --login" {
		t.Fatalf("%+v", c)
	}
	if c := Warm(warmcheck.Result{Message: "Authentication required", Fix: "Sign in with: agy acp --login"}); c.Status != Fail || c.Hint == "" {
		t.Fatalf("%+v", c)
	}
	if c := Relay(false, "", ""); c.Status != Fail || c.Hint != "Run: meetstream agent login" {
		t.Fatalf("%+v", c)
	}
	heavy := []meetingctx.Item{{Name: "CLAUDE.md", Size: 40000, Loaded: true}, {Name: "AGENTS.md", Size: 40000, Loaded: false}}
	if c := Context(heavy, "project rules included", "~/MeetStream Agent"); c.Status != Warn || !strings.Contains(c.Detail, "~10000 tokens a turn (1 file(s))") {
		t.Fatalf("%+v", c)
	}
	if c := Context(heavy[:0], "meeting only", ""); c.Status != OK {
		t.Fatalf("%+v", c)
	}
}

func TestDoctorRunsTheChecksInOrder(t *testing.T) {
	local := t.TempDir()
	stub := exe(t, local, "agy")
	e := env(t, local)
	spec := agents.All["agy"]
	warmRan := false
	in := DoctorInput{
		Spec: &spec, Workdir: t.TempDir(), Home: t.TempDir(), Port: 0,
		Login: func(binary string) (string, string) {
			if binary != stub {
				t.Errorf("login check ran %q, not the resolved binary", binary)
			}
			return "unknown", ""
		},
		Warm: func(binary string) warmcheck.Result {
			warmRan = binary == stub
			return warmcheck.Result{OK: true, Message: "Warm mode (ACP) ready"}
		},
		Relay:   func() (bool, string, string) { return true, "laptop", "wss://relay" },
		Context: func(agents.Spec, string) ([]meetingctx.Item, string, string) { return nil, "meeting only", "" },
	}
	var names []string
	for _, c := range Doctor(e, in) {
		names = append(names, c.Name+":"+string(c.Status))
	}
	want := "config:ok workdir:ok agent:warn login:warn warm:ok relay:ok context:ok"
	if strings.Join(names, " ") != want || !warmRan {
		t.Fatalf("got  %s\nwant %s (warm ran: %v)", strings.Join(names, " "), want, warmRan)
	}
	verdict, failed := Verdict(Doctor(e, in))
	if verdict != "All good." || failed != 0 {
		t.Fatal(verdict)
	}
	// signed out: no warm check
	in.Login = func(string) (string, string) { return "expired", "" }
	cs := Doctor(e, in)
	for _, c := range cs {
		if c.Name == "warm" {
			t.Fatal("warm check after a failed sign-in")
		}
	}
	if _, failed := Verdict(cs); failed != 1 {
		t.Fatalf("failed %d", failed)
	}
	// unknown agent
	bad := Check{"agent", Fail, "unknown agent x", "choose: claude, codex"}
	cs = Doctor(e, DoctorInput{AgentErr: &bad, Workdir: t.TempDir(), Home: t.TempDir()})
	if len(cs) != 3 || cs[1] != bad {
		t.Fatalf("%+v", cs)
	}
}

func TestDiagnoseTurn(t *testing.T) {
	claude := agents.All["claude"]
	if DiagnoseTurn(claude, "hi", "", "", 0) != nil {
		t.Fatal("an answer is not a problem")
	}
	cases := []struct {
		answer, err, tail string
		kind              string
	}{
		{"", "agent CLI not found: claude", "", "not_installed"},
		{"", "agent exited with 1", "Please log in", "signed_out"},
		{"", "agent timed out after 60 s", "", "timeout"},
		{"", "agent exited with 2", "boom", "failed"},
		{"", "", "", "empty"},
	}
	for _, c := range cases {
		d := DiagnoseTurn(claude, c.answer, c.err, c.tail, 60)
		if d == nil || d.Kind != c.kind {
			t.Errorf("%q/%q: %+v, want %s", c.err, c.tail, d, c.kind)
		}
	}
	if d := DiagnoseTurn(claude, "", "agent timed out", "", 60); d.Message != "Claude Code did not answer within 60 s" {
		t.Fatal(d.Message)
	}
	if d := DiagnoseTurn(claude, "", "x", "Please log in", 0); d.Fix != "Sign in with: claude auth login" || d.Detail != "x\nPlease log in" {
		t.Fatalf("%+v", d)
	}
}
