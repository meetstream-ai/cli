# [[.Title]]

Live captions on your laptop with [[.Live]] live transcription (Python 3.9+, no packages).

```bash
python app.py                                                                       # terminal 1
meetstream listen --forward-to http://127.0.0.1:8000/webhook --tunnel cloudflared   # terminal 2
meetstream join <meeting-url> --live-transcription [[.Live]]                        # terminal 3
```

A live provider makes no post-call transcript. For one after the meeting:
`meetstream transcribe <bot-id> --transcription meetstream`.
