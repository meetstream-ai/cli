// Package bridge exposes the Python kit's HTTP contract on loopback. Relay
// clients can supply a per-process credential through SetInternalToken.
package bridge

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"github.com/meetstream-ai/cli/internal/agentkit/access"
	"github.com/meetstream-ai/cli/internal/agentkit/activation"
	"github.com/meetstream-ai/cli/internal/agentkit/config"
	"github.com/meetstream-ai/cli/internal/agentkit/localapps"
	"github.com/meetstream-ai/cli/internal/agentkit/login"
	"github.com/meetstream-ai/cli/internal/agentkit/pairing"
	"github.com/meetstream-ai/cli/internal/agentkit/parsers"
	"github.com/meetstream-ai/cli/internal/agentkit/prompts"
	"github.com/meetstream-ai/cli/internal/agentkit/redact"
	"github.com/meetstream-ai/cli/internal/agentkit/session"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Authorizer interface{ Verify(string) bool }
type Login interface {
	Check(bool) login.Status
	Expired()
}
type Server struct {
	Config        config.Config
	Pairings      Authorizer
	Login         Login
	mu            sync.RWMutex
	sessions      map[string]*session.Session
	internalToken string
	Port          int
	startMu       sync.Mutex
}

func New(c config.Config) *Server {
	c = c.Defaults()
	return &Server{Config: c, Pairings: &pairing.Store{Dir: c.RuntimeDir}, Login: &login.Checker{Agent: c.Agent, Binary: c.Binary, Notify: c.Notify}, sessions: map[string]*session.Session{}}
}
func (s *Server) SetInternalToken(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.internalToken = token
}
func (s *Server) Session(id string) *session.Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessions[id]
}
func (s *Server) Start(ctx context.Context, id string, settings session.Settings) (*session.Session, bool) {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	newSession := session.New(id, s.Config, settings, s.Port)
	s.mu.Lock()
	old := s.sessions[id]
	if old != nil && old.Key() == newSession.Key() {
		s.mu.Unlock()
		return old, true
	}
	if settings.ActivationRequired {
		if old != nil {
			newSession.Activation = old.Activation
		}
		if newSession.Activation == nil {
			newSession.Activation = activation.New(s.Config.Agent.Label, s.Config.Notify)
			newSession.Activation.Log = func(text string) {
				s.Config.Log("[activate] " + id + ": " + text)
				s.Config.Event("activation", map[string]any{"session_id": id, "claim": activationState(text)})
			}
		}
	}
	s.sessions[id] = newSession
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}
	_ = newSession.Start(ctx)
	return newSession, false
}

// activationState is the claim state an activation log line reports (no names in events).
func activationState(text string) string {
	switch {
	case strings.HasPrefix(text, "activated"):
		return "claimed"
	case strings.HasPrefix(text, "too many"):
		return "locked"
	}
	return "released"
}

func (s *Server) End(id string) bool {
	s.mu.Lock()
	meeting := s.sessions[id]
	delete(s.sessions, id)
	s.mu.Unlock()
	if meeting == nil {
		return false
	}
	meeting.Close()
	return true
}
func (s *Server) Close() {
	s.mu.Lock()
	sessions := s.sessions
	s.sessions = map[string]*session.Session{}
	s.mu.Unlock()
	for _, m := range sessions {
		m.Close()
	}
}

// Listen binds only IPv4 loopback; no request or header logging is installed.
func (s *Server) Listen(port int) (net.Listener, error) {
	ln, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err == nil {
		s.Port = ln.Addr().(*net.TCPAddr).Port
	}
	return ln, err
}
func (s *Server) HTTPServer() *http.Server {
	return &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 90 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 * 1024}
}
func jsonReply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Server", "meetstream-agent-kit")
	if v == nil {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(status)
		return
	}
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", fmt.Sprint(len(b)))
	w.WriteHeader(status)
	_, _ = w.Write(b)
}
func (s *Server) authorized(r *http.Request) bool {
	s.mu.RLock()
	internal := s.internalToken
	s.mu.RUnlock()
	auth := r.Header.Get("Authorization")
	if internal != "" && subtle.ConstantTimeCompare([]byte(auth), []byte("Bearer "+internal)) == 1 {
		return true
	}
	return s.Pairings != nil && s.Pairings.Verify(auth)
}
func readBody(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	if r.ContentLength <= 0 || r.ContentLength > config.MaxBodyBytes {
		jsonReply(w, 413, map[string]any{"error": fmt.Sprintf("body must be 1..%d bytes", config.MaxBodyBytes)})
		return nil, false
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, r.ContentLength+1))
	if err != nil || int64(len(b)) != r.ContentLength {
		jsonReply(w, 400, map[string]any{"error": "invalid JSON"})
		return nil, false
	}
	var raw any
	if json.Unmarshal(b, &raw) != nil {
		jsonReply(w, 400, map[string]any{"error": "invalid JSON"})
		return nil, false
	}
	m, ok := raw.(map[string]any)
	if !ok {
		jsonReply(w, 400, map[string]any{"error": "body must be an object"})
		return nil, false
	}
	return m, true
}

