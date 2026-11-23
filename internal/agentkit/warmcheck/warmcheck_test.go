package warmcheck

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"github.com/meetstream-ai/cli/internal/agentkit/bridge"
	"github.com/meetstream-ai/cli/internal/agentkit/config"
	"github.com/meetstream-ai/cli/internal/testutil/fakecli"
)

// The ACP driver starts the meeting-tools shim as `<this binary> agent mcp-stdio
// --url …`. In tests this binary is the test binary, so it plays the shim.
func TestMain(m *testing.M) {
	if len(os.Args) >= 5 && os.Args[1] == "agent" && os.Args[2] == "mcp-stdio" && os.Args[3] == "--url" {
		if err := bridge.Stdio(context.Background(), os.Args[4], os.Getenv(config.MCPSecretEnv), os.Stdin, os.Stdout); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	ToolListWait = 500 * time.Millisecond
	os.Exit(m.Run())
}

// stubConfig runs the kit's stub agent (no model, no network) as agent name.
func stubConfig(t *testing.T, name string) config.Config {
	t.Helper()
	dir := t.TempDir()
	stub := fakecli.Python(t, dir, name, filepath.Join("..", "bridge", "testdata", "stub_agent.py"))
	return config.Config{Agent: agents.All[name], Binary: stub, Workdir: dir, RuntimeDir: dir, Owner: "Nav", Timeout: 30 * time.Second}
}

func TestWarmCheckCallsAMeetingToolThroughACP(t *testing.T) {
	var stages []string
	r := Check(context.Background(), stubConfig(t, "agy"), Options{Turn: true, OnStage: func(s string, _ time.Duration) { stages = append(stages, s) }})
	if !r.OK || !r.ToolCalled || r.ToolsListed == nil || *r.ToolsListed != 1 {
		t.Fatalf("%+v", r)
	}
	if r.Message != "Warm mode (ACP) ready · meeting tools work (test tool called)" {
		t.Fatal(r.Message)
	}
	if strings.Join(stages, ",") != "ready,tool_call,done" {
		t.Fatalf("stages %v", stages)
	}
}

func TestDoctorStyleCheckStopsAfterTheHandshake(t *testing.T) {
	lazy := Check(context.Background(), stubConfig(t, "hermes"), Options{})
	if !lazy.OK || lazy.ToolCalled {
		t.Fatalf("%+v", lazy)
	}
	if lazy.Message != "Warm mode (ACP) ready · meeting tools offered; the agent loads them on first use" {
		t.Fatal(lazy.Message)
	}
	t.Setenv("STUB_MCP_AT_START", "1") // like agents that load MCP tools at session start
	eager := Check(context.Background(), stubConfig(t, "agy"), Options{})
	if !eager.OK || eager.ToolsListed == nil || *eager.ToolsListed != 1 {
		t.Fatalf("%+v", eager)
	}
	if eager.Message != "Warm mode (ACP) ready · 1 meeting tool loaded" {
		t.Fatal(eager.Message)
	}
}

func TestSignedOutACPAgentIsReportedWithItsSignIn(t *testing.T) {
	t.Setenv("STUB_AUTH_FAIL", "1")
	r := Check(context.Background(), stubConfig(t, "agy"), Options{})
	if r.OK || !strings.Contains(r.Message, "Authentication required") || r.Fix != "Sign in with: agy acp --login" {
		t.Fatalf("%+v", r)
	}
}

func TestAnAgentThatIgnoresTheMeetingToolsFailsTheCheck(t *testing.T) {
	t.Setenv("STUB_NO_TOOLS", "1")
	r := Check(context.Background(), stubConfig(t, "agy"), Options{Turn: true})
	if r.OK || !strings.Contains(r.Message, "did not call the meeting tool") || !strings.Contains(r.Message, "never loaded the meeting tools") {
		t.Fatalf("%+v", r)
	}
}

func TestTextOnlyACPAgentGetsAPlainTurn(t *testing.T) {
	r := Check(context.Background(), stubConfig(t, "openclaw"), Options{Turn: true})
	if !r.OK || !strings.Contains(r.Message, "text only") {
		t.Fatalf("%+v", r)
	}
}

func TestOnlyACPAgentsGetTheWarmCheck(t *testing.T) {
	if !AppliesTo(agents.All["agy"]) || AppliesTo(agents.All["claude"]) || AppliesTo(agents.All["codex"]) {
		t.Fatal("AppliesTo is wrong")
	}
}

// ── with an injected session: no process at all ──────────────────────────────

type fakeSession struct {
	ready  error
	listed *int
	events []map[string]any
	echo   bool // answer with the tool's result, like a working agent
	got    string
}

func (f *fakeSession) Ready(context.Context) error { return f.ready }
func (f *fakeSession) Listed() *int                { return f.listed }
func (f *fakeSession) Resolve(_, output string, _ bool) bool {
	f.got = output
	return true
}
func (f *fakeSession) Run(_ context.Context, _ string, _ bool, emit func(map[string]any) error) {
	for _, e := range f.events {
		if e["type"] == "done" && f.echo {
			e = map[string]any{"type": "done", "text": f.got}
		}
		_ = emit(e)
	}
}

func opener(f *fakeSession, closed *bool) Opener {
	return func(context.Context, config.Config, []map[string]any) (Session, func(), error) {
		return f, func() { *closed = true }, nil
	}
}

func TestInjectedSessionToolTurn(t *testing.T) {
	one := 1
	f := &fakeSession{listed: &one, echo: true, events: []map[string]any{
		{"type": "tool_call", "name": CheckTool, "call_id": "c1"},
		{"type": "done"},
	}}
	closed := false
	r := Check(context.Background(), config.Config{Agent: agents.All["agy"]}, Options{Turn: true, Open: opener(f, &closed)})
	if !r.OK || !r.ToolCalled || !strings.HasPrefix(f.got, "ready-") || !closed {
		t.Fatalf("%+v got=%q closed=%v", r, f.got, closed)
	}
}

func TestInjectedSessionFailures(t *testing.T) {
	closed := false
	agy := config.Config{Agent: agents.All["agy"]}
	r := Check(context.Background(), agy, Options{Open: opener(&fakeSession{ready: errors.New("exec: no such file")}, &closed)})
	if r.OK || r.Fix != "Run `agy acp` yourself to see its error." || !closed {
		t.Fatalf("%+v", r)
	}
	r = Check(context.Background(), agy, Options{Turn: true, Open: opener(&fakeSession{events: []map[string]any{{"type": "error", "message": "boom"}}}, &closed)})
	if r.OK || r.Message != "boom" {
		t.Fatalf("%+v", r)
	}
	r = Check(context.Background(), agy, Options{Turn: true, Open: opener(&fakeSession{}, &closed)})
	if r.OK || r.Message != "the warm turn did not finish" {
		t.Fatalf("%+v", r)
	}
	openFail := func(context.Context, config.Config, []map[string]any) (Session, func(), error) {
		return nil, nil, errors.New("cannot start the local bridge")
	}
	if r := Check(context.Background(), agy, Options{Open: openFail}); r.OK || r.Message != "cannot start the local bridge" {
		t.Fatalf("%+v", r)
	}
	if r := Check(context.Background(), config.Config{Agent: agents.Spec{Name: "custom"}}, Options{}); r.OK || r.Message != "this agent has no warm mode" {
		t.Fatalf("%+v", r)
	}
}
