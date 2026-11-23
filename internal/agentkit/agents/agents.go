package agents

import (
	"fmt"
	"runtime"
	"strings"
)

type Spec struct {
	Name            string
	Label           string
	Binary          string
	OneShotArgs     []string
	PromptVia       string
	Parser          string
	Relogin         string
	Warm            string
	ACPArgs         []string
	ACPMCP          bool
	ACPSetModel     bool
	EffortArgs      []string
	ReadonlyArgs    []string
	FullArgs        []string
	ModelFlag       string
	ModelEnv        string
	ProviderFlag    string
	IgnoreRulesArgs []string
	IgnoreRulesWarm bool
	LoginCheck      []string
	LoginOKCodes    []int
	IgnoreRulesEnv  map[string]string
	InstallHint     string
	// Install is the vendor's official install command as pipeline stages
	// (curl … | bash is two stages), run without a shell. Empty: no installer.
	Install      [][]string
	InstallNeeds []string // programs the install command needs (npm, or curl and bash)
	AfterInstall []string // first-run setup offered right after installing
}

var All = map[string]Spec{
	"claude":   {Name: "claude", Label: "Claude Code", Binary: "claude", OneShotArgs: []string{"-p", "--output-format", "stream-json", "--include-partial-messages", "--verbose", "--no-session-persistence"}, PromptVia: "stdin", Parser: "claude_stream_json", Relogin: "claude auth login", InstallHint: "Install Claude Code: npm install -g @anthropic-ai/claude-code", Warm: "claude_stream_json", ACPArgs: []string{}, ACPMCP: true, ACPSetModel: false, EffortArgs: []string{"--effort", "{effort}"}, ReadonlyArgs: []string{"--tools=Read,Grep,Glob", "--permission-mode", "dontAsk"}, FullArgs: []string{"--tools=default", "--permission-mode", "acceptEdits", "--allowedTools=Bash"}, ModelFlag: "--model", IgnoreRulesArgs: []string{"--setting-sources", "user"}, IgnoreRulesWarm: true, IgnoreRulesEnv: map[string]string{}, LoginCheck: []string{"auth", "status"}, LoginOKCodes: []int{0}, Install: [][]string{{"npm", "install", "-g", "@anthropic-ai/claude-code"}}, InstallNeeds: []string{"npm"}},
	"codex":    {Name: "codex", Label: "Codex CLI", Binary: "codex", OneShotArgs: []string{"exec", "--json", "--ephemeral", "--skip-git-repo-check", "-"}, PromptVia: "stdin", Parser: "codex_jsonl", Relogin: "codex login", InstallHint: "Install Codex CLI: npm install -g @openai/codex", Warm: "codex_app_server", ACPArgs: []string{}, ACPMCP: true, ACPSetModel: false, EffortArgs: []string{"-c", "model_reasoning_effort={effort}"}, ReadonlyArgs: []string{"--sandbox", "read-only"}, FullArgs: []string{"--sandbox", "workspace-write"}, ModelFlag: "-m", IgnoreRulesArgs: []string{"-c", "project_doc_max_bytes=0"}, IgnoreRulesWarm: true, IgnoreRulesEnv: map[string]string{}, LoginCheck: []string{"login", "status"}, LoginOKCodes: []int{0}, Install: [][]string{{"npm", "install", "-g", "@openai/codex"}}, InstallNeeds: []string{"npm"}},
	"copilot":  {Name: "copilot", Label: "GitHub Copilot CLI", Binary: "copilot", OneShotArgs: []string{"-p", "{prompt}", "--no-color", "--no-ask-user", "-s"}, PromptVia: "arg", Parser: "text", Relogin: "copilot login", InstallHint: "Install GitHub Copilot CLI: npm install -g @github/copilot", Warm: "acp", ACPArgs: []string{"--acp", "--stdio"}, ACPMCP: true, ACPSetModel: false, EffortArgs: []string{"--effort", "{effort}"}, ReadonlyArgs: []string{"--deny-tool=shell", "--deny-tool=write"}, FullArgs: []string{"--allow-tool=shell,write"}, ModelFlag: "--model", IgnoreRulesArgs: []string{}, IgnoreRulesWarm: false, IgnoreRulesEnv: map[string]string{}, LoginOKCodes: []int{0}, Install: [][]string{{"npm", "install", "-g", "@github/copilot"}}, InstallNeeds: []string{"npm"}},
	"agy":      {Name: "agy", Label: "Antigravity CLI", Binary: "agy", OneShotArgs: []string{"-p", "{prompt}", "--output-format", "stream-json"}, PromptVia: "arg", Parser: "agy_stream_json", Relogin: "agy acp --login", InstallHint: "Install the Antigravity CLI: curl -fsSL https://antigravity.google/cli/install.sh | bash", Warm: "acp", ACPArgs: []string{"acp"}, ACPMCP: true, ACPSetModel: true, EffortArgs: []string{"--effort", "{effort}"}, ReadonlyArgs: []string{}, FullArgs: []string{"--mode=accept-edits"}, ModelFlag: "--model", IgnoreRulesArgs: []string{}, IgnoreRulesWarm: false, IgnoreRulesEnv: map[string]string{}, LoginOKCodes: []int{0}, Install: [][]string{{"curl", "-fsSL", "https://antigravity.google/cli/install.sh"}, {"bash"}}, InstallNeeds: []string{"curl", "bash"}},
	"muse":     {Name: "muse", Label: "Muse Code", Binary: "muse", OneShotArgs: []string{"exec", "{prompt}"}, PromptVia: "arg", Parser: "text", Relogin: "muse   (sign in when prompted)", InstallHint: "Install Muse Code: curl -fsSL https://dev.meta.ai/install.sh | bash", Warm: "acp", ACPArgs: []string{"acp"}, ACPMCP: true, ACPSetModel: false, EffortArgs: []string{}, ReadonlyArgs: []string{}, FullArgs: []string{"--disable-approval"}, IgnoreRulesArgs: []string{}, IgnoreRulesWarm: false, IgnoreRulesEnv: map[string]string{}, LoginOKCodes: []int{0}, Install: [][]string{{"curl", "-fsSL", "https://dev.meta.ai/install.sh"}, {"bash"}}, InstallNeeds: []string{"curl", "bash"}},
	"hermes":   {Name: "hermes", Label: "Hermes Agent", Binary: "hermes", OneShotArgs: []string{"-z", "{prompt}"}, PromptVia: "arg", Parser: "text", Relogin: "hermes setup", InstallHint: "Install Hermes Agent: curl -fsSL https://hermes-agent.nousresearch.com/install.sh | bash", Warm: "acp", ACPArgs: []string{"acp"}, ACPMCP: true, ACPSetModel: false, EffortArgs: []string{"--reasoning", "{effort}"}, ReadonlyArgs: []string{"--safe-mode", "-t", "web,vision,session_search"}, FullArgs: []string{}, ModelFlag: "-m", ModelEnv: "HERMES_INFERENCE_MODEL", ProviderFlag: "--provider", IgnoreRulesArgs: []string{"--ignore-rules"}, IgnoreRulesWarm: false, IgnoreRulesEnv: map[string]string{"HERMES_IGNORE_RULES": "1"}, LoginOKCodes: []int{0}, Install: [][]string{{"curl", "-fsSL", "https://hermes-agent.nousresearch.com/install.sh"}, {"bash"}}, InstallNeeds: []string{"curl", "bash"}},
	"openclaw": {Name: "openclaw", Label: "OpenClaw", Binary: "openclaw", OneShotArgs: []string{"agent", "--agent", "main", "-m", "{prompt}"}, PromptVia: "arg", Parser: "text", Relogin: "openclaw models auth login --provider <your provider>", InstallHint: "Install OpenClaw: curl -fsSL https://openclaw.ai/install.sh | bash, then run: openclaw onboard --install-daemon", Warm: "acp", ACPArgs: []string{"acp"}, ACPMCP: false, ACPSetModel: false, EffortArgs: []string{"--thinking", "{effort}"}, ReadonlyArgs: []string{}, FullArgs: []string{}, ModelFlag: "--model", IgnoreRulesArgs: []string{}, IgnoreRulesWarm: false, IgnoreRulesEnv: map[string]string{}, LoginCheck: []string{"models", "status", "--json", "--check"}, LoginOKCodes: []int{0, 2}, Install: [][]string{{"curl", "-fsSL", "https://openclaw.ai/install.sh"}, {"bash"}}, InstallNeeds: []string{"curl", "bash"}, AfterInstall: []string{"openclaw", "onboard", "--install-daemon"}},
}

