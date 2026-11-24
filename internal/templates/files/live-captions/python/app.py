"""Print live captions while the meeting runs.

Run:  python app.py
Then: meetstream listen --forward-to http://127.0.0.1:8000/webhook --tunnel cloudflared
      meetstream join <meeting-url> --live-transcription [[.Live]]
"""
import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(os.environ.get("PORT", "8000"))


class Hook(BaseHTTPRequestHandler):
    def do_POST(self):
        raw = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        self.send_response(200)
        self.end_headers()
        try:
            e = json.loads(raw)
        except ValueError:
            return
        if e.get("transcript") and not e.get("event"):
            print(f"\033[1m{e.get('speakerName', 'Unknown')}\033[0m: {e['transcript']}", flush=True)
        elif e.get("event") == "bot.stopped":
            print(f"-- meeting ended ({e.get('bot_event')}) --", flush=True)

    def log_message(self, *args):
        pass


if __name__ == "__main__":
    print(f"captions on http://127.0.0.1:{PORT}/webhook")
    ThreadingHTTPServer(("127.0.0.1", PORT), Hook).serve_forever()