var sessionID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)
var toolName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var imageResult = regexp.MustCompile(`^MIA_IMAGE (image/(?:jpeg|png|webp)) ([A-Za-z0-9+/]+={0,2})$`)

func ValidTools(raw any) []map[string]any {
	out := []map[string]any{}
	tools, _ := raw.([]any)
	for _, raw := range tools {
		t := parsers.Obj(raw)
		name := parsers.Str(t["name"])
		if !toolName.MatchString(name) {
			continue
		}
		var schema any
		if p, ok := t["parameters"].(map[string]any); ok {
			schema = p
		}
		out = append(out, map[string]any{"name": name, "description": prompts.Truncate(parsers.Str(t["description"]), 1000), "parameters": schema})
		if len(out) == 64 {
			break
		}
	}
	return out
}
func MCPContent(output string) []map[string]any {
	if m := imageResult.FindStringSubmatch(output); m != nil {
		return []map[string]any{{"type": "image", "data": m[2], "mimeType": m[1]}, {"type": "text", "text": "Screenshot of the meeting screen."}}
	}
	return []map[string]any{{"type": "text", "text": output}}
}
func (s *Server) loginPayload(force bool) map[string]any {
	status := s.Login.Check(force)
	v := map[string]any{"login": status.State, "relogin": s.Config.Agent.Relogin}
	if status.State == "not_installed" {
		v["install"] = s.Config.Agent.InstallHint
	}
	if status.State != "ok" && status.Detail != "" {
		v["login_detail"] = prompts.Truncate(status.Detail, 200)
	}
	return v
}

