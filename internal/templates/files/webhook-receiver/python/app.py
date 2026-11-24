"""Receive every MeetStream bot event and branch on it.

Run:  python app.py            (listens on 127.0.0.1:8000/webhook)
Then: meetstream listen --forward-to http://127.0.0.1:8000/webhook
"""
import json
import os
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import handlers

PORT = int(os.environ.get("PORT", "8000"))
_seen = set()
_lock = threading.Lock()


class Hook(BaseHTTPRequestHandler):
    def do_POST(self):
        if self.path != "/webhook":
            self.send_response(404)
            self.end_headers()
            return
        raw = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        # Answer 2xx fast, work after.
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"status":"ok"}')
        try:
            event = json.loads(raw)
        except ValueError:
            return
        key = (event.get("bot_id"), event.get("event"), event.get("bot_event"), event.get("timestamp"), event.get("turn_order"))
        with _lock:
            if key in _seen:  # MeetStream can retry: de-duplicate
                return
            _seen.add(key)
        threading.Thread(target=handlers.handle, args=(event,), daemon=True).start()

    def log_message(self, *args):
        pass


if __name__ == "__main__":
    print(f"listening on http://127.0.0.1:{PORT}/webhook", flush=True)
    ThreadingHTTPServer(("127.0.0.1", PORT), Hook).serve_forever()
