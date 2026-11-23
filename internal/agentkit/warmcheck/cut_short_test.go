package warmcheck

import (
	"context"
	"testing"

	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"github.com/meetstream-ai/cli/internal/agentkit/config"
)

// A done the bridge cut short (timed out, cancelled) is not the agent answering.
func TestInjectedSessionCutShort(t *testing.T) {
	closed := false
	textOnly := config.Config{Agent: agents.Spec{Name: "x", Warm: "acp"}}
	timedOut := map[string]any{"type": "done", "text": "ready That took too long, so I stopped it.", "timed_out": true}
	if r := Check(context.Background(), textOnly, Options{Turn: true, Open: opener(&fakeSession{events: []map[string]any{timedOut}}, &closed)}); r.OK || r.Message != "the warm turn timed out" {
		t.Fatalf("text-only agent: %+v", r)
	}
	one := 1
	f := &fakeSession{listed: &one, events: []map[string]any{{"type": "tool_call", "name": CheckTool, "call_id": "c1"}, timedOut}}
	if r := Check(context.Background(), config.Config{Agent: agents.All["agy"]}, Options{Turn: true, Open: opener(f, &closed)}); r.OK || r.Message != "the warm turn timed out" {
		t.Fatalf("tools: %+v", r)
	}
	f = &fakeSession{events: []map[string]any{{"type": "done", "text": "ready", "cancelled": true}}}
	if r := Check(context.Background(), textOnly, Options{Turn: true, Open: opener(f, &closed)}); r.OK || r.Message != "the warm turn was cancelled" {
		t.Fatalf("cancelled: %+v", r)
	}
}
