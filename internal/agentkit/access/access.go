package access

import (
	"fmt"
	"github.com/meetstream-ai/cli/internal/agentkit/agents"
)

type Decision struct{ Level, Reason string }

func rank(s string) int {
	switch s {
	case "readonly":
		return 0
	case "confirm":
		return 1
	case "full":
		return 2
	}
	return -1
}
func Valid(s string) bool { return rank(s) >= 0 }
func Effective(agent agents.Spec, ceiling, requested string, verified bool) Decision {
	if !Valid(ceiling) {
		ceiling = "readonly"
	}
	d := Decision{Level: ceiling}
	if Valid(requested) {
		if rank(requested) < rank(d.Level) {
			d.Level = requested
		} else if rank(requested) > rank(d.Level) {
			d.Reason = fmt.Sprintf("this machine allows up to %s (meetstream-agent-kit serve --max-access)", d.Level)
		}
	}
	if d.Level == "full" && !verified {
		d.Level = "confirm"
		d.Reason = "full needs only-me matched by a verified id"
	}
	if d.Level == "confirm" && !agent.CanConfirm() {
		d.Level = "readonly"
		d.Reason = agent.Label + " cannot ask before acting"
	}
	return d
}
