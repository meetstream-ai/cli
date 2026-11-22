package alerts

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/meetstream-ai/cli/internal/agentkit/agents"
)

const codexOutOfCredits = "ERROR: Your workspace is out of credits. Ask your workspace owner to refill in order to continue."

// Ported from tests/test_rules_alerts.py::test_failure_kinds.
func TestFailureKinds(t *testing.T) {
	cases := []struct {
		event map[string]any
		want  string
	}{
		{map[string]any{"type": "error", "code": "agent_error", "message": codexOutOfCredits}, "out_of_credits"},
		{map[string]any{"type": "error", "code": "agent_error", "message": "429 Too Many Requests"}, "rate_limited"},
		{map[string]any{"type": "error", "code": "agent_error", "message": "the agent process exited (1)"}, "crashed"},
		{map[string]any{"type": "error", "code": "agent_error", "message": "boom"}, "failed"},
		{map[string]any{"type": "done", "text": "That took too long, so I stopped it.", "timed_out": true}, "timeout"},
		{map[string]any{"type": "error", "code": "auth_required", "message": "Not logged in"}, ""}, // the login checker tells
		{map[string]any{"type": "error", "code": "cancelled", "message": "barge-in"}, ""},
		{map[string]any{"type": "error", "code": "busy", "message": "a turn is already running"}, ""},
		{map[string]any{"type": "done", "text": "ok"}, ""},
	}
	for _, c := range cases {
		if got := Kind(c.event); got != c.want {
			t.Errorf("Kind(%v) = %q, want %q", c.event, got, c.want)
		}
	}
}

// Ported from tests/test_rules_alerts.py::test_alert_text_says_what_to_do.
func TestAlertTextSaysWhatToDo(t *testing.T) {
	got := Message("out_of_credits", agents.All["codex"])
	want := "Codex CLI ran out of credits. MIA is quiet in your meeting; run `codex login` or switch agents."
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// The kit's message_for texts for every kind, including the plain failure,
// which carries the agent's error (cut to 120 characters).
func TestMessages(t *testing.T) {
	claude := agents.All["claude"]
	cases := map[string]string{
		"rate_limited": "Claude Code is rate limited. MIA is quiet until it recovers; wait or switch models.",
		"timeout":      "Claude Code took too long, so the turn was stopped. MIA told the meeting.",
		"crashed":      "Claude Code stopped unexpectedly. The bridge starts it again on the next turn.",
		"failed":       "Claude Code failed: boom. MIA is quiet in your meeting.",
		"unknown":      "Claude Code failed: boom. MIA is quiet in your meeting.",
	}
	for kind, want := range cases {
		if got := Message(kind, claude, "boom"); got != want {
			t.Errorf("%s: got %q\nwant %q", kind, got, want)
		}
	}
	long := strings.Repeat("é", 200)
	if got := Message("failed", claude, long); got != "Claude Code failed: "+strings.Repeat("é", 120)+". MIA is quiet in your meeting." {
		t.Errorf("detail not cut to 120 characters: %q", got)
	}
	custom, _ := agents.Custom("mytool {prompt}")
	if got := Message("out_of_credits", custom); !strings.HasSuffix(got, "; check your agent or switch agents.") {
		t.Errorf("no relogin: %q", got)
	}
}

type call struct {
	name string
	args []string
}

// fakeNotifier swaps the OS, PATH lookup and runner so no real notification fires.
func fakeNotifier(t *testing.T, os string, onPath ...string) *[]call {
	t.Helper()
	oldGOOS, oldLook, oldRun := goos, lookPath, run
	t.Cleanup(func() { goos, lookPath, run = oldGOOS, oldLook, oldRun })
	goos = os
	lookPath = func(name string) (string, error) {
		for _, p := range onPath {
			if p == name {
				return "/usr/bin/" + name, nil
			}
		}
		return "", errors.New("not found")
	}
	var calls []call
	run = func(ctx context.Context, name string, args ...string) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("the notifier must run with a timeout")
		}
		calls = append(calls, call{name, args})
		return errors.New("a failing notifier must not matter")
	}
	return &calls
}

// The per-OS command the kit's console.notify runs.
func TestDesktopCommandPerOS(t *testing.T) {
	title, text := "MeetStream Agent Kit", `Codex CLI said "no" \ twice`
	cases := []struct {
		os     string
		onPath []string
		want   []call
	}{
		{"darwin", []string{"osascript"}, []call{{"osascript", []string{"-e", `display notification "Codex CLI said \"no\" \\ twice" with title "MeetStream Agent Kit"`}}}},
		{"darwin", nil, nil},
		{"darwin", []string{"notify-send"}, nil}, // macOS only uses osascript
		{"linux", []string{"notify-send"}, []call{{"notify-send", []string{title, text}}}},
		{"linux", nil, nil},
		{"freebsd", []string{"notify-send"}, []call{{"notify-send", []string{title, text}}}},
		{"windows", nil, nil},
	}
	for _, c := range cases {
		calls := fakeNotifier(t, c.os, c.onPath...)
		Desktop(title, text)
		if !reflect.DeepEqual(*calls, c.want) {
			t.Errorf("%s %v: ran %v, want %v", c.os, c.onPath, *calls, c.want)
		}
	}
}
