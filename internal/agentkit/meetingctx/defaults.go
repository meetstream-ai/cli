package meetingctx

const Default = "# Meeting instructions\n\nThese go to your agent in every MeetStream meeting. Keep them short: every\nline is read on every turn.\n\n- Answer in one or two spoken sentences unless asked for more.\n- If you are not sure, say so instead of guessing.\n"

var project = map[string][]string{"claude": {"CLAUDE.md", "CLAUDE.local.md"}, "codex": {"AGENTS.md"}, "agy": {"GEMINI.md", "AGENTS.md"}, "hermes": {"AGENTS.md", "CLAUDE.md", ".cursorrules"}, "copilot": {"AGENTS.md", "CLAUDE.md", "GEMINI.md", ".github/copilot-instructions.md"}}
var global = map[string][]string{"claude": {".claude/CLAUDE.md"}, "codex": {".codex/AGENTS.md"}, "agy": {".gemini/GEMINI.md"}, "hermes": {".hermes/SOUL.md", ".hermes/memories/MEMORY.md", ".hermes/memories/USER.md"}}
var anyProject = []string{"AGENTS.md", "CLAUDE.md", "CLAUDE.local.md", "GEMINI.md", ".cursorrules", ".github/copilot-instructions.md"}
