# [[.Title]]

A handler for every MeetStream bot event (Python 3.9+, no packages). It answers 200 at once,
de-duplicates retries, and branches on `bot_event` (the reason a bot stopped).

```bash
python app.py                                                   # terminal 1
meetstream listen --forward-to http://127.0.0.1:8000/webhook    # terminal 2 (add --tunnel cloudflared for real bots)
meetstream trigger bot.notallowed                               # terminal 3: a sample event
```
