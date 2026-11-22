package agents

import (
	"reflect"
	"strings"
	"testing"
)

// The install commands are the kit's (agents.py `install`), word for word.
func TestInstallCommandsMatchTheKit(t *testing.T) {
	want := map[string]struct {
		cmd   string
		needs []string
	}{
		"claude":   {"npm install -g @anthropic-ai/claude-code", []string{"npm"}},
		"codex":    {"npm install -g @openai/codex", []string{"npm"}},
		"copilot":  {"npm install -g @github/copilot", []string{"npm"}},
		"agy":      {"curl -fsSL https://antigravity.google/cli/install.sh | bash", []string{"curl", "bash"}},
		"muse":     {"curl -fsSL https://dev.meta.ai/install.sh | bash", []string{"curl", "bash"}},
		"hermes":   {"curl -fsSL https://hermes-agent.nousresearch.com/install.sh | bash", []string{"curl", "bash"}},
		"openclaw": {"curl -fsSL https://openclaw.ai/install.sh | bash", []string{"curl", "bash"}},
	}
	if len(All) != len(want) {
		t.Fatalf("%d agents, table has %d", len(All), len(want))
	}
	for name, w := range want {
		s := All[name]
		if got := s.InstallCommand(); got != w.cmd {
			t.Errorf("%s: install %q, want %q", name, got, w.cmd)
		}
		if !reflect.DeepEqual(s.InstallNeeds, w.needs) {
			t.Errorf("%s: needs %v, want %v", name, s.InstallNeeds, w.needs)
		}
		if !strings.Contains(s.InstallHint, w.cmd) {
			t.Errorf("%s: hint %q does not show the install command", name, s.InstallHint)
		}
		for _, stage := range s.Install {
			for _, arg := range stage {
				if strings.ContainsAny(arg, "|;&$` ") {
					t.Errorf("%s: stage argument %q looks like shell", name, arg)
				}
			}
		}
	}
	if got := All["openclaw"].AfterInstall; !reflect.DeepEqual(got, []string{"openclaw", "onboard", "--install-daemon"}) {
		t.Errorf("openclaw after-install %v", got)
	}
}

// LoginArgv follows the kit's checks.login_argv.
func TestLoginArgv(t *testing.T) {
	cases := map[string][]string{
		"muse":     {"muse"},
		"claude":   {"claude", "auth", "login"},
		"agy":      {"agy", "acp", "--login"},
		"openclaw": nil, // has a <placeholder>
	}
	for name, want := range cases {
		if got := All[name].LoginArgv(); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
}
