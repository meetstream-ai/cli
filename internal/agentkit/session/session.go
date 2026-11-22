// Package session owns one meeting and serializes its turns across transports.
package session

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/meetstream-ai/cli/internal/agentkit/activation"
	"github.com/meetstream-ai/cli/internal/agentkit/alerts"
	"github.com/meetstream-ai/cli/internal/agentkit/config"
	"github.com/meetstream-ai/cli/internal/agentkit/drivers"
	"github.com/meetstream-ai/cli/internal/agentkit/localapps"
	"github.com/meetstream-ai/cli/internal/agentkit/pairing"
	"github.com/meetstream-ai/cli/internal/agentkit/parsers"
	"github.com/meetstream-ai/cli/internal/agentkit/prompts"
	"strings"
	"sync"
	"time"
)

type Event = map[string]any
type Settings struct {
	Instructions       string
	Tools              []map[string]any
	Model, Access      string
	ActivationRequired bool
}
type Result struct {
	Output  string
	IsError bool
}
type Session struct {
	ID, Secret     string
	Config         config.Config
	Settings       Settings
	Activation     *activation.Gate
	Driver         *drivers.Runtime
	Shares         *localapps.Shares
	turnLock       chan struct{}
	mu             sync.Mutex
	cancel         context.CancelFunc
	warmup, closed bool
	started        int
	pending        map[string]chan Result
	toolEvents     chan Event
	alerted        map[string]bool
	toolsListed    *int
	initDone       chan struct{}
	initErr        error
}

