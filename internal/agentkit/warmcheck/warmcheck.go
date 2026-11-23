// Package warmcheck checks the warm path meetings use, before a meeting does
// (the kit's warm_check.py).
//
// Setup's test turn runs an agent once (agy -p), but meetings keep one warm
// process (agy acp and the other ACP agents). Check runs that warm path on its
// own: a bridge on a free local port, the agent's ACP process, initialize and
// session/new with the meeting tools through the stdio shim, and (in setup)
// one turn asking the agent to call a harmless test tool, answered here as MIA
// would. Doctor stops after the handshake, so it costs no model tokens.
package warmcheck

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"github.com/meetstream-ai/cli/internal/agentkit/bridge"
	"github.com/meetstream-ai/cli/internal/agentkit/config"
	"github.com/meetstream-ai/cli/internal/agentkit/parsers"
	"github.com/meetstream-ai/cli/internal/agentkit/prompts"
	"github.com/meetstream-ai/cli/internal/agentkit/session"
)

// CheckTool is the harmless test tool the agent is asked to call.
const CheckTool = "check_ping"

// CheckTools offers only CheckTool.
var CheckTools = []map[string]any{{
	"name":        CheckTool,
	"description": "Setup check: returns one word. Call it only when asked to.",
	"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
}}

// ToolListWait is how long, after the handshake, doctor waits for the agent to
// load the meeting tools (agents that load them at session start).
var ToolListWait = 8 * time.Second

// DefaultTimeout caps the whole check.
const DefaultTimeout = 120 * time.Second

// Result of a check.
type Result struct {
	OK          bool
	Message     string
	Fix         string
	ToolsListed *int // meeting tools the agent loaded from the bridge; nil: it did not load them
	ToolCalled  bool
	Total       time.Duration
}

// AppliesTo: only ACP agents, whose meetings run a different command than the one-shot test.
func AppliesTo(a agents.Spec) bool { return a.Warm == "acp" }

// Session is the warm meeting session the check drives. *session.Session has
// every method but Ready; the bridge opener adds it.
type Session interface {
	// Ready waits for the warm process's handshake and returns its error.
	Ready(ctx context.Context) error
	// Listed is how many meeting tools the agent listed, nil before it lists them.
	Listed() *int
	// Run sends one turn and calls emit for each event (delta, tool_call, done, error).
	Run(ctx context.Context, text string, warmup bool, emit func(map[string]any) error)
	// Resolve answers a tool_call event, as MIA would.
	Resolve(callID, output string, isError bool) bool
}

// Opener starts the agent's warm session with tools offered; close ends it
// (and its process). The default is BridgeOpener; tests inject their own.
type Opener func(ctx context.Context, c config.Config, tools []map[string]any) (s Session, close func(), err error)

// Options for Check.
type Options struct {
	Turn    bool          // also run one turn that calls the test tool (setup); false stops after the handshake (doctor)
	Timeout time.Duration // 0: DefaultTimeout
	OnStage func(stage string, elapsed time.Duration)
	Open    Opener // nil: BridgeOpener
}

func fail(c config.Config, message string, t0 time.Time) Result {
	a := c.Agent
	var fix string
	switch {
	case parsers.AuthError(message) && a.Relogin != "":
		fix = "Sign in with: " + a.Relogin
	case parsers.AuthError(message):
		fix = "Sign in to the agent CLI."
	default:
		fix = fmt.Sprintf("Run `%s` yourself to see its error.", strings.TrimSpace(a.Binary+" "+strings.Join(a.ACPArgs, " ")))
	}
	return Result{OK: false, Message: message, Fix: fix, Total: time.Since(t0)}
}

// Check starts the agent's warm session with the meeting tools; with o.Turn
// it also asks the agent to call the test tool. Stages: ready, tool_call, done.
func Check(ctx context.Context, c config.Config, o Options) Result {
	t0 := time.Now()
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	c = c.Defaults()
	c.Quiet, c.Prime = true, false
	c.Timeout = min(c.Timeout, timeout)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stage := func(name string) {
		if o.OnStage != nil {
			o.OnStage(name, time.Since(t0))
		}
	}
	if c.Agent.Warm == "" {
		return Result{Message: "this agent has no warm mode", Total: time.Since(t0)}
	}
	offersTools := c.Agent.ACPMCP
	tools := []map[string]any{}
	if offersTools {
		tools = CheckTools
	}
	open := o.Open
	if open == nil {
		open = BridgeOpener
	}
	s, closeSession, err := open(ctx, c, tools)
	if err != nil {
		return fail(c, err.Error(), t0)
	}
	defer func() {
		if closeSession != nil {
			closeSession()
		}
		stage("done")
	}()
	if err := s.Ready(ctx); err != nil {
		return fail(c, err.Error(), t0)
	}
	stage("ready")
	if !o.Turn {
		if offersTools {
			deadline := time.Now().Add(ToolListWait)
			for s.Listed() == nil && time.Now().Before(deadline) && ctx.Err() == nil {
				time.Sleep(100 * time.Millisecond)
			}
		}
		listed := s.Listed()
		return Result{OK: true, Message: readyMessage(offersTools, listed, false), ToolsListed: listed, Total: time.Since(t0)}
	}
	return toolTurn(ctx, c, s, offersTools, t0, stage)
}

