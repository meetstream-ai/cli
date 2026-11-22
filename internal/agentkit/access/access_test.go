package access

import (
	"strings"
	"testing"

	"github.com/meetstream-ai/cli/internal/agentkit/agents"
)

// Ported from the kit's tests/test_units.py::test_effective_access: the bridge's
// --max-access always wins; MIA's config may lower the level, never raise it.
func TestEffective(t *testing.T) {
	cases := []struct {
		agent, max, requested string
		verified              bool
		want                  string
	}{
		{"claude", "readonly", "full", true, "readonly"}, // MeetStream cannot raise it
		{"claude", "confirm", "full", true, "confirm"},
		{"claude", "full", "", true, "full"},             // no request: the machine's level
		{"claude", "full", "readonly", true, "readonly"}, // the config may lower it
		{"claude", "full", "full", false, "confirm"},     // full needs a verified owner
		{"codex", "full", "full", true, "full"},
		{"hermes", "confirm", "", false, "confirm"},
		{"agy", "confirm", "", false, "confirm"}, // agy asks Allow/Deny over ACP
		{"agy", "full", "", true, "full"},
		{"claude", "bogus", "full", true, "readonly"},
	}
	for _, c := range cases {
		d := Effective(agents.All[c.agent], c.max, c.requested, c.verified)
		if d.Level != c.want {
			t.Errorf("%s max=%s req=%q verified=%v: level %q, want %q", c.agent, c.max, c.requested, c.verified, d.Level, c.want)
		}
		asked := c.requested
		if asked == "" {
			asked = c.max
		}
		if (d.Reason != "") != (c.want != asked) {
			t.Errorf("%s max=%s req=%q: reason %q, want a reason only when the level differs from what was asked", c.agent, c.max, c.requested, d.Reason)
		}
	}
}

// Ported from tests/test_units.py (custom agent case): an agent that cannot ask
// first never runs at confirm.
func TestCustomAgentCannotConfirm(t *testing.T) {
	spec, err := agents.Custom("mytool {prompt}")
	if err != nil {
		t.Fatal(err)
	}
	d := Effective(spec, "confirm", "", false)
	if d.Level != "readonly" || !strings.Contains(d.Reason, "cannot ask before acting") {
		t.Fatalf("custom agent: %+v", d)
	}
}

func TestValid(t *testing.T) {
	for _, s := range []string{"readonly", "confirm", "full"} {
		if !Valid(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []string{"", "bogus", "Full", "admin"} {
		if Valid(s) {
			t.Errorf("%q should be invalid", s)
		}
	}
}
