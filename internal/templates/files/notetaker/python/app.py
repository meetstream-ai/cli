"""AI notetaker: when a transcript is ready, summarize it and send the notes.

Run:  python app.py      (listens on 127.0.0.1:8000/webhook)
Then: meetstream listen --forward-to http://127.0.0.1:8000/webhook   (add --tunnel for real bots)
      meetstream join <meeting-url> --transcription [[.Transcription]]
"""
import json
import os
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from deliver import send_notes
from meetstream_api import fetch_transcript, safe_id, transcript_lines
from summarize import summarize

PORT = int(os.environ.get("PORT", "8000"))
_seen = set()
_lock = threading.Lock()


def on_transcript(bot_id):
    try:
        lines = transcript_lines(fetch_transcript(bot_id))
    except Exception as exc:  # noqa: BLE001 - report and keep serving
        print(f"[{bot_id}] could not fetch the transcript: {exc}", flush=True)
        return
    notes = summarize("\n".join(lines))
    send_notes(bot_id, notes)


# Anyone who can reach this port can post events. Keep it on 127.0.0.1 and put
# "meetstream listen" in front of it; MeetStream webhooks are not signed today.
class Hook(BaseHTTPRequestHandler):
    def do_POST(self):
        raw = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        self.send_response(200)
        self.end_headers()
        try:
            e = json.loads(raw)
        except ValueError:
            return
        key = (e.get("bot_id"), e.get("event"), e.get("bot_event"), e.get("timestamp"))
        with _lock:
            if key in _seen:
                return
            _seen.add(key)
        name, bot = e.get("event"), e.get("bot_id")
        try:
            safe_id(bot)
        except ValueError:
            print(f"ignored an event with a bad bot_id: {bot!r}", flush=True)
            return
        if name == "transcription.processed":
            print(f"[{bot}] transcript ready: summarizing", flush=True)
            threading.Thread(target=on_transcript, args=(bot,), daemon=True).start()
        elif name == "transcription.failed":
            print(f"[{bot}] transcript failed: {e.get('message')}", flush=True)
        elif name == "bot.stopped":
            print(f"[{bot}] meeting ended ({e.get('bot_event')})", flush=True)

    def log_message(self, *args):
        pass


if __name__ == "__main__":
    print(f"notetaker on http://127.0.0.1:{PORT}/webhook")
    ThreadingHTTPServer(("127.0.0.1", PORT), Hook).serve_forever()