func toolTurn(ctx context.Context, c config.Config, s Session, offersTools bool, t0 time.Time, stage func(string)) Result {
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	word := "ready-" + hex.EncodeToString(b)
	ask := "This is a setup check. Reply with the single word: ready"
	if offersTools {
		ask = fmt.Sprintf("This is a setup check. Call the %s tool once, then reply with only the word it returned.", CheckTool)
	}
	called := false
	var last map[string]any
	s.Run(ctx, prompts.Turn([]map[string]any{{"speaker": c.Owner, "text": ask}}), false, func(e map[string]any) error {
		if e["type"] == "tool_call" && e["name"] == CheckTool {
			if !called {
				stage("tool_call")
			}
			called = true
			s.Resolve(parsers.Str(e["call_id"]), word, false)
		}
		last = e
		return nil
	})
	if last == nil || last["type"] != "done" {
		message := "the warm turn did not finish"
		if last != nil && parsers.Str(last["message"]) != "" {
			message = parsers.Str(last["message"])
		}
		return fail(c, message, t0)
	}
	// A done that was cut short is the bridge's own reply, not the agent's.
	if last["timed_out"] == true {
		return fail(c, "the warm turn timed out", t0)
	}
	if last["cancelled"] == true {
		return fail(c, "the warm turn was cancelled", t0)
	}
	listed := s.Listed()
	if offersTools && !(called && strings.Contains(parsers.Str(last["text"]), word)) {
		message := "warm mode answered, but did not call the meeting tool"
		if listed == nil || *listed == 0 {
			message += " (it never loaded the meeting tools)"
		}
		return Result{Message: message, Fix: "Meeting tools will not work in meetings. Check that the agent accepts MCP servers per session.",
			ToolsListed: listed, ToolCalled: called, Total: time.Since(t0)}
	}
	return Result{OK: true, Message: readyMessage(offersTools, listed, called), ToolsListed: listed, ToolCalled: called, Total: time.Since(t0)}
}

func readyMessage(offersTools bool, listed *int, called bool) string {
	switch {
	case !offersTools:
		return "Warm mode (ACP) ready · text only (this agent takes no meeting tools)"
	case called:
		return "Warm mode (ACP) ready · meeting tools work (test tool called)"
	case listed != nil && *listed > 0:
		plural := "s"
		if *listed == 1 {
			plural = ""
		}
		return fmt.Sprintf("Warm mode (ACP) ready · %d meeting tool%s loaded", *listed, plural)
	}
	return "Warm mode (ACP) ready · meeting tools offered; the agent loads them on first use"
}

// bridgeSession adds Ready to a bridge session.
type bridgeSession struct{ *session.Session }

// Ready returns the handshake's error. The driver keeps it while its process
// lives; a process that already exited is started once more to get it.
func (b bridgeSession) Ready(ctx context.Context) error { return b.Driver.Start(ctx, false) }

// BridgeOpener is the real opener: a bridge on a free loopback port (for the
// meeting tools' MCP endpoint), and the session "warm-check" on it, read-only.
func BridgeOpener(ctx context.Context, c config.Config, tools []map[string]any) (Session, func(), error) {
	srv := bridge.New(c)
	ln, err := srv.Listen(0)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot start the local bridge: %w", err)
	}
	hs := srv.HTTPServer()
	go func() { _ = hs.Serve(ln) }()
	closeAll := func() { srv.Close(); _ = hs.Close() }
	s, _ := srv.Start(ctx, "warm-check", session.Settings{Tools: tools, Model: c.Model, Access: "readonly"})
	if s == nil {
		closeAll()
		return nil, nil, errors.New("the warm session did not start")
	}
	return bridgeSession{s}, closeAll, nil
}
