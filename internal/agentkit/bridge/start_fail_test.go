package bridge

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"github.com/meetstream-ai/cli/internal/agentkit/config"
)

// An agent CLI that cannot start fails the turn at once with that reason; it
// used to look like a slow agent until the turn timed out.
func TestOneShotAgentThatCannotStartFailsTheTurnAtOnce(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-agent")
	spec, err := agents.Custom("'" + missing + "' {prompt}")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	srv := New(config.Config{Agent: spec, Binary: missing, Workdir: dir, RuntimeDir: dir, Owner: "Nav", Timeout: 30 * time.Second})
	srv.SetInternalToken("tok")
	hs := httptest.NewServer(srv)
	t.Cleanup(func() { hs.Close(); srv.Close() })

	res := post(t, hs.URL+"/v1/session/start", "tok", map[string]any{"session_id": "bot_x"}) // a custom CLI has no login check: "unknown"
	res.Body.Close()
	if res.StatusCode != 200 || srv.Session("bot_x") == nil {
		t.Fatalf("session/start %d", res.StatusCode)
	}
	start := time.Now()
	_, last := readTurn(t, post(t, hs.URL+"/v1/turn", "tok", map[string]any{"session_id": "bot_x", "messages": []map[string]string{{"speaker": "Nav", "text": "hi"}}}))
	if last["type"] != "error" || !strings.Contains(last["message"].(string), "could not be started") {
		t.Fatalf("last event %v", last)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("took %s: waited for the timeout", took)
	}
}

// A start refused because the CLI is missing leaves no session; the terminal
// says why at once instead of only "unknown session" at the next turn.
func TestRefusedStartIsLogged(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	dir := t.TempDir()
	srv := New(config.Config{Agent: agents.All["claude"], Binary: filepath.Join(dir, "missing-claude"), Workdir: dir, RuntimeDir: dir, Owner: "Nav",
		Log: func(l string) { mu.Lock(); lines = append(lines, l); mu.Unlock() }})
	srv.SetInternalToken("tok")
	defer srv.Close()
	r := httptest.NewRequest(http.MethodPost, "/v1/session/start", strings.NewReader(`{"session_id":"s1"}`))
	r.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"login":"not_installed"`) || srv.Session("s1") != nil {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	mu.Lock()
	got := strings.Join(lines, "\n")
	mu.Unlock()
	if !strings.Contains(got, "[session] s1: not started: Claude Code is not installed or cannot run") {
		t.Fatalf("log:\n%s", got)
	}
}
