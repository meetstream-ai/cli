# [[.Title]]

Sends a MeetStream bot to a meeting, prints each status change, then prints the transcript
([[.Transcription]] transcription). Python 3.9 or later, no packages.

```bash
python send_bot.py https://meet.google.com/abc-defg-hij
```

## Test with no live bot

```bash
meetstream mock &                      # fake API on 127.0.0.1:4010
MEETSTREAM_API_URL=http://127.0.0.1:4010/api/v1 python send_bot.py https://meet.google.com/abc-defg-hij
```