// InstallCommand is the install command as the vendor documents it, for showing.
func (s Spec) InstallCommand() string {
	stages := make([]string, 0, len(s.Install))
	for _, st := range s.Install {
		stages = append(stages, strings.Join(st, " "))
	}
	return strings.Join(stages, " | ")
}

// LoginArgv is the sign-in command as argv when it can be run as is: the part of
// Relogin before any "(" note, and nil when it has a <placeholder>.
func (s Spec) LoginArgv() []string {
	text := strings.TrimSpace(strings.SplitN(s.Relogin, "(", 2)[0])
	if text == "" || strings.Contains(text, "<") {
		return nil
	}
	argv, err := SplitCommand(text)
	if err != nil {
		return nil
	}
	return argv
}

func (s Spec) CanConfirm() bool {
	return s.Warm == "acp" || s.Warm == "claude_stream_json" || s.Warm == "codex_app_server"
}
func SplitProvider(model string) (string, string) {
	h, r, ok := strings.Cut(model, ":")
	if ok && h != "" && r != "" && !strings.Contains(h, "/") {
		return h, r
	}
	return "", model
}
func (s Spec) ModelArgs(model string) []string {
	if model == "" || s.ModelFlag == "" {
		return nil
	}
	args := []string{}
	if s.ProviderFlag != "" {
		p, n := SplitProvider(model)
		model = n
		if p != "" {
			args = append(args, s.ProviderFlag, p)
		}
	}
	return append(args, s.ModelFlag, model)
}
func (s Spec) EffortFlags(e string) []string {
	if e == "" {
		return nil
	}
	args := []string{}
	for _, a := range s.EffortArgs {
		args = append(args, strings.ReplaceAll(a, "{effort}", e))
	}
	return args
}

