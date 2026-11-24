---
name: meetstream
description: Use the meetstream CLI to send meeting bots to Zoom, Google Meet and Microsoft Teams, read transcripts and recordings, and manage MIA agents. Use when the user asks to join, record or transcribe a meeting with MeetStream, or to check their MeetStream setup.
---

# meetstream CLI

One binary. Install: `npx @meetstream/cli`, `uvx --from meetstream-cli meetstream` or `brew install meetstream-ai/tap/meetstream`.

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
| `meetstream doctor --json [--agent <name>] [--no-warm]` | `{"ok": bool, "version": str, "verdict": str, "checks": [{"name","ok","status": "ok"\|"warn"\|"fail","detail","fix"}]}`. API key lines first, then the agent checks (`config`, `workdir`, `agent`, `app`, `login`, `warm`, `relay`, `context`). Exit 1 when a check fails, 2 when no API key is set |
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
| `listen [-p 3333] [--host 127.0.0.1] [--path /webhook] [--forward-to <url>] [--tunnel cloudflared\|ngrok\|https://…]` | One JSON line per received event; invalid JSON becomes `{raw: str}`; Ctrl-C exits 130. With `--json`, stderr gets one line `{local_url, public_url, tunnel, forward_to}`; stdout stays one line per event. While it runs, `join` with no `-c` sends events here (tunnel URL, or the local URL when `MEETSTREAM_API_URL` is local). Missing tunnel binary: exit 3 with an install hint |
| `mcp add <claude/codex/cursor/vscode>` | `{agent, path, entry: {type: "http", url: "https://mcp.meetstream.ai/mcp"}, snippet, written: false}`; print-only |
| `mcp add <codex/cursor/vscode> --write` | `{agent, path, entry: {type: "http", url: "https://mcp.meetstream.ai/mcp"}, written: true}`; existing config backed up to `.bak`; refuses an existing backup; Claude prints its `claude mcp add` command and does not support `--write` |

`teams ...` is a hidden alias for `logins teams ...`. Teams domain creation fixes
`login_mode` to `always`; Google accepts `--login-mode` on add/update-domain
(default `always` when adding). Both accept `--name`; account add accepts
`--inactive`. Google has no account get-by-id endpoint.

MCP config writes preserve unrelated entries. JSON comments and ambiguous TOML
(multiline strings, inline/dotted MCP tables or alternate MeetStream section
spellings) require manual editing with the printed snippet. No API key is written.

## Guided flows (1.1)

| Command | JSON shape |
|---|---|
| `init --list --json` | `{use_cases: [{id, title, summary, langs, options, available}], transcription: [...], live_transcription: [...], llm: [...], email: [...]}` |
| `init <use-case> --lang <python\|node> [--transcription <id>] [--live-transcription <id>] [--llm anthropic\|openai\|none] [--email resend\|sendgrid\|smtp\|none] [--dir <path>] [--with-key] --yes --json` | `{use_case, lang, dir, path, files: [...], env: {meetstream_auth: "cli-sign-in"\|"key", filled, missing}, next: [...]}`. Writes the template, `.env` (mode 0600) and `AGENTS.md`. By default `.env` holds no MeetStream key: the app calls MeetStream through `meetstream api` with the CLI sign-in. `--with-key` writes the key and its API URL (for servers). Missing `--use-case` with `--no-input`: exit 2. Non-empty folder: exit 2 unless `--force`. Coming-soon use case: exit 3 |
| `env [--dir .] [--check] [--overwrite] [--with-key]` | `{path, meetstream_auth, set, filled, missing}`; never prints values. No MeetStream key by default; `--with-key` writes the key plus the profile API URL (last line). `--check` writes nothing and exits 3 when a key is empty |
| `api <path> [-X METHOD] [-d JSON\|-] [--envelope]` | One API call with the CLI sign-in; prints the answer body (non-2xx: exit 1, 401/403: exit 2). `--envelope`: `{"status", "body"}`, exit 0 for any HTTP answer. Path must be relative (`/bots/...`) |
| `join <url> --transcription <id>` | Same as `-t`; post-call provider (`transcription providers`). Cannot be combined with `--live-transcription` (a bot runs one provider): exit 2 |
| `join <url> --live-transcription <id>` | Maps `meetstream` → `meetstream_streaming` etc.; webhook = `--live-transcript-webhook`, else the running `listen` session, else exit 2 |
| `join <url> --follow --json` | NDJSON: `{event: "sent", bot_id}`, `{event: "status", status}` per change, then `{event: "transcript_ready", segments, next}` (exit 0), `{event: "done", transcript: "not requested"\|"live only"}` (exit 0), or exit 1 (bot not admitted, denied, failed) / exit 4 (transcript failed) |
| `transcribe <bot-id> --transcription <id>` | Same as `-p` (default still `deepgram`) |
| `transcription providers --json` | `{post_call: [{id, label, key: "none"\|"dashboard", note}], live: [{id, maps_to, ...}], defaults}` |
| `trigger <event> [--forward-to <url>] [--bot <id>] --json` | `{sent, event, bot_id, to, status, ms}`; production envelope (`bot.notallowed` is sent as `event: "bot.stopped"`, `bot_event: "bot.notallowed"`). Default target: the running `listen`. `--list` for names. Non-2xx answer: exit 1 |
| `mock [--scenario <name>] [--port 4010] [--step 2] --json` | NDJSON `{event: "ready", url, scenario}` then `{event: "log", line}`. Set `MEETSTREAM_API_URL` to `url`. Scenarios: notetaker-happy, waiting-room-timeout, transcript-failed, bot-removed, zoom-needs-setup. Loopback only; any key accepted; no bots join |
| `ai install [claude\|codex\|cursor ...] [--all] [--dry-run] --json` | `{dry_run, agents: [{agent, skill, mcp, changed, todo}]}`. Writes this SKILL.md to the agent's skills folder and the hosted MCP entry (backup to `.bak`); Claude's MCP is a printed `claude mcp add` command (`todo`) |
| `commands --json` | `{version, global_flags, exit_codes, error_shape, commands: [{path, use, short, group, flags: [{name, shorthand, type, default, usage, enum}]}]}` |

