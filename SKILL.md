---
name: meetstream
description: Use the meetstream CLI to send meeting bots to Zoom, Google Meet and Microsoft Teams, read transcripts and recordings, and manage MIA agents. Use when the user asks to join, record or transcribe a meeting with MeetStream, or to check their MeetStream setup.
---

# meetstream CLI

One binary. Install: `npx @meetstream/cli`, `uvx meetstream` or `brew install meetstream-ai/tap/meetstream`.

## Rules for agents

- Always pass `--json --no-input`. Parse stdout; ignore stderr.
- Exit codes: `0` ok, `1` runtime/API error, `2` usage error or missing auth,
  `3` not ready yet (retry later), `4` transcript failed.
- On exit 2 with `"not signed in"`, run `meetstream login --json` (it opens the
  user's browser; wait for it) or ask the user to set `MEETSTREAM_API_KEY`. Never ask for the key in chat.
- Never print, log or echo the API key.

## Commands available now

| Command | JSON shape |
|---|---|
| `meetstream doctor --json` | `{"ok": bool, "version": str, "checks": [{"name","ok","detail","fix"}]}` |
| `meetstream login --json` | Opens the browser; the user signs in. `{"signed_in": true, "key": "ms_…abcd", "key_name", "config_path", "validated": bool, "bots_on_account", "env_overrides": bool}` |
| `meetstream auth status --json` | `{"signed_in": bool, "source": "env"\|"file"\|"", "key": "ms_…abcd", "key_name": str, "config_path": str, "valid": bool, "bots_on_account": str}`; `--offline` skips the API check |
| `meetstream auth logout --json` | `{"removed": bool}` |
| `meetstream version --json` | `{"version","commit","date","os","arch"}` |
| `meetstream docs --json` | `{"format":"markdown","content":str,"url":str}` |

All API commands below pass through the API JSON; fields shown are examples used
by the CLI, not a complete API response schema. Empty API responses emit `null`.
Append `--json --no-input` to commands in this table.

| M3 command | JSON shape |
|---|---|
| `join <meeting-url>` (hidden `bot create`) | API bot creation response; same response for an idempotent replay |
| `bots list` (hidden `bot list`) | `{bots: [...], hasNextPage?: bool, nextCursor?: str}` |
| `bots status <id> [--watch]` | API status object, e.g. `{status: str}`; watch emits successive JSON values |
| `bots get <id> --part detail/audio/video/streams/audio-streams/chats/screenshots/participants` | API object or array for that part; hidden `bot detail/audio/video/streams/audio-streams/chats/screenshots/participants <id>` aliases |
| `bots timeline <id> [--download]` (hidden `bot timeline`) | API timeline array or `{speaker_timeline_url, size_bytes, ...}`; download emits fetched JSON |
| `bots remove <id>` / `bots delete <id> --yes` | API result; declined deletion emits `{deleted: false}`; hidden `bot remove/delete` aliases |
| `bots say <id> <message...>` / `bots show-image <id> <url>` / `bots hand <id> <up/down>` | API action result; hidden `bot send-message/send-image/raise-hand/lower-hand` aliases |
| `bots summary <id>` (hidden `bot summary`) | Error envelope, exit 2; fetch `transcript` and summarize it |
| `transcript <bot-id> [--wait] [--raw]` | Transcript JSON (array, string or provider object); exit 3 if pending; failure: `{error: "transcript_failed", reason, transcript_status, transcript_id}`, exit 4 |
| `transcriptions <bot-id>` / `transcribe <bot-id>` | API transcription list / start result |
| `recordings <bot-id>` | `{bot_id, audio, video, streams?, audio_streams?}` |
| `balance` | API balance object, e.g. `{balance_usd, currency, customer_type}`; unavailable: `{error: "balance_unavailable", status: 404, message, billing_url}`, exit 3 |
| `mia list/get/create/update/delete/check` (alias `agents`) | API config/list/delete/check response; `check` uses `{local_agent?: {status, latency_ms?}}`, exit 3 unless status is `ok`; declined deletion: `{deleted: false}` |
| `mia apply -f config.json` (alias `agents apply`) | API create/update result containing `agent_config_id` or `id`; presence of file `id`/`agent_config_id` selects PUT; otherwise POST |
| `mia apply -f config.json --dry-run` | Request body object without auth or a request; `id` normalized to `agent_config_id`. YAML awaits an offline `gopkg.in/yaml.v3` dependency |
| `logins teams/google domains` | API `{domains: [...]}` |
| `logins teams/google domain <domain>` | API domain object with `logins` array |
| `logins teams/google add-domain/update-domain/remove-domain <domain>` | API domain result; `--yes` required for removal in no-input mode; declined deletion: `{deleted: false}` |
| `logins teams/google list --domain <domain>` | API `{logins: [...]}` |
| `logins teams get <login-id>` | API account object, e.g. `{login_id, domain, email, is_active, ...}` |
| `logins teams add --domain <domain> --email <email> --password-stdin` / `set-password <login-id> --password-stdin` | API account result, e.g. `{login_id, ...}`; passwords never emitted. Alternative input: `MEETSTREAM_LOGIN_PASSWORD` or hidden terminal prompt (without `--no-input`) |
| `logins google add --domain <domain> --email <email> --key <path> --cert <path>` / `set-cert <login-id> --domain <domain> --key <path> --cert <path>` | API account result with submitted PEM credentials redacted |
| `logins teams/google enable/disable/remove <login-id>` | API account result; Google requires `--domain`; removal requires `--yes` in no-input mode; declined deletion: `{deleted: false}` |
| `calendar connect --provider google [--join-rule accepted/all] --no-input [--open]` | Waits (up to 10 min) for the user to sign in at the link printed on stderr, then `{provider, session_id, status: "connected", email}`. `--provider` is required with `--no-input`; the browser opens only with `--open`. `accepted` (default) joins meetings the user hosts or accepted; `all` joins every meeting with a link. Auto-join is turned on; `calendar auto-join off` turns it off. Sign-in failed, expired or timed out: error envelope, exit 1. Server without one-click connect: exit 3 (use `--own-app`) |
| `calendar connect --own-app --client-id <id> --client-secret <secret> --refresh-token <token>` | API connection result with submitted credentials redacted; legacy refresh-token flow also works without `--own-app` |
| `calendar list/events` | API calendar/event JSON |
| `calendar schedule/unschedule <event-id>` | API scheduling result |
| `calendar auto-join <on/off/enable/disable> [-n <name>] [--video]` | API auto-scheduling result |
| `calendar reschedule <bot-id> <iso8601>` / `cancel <bot-id>` | API scheduled-bot result |
| `listen [-p 3333] [--host 127.0.0.1] [--path /webhook] [--forward <url>]` | One JSON line per received event; invalid JSON becomes `{raw: str}`; Ctrl-C exits 130 |
| `mcp add <claude/codex/cursor/vscode>` | `{agent, path, entry: {type: "http", url: "https://mcp.meetstream.ai/mcp"}, snippet, written: false}`; print-only |
| `mcp add <codex/cursor/vscode> --write` | `{agent, path, entry: {type: "http", url: "https://mcp.meetstream.ai/mcp"}, written: true}`; existing config backed up to `.bak`; refuses an existing backup; Claude prints its `claude mcp add` command and does not support `--write` |

`teams ...` is a hidden alias for `logins teams ...`. Teams domain creation fixes
`login_mode` to `always`; Google accepts `--login-mode` on add/update-domain
(default `always` when adding). Both accept `--name`; account add accepts
`--inactive`. Google has no account get-by-id endpoint.

MCP config writes preserve unrelated entries. JSON comments and ambiguous TOML
(multiline strings, inline/dotted MCP tables or alternate MeetStream section
spellings) require manual editing with the printed snippet. No API key is written.

Errors (with `--json`): `{"error": {"message": str, "hint": str, "exit_code": int}}`.

## Your own agent in meetings

- `meetstream setup --agent claude --access readonly --no-start --json` → `{"agent","device_id","agent_config_id","access","workdir"}`. Under `--no-input`, run `meetstream agent login` first (it opens the user's browser).
- `meetstream agent serve --json` runs until stopped and prints JSON-line events (`ready`, `relay_connected`, …). Start it in its own terminal; don't wait for it to exit.
- `meetstream agent status --json` → `{"signed_in": bool, "device_id", "relay_url", "store"}`.
- Then send MIA: `meetstream join <url> --agent-config <agent_config_id>`.

## Planned commands

- `bots tail [id]`: exit 2; use `meetstream listen` with a tunnel until the relay carries events.