// clip keeps a log line's quote to n characters.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func merge(a, b map[string]any) map[string]any {
	for k, v := range b {
		a[k] = v
	}
	return a
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.RequestURI()
	if strings.HasPrefix(path, "/mcp/") {
		if r.Method == http.MethodGet {
			w.Header().Set("Allow", "POST")
			jsonReply(w, 405, map[string]any{"error": "use POST"})
			return
		}
		if r.Method == http.MethodPost {
			s.mcp(w, r)
			return
		}
	}
	known := false
	if r.Method == http.MethodGet && path == "/v1/health" {
		known = true
	}
	if r.Method == http.MethodPost {
		switch path {
		case "/v1/session/start", "/v1/turn", "/v1/tool_result", "/v1/cancel", "/v1/session/end", "/v1/claim", "/v1/release":
			known = true
		}
	}
	if !known {
		jsonReply(w, 404, map[string]any{"error": "not found"})
		return
	}
	if !s.authorized(r) {
		jsonReply(w, 401, map[string]any{"error": "unauthorized"})
		return
	}
	if r.Method == http.MethodGet {
		s.mu.RLock()
		count := len(s.sessions)
		s.mu.RUnlock()
		c := s.Config
		jsonReply(w, 200, merge(map[string]any{"ok": true, "agent": c.Agent.Name, "label": c.Agent.Label, "mode": c.MaxAccess, "max_access": c.MaxAccess, "warm": c.Agent.Warm != "", "sessions": count, "version": config.Version}, s.loginPayload(false)))
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	id := parsers.Str(body["session_id"])
	if path == "/v1/session/start" {
		s.start(w, r, body)
		return
	}
	if path == "/v1/session/end" {
		jsonReply(w, 200, map[string]any{"ok": s.End(id)})
		return
	}
	meeting := s.Session(id)
	if meeting == nil {
		jsonReply(w, 404, map[string]any{"error": "unknown session; call /v1/session/start first"})
		return
	}
	switch path {
	case "/v1/turn":
		s.turn(w, r, meeting, body)
	case "/v1/cancel":
		meeting.Cancel()
		jsonReply(w, 200, map[string]any{"ok": true})
	case "/v1/tool_result":
		output := parsers.Str(body["output"])
		if !imageResult.MatchString(output) {
			output = prompts.Truncate(output, config.MaxAnswerChars)
		}
		ok := meeting.Resolve(parsers.Str(body["call_id"]), output, body["is_error"] == true)
		status := 404
		if ok {
			status = 200
		}
		jsonReply(w, status, map[string]any{"ok": ok})
	case "/v1/claim":
		if meeting.Activation == nil {
			jsonReply(w, 409, map[string]any{"ok": false, "reason": "no_activation_code"})
			return
		}
		ok, reason := meeting.Activation.Claim(prompts.Truncate(parsers.Str(body["code"]), 12), prompts.Truncate(parsers.Str(body["participant_id"]), 200), parsers.Str(body["name"]))
		jsonReply(w, 200, map[string]any{"ok": ok, "reason": reason, "claim": meeting.ClaimState()})
	case "/v1/release":
		ids := []string{}
		raw, _ := body["participant_ids"].([]any)
		for _, id := range raw {
			if v := parsers.Str(id); v != "" {
				ids = append(ids, prompts.Truncate(v, 200))
			}
		}
		released := meeting.Activation != nil && meeting.Activation.Release(ids)
		jsonReply(w, 200, map[string]any{"released": released, "claim": meeting.ClaimState()})
	}
}
func (s *Server) start(w http.ResponseWriter, r *http.Request, body map[string]any) {
	id := parsers.Str(body["session_id"])
	if !sessionID.MatchString(id) {
		jsonReply(w, 400, map[string]any{"error": "session_id is required"})
		return
	}
	login := s.loginPayload(true)
	if login["login"] == "expired" || login["login"] == "not_installed" {
		jsonReply(w, 200, merge(map[string]any{"agent": s.Config.Agent.Name, "warm": false}, login))
		return
	}
	claim := body["claim_required"] == true
	decision := access.Effective(s.Config.Agent, s.Config.MaxAccess, parsers.Str(body["access"]), body["owner_verified"] == true || claim)
	meeting, reused := s.Start(r.Context(), id, session.Settings{Instructions: parsers.Str(body["instructions"]), Tools: ValidTools(body["tools"]), Model: prompts.Truncate(parsers.Str(body["model"]), 80), Access: decision.Level, ActivationRequired: claim})
	if meeting.Warm() && s.Config.Prime && !reused {
		go meeting.Run(context.Background(), "<meeting_turns>\n[system]: The meeting is starting. Reply with the single word: ready\n</meeting_turns>", true, func(session.Event) error { return nil })
	}
	how, mode := "started", "one run per turn"
	if reused {
		how = "reused"
	}
	if meeting.Warm() {
		mode = "warm"
	}
	reason := ""
	if decision.Reason != "" {
		reason = ": " + decision.Reason
	}
	s.Config.Log(fmt.Sprintf("[session] %s: %s (%s, %s, %d meeting tools, access %s%s)", id, how, s.Config.Agent.Label, mode, len(meeting.Settings.Tools), decision.Level, reason))
	s.Config.Event("session_started", map[string]any{"agent": s.Config.Agent.Name, "warm": meeting.Warm(), "reused": reused, "access": decision.Level,
		"session_id": id, "tools": len(meeting.Settings.Tools), "access_reason": decision.Reason})
	jsonReply(w, 200, merge(map[string]any{"agent": s.Config.Agent.Name, "warm": meeting.Warm(), "reused": reused, "spawn_ms": meeting.Driver.SpawnMS(), "access": decision.Level, "max_access": s.Config.MaxAccess, "access_reason": decision.Reason, "claim": meeting.ClaimState()}, login))
}
func (s *Server) turn(w http.ResponseWriter, r *http.Request, meeting *session.Session, body map[string]any) {
	if state := meeting.ClaimState(); state != "off" && state != "claimed" {
		jsonReply(w, 403, map[string]any{"error": "locked", "claim": state})
		return
	}
	raw, _ := body["messages"].([]any)
	messages := []map[string]any{}
	for _, m := range raw {
		if v, ok := m.(map[string]any); ok {
			messages = append(messages, v)
		}
	}
	if len(messages) == 0 {
		jsonReply(w, 400, map[string]any{"error": "messages are required"})
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Server", "meetstream-agent-kit")
	w.WriteHeader(200)
	controller := http.NewResponseController(w)
	_ = controller.Flush()
	last := messages[len(messages)-1]
	speaker := parsers.Str(last["speaker"])
	if speaker == "" {
		speaker = "?"
	}
	// The kit's line: who spoke and what, on this terminal only, secrets hidden.
	if s.Config.HideTurnText {
		s.Config.Log("[turn] " + redact.Secrets(clip(speaker, 40)))
	} else {
		s.Config.Log("[turn] " + redact.Secrets(clip(speaker, 40)+": "+clip(parsers.Str(last["text"]), 160)))
	}
	meeting.Run(r.Context(), prompts.Turn(messages), false, func(e session.Event) error {
		if e["code"] == "auth_required" {
			s.Login.Expired()
			e["relogin"] = s.Config.Agent.Relogin
		}
		switch e["type"] {
		case "tool_call":
			s.Config.Log("[tool] " + parsers.Str(e["name"]))
			s.Config.Event("tool_call", map[string]any{"session_id": meeting.ID, "name": e["name"]})
		case "done":
			if e["timed_out"] == true {
				s.Config.Log(fmt.Sprintf("[timeout] stopped after %v ms; MIA says: %s", e["total_ms"], config.TimeoutReply))
			} else {
				s.Config.Log(fmt.Sprintf("[done] first output %v ms, total %v ms", e["first_output_ms"], e["total_ms"]))
			}
		case "error": // never silent: every failed turn shows here
			s.Config.Log("[error] " + parsers.Str(e["code"]) + ": " + redact.Secrets(clip(parsers.Str(e["message"]), 200)))
		}
		if e["type"] == "done" || e["type"] == "error" {
			s.Config.Event("turn_"+parsers.Str(e["type"]), map[string]any{"code": e["code"], "total_ms": e["total_ms"], "timed_out": e["timed_out"],
				"session_id": meeting.ID, "first_output_ms": e["first_output_ms"]})
		}
		if err := json.NewEncoder(w).Encode(e); err != nil {
			return err
		}
		return controller.Flush()
	})
}
func (s *Server) mcp(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.RequestURI(), "/mcp/"), "/")
	meeting := s.Session(id)
	if meeting == nil || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+meeting.Secret)) != 1 {
		jsonReply(w, 401, map[string]any{"error": "unauthorized"})
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	idVal := body["id"]
	if idVal == nil {
		jsonReply(w, 202, nil)
		return
	}
	params := parsers.Obj(body["params"])
	result := map[string]any{}
	switch parsers.Str(body["method"]) {
	case "initialize":
		version := parsers.Str(params["protocolVersion"])
		if version == "" {
			version = "2025-06-18"
		}
		result = map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "meetstream-meeting", "version": config.Version}}
	case "ping":
	case "tools/list":
		tools := meeting.Tools()
		meeting.MarkListed(len(tools))
		result["tools"] = tools
	case "tools/call":
		name := parsers.Str(params["name"])
		args := parsers.Obj(params["arguments"])
		reply := session.Result{Output: "unknown tool " + name, IsError: true}
		if name == localapps.ShareTool || name == localapps.StopTool {
			reply = meeting.LocalTool(r.Context(), name, args)
		} else if name == "approve_action" && meeting.Settings.Access == "confirm" {
			input := parsers.Obj(args["input"])
			b, _ := json.Marshal(input)
			allowed := meeting.Allow(parsers.Str(args["tool_name"]) + " " + prompts.Truncate(string(b), 240))
			decision := map[string]any{"behavior": "deny", "message": "The owner did not approve this action."}
			if allowed {
				decision = map[string]any{"behavior": "allow", "updatedInput": input}
			}
			b, _ = json.Marshal(decision)
			result["content"] = []map[string]any{{"type": "text", "text": string(b)}}
			break
		} else {
			for _, tool := range meeting.Settings.Tools {
				if tool["name"] == name {
					reply = meeting.CallTool(r.Context(), name, args)
					break
				}
			}
		}
		result = map[string]any{"content": MCPContent(reply.Output), "isError": reply.IsError}
	default:
		jsonReply(w, 200, map[string]any{"jsonrpc": "2.0", "id": idVal, "error": map[string]any{"code": -32601, "message": "method not found"}})
		return
	}
	jsonReply(w, 200, map[string]any{"jsonrpc": "2.0", "id": idVal, "result": result})
}
