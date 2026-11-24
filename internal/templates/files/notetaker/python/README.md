# [[.Title]]

An AI notetaker: a MeetStream bot records the meeting, [[.Transcription]] makes the transcript,
[[.LLM]] writes the notes, [[.Email]] sends them. Python 3.9+, no packages.

```bash
meetstream env --check                                          # which keys are still empty
python app.py                                                   # terminal 1
meetstream listen --forward-to http://127.0.0.1:8000/webhook    # terminal 2 (add --tunnel cloudflared for real bots)
meetstream join <meeting-url> --transcription [[.Transcription]] --follow   # terminal 3
```

## Test with no live bot

```bash
meetstream mock &                                 # fake API: no bots join, nothing is charged
export MEETSTREAM_API_URL=http://127.0.0.1:4010/api/v1
python app.py & meetstream listen --forward-to http://127.0.0.1:8000/webhook &
meetstream join https://meet.google.com/abc-defg-hij --transcription [[.Transcription]] --follow
```

Zoom always needs setup: bots join Zoom through your own Zoom app, set up in MeetStream first.
