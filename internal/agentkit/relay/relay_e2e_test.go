package relay_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"github.com/meetstream-ai/cli/internal/agentkit/bridge"
	"github.com/meetstream-ai/cli/internal/agentkit/config"
	"github.com/meetstream-ai/cli/internal/agentkit/relay"
)

type frame = map[string]any

func readFrame(t *testing.T, ctx context.Context, c *websocket.Conn) frame {
	t.Helper()
	_, b, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("relay read: %v", err)
	}
	var f frame
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func writeFrame(t *testing.T, ctx context.Context, c *websocket.Conn, f frame) {
	t.Helper()
	f["v"] = 1
	b, _ := json.Marshal(f)
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("relay write: %v", err)
	}
}

// A fake relay drives the real bridge (with the kit's stub agent) through
// the client, as MIA would through the real relay (PROTOCOL.md §2).
func TestRelayEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is needed for the stub agent")
	}
	stub, _ := filepath.Abs("../bridge/testdata/stub_agent.py")
	dir := t.TempDir()
	br := bridge.New(config.Config{Agent: agents.All["claude"], Binary: stub, Workdir: dir, RuntimeDir: dir, Owner: "Nav", Timeout: 20 * time.Second})
	t.Cleanup(br.Close)
	br.SetInternalToken("internal-test-token")

	done := make(chan struct{})
	var sawAuth, sawProto atomic.Bool
	relaySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth.Store(r.Header.Get("Authorization") == "Bearer at_1")
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"meetstream.relay.v1"}})
		if err != nil {
			t.Error(err)
			return
		}
		sawProto.Store(c.Subprotocol() == "meetstream.relay.v1")
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		defer close(done)

		hello := readFrame(t, ctx, c)
		if hello["t"] != "hello" || hello["caps"] == nil {
			t.Errorf("hello %v", hello)
		}
		writeFrame(t, ctx, c, frame{"t": "welcome", "conn_id": "c1", "heartbeat_s": 30})

		// Plain call.
		writeFrame(t, ctx, c, frame{"t": "req", "s": 1, "method": "GET", "path": "/v1/health"})
		res := readFrame(t, ctx, c)
		if res["t"] != "res" || res["s"] != 1.0 || res["status"] != 200.0 || res["body"].(map[string]any)["ok"] != true {
			t.Errorf("health res %v", res)
		}
		// A path the relay may not ask for.
		writeFrame(t, ctx, c, frame{"t": "req", "s": 2, "method": "POST", "path": "/v1/secret", "body": map[string]any{}})
		if res := readFrame(t, ctx, c); res["status"] != 404.0 {
			t.Errorf("disallowed path %v", res)
		}
		// Session, then a streamed turn.
		writeFrame(t, ctx, c, frame{"t": "req", "s": 3, "method": "POST", "path": "/v1/session/start", "body": map[string]any{"session_id": "bot_r"}})
		if res := readFrame(t, ctx, c); res["status"] != 200.0 {
			t.Errorf("session/start %v", res)
		}
		writeFrame(t, ctx, c, frame{"t": "req", "s": 4, "method": "POST", "path": "/v1/turn",
			"body": map[string]any{"session_id": "bot_r", "messages": []map[string]string{{"speaker": "Nav", "text": "answer?"}}}})
		head := readFrame(t, ctx, c)
		if head["t"] != "res_head" || head["status"] != 200.0 {
			t.Errorf("res_head %v", head)
		}
		var text strings.Builder
		sawDone := false
		for {
			f := readFrame(t, ctx, c)
			if f["t"] == "end" {
				break
			}
			ev, _ := f["event"].(map[string]any)
			if f["t"] != "chunk" || ev == nil {
				t.Fatalf("stream frame %v", f)
			}
			if ev["type"] == "delta" {
				text.WriteString(ev["text"].(string))
			}
			if ev["type"] == "done" {
				sawDone = true
				text.WriteString(ev["text"].(string))
			}
		}
		if !sawDone || !strings.Contains(text.String(), "42") {
			t.Errorf("turn text %q done=%v", text.String(), sawDone)
		}
		// Revoked: the client must stop, not reconnect.
		c.Close(4403, "revoked")
	}))
	defer relaySrv.Close()

	store := &relay.Store{Home: t.TempDir(), Backend: "file"}
	creds := &relay.Credentials{DeviceID: "dev_1", RefreshToken: "rt_0", AuthURL: "http://auth.invalid", RelayURL: "ws" + strings.TrimPrefix(relaySrv.URL, "http")}
	var refreshes atomic.Int32
	cl := &relay.Client{
		Store: store, Creds: creds, Bridge: br, InternalToken: "internal-test-token",
		Hello: relay.HelloFields("claude", "Claude Code", "ok", "readonly"),
		Refresh: func(_ context.Context, _, rt, dev string) (*relay.Tokens, error) {
			if rt != "rt_0" {
				t.Errorf("refreshed with %q", rt)
			}
			refreshes.Add(1)
			return &relay.Tokens{AccessToken: "at_1", RefreshToken: "rt_1", ExpiresAt: time.Now().Add(15 * time.Minute), DeviceID: dev}, nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	err := cl.Run(ctx)
	var stop *relay.StopError
	if !errors.As(err, &stop) {
		t.Fatalf("Run returned %v, want a StopError after 4403", err)
	}
	<-done
	if !sawAuth.Load() || !sawProto.Load() {
		t.Fatalf("handshake: bearer=%v subprotocol=%v", sawAuth.Load(), sawProto.Load())
	}
	saved, err := store.Load()
	if err != nil || saved == nil || saved.RefreshToken != "rt_1" {
		t.Fatalf("rotated refresh token not saved: %+v %v", saved, err)
	}
	if refreshes.Load() != 1 {
		t.Fatalf("refreshes = %d", refreshes.Load())
	}
}

func TestCheckURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"wss://relay.meetstream.ai/v1/connect": true,
		"ws://127.0.0.1:8080/v1/connect":       true,
		"ws://localhost:8080/v1/connect":       true,
		"ws://relay.meetstream.ai/v1/connect":  false,
		"https://relay.meetstream.ai":          false,
	} {
		if err := relay.CheckURL(raw); (err == nil) != ok {
			t.Errorf("%s: err=%v", raw, err)
		}
	}
}