Build without live bots: `meetstream mock &`, `export MEETSTREAM_API_URL=http://127.0.0.1:4010/api/v1`, then `listen`, `join --follow` and `trigger` work end to end at no cost.

Errors (with `--json`): `{"error": {"message": str, "hint": str, "exit_code": int}}`.

## Your own agent in meetings

- `meetstream setup --mode hosted|byok --json --no-input [--ears|--brain|--voice provider:model] [--name <name>]` → `{"mode","agent_config_id","name","price_per_hour","breakdown": [{"label","price_per_hour"}], "plus_your_providers"?: true, "managed_key_sections"?: [...]}`. `hosted` runs ears, brain and voice on MeetStream keys; `byok` uses the user's own provider keys (added in the dashboard; setup never asks for keys). Prices are per hour for this user and come from the API: report them as shown, never from memory. `managed_key_sections` lists sections that had no saved key and run on MeetStream keys instead. Exit 3 when the server does not offer MeetStream keys for what was asked.
- `meetstream setup --agent claude --access readonly [--model <id>] [--effort low|medium|high] [--voice-keys meetstream|own] --no-start --json` (= `--mode local-agent`, the default) → `{"mode","agent","device_id","agent_config_id","access","workdir","model","effort","owner","ignore_rules","meeting_md","test","login","updated","price_per_hour","breakdown"}`. Without `--model`/`--effort` the CLI's own defaults are used (`""`); it never asks. Defaults come from the last setup (`setup.json`), else `~/MeetStream Agent` and `$USER` (`--workdir`, `--owner`); re-running updates the saved MIA agent (`"updated": true`) unless `--new-agent`. The test turn is skipped (`"test": "skipped"`) unless `--test`; a signed-out agent exits 2, and a server without the relay exits 3. The calendar step is skipped under `--json`/`--no-input` (or `--no-calendar`); connect it with `calendar connect`. Under `--no-input`, run `meetstream agent login` first (it opens the user's browser).
- `meetstream setup ... --dry-run --json` → `{"dry_run": true, "mode", "method": "POST"|"PUT", "agent_config_id", "body": {..., "model": {"provider": "local_agent", "model"?: str, "local_agent": {...}}}, "setup"?: {"agent","model","effort","workdir"}, "price_per_hour", "breakdown"}`; nothing is sent or saved.
- `meetstream agent bench [--agent <name>] [--compare] [--models a,b] [--parallel N] [--effort e] [--export file] --json --no-input` → `{"agent", "effort", "stopped": int, "results": [{"model","state": "done"|"failed"|"cancelled","input_tokens","output_tokens","tokens_per_hour","cost_per_hour_usd","free","seconds","first_output_ms","error"?}], "export"?: {"path","written","models_in_file"}}`. Each model is one real, short turn on the user's plan or API credits. Exit 1 when no model reported usage; exit 2 for a CLI that reports no token usage (Copilot, Muse, OpenClaw).
- `meetstream agent serve --json` runs until stopped and prints JSON-line events (`ready`, `relay_connected`, …). Start it in its own terminal; don't wait for it to exit.
- `meetstream agent status --json` → `{"signed_in": bool, "device_id", "relay_url", "store"}`.
- Then send MIA: `meetstream join <url> --agent-config <agent_config_id>`.

## Planned commands

- `bots tail [id]`: exit 2; use `meetstream listen` with a tunnel until the relay carries events.
