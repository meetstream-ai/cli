package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	subprotocol   = "meetstream.relay.v1"
	maxMessage    = 4 << 20
	backoffBase   = time.Second
	backoffCap    = 60 * time.Second
	heartbeat     = 30 * time.Second
	renewFraction = 0.8
)

// Paths the relay may ask for (PROTOCOL.md §2). Anything else gets 404.
var allowed = map[string]string{
	"/v1/health": http.MethodGet, "/v1/session/start": http.MethodPost, "/v1/turn": http.MethodPost,
	"/v1/tool_result": http.MethodPost, "/v1/cancel": http.MethodPost, "/v1/claim": http.MethodPost,
	"/v1/release": http.MethodPost, "/v1/session/end": http.MethodPost,
}

// StopError means the user has to act (sign in again); never retried.
type StopError struct{ Msg string }

func (e *StopError) Error() string { return e.Msg }

// Client keeps one outbound connection to the relay and answers MIA's calls
// with the local bridge, in process.
type Client struct {
	Store         *Store
	Creds         *Credentials
	Bridge        http.Handler // the bridge's ServeHTTP
	InternalToken string       // the bridge accepts this instead of a pairing token
	Hello         map[string]any
	Logf          func(format string, args ...any) // metadata only, never tokens or turn text
	OnState       func(connected bool, detail string)

	// for tests
	Refresh func(ctx context.Context, authURL, refreshToken, deviceID string) (*Tokens, error)

	mu      sync.Mutex
	tokens  *Tokens
	conn    *websocket.Conn
	writeMu sync.Mutex
	streams map[int]context.CancelFunc
}

func (c *Client) logf(f string, a ...any) {
	if c.Logf != nil {
		c.Logf(f, a...)
	}
}

func (c *Client) state(on bool, d string) {
	if c.OnState != nil {
		c.OnState(on, d)
	}
}

// AccessToken returns a valid access token, refreshing (and saving the
// rotated refresh token first) when it has under two minutes left.
func (c *Client) AccessToken(ctx context.Context, force bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && c.tokens != nil && time.Until(c.tokens.ExpiresAt) > 2*time.Minute {
		return c.tokens.AccessToken, nil
	}
	refresh := c.Refresh
	if refresh == nil {
		refresh = Refresh
	}
	t, err := refresh(ctx, c.Creds.AuthURL, c.Creds.RefreshToken, c.Creds.DeviceID)
	if err != nil {
		if errors.Is(err, ErrSignInRequired) {
			return "", &StopError{Msg: "MeetStream sign-in is no longer valid. Run: meetstream agent login"}
		}
		return "", err
	}
	c.Creds.RefreshToken = t.RefreshToken
	if t.DeviceID != "" {
		c.Creds.DeviceID = t.DeviceID
	}
	if c.Store != nil {
		if err := c.Store.Save(c.Creds); err != nil {
			return "", fmt.Errorf("could not save the rotated sign-in: %w", err)
		}
	}
	c.tokens = t
	return t.AccessToken, nil
}

// CheckURL allows wss://, and ws:// only to loopback hosts (local testing).
func CheckURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("bad relay URL %q", raw)
	}
	switch u.Scheme {
	case "wss":
		return nil
	case "ws":
		h := u.Hostname()
		if h == "localhost" || net.ParseIP(h).IsLoopback() {
			return nil
		}
	}
	return fmt.Errorf("the relay URL must be wss:// (ws:// only to localhost): %q", raw)
}

// Run connects, serves and reconnects with full-jitter backoff until ctx ends
// or the user must act (StopError).
func (c *Client) Run(ctx context.Context) error {
	if err := CheckURL(c.Creds.RelayURL); err != nil {
		return &StopError{Msg: err.Error()}
	}
	attempt := 0
	for ctx.Err() == nil {
		served, err := c.runOnce(ctx)
		var stop *StopError
		if errors.As(err, &stop) {
			c.state(false, stop.Msg)
			return err
		}
		if served {
			attempt = 0
		}
		if ctx.Err() != nil {
			return nil
		}
		c.state(false, "reconnecting")
		if err != nil {
			c.logf("relay: %v", err)
		}
		attempt++
		d := min(backoffCap, backoffBase<<min(attempt, 6))
		select {
		case <-time.After(time.Duration(rand.Int64N(int64(d)) + 1)):
		case <-ctx.Done():
		}
	}
	return nil
}

