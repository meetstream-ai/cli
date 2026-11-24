"""What to do for each event. Branch on bot_event, not bot_status."""

ENDINGS = {
    "bot.stopped": "clean exit",
    "bot.kicked": "removed by a participant",
    "bot.notallowed": "not admitted (waiting room timeout)",
    "bot.denied": "the host refused entry",
    "bot.failed": "unexpected error",
}


def handle(e):
    name, bot = e.get("event"), e.get("bot_id")
    if not name and e.get("transcript"):
        print(f"[live] {e.get('speakerName', 'Unknown')}: {e['transcript']}", flush=True)
    elif name == "bot.stopped":
        print(f"[{bot}] ended: {ENDINGS.get(e.get('bot_event'), e.get('bot_event'))}", flush=True)
    elif name == "transcription.processed":
        print(f"[{bot}] transcript ready: fetch it with GET /bots/{bot}/detail -> transcript_id", flush=True)
    elif name == "transcription.failed":
        print(f"[{bot}] transcript failed: {e.get('message')}", flush=True)
    elif name == "bot.done":
        print(f"[{bot}] all post-call work finished", flush=True)
    else:
        print(f"[{bot}] {name}", flush=True)
