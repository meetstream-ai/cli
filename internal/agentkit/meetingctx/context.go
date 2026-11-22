package meetingctx

import (
	"fmt"
	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"github.com/meetstream-ai/cli/internal/agentkit/prompts"
	"os"
	"path/filepath"
	"strings"
)

func Expand(s string) string {
	if s == "~" || strings.HasPrefix(s, "~/") {
		h, _ := os.UserHomeDir()
		return filepath.Join(h, strings.TrimPrefix(s, "~/"))
	}
	return s
}
func Workspace() string {
	if v := os.Getenv("MEETSTREAM_AGENT_KIT_WORKSPACE"); v != "" {
		return Expand(v)
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "MeetStream Agent")
}
func File() string { return filepath.Join(Workspace(), "MEETING.md") }
func Ensure() (string, error) {
	p := File()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return p, err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	_, err = f.WriteString(Default)
	e := f.Close()
	if err == nil {
		err = e
	}
	return p, err
}
func Notes() string {
	b, err := os.ReadFile(File())
	if err != nil {
		return ""
	}
	return prompts.Truncate(strings.TrimSpace(string(b)), 4000)
}

type Item struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Loaded bool   `json:"loaded"`
	Note   string `json:"note"`
}

func Effective(a agents.Spec, workdir, home string, ignore, useMeeting bool) []Item {
	out := []Item{}
	add := func(name, p string, loaded bool, note string, capSize bool) {
		if s, err := os.Stat(p); err == nil && s.Mode().IsRegular() {
			size := s.Size()
			if capSize && size > 4000 {
				size = 4000
			}
			out = append(out, Item{name, p, size, loaded, note})
		}
	}
	files, ok := project[a.Name]
	if !ok {
		files = anyProject
	}
	skip := ignore && len(a.IgnoreRulesArgs) > 0 && a.IgnoreRulesWarm
	note := ""
	if skip {
		note = "skipped"
	} else if ignore {
		note = "this agent cannot skip it"
	}
	for _, f := range files {
		add(f, filepath.Join(workdir, f), !skip, note, false)
	}
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	for _, f := range global[a.Name] {
		add("~/"+f, filepath.Join(home, f), true, "your agent's own memory", false)
	}
	add("MEETING.md", File(), useMeeting, "yours", true)
	return out
}
func Tokens(items []Item) int64 {
	var n int64
	for _, i := range items {
		if i.Loaded {
			n += i.Size / 4
		}
	}
	return n
}

// Rule is a project rules file the agent CLI reads from the work folder on every turn.
type Rule struct {
	Name string
	Size int64
}

// ProjectRules lists the project rules files in folder (the kit's PROJECT_RULE_FILES).
func ProjectRules(folder string) []Rule {
	var out []Rule
	for _, name := range anyProject {
		if s, err := os.Stat(filepath.Join(folder, name)); err == nil && s.Mode().IsRegular() {
			out = append(out, Rule{name, s.Size()})
		}
	}
	return out
}

// Summary is the one-line context label: "meeting only · 0.4k tokens/turn".
func Summary(a agents.Spec, workdir string, ignore, useMeeting bool) string {
	items := Effective(a, workdir, "", ignore, useMeeting)
	return fmt.Sprintf("%s · %.1fk tokens/turn", Mode(workdir, ignore), float64(Tokens(items))/1000)
}

// Table is one row per context file: loaded or not, size, tokens and note.
func Table(items []Item) []string {
	if len(items) == 0 {
		return []string{"(nothing extra: only MIA's rules and the meeting)"}
	}
	width := 0
	for _, i := range items {
		width = max(width, len(i.Name))
	}
	var rows []string
	for _, i := range items {
		mark := "·"
		if i.Loaded {
			mark = "✓"
		}
		note := ""
		if i.Note != "" {
			note = "  " + i.Note
		}
		rows = append(rows, fmt.Sprintf("%s %-*s  %5.1f KB  ≈ %6d tokens%s", mark, width, i.Name, float64(i.Size)/1024, i.Size/4, note))
	}
	return rows
}

func Mode(workdir string, ignore bool) string {
	w, _ := filepath.Abs(workdir)
	m, _ := filepath.Abs(Workspace())
	if w == m {
		return "meeting only"
	}
	if ignore {
		return "project, rules ignored"
	}
	return "project rules included"
}