type frame map[string]any

func (c *Client) runOnce(ctx context.Context) (served bool, err error) {
	tok, err := c.AccessToken(ctx, false)
	if err != nil {
		return false, err
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+tok)
	h.Set("X-Agent-Kit-Version", KitVersion)
	conn, res, err := websocket.Dial(ctx, c.Creds.RelayURL, &websocket.DialOptions{HTTPHeader: h, Subprotocols: []string{subprotocol}})
	if err != nil {
		if res != nil {
			switch res.StatusCode {
			case 401:
				c.mu.Lock()
				c.tokens = nil
				c.mu.Unlock()
				return false, fmt.Errorf("relay refused the token (401); refreshing")
			case 403:
				return false, &StopError{Msg: "The relay refused this computer. Run: meetstream agent login"}
			}
			return false, fmt.Errorf("relay handshake failed (%d)", res.StatusCode)
		}
		return false, err
	}
	conn.SetReadLimit(maxMessage)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.mu.Lock()
	c.conn, c.streams = conn, map[int]context.CancelFunc{}
	c.mu.Unlock()
	defer func() {
		c.abortAll()
		conn.CloseNow()
	}()

	hello := frame{"v": 1, "t": "hello"}
	for k, v := range c.Hello {
		hello[k] = v
	}
	if err := c.send(ctx, hello); err != nil {
		return false, err
	}
	var welcome frame
	if err := c.read(ctx, conn, &welcome); err != nil {
		return false, closeErr(err)
	}
	if welcome["t"] != "welcome" {
		return false, fmt.Errorf("relay sent %v before welcome", welcome["t"])
	}
	c.state(true, fmt.Sprint(welcome["conn_id"]))
	served = true

	go c.renewLoop(ctx)
	go func() {
		t := time.NewTicker(heartbeat)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pctx, pc := context.WithTimeout(ctx, 45*time.Second)
				err := conn.Ping(pctx)
				pc()
				if err != nil && ctx.Err() == nil {
					conn.Close(websocket.StatusGoingAway, "silent")
					return
				}
			}
		}
	}()

	for {
		var f frame
		if err := c.read(ctx, conn, &f); err != nil {
			return served, closeErr(err)
		}
		switch f["t"] {
		case "req":
			go c.serve(ctx, f)
		case "abort":
			c.abort(intOf(f["s"]))
		case "drain":
			conn.Close(websocket.StatusGoingAway, "drain")
			return served, nil
		case "notice":
			if f["kind"] == "revoked" {
				return served, &StopError{Msg: "This computer was disconnected from MeetStream. Run: meetstream agent login"}
			}
			c.logf("relay notice: %v", f["kind"])
		case "auth_err":
			c.mu.Lock()
			c.tokens = nil
			c.mu.Unlock()
		}
	}
}

func closeErr(err error) error {
	switch websocket.CloseStatus(err) {
	case 4403:
		return &StopError{Msg: "This computer was disconnected from MeetStream (dashboard or a security check). Run: meetstream agent login"}
	case 4401:
		return fmt.Errorf("relay: token expired")
	case -1:
		return err
	}
	return fmt.Errorf("relay closed the connection (%d)", websocket.CloseStatus(err))
}

