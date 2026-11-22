<div align="center">

# MeetStream CLI

**Meeting bots, transcripts and your own agent in the call, from one command.**

Send bots to Google Meet, Zoom and Microsoft Teams, read transcripts and recordings,<br>
and let Claude Code, Codex and other agent CLIs answer out loud in the meeting.

[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)
![Platforms](https://img.shields.io/badge/macOS%20·%20Linux%20·%20Windows-amd64%20·%20arm64-f97316)

<img src="docs/images/mia-in-a-meeting.webp" alt="A MeetStream agent answering a question out loud in a Google Meet call" width="860">

</div>

`meetstream` is one small binary. It replaces two earlier tools:
`@meetstream/cli` (Node) and `meetstream-agent-kit` (Python). Install it from
whichever package manager you already use. Every package ships the same Go binary.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/agents-dark.png">
  <img src="docs/images/agents-light.png" alt="Works with Claude Code, Codex, Hermes, Copilot CLI, Muse Code, OpenClaw, Antigravity and any CLI, in Google Meet, Zoom and Microsoft Teams" width="100%">
</picture>

<img src="docs/images/home.png" alt="The meetstream home screen: an arrow-key menu of common tasks" width="720">

## Contents

- [Install](#install)
- [Quick start](#quick-start)
- [Commands](#commands)
- [For scripts and agents](#for-scripts-and-agents)
- [Configuration](#configuration)
- [Status](#status)
- [Development](#development)

## Install

**From source on your Mac (works today):**

```bash
git clone https://github.com/meetstream-ai/cli.git meetstream-cli && cd meetstream-cli
make install            # builds and installs ~/.local/bin/meetstream
make smoke              # version, doctor, and the key in use
```

**Sign in.** `meetstream login` opens your browser. If browser sign-in isn't available, `login`
says so and takes a pasted key instead. You can also set the key directly:

```bash
meetstream auth set-key                                # hidden prompt
printf %s "$MEETSTREAM_API_KEY" | meetstream auth set-key  # from stdin
```

**From a package manager (after the first release):**

| You have | Run |
|---|---|
| Node | `npm install -g @meetstream/cli` or `npx @meetstream/cli` |
| Python | `pipx install meetstream` or `uvx meetstream` |
| Homebrew | `brew install meetstream-ai/tap/meetstream` |
| Nothing | download a binary from [Releases](../../releases) |

Each package carries the binary for your platform: macOS, Linux or Windows, on
amd64 or arm64. Nothing is downloaded at install time.

## Quick start

```bash
meetstream            # the home screen: pick what to do
meetstream doctor     # check your key, config and agent CLIs
meetstream login      # sign in with your browser
meetstream join https://meet.google.com/abc-defg-hij
```

No key yet? Create one at [app.meetstream.ai/api-keys](https://app.meetstream.ai/api-keys)
and set `MEETSTREAM_API_KEY`.

## Commands

| Command | What it does | Status |
|---|---|---|
| `meetstream` | Home screen: an arrow-key menu of the common tasks | ✅ |
| `doctor` | Checks the API key, config file and agent CLIs | ✅ |
| `auth status` / `auth logout` | Checks the key in use with the API (masked; `--offline` skips the check) / removes the stored one | ✅ |
| `docs` | Shows this README in your terminal | ✅ |
| `skill` | Prints [SKILL.md](SKILL.md) for coding agents | ✅ |
| `version` | Shows version, commit and platform | ✅ |
| `login` | Signs in with your browser and stores a named API key, which is never shown (`--with-key` reads one from stdin for CI) | ✅ |
| `join <url>` | Sends a bot to a meeting (all `create_bot` options) | ✅ |
| `bots list/status/get/timeline/remove/delete/say/show-image/hand` | Manages your bots (old `bot …` names still work) | ✅ |
| `bots tail` | Planned: live bot events over the relay; exits 2 with a local receiver hint | planned |
| `listen` | Local webhook receiver; loopback by default, `--forward` to relay elsewhere | ✅ |
| `logins teams/google` | Manages domains and signed-in accounts; passwords from stdin, environment or hidden prompt | ✅ |
| `calendar connect/list/events/schedule/unschedule/auto-join/reschedule/cancel` | Connects your OAuth app and manages calendar bots | ✅ |
| `mia apply -f config.json` | Creates or updates an agent; `--dry-run` prints the body offline | ✅ |
| `mcp add claude/codex/cursor/vscode` | Prints hosted OAuth MCP setup; `--write` merges Codex/Cursor/VS Code configs with a backup | ✅ |
| `transcript <bot-id>` | Prints the transcript | ✅ |
| `recordings <bot-id>` | Prints audio and video links | ✅ |
| `balance` | Shows your prepaid balance | ✅ |
| `mia list/get/create/update/delete/check` | Manages MIA agent configs and checks the local bridge | ✅ |
| `setup` (= `agent setup`) | Picks your agent CLI, connects this computer to MeetStream, creates the MIA agent | ✅ |
| `agent serve` | Runs your agent for MIA over the MeetStream relay (no tunnel); `--no-relay --tunnel …` to self-host | ✅ |
| `agent login` / `logout` / `status` | This computer's relay sign-in (shared with the Python kit) | ✅ |
| `agent context show|edit` | What your agent reads in meetings (MEETING.md) | ✅ |

## For scripts and agents

Every command accepts:

- `--json`: prints JSON on stdout. `listen` emits one JSON line per event; `bots status --watch` emits successive values. Human messages go to stderr.
- `--no-input`: never prompts. Where a command would ask, it exits with code 2 and a hint.
- `--no-color` (or `NO_COLOR=1`): plain text output.

Exit codes: `0` ok, `1` runtime or API error, `2` usage error or missing auth,
`3` not ready or not available, `4` transcript failed, `130` interrupted. These are the same codes as `@meetstream/cli`. With `--json`, errors are JSON too:

```json
{ "error": { "message": "not signed in", "hint": "run: meetstream login", "exit_code": 2 } }
```

Coding agents can read [SKILL.md](SKILL.md), or run `meetstream skill`.

## Configuration

| Where | What |
|---|---|
| `MEETSTREAM_API_KEY` | API key. It wins over the config file. |
| `~/.meetstream/config.json` | Stored key (`api_key`, `api_key_name`). Directory 0700, file 0600. The same file the Node CLI uses. |
| `MEETSTREAM_API_URL` | API base URL (default `https://api.meetstream.ai/api/v1`). |
| `MEETSTREAM_AUTH_URL` | Sign-in server (default `https://mcp.meetstream.ai`). |
| `MEETSTREAM_MASCOT=off` | Hides MIA. |

The CLI never prints or logs your key. Status output shows only the last four characters.

## Status

Everything in the command table marked ✅ works today. Planned next: `bots tail` over the relay,
`agent bench`, a model picker with costs, and `agent pair/list/revoke` for tunnel mode.
`meetstream` replaces `@meetstream/cli` and `meetstream-agent-kit`; both keep working during the switch.

## Development

```bash
go test ./...
go build -o dist-local/meetstream ./cmd/meetstream
goreleaser release --snapshot --clean     # all six platforms into dist/
python3 scripts/pack.py npm  0.1.0-dev       # npm launcher + 6 platform packages
python3 scripts/pack.py pypi 0.1.0-dev       # 6 platform wheels
```

Built with [Cobra](https://github.com/spf13/cobra),
[Bubble Tea](https://github.com/charmbracelet/bubbletea),
[Lip Gloss](https://github.com/charmbracelet/lipgloss) and
[Glamour](https://github.com/charmbracelet/glamour).

## License

Apache-2.0
