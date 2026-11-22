// Package drivers implements warm CLI protocols over subprocess stdio.
package drivers

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/meetstream-ai/cli/internal/agentkit/config"
	"github.com/meetstream-ai/cli/internal/agentkit/pairing"
	"github.com/meetstream-ai/cli/internal/agentkit/parsers"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Options struct {
	Config                                          config.Config
	SessionID, Model, Access, Rules, MCPURL, Secret string
	ToolNames                                       []string
	Allow                                           func(string) bool
}
type Runtime struct {
	opts                    Options
	mu                      sync.Mutex
	p                       *process
	conversationID, mcpFile string
	spawnMS                 float64
	readyErr                error
}

func New(o Options) *Runtime {
	id := pairing.Secret(16)
	id = id[:8] + "-" + id[8:12] + "-4" + id[13:16] + "-a" + id[17:20] + "-" + id[20:]
	return &Runtime{opts: o, conversationID: id}
}
func (r *Runtime) Warm() bool       { return r.opts.Config.Agent.Warm != "" }
func (r *Runtime) SpawnMS() float64 { r.mu.Lock(); defer r.mu.Unlock(); return r.spawnMS }
func (r *Runtime) Args(resume bool) ([]string, error) {
	o := r.opts
	c := o.Config
	a := c.Agent
	argv := []string{c.Binary}
	switch a.Warm {
	case "claude_stream_json", "agy_stream_json":
		argv = append(argv, "-p", "--input-format", "stream-json", "--output-format", "stream-json")
		if a.Warm == "claude_stream_json" {
			if r.mcpFile == "" {
				dir := c.RuntimeDir
				if dir == "" {
					dir = pairing.Home()
				}
				dir = filepath.Join(dir, "run")
				r.mcpFile = filepath.Join(dir, "mcp-"+o.SessionID+"-"+pairing.Secret(4)+".json")
				v := map[string]any{"mcpServers": map[string]any{"meeting": map[string]any{"type": "http", "url": o.MCPURL, "headers": map[string]any{"Authorization": "Bearer " + o.Secret}}}}
				if err := pairing.WritePrivate(r.mcpFile, v); err != nil {
					return nil, err
				}
			}
			argv = append(argv, "--include-partial-messages", "--verbose", "--append-system-prompt", o.Rules, "--mcp-config", r.mcpFile, "--strict-mcp-config")
			flag := "--session-id"
			if resume {
				flag = "--resume"
			}
			argv = append(argv, flag, r.conversationID)
			switch o.Access {
			case "confirm":
				argv = append(argv, "--tools=default", "--allowedTools=Read,Grep,Glob,mcp__meeting", "--permission-prompt-tool", "mcp__meeting__approve_action")
			case "full":
				argv = append(argv, "--tools=default", "--permission-mode", "acceptEdits", "--allowedTools=Bash,mcp__meeting")
			default:
				argv = append(argv, "--tools=Read,Grep,Glob", "--permission-mode", "dontAsk", "--allowedTools=mcp__meeting")
			}
		} else if o.Access == "full" {
			argv = append(argv, a.FullArgs...)
		} else {
			argv = append(argv, a.ReadonlyArgs...)
		}
		argv = append(argv, a.ModelArgs(o.Model)...)
		argv = append(argv, a.EffortFlags(c.Effort)...)
	case "acp":
		argv = append(argv, a.ACPArgs...)
	case "codex_app_server":
		argv = append(argv, "app-server")
		if len(o.ToolNames) > 0 {
			u, _ := json.Marshal(o.MCPURL)
			argv = append(argv, "-c", "mcp_servers.meeting.url="+string(u), "-c", `mcp_servers.meeting.bearer_token_env_var="MEETSTREAM_AGENT_KIT_MCP_SECRET"`, "-c", `mcp_servers.meeting.default_tools_approval_mode="approve"`)
		}
	}
	if c.IgnoreRules && a.IgnoreRulesWarm {
		argv = append(argv, a.IgnoreRulesArgs...)
	}
	return argv, nil
}
func (r *Runtime) Start(ctx context.Context, resume bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.p != nil {
		select {
		case <-r.p.done:
		default:
			return r.readyErr
		}
	}
	args, err := r.Args(resume)
	if err != nil {
		return err
	}
	p, ms, err := spawn(args, r.opts.Config.Workdir, Env(r.opts.Config, r.opts.Model, r.opts.Secret))
	if err != nil {
		return err
	}
	r.p = p
	r.spawnMS = ms
	r.readyErr = nil
	go p.readLines(func(line string) { r.read(p, line) })
	if r.opts.Config.Agent.Warm == "acp" || r.opts.Config.Agent.Warm == "codex_app_server" {
		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		err = r.handshake(ctx, p)
		r.readyErr = err
	}
	return err
}
func (r *Runtime) Events() <-chan Event { r.mu.Lock(); defer r.mu.Unlock(); return r.p.events }
func (r *Runtime) Send(ctx context.Context, text string, first bool) error {
	r.mu.Lock()
	p := r.p
	err := r.readyErr
	r.mu.Unlock()
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("agent process not running")
	}
	o := r.opts
	switch o.Config.Agent.Warm {
	case "claude_stream_json":
		return p.send(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}, "parent_tool_use_id": nil}, false)
	case "agy_stream_json":
		if first {
			text = o.Rules + "\n\n" + text
		}
		return p.send(map[string]any{"event": "user", "message": map[string]any{"content": text}}, false)
	case "acp":
		if first {
			text = o.Rules + "\n\n" + text
		}
		id := p.id()
		p.mu.Lock()
		p.promptID = idKey(id)
		sid := p.sessionID
		p.mu.Unlock()
		return p.send(map[string]any{"id": id, "method": "session/prompt", "params": map[string]any{"sessionId": sid, "prompt": []any{map[string]any{"type": "text", "text": text}}}}, true)
	case "codex_app_server":
		p.mu.Lock()
		tid := p.threadID
		p.mu.Unlock()
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		v, err := p.request(ctx, "turn/start", map[string]any{"threadId": tid, "input": []any{map[string]any{"type": "text", "text": text, "text_elements": []any{}}}}, false)
		if err == nil {
			p.mu.Lock()
			p.turnID = parsers.Str(parsers.Obj(v["turn"])["id"])
			p.mu.Unlock()
		}
		return err
	}
	return nil
}

