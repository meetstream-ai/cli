# [[.Title]]

Sends a MeetStream bot to a meeting, prints each status change, then prints the transcript
([[.Transcription]] transcription). Node 18 or later, no packages.

```bash
node send_bot.mjs https://meet.google.com/abc-defg-hij
```

## Test with no live bot

```bash
meetstream mock &                      # fake API on 127.0.0.1:4010
MEETSTREAM_API_URL=http://127.0.0.1:4010/api/v1 node send_bot.mjs https://meet.google.com/abc-defg-hij
```

## On a server

Your laptop needs no MeetStream key in `.env`: the app calls MeetStream through the `meetstream` CLI
with your sign-in. A server has no CLI sign-in, so add the key there: `meetstream env --with-key`
(or set `MEETSTREAM_API_KEY` in the server's environment). The same code then calls the API directly.