func New(id string, c config.Config, settings Settings, port int) *Session {
	c = c.Defaults()
	if settings.Access == "" {
		settings.Access = c.MaxAccess
	}
	if settings.Model == "" {
		settings.Model = c.Model
	}
	s := &Session{ID: id, Secret: pairing.Secret(24), Config: c, Settings: settings, turnLock: make(chan struct{}, 1), pending: map[string]chan Result{}, toolEvents: make(chan Event, 64), alerted: map[string]bool{}, initDone: make(chan struct{}), Shares: localapps.New()}
	s.Driver = drivers.New(drivers.Options{Config: c, SessionID: id, Model: settings.Model, Access: settings.Access, Rules: s.Rules(), MCPURL: fmt.Sprintf("http://127.0.0.1:%d/mcp/%s", port, id), Secret: s.Secret, ToolNames: s.OfferedNames(), Allow: s.Allow})
	return s
}
func (s *Session) Rules() string {
	return prompts.System(s.Config.Owner, s.Settings.Instructions, s.Settings.Access, s.Config.MeetingNotes)
}
func (s *Session) Key() string { b, _ := json.Marshal(s.Settings); return string(b) }
func (s *Session) Start(ctx context.Context) error {
	if s.Warm() {
		s.initErr = s.Driver.Start(ctx, false)
	}
	close(s.initDone)
	return s.initErr
}
func (s *Session) Warm() bool { return s.Driver.Warm() }
func (s *Session) ClaimState() string {
	if s.Activation == nil {
		return "off"
	}
	return s.Activation.State()
}
func (s *Session) LocalAllowed() bool {
	return s.Settings.Access == "confirm" || s.Settings.Access == "full"
}
func (s *Session) OfferedNames() []string {
	names := []string{}
	for _, t := range s.Settings.Tools {
		names = append(names, parsers.Str(t["name"]))
	}
	if s.LocalAllowed() {
		names = append(names, localapps.ShareTool, localapps.StopTool)
	}
	return names
}
func (s *Session) Allow(description string) bool {
	s.mu.Lock()
	closed := s.closed
	running := s.cancel != nil
	s.mu.Unlock()
	if closed || !running {
		return false
	}
	if s.Settings.Access == "full" {
		return true
	}
	if s.Settings.Access == "confirm" {
		return s.Config.Confirm(description)
	}
	return false
}
func (s *Session) Cancel() {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.failPendingLocked("cancelled by the speaker")
	s.mu.Unlock()
}
func (s *Session) Close() {
	s.mu.Lock()
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	s.failPendingLocked("the meeting ended")
	s.mu.Unlock()
	s.Shares.Close()
	go func() { s.turnLock <- struct{}{}; defer func() { <-s.turnLock }(); s.Driver.Close() }()
}
func (s *Session) Run(ctx context.Context, text string, warmup bool, emit func(Event) error) {
	send := func(e Event) { _ = emit(e) }
	select {
	case s.turnLock <- struct{}{}:
	case <-time.After(200 * time.Millisecond):
		s.mu.Lock()
		priming := s.warmup
		s.mu.Unlock()
		if !priming {
			s.Cancel()
		}
		select {
		case s.turnLock <- struct{}{}:
		case <-ctx.Done():
			return
		case <-time.After(15 * time.Second):
			send(Event{"type": "error", "code": "busy", "message": "a turn is already running"})
			return
		}
	case <-ctx.Done():
		return
	}
	defer func() { <-s.turnLock }()
	select {
	case <-s.initDone:
	case <-ctx.Done():
		return
	}
	turnCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		send(errorEvent("the meeting ended"))
		return
	}
	s.cancel = cancel
	s.warmup = warmup
	s.mu.Unlock()
	defer func() { cancel(); s.mu.Lock(); s.cancel = nil; s.warmup = false; s.mu.Unlock() }()
	t0 := time.Now()
	var first any
	var chunks strings.Builder
	sendEvent := func(e Event) bool {
		kind := alerts.Kind(e)
		if kind != "" {
			if e["type"] == "error" {
				e["reason"] = kind
			}
			s.mu.Lock()
			alert := !s.alerted[kind]
			s.alerted[kind] = true
			s.mu.Unlock()
			if alert {
				detail, _ := e["message"].(string)
				s.Config.Notify("MeetStream Agent Kit", alerts.Message(kind, s.Config.Agent, detail))
			}
		}
		if err := emit(e); err != nil {
			cancel()
			return false
		}
		return true
	}
	delta := func(t string) bool {
		if t == "" {
			return true
		}
		if first == nil {
			first = millis(t0)
		}
		chunks.WriteString(t)
		return sendEvent(Event{"type": "delta", "text": t})
	}
	done := func(timed bool, spawn float64) {
		e := Event{"type": "done", "text": prompts.Truncate(strings.TrimSpace(chunks.String()), config.MaxAnswerChars), "first_output_ms": first, "total_ms": millis(t0), "warm": s.Warm()}
		if timed {
			e["timed_out"] = true
		}
		if !s.Warm() {
			e["spawn_ms"] = spawn
		}
		sendEvent(e)
	}
	timeout := func() {
		s.mu.Lock()
		s.failPendingLocked("cancelled by the speaker")
		s.mu.Unlock()
		if s.Warm() {
			s.Driver.StopTurn()
		}
		suffix := config.TimeoutReply
		if strings.TrimSpace(chunks.String()) != "" {
			suffix = " " + suffix
		}
		_ = delta(suffix)
		done(true, 0)
	}
	if !s.Warm() {
		runCtx, stop := context.WithTimeout(turnCtx, s.Config.Timeout)
		defer stop()
		events := make(chan drivers.Event, 256)
		finished := make(chan float64, 1)
		go func() {
			ms := drivers.RunOnce(runCtx, s.Config, s.Rules()+"\n\n"+text, s.Settings.Access, s.Settings.Model, func(e drivers.Event) {
				select {
				case events <- e:
				case <-runCtx.Done():
				}
			})
			finished <- ms
		}()
		var failure string
		for {
			select {
			case <-runCtx.Done():
				if turnCtx.Err() != nil {
					sendEvent(cancelled())
				} else {
					timeout()
				}
				return
			case e := <-events:
				switch e.Kind {
				case "delta":
					if !delta(e.Text) {
						return
					}
				case "error":
					failure = e.Text
				case "end":
					ms := <-finished
					if chunks.Len() > 0 {
						done(false, ms)
					} else if failure != "" {
						sendEvent(errorEvent(failure))
					} else {
						sendEvent(errorEvent("the agent returned no answer"))
					}
					return
				}
			}
		}
	}
	if s.initErr != nil {
		sendEvent(errorEvent(s.initErr.Error()))
		return
	}
	if err := s.Driver.Start(turnCtx, s.started > 0); err != nil {
		sendEvent(errorEvent(err.Error()))
		return
	}
	events := s.Driver.Events()
	for {
		select {
		case <-events:
		default:
			goto drained
		}
	}
