# [[.Title]]

Made with `meetstream init [[.UseCase]] --lang [[.Lang]]`. Guidance for coding agents working here:

- MeetStream API: https://docs.meetstream.ai (fetch any page with `.md` appended). CLI guide: `meetstream skill`.
- Run the CLI with `--json --no-input`. Exit codes: 0 ok, 1 API error, 2 usage or not signed in, 3 not ready, 4 transcript failed.
- Never print or ask for API keys in chat. On a laptop there is no MeetStream key in `.env`: the app calls MeetStream through `meetstream api` with the CLI sign-in. For a server: `meetstream env --with-key`. `meetstream env --check` lists other keys that are empty.
- Test with no live bot: `meetstream mock` and `MEETSTREAM_API_URL=http://127.0.0.1:4010/api/v1`; send sample events with `meetstream trigger <event>`.
- Webhooks: branch on `bot_event`, not `bot_status`; `transcript_id` is not in any webhook (read `GET /bots/{id}/detail`).
- Zoom always needs setup: bots join through the customer's own Zoom app.