// renewLoop sends a fresh access token in band at 80% of its life.
func (c *Client) renewLoop(ctx context.Context) {
	for {
		c.mu.Lock()
		t := c.tokens
		c.mu.Unlock()
		wait := time.Minute
		if t != nil {
			life := time.Until(t.ExpiresAt)
			wait = max(5*time.Second, time.Duration(float64(life)*renewFraction)) // at 80% of what is left
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		tok, err := c.AccessToken(ctx, true)
		if err != nil {
			c.logf("relay: token renewal failed: %v", err)
			continue
		}
		_ = c.send(ctx, frame{"v": 1, "t": "auth", "token": tok})
	}
}

func (c *Client) read(ctx context.Context, conn *websocket.Conn, out *frame) error {
	_, b, err := conn.Read(ctx)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func (c *Client) send(ctx context.Context, f frame) error {
	f["v"] = 1
	b, _ := json.Marshal(f)
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return errors.New("relay not connected")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return conn.Write(ctx, websocket.MessageText, b)
}

func (c *Client) abort(s int) {
	c.mu.Lock()
	cancel := c.streams[s]
	delete(c.streams, s)
	c.mu.Unlock()
	if cancel != nil {
		cancel() // the bridge sees the request end and stops the agent's turn
	}
}

func (c *Client) abortAll() {
	c.mu.Lock()
	ids := make([]int, 0, len(c.streams))
	for s := range c.streams {
		ids = append(ids, s)
	}
	c.mu.Unlock()
	for _, s := range ids {
		c.abort(s)
	}
}

func intOf(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

// serve answers one req frame with the bridge.
func (c *Client) serve(ctx context.Context, f frame) {
	s := intOf(f["s"])
	method, _ := f["method"].(string)
	path, _ := f["path"].(string)
	if want, ok := allowed[path]; !ok || want != method {
		_ = c.send(ctx, frame{"t": "res", "s": s, "status": 404, "body": map[string]any{"error": "not_found"}})
		return
	}
	var body []byte
	if method == http.MethodPost {
		body, _ = json.Marshal(f["body"])
	}
	rctx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.streams[s] = cancel
	c.mu.Unlock()
	defer c.abort(s)

	req, _ := http.NewRequestWithContext(rctx, method, "http://127.0.0.1"+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+c.InternalToken)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	req.RemoteAddr = "127.0.0.1:0"
	w := &frameWriter{c: c, ctx: ctx, s: s, header: http.Header{}}
	c.Bridge.ServeHTTP(w, req)
	w.finish()
}

// frameWriter turns a bridge response into relay frames: res for JSON, and
// res_head + one chunk per NDJSON line + end for a streamed turn.
type frameWriter struct {
	c       *Client
	ctx     context.Context
	s       int
	header  http.Header
	status  int
	stream  bool
	started bool
	buf     bytes.Buffer
}

func (w *frameWriter) Header() http.Header { return w.header }

func (w *frameWriter) WriteHeader(status int) {
	if w.started {
		return
	}
	w.started, w.status = true, status
	if strings.HasPrefix(w.header.Get("Content-Type"), "application/x-ndjson") {
		w.stream = true
		_ = w.c.send(w.ctx, frame{"t": "res_head", "s": w.s, "status": status})
	}
}

func (w *frameWriter) Write(p []byte) (int, error) {
	if !w.started {
		w.WriteHeader(http.StatusOK)
	}
	w.buf.Write(p)
	if w.stream {
		w.flushLines(false)
	}
	return len(p), nil
}

func (w *frameWriter) Flush() {}

func (w *frameWriter) flushLines(all bool) {
	for {
		line, err := w.buf.ReadBytes('\n')
		if err != nil { // partial line: keep it for later
			if all && len(bytes.TrimSpace(line)) > 0 {
				w.chunk(line)
			} else if !all {
				w.buf.Write(line)
			}
			return
		}
		w.chunk(line)
	}
}

func (w *frameWriter) chunk(line []byte) {
	var ev map[string]any
	if json.Unmarshal(bytes.TrimSpace(line), &ev) == nil {
		_ = w.c.send(w.ctx, frame{"t": "chunk", "s": w.s, "event": ev})
	}
}

func (w *frameWriter) finish() {
	if !w.started {
		w.WriteHeader(http.StatusOK)
	}
	if w.stream {
		w.flushLines(true)
		_ = w.c.send(w.ctx, frame{"t": "end", "s": w.s})
		return
	}
	var body any
	if json.Unmarshal(bytes.TrimSpace(w.buf.Bytes()), &body) != nil {
		body = map[string]any{}
	}
	_ = w.c.send(w.ctx, frame{"t": "res", "s": w.s, "status": w.status, "body": body})
}

// HelloFields is the hello frame body for this computer.
func HelloFields(agentID, agentLabel, login, maxAccess string) map[string]any {
	return map[string]any{
		"kit_version": KitVersion, "device_name": DeviceName(), "platform": Platform(),
		"agents":     []map[string]string{{"id": agentID, "label": agentLabel, "login": login}},
		"max_access": maxAccess, "caps": []string{"bridge.v1"},
	}
}
