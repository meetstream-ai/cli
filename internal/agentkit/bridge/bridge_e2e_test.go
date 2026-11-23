package bridge

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"github.com/meetstream-ai/cli/internal/agentkit/config"
	"github.com/meetstream-ai/cli/internal/agentkit/pairing"
	"github.com/meetstream-ai/cli/internal/testutil/fakecli"
)

// End to end through the HTTP protocol (docs/protocol.md) with the kit's own
// stub agent, which speaks Claude Code's stream-json and never calls a model.
func newStubBridge(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	stub := stubAgent(t, "claude")
	dir := t.TempDir()
	spec := agents.All["claude"]
	srv := New(config.Config{Agent: spec, Binary: stub, Workdir: dir, RuntimeDir: dir, Owner: "Nav", Timeout: 20 * time.Second})
	store := &pairing.Store{Dir: dir}
	_, token, err := store.Create("test")
	if err != nil {
		t.Fatal(err)
	}
	srv.Pairings = store
	hs := httptest.NewServer(srv)
	t.Cleanup(func() { hs.Close(); srv.Close() })
	return hs, token
}

// stubAgent is the kit's stub agent as a program called name: Python runs the
// script, so no shebang or execute bit is needed (Windows has neither).
func stubAgent(t *testing.T, name string) string {
	t.Helper()
	return fakecli.Python(t, t.TempDir(), name, filepath.Join("testdata", "stub_agent.py"))
}

// startSession starts a session and fails the test at once when the stub
// agent could not run, instead of at the turn with "unknown session".
func startSession(t *testing.T, url, token string, body map[string]any) {
	t.Helper()
	res := post(t, url+"/v1/session/start", token, body)
	defer res.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(res.Body).Decode(&m)
	if res.StatusCode != 200 || m["login"] != "ok" {
		t.Fatalf("session/start %d %v", res.StatusCode, m)
	}
}

func post(t *testing.T, url, token string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestBridgeTurnEndToEnd(t *testing.T) {
	hs, token := newStubBridge(t)

	// No or wrong token → 401 {"error":"unauthorized"}.
	for _, tok := range []string{"", "msak_wrong_token"} {
		res := post(t, hs.URL+"/v1/session/start", tok, map[string]any{"session_id": "bot_1"})
		var m map[string]any
		_ = json.NewDecoder(res.Body).Decode(&m)
		res.Body.Close()
		if res.StatusCode != 401 || m["error"] != "unauthorized" {
			t.Fatalf("token %q → %d %v", tok, res.StatusCode, m)
		}
	}

	res := post(t, hs.URL+"/v1/session/start", token, map[string]any{"session_id": "bot_1", "instructions": "be brief", "access": "readonly"})
	var started map[string]any
	_ = json.NewDecoder(res.Body).Decode(&started)
	res.Body.Close()
	// login "ok": the stub agent really ran (a CLI that cannot start is
	// "not_installed", and then no session exists for the turn).
	if res.StatusCode != 200 || started["agent"] != "claude" || started["login"] != "ok" {
		t.Fatalf("session/start %d %v", res.StatusCode, started)
	}

	res = post(t, hs.URL+"/v1/turn", token, map[string]any{"session_id": "bot_1", "messages": []map[string]string{{"speaker": "Nav", "text": "what is the answer?"}}})
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-ndjson") {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("content-type %q: %d %s", ct, res.StatusCode, b)
	}
	var text strings.Builder
	var done map[string]any
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		var line map[string]any
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			t.Fatalf("not ndjson: %q", sc.Text())
		}
		switch line["type"] {
		case "delta":
			text.WriteString(line["text"].(string))
		case "done":
			done = line
		case "error":
			t.Fatalf("turn error %v", line)
		}
	}
	if done == nil || !strings.Contains(text.String()+done["text"].(string), "42") {
		t.Fatalf("answer %q done %v", text.String(), done)
	}

	res = post(t, hs.URL+"/v1/session/end", token, map[string]any{"session_id": "bot_1"})
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("session/end %d", res.StatusCode)
	}
}

func TestBridgeHealth(t *testing.T) {
	hs, token := newStubBridge(t)
	req, _ := http.NewRequest(http.MethodGet, hs.URL+"/v1/health", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(res.Body).Decode(&m)
	if res.StatusCode != 200 || m["ok"] != true || m["agent"] != "claude" || m["max_access"] != "readonly" {
		t.Fatalf("health %d %v", res.StatusCode, m)
	}
}

func readTurn(t *testing.T, res *http.Response) (text string, last map[string]any) {
	t.Helper()
	defer res.Body.Close()
	var b strings.Builder
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		var line map[string]any
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			t.Fatalf("not ndjson: %q", sc.Text())
		}
		if line["type"] == "delta" {
			b.WriteString(line["text"].(string))
		}
		last = line
	}
	return b.String(), last
}

func TestBridgeCancelThenNextTurn(t *testing.T) {
	t.Setenv("STUB_FIRST_S", "5") // the first answer token takes 5 s
	hs, token := newStubBridge(t)
	startSession(t, hs.URL, token, map[string]any{"session_id": "bot_2"})

	got := make(chan map[string]any, 1)
	go func() {
		res := post(t, hs.URL+"/v1/turn", token, map[string]any{"session_id": "bot_2", "messages": []map[string]string{{"speaker": "Nav", "text": "slow one"}}})
		_, last := readTurn(t, res)
		got <- last
	}()
	time.Sleep(700 * time.Millisecond)
	start := time.Now()
	res := post(t, hs.URL+"/v1/cancel", token, map[string]any{"session_id": "bot_2"})
	res.Body.Close()
	select {
	case last := <-got:
		if last["type"] != "error" || last["code"] != "cancelled" {
			t.Fatalf("cancelled turn ended with %v", last)
		}
		if time.Since(start) > 3*time.Second {
			t.Fatalf("cancel took %s", time.Since(start))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled turn never ended")
	}
}