drained:
	if err := s.Driver.Send(turnCtx, text, s.started == 0); err != nil {
		if turnCtx.Err() != nil {
			s.Driver.StopTurn()
			sendEvent(cancelled())
		} else {
			sendEvent(errorEvent(err.Error()))
		}
		return
	}
	s.started++
	timer := time.NewTimer(s.Config.Timeout)
	defer timer.Stop()
	reset := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(s.Config.Timeout)
	}
	for {
		select {
		case <-turnCtx.Done():
			s.Driver.StopTurn()
			sendEvent(cancelled())
			return
		case <-timer.C:
			s.mu.Lock()
			waiting := len(s.pending) > 0
			s.mu.Unlock()
			if waiting {
				timer.Reset(s.Config.Timeout)
			} else {
				timeout()
				return
			}
		case tool := <-s.toolEvents:
			reset()
			if !sendEvent(tool) {
				s.Driver.StopTurn()
				return
			}
		case e := <-events:
			reset()
			switch e.Kind {
			case "delta":
				if !delta(e.Text) {
					s.Driver.StopTurn()
					return
				}
			case "final":
				if chunks.Len() == 0 && !delta(e.Text) {
					s.Driver.StopTurn()
					return
				}
			case "end":
				done(false, 0)
				return
			case "cancelled":
				sendEvent(cancelled())
				return
			case "error", "exit":
				sendEvent(errorEvent(e.Text))
				return
			}
		}
	}
}
func millis(t time.Time) float64 { return float64(time.Since(t).Microseconds()/100) / 10 }
func errorEvent(message string) Event {
	return Event{"type": "error", "code": parsers.Classify(message), "message": prompts.Truncate(message, 300)}
}
func cancelled() Event { return Event{"type": "error", "code": "cancelled", "message": "cancelled"} }
func (s *Session) CallTool(ctx context.Context, name string, args map[string]any) Result {
	id := "call_" + pairing.Secret(8)
	ch := make(chan Result, 1)
	s.mu.Lock()
	if s.closed || s.cancel == nil {
		s.mu.Unlock()
		return Result{"no active turn", true}
	}
	s.pending[id] = ch
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.pending, id); s.mu.Unlock() }()
	b, _ := json.Marshal(args)
	event := Event{"type": "tool_call", "call_id": id, "name": name, "arguments": string(b)}
	select {
	case s.toolEvents <- event:
	case <-ctx.Done():
		return Result{"cancelled", true}
	}
	select {
	case r := <-ch:
		return r
	case <-ctx.Done():
		return Result{"cancelled", true}
	case <-time.After(60 * time.Second):
		return Result{"MIA did not return a result in time.", true}
	}
}
func (s *Session) Resolve(id, output string, isError bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := s.pending[id]
	if ch == nil {
		return false
	}
	delete(s.pending, id)
	ch <- Result{output, isError}
	return true
}
func (s *Session) failPendingLocked(reason string) {
	for id, ch := range s.pending {
		delete(s.pending, id)
		ch <- Result{reason, true}
	}
}
func (s *Session) Tools() []map[string]any {
	tools := []map[string]any{}
	for _, t := range s.Settings.Tools {
		schema := t["parameters"]
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		tools = append(tools, map[string]any{"name": t["name"], "description": t["description"], "inputSchema": schema})
	}
	if s.LocalAllowed() {
		tools = append(tools, localapps.Tools()...)
	}
	if s.Settings.Access == "confirm" {
		tools = append(tools, map[string]any{"name": "approve_action", "description": "Internal: asks the owner to approve an action.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"tool_name": map[string]any{"type": "string"}, "input": map[string]any{"type": "object"}}}})
	}
	return tools
}
func (s *Session) MarkListed(n int) { s.mu.Lock(); defer s.mu.Unlock(); s.toolsListed = &n }
func (s *Session) Listed() *int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.toolsListed == nil {
		return nil
	}
	n := *s.toolsListed
	return &n
}
func (s *Session) LocalTool(ctx context.Context, name string, args map[string]any) Result {
	if !s.LocalAllowed() {
		return Result{"not allowed at this access level", true}
	}
	if name == localapps.StopTool {
		port := 0
		if f, ok := args["port"].(float64); ok {
			port = int(f)
		}
		text, err := s.Shares.Stop(port)
		return Result{text, err}
	}
	port := -1
	if v, ok := args["port"].(float64); ok && v == float64(int(v)) {
		port = int(v)
	}
	if s.Settings.Access == "confirm" && !s.Allow(fmt.Sprintf("share localhost:%d with the meeting through a temporary public address", port)) {
		return Result{"the owner did not allow sharing that app", true}
	}
	text, isError := s.Shares.Share(ctx, port, parsers.Str(args["path"]))
	return Result{text, isError}
}