// StopTurn waits for the cancelled turn's terminal event. A noncooperative
// process is replaced before the next turn can touch its queues or stdin.
func (r *Runtime) StopTurn() {
	r.mu.Lock()
	p := r.p
	r.mu.Unlock()
	if p == nil {
		return
	}
	o := r.opts
	if o.Config.Agent.Warm == "claude_stream_json" || o.Config.Agent.Warm == "agy_stream_json" {
		r.Restart()
		return
	}
	p.mu.Lock()
	sid, tid, turn := p.sessionID, p.threadID, p.turnID
	p.mu.Unlock()
	if o.Config.Agent.Warm == "acp" {
		_ = p.send(map[string]any{"method": "session/cancel", "params": map[string]any{"sessionId": sid}}, true)
	} else if turn != "" {
		_ = p.send(map[string]any{"id": p.id(), "method": "turn/interrupt", "params": map[string]any{"threadId": tid, "turnId": turn}}, false)
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case e := <-p.events:
			if e.Kind == "end" || e.Kind == "exit" || e.Kind == "error" {
				p.mu.Lock()
				if turn != "" {
					p.oldTurns[turn] = true
				}
				p.mu.Unlock()
				return
			}
		case <-timer.C:
			r.Restart()
			return
		}
	}
}
func (r *Runtime) Restart() {
	r.mu.Lock()
	p := r.p
	r.p = nil
	r.mu.Unlock()
	if p != nil {
		p.close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_ = r.Start(ctx, true)
}
func (r *Runtime) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.p != nil {
		r.p.close()
		r.p = nil
	}
	if r.mcpFile != "" {
		_ = os.Remove(r.mcpFile)
		r.mcpFile = ""
	}
}
func (r *Runtime) handshake(ctx context.Context, p *process) error {
	o := r.opts
	rpc := o.Config.Agent.Warm == "acp"
	info := map[string]any{"name": "meetstream-agent-kit", "title": "MeetStream Agent Kit", "version": config.Version}
	init := map[string]any{"clientInfo": info, "capabilities": nil}
	if rpc {
		init = map[string]any{"protocolVersion": 1, "clientInfo": info, "clientCapabilities": map[string]any{"fs": map[string]any{"readTextFile": false, "writeTextFile": false}, "terminal": false}}
	}
	v, err := p.request(ctx, "initialize", init, rpc)
	if err != nil {
		return err
	}
	if rpc {
		servers := []any{}
		if len(o.ToolNames) > 0 && o.Config.Agent.ACPMCP {
			caps := parsers.Obj(parsers.Obj(v["agentCapabilities"])["mcpCapabilities"])
			server := map[string]any{}
			if caps["http"] == true {
				server = map[string]any{"type": "http", "name": "meeting", "url": o.MCPURL, "headers": []any{map[string]any{"name": "Authorization", "value": "Bearer " + o.Secret}}}
			} else {
				binary, err := os.Executable()
				if err != nil {
					return err
				}
				server = map[string]any{"name": "meeting", "command": binary, "args": []any{"agent", "mcp-stdio", "--url", o.MCPURL}, "env": []any{map[string]any{"name": config.MCPSecretEnv, "value": o.Secret}}}
			}
			servers = append(servers, server)
		}
		created, err := p.request(ctx, "session/new", map[string]any{"cwd": o.Config.Workdir, "mcpServers": servers}, true)
		if err != nil {
			return err
		}
		sid := parsers.Str(created["sessionId"])
		if sid == "" {
			return fmt.Errorf("ACP session not ready")
		}
		p.mu.Lock()
		p.sessionID = sid
		p.mu.Unlock()
		setModel := o.Config.Agent.ACPSetModel
		available, _ := parsers.Obj(created["models"])["availableModels"].([]any)
		for _, m := range available {
			if parsers.Obj(m)["modelId"] == o.Model {
				setModel = true
			}
		}
		if o.Model != "" && setModel {
			_, _ = p.request(ctx, "session/set_model", map[string]any{"sessionId": sid, "modelId": o.Model}, true)
		}
		opts, _ := created["configOptions"].([]any)
		for _, opt := range opts {
			m := parsers.Obj(opt)
			if m["category"] != "thought_level" {
				continue
			}
			values, _ := m["options"].([]any)
			for _, value := range values {
				if parsers.Obj(value)["value"] == o.Config.Effort && o.Config.Effort != "" {
					_, _ = p.request(ctx, "session/set_config_option", map[string]any{"sessionId": sid, "configId": m["id"], "value": o.Config.Effort}, true)
					break
				}
			}
		}
		return nil
	}
	_ = p.send(map[string]any{"method": "initialized"}, false)
	policy, sandbox := "never", "read-only"
	if o.Access == "confirm" {
		policy = "on-request"
	}
	if o.Access == "full" {
		sandbox = "workspace-write"
	}
	params := map[string]any{"cwd": o.Config.Workdir, "approvalPolicy": policy, "sandbox": sandbox, "developerInstructions": o.Rules, "ephemeral": true, "config": nil}
	if o.Config.Effort != "" {
		params["config"] = map[string]any{"model_reasoning_effort": o.Config.Effort}
	}
	if o.Model != "" {
		params["model"] = o.Model
	}
	v, err = p.request(ctx, "thread/start", params, false)
	if err != nil {
		return err
	}
	tid := parsers.Str(parsers.Obj(v["thread"])["id"])
	if tid == "" {
		return fmt.Errorf("Codex thread not ready")
	}
	p.mu.Lock()
	p.threadID = tid
	p.mu.Unlock()
	return nil
}
func (r *Runtime) read(p *process, line string) {
	o := r.opts
	warm := o.Config.Agent.Warm
	if warm == "claude_stream_json" || warm == "agy_stream_json" {
		parsed := parsers.Parse(warm, line)
		if parsed.Delta != "" {
			p.emit(Event{"delta", parsed.Delta})
		}
		if parsed.HasError {
			p.emit(Event{"error", parsed.Error})
			p.emit(Event{Kind: "end"})
		} else if parsed.HasFinal {
			p.emit(Event{"final", parsed.Final})
			p.emit(Event{Kind: "end"})
		}
		return
	}
	var m map[string]any
	if json.Unmarshal([]byte(line), &m) != nil || m == nil {
		return
	}
	method := parsers.Str(m["method"])
	params := parsers.Obj(m["params"])
	if o.Config.Debug {
		o.Config.Event("protocol", map[string]any{"method": method})
	}
	if method == "" && m["id"] != nil {
		if warm == "acp" {
			p.mu.Lock()
			prompt := idKey(m["id"]) == p.promptID
			if prompt {
				p.promptID = ""
			}
			p.mu.Unlock()
			if prompt {
				if e, ok := m["error"]; ok {
					p.emit(Event{"error", parsers.Str(parsers.Obj(e)["message"])})
				} else if parsers.Obj(m["result"])["stopReason"] == "cancelled" {
					p.emit(Event{Kind: "cancelled"})
				}
				p.emit(Event{Kind: "end"})
				return
			}
		}
		p.resolve(m)
		return
	}
	if m["id"] != nil {
		go r.answer(p, m)
		return
	}
	if warm == "acp" && method == "session/update" {
		u := parsers.Obj(params["update"])
		c := parsers.Obj(u["content"])
		if u["sessionUpdate"] == "agent_message_chunk" && c["type"] == "text" {
			p.emit(Event{"delta", parsers.Str(c["text"])})
		}
		return
	}
	if warm != "codex_app_server" {
		return
	}
	turn := parsers.Obj(params["turn"])
	tid := parsers.Str(params["turnId"])
	if tid == "" {
		tid = parsers.Str(turn["id"])
	}
	p.mu.Lock()
	stale := p.oldTurns[tid]
	p.mu.Unlock()
	if stale {
		return
	}
	switch method {
	case "item/agentMessage/delta":
		p.emit(Event{"delta", parsers.Str(params["delta"])})
	case "turn/completed":
		p.mu.Lock()
		p.turnID = ""
		p.mu.Unlock()
		if turn["status"] == "failed" {
			p.emit(Event{"error", codexError(parsers.Obj(turn["error"]))})
		} else if turn["status"] == "interrupted" {
			p.emit(Event{Kind: "cancelled"})
		}
		p.emit(Event{Kind: "end"})
	case "error":
		e := parsers.Obj(params["error"])
		if params["willRetry"] != true && e["codexErrorInfo"] == "unauthorized" {
			p.emit(Event{"error", codexError(e)})
			p.emit(Event{Kind: "end"})
		}
	}
}
func codexError(e map[string]any) string {
	s := parsers.Str(e["message"])
	if s == "" {
		s = "Codex turn failed"
	}
	if e["codexErrorInfo"] == "unauthorized" {
		s = "unauthorized: Codex login required. " + s
	}
	return s
}
func PermissionOption(options []any, allowed bool) map[string]any {
	exact := []string{"reject_once", "reject_always"}
	prefixes := []string{"reject", "deny", "no", "decline"}
	if allowed {
		exact = []string{"allow_once", "allow_always"}
		prefixes = []string{"allow", "approve", "yes", "accept"}
	}
	for _, field := range []string{"exact", "kind", "name"} {
		for _, raw := range options {
			v := parsers.Obj(raw)
			if parsers.Str(v["optionId"]) == "" {
				continue
			}
			key := field
			if key == "exact" {
				key = "kind"
			}
			s := strings.ToLower(parsers.Str(v[key]))
			if field == "exact" {
				for _, e := range exact {
					if s == e {
						return v
					}
				}
			} else {
				for _, prefix := range prefixes {
					if strings.HasPrefix(s, prefix) {
						return v
					}
				}
			}
		}
	}
	return nil
}
func (r *Runtime) answer(p *process, m map[string]any) {
	o := r.opts
	method := parsers.Str(m["method"])
	params := parsers.Obj(m["params"])
	rpc := o.Config.Agent.Warm == "acp"
	reply := map[string]any{"id": m["id"]}
	allow := func(s string) bool { return o.Allow != nil && o.Allow(s) }
	switch method {
	case "session/request_permission":
		tool := parsers.Obj(params["toolCall"])
		kind := parsers.Str(tool["kind"])
		title := parsers.Str(tool["title"])
		safe := kind == "read" || kind == "search" || kind == "think" || kind == "fetch"
		for _, name := range o.ToolNames {
			if name != "" && strings.Contains(title, name) {
				safe = true
			}
		}
		allowed := safe || allow(kind+": "+title)
		options, _ := params["options"].([]any)
		choice := PermissionOption(options, allowed)
		outcome := map[string]any{"outcome": "cancelled"}
		if choice != nil {
			outcome = map[string]any{"outcome": "selected", "optionId": choice["optionId"]}
		}
		reply["result"] = map[string]any{"outcome": outcome}
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		decision := "decline"
		what := parsers.Str(params["command"])
		if what == "" {
			what = parsers.Str(params["reason"])
		}
		if allow(method + ": " + what) {
			decision = "accept"
		}
		reply["result"] = map[string]any{"decision": decision}
	case "execCommandApproval", "applyPatchApproval":
		decision := "denied"
		if allow(method) {
			decision = "approved"
		}
		reply["result"] = map[string]any{"decision": decision}
	case "mcpServer/elicitation/request":
		action := "decline"
		var content any
		if params["serverName"] == "meeting" {
			action = "accept"
			content = map[string]any{}
		}
		reply["result"] = map[string]any{"action": action, "content": content, "_meta": nil}
	default:
		reply["error"] = map[string]any{"code": -32601, "message": "not supported by the bridge"}
	}
	_ = p.send(reply, rpc)
}