// SplitCommand parses shell quoting without invoking a shell or expanding variables.
// On Windows a backslash is the path separator (C:\tools\agent.exe), so there it
// escapes only a double quote and is otherwise kept as typed.
func SplitCommand(command string) ([]string, error) {
	return splitCommand(command, runtime.GOOS == "windows")
}

func splitCommand(command string, windows bool) ([]string, error) {
	var args []string
	var b strings.Builder
	var quote rune
	escaped, word := false, false
	runes := []rune(command)
	for i, r := range runes {
		if escaped {
			b.WriteRune(r)
			escaped = false
			word = true
			continue
		}
		if r == '\\' && windows && (i+1 == len(runes) || runes[i+1] != '"') {
			b.WriteRune(r)
			word = true
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			word = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
			word = true
		case ' ', '\t', '\n':
			if word {
				args = append(args, b.String())
				b.Reset()
				word = false
			}
		default:
			b.WriteRune(r)
			word = true
		}
	}
	if escaped || quote != 0 {
		return nil, fmt.Errorf("unterminated quote or escape in command")
	}
	if word {
		args = append(args, b.String())
	}
	return args, nil
}
func Custom(command string) (Spec, error) {
	parts, err := SplitCommand(command)
	if err != nil {
		return Spec{}, err
	}
	if len(parts) == 0 {
		return Spec{}, fmt.Errorf("--command is empty")
	}
	via := "stdin"
	for _, p := range parts[1:] {
		if p == "{prompt}" {
			via = "arg"
		}
	}
	return Spec{Name: "custom", Label: parts[0], Binary: parts[0], OneShotArgs: parts[1:], PromptVia: via, Parser: "text", ACPMCP: true}, nil
}
