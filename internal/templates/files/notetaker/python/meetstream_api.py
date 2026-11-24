"""Small MeetStream API helpers (standard library only)."""
import json, os, re, time, urllib.error, urllib.parse, urllib.request
from pathlib import Path


def load_env():
    """Read .env next to this file (no packages needed)."""
    p = Path(__file__).with_name(".env")
    if p.exists():
        for line in p.read_text().splitlines():
            if "=" in line and not line.lstrip().startswith("#"):
                k, v = line.split("=", 1)
                os.environ.setdefault(k.strip(), v.strip())


load_env()
API = os.environ.get("MEETSTREAM_API_URL", "https://api.meetstream.ai/api/v1").rstrip("/")
KEY = os.environ.get("MEETSTREAM_API_KEY", "")
POLL = 1 if ("127.0.0.1" in API or "localhost" in API) else 5


_ID = re.compile(r"^[A-Za-z0-9_-]{1,128}$")


def safe_id(value):
    """Check an id from a webhook or an API answer before it goes into a URL path."""
    if not isinstance(value, str) or not _ID.match(value):
        raise ValueError(f"unexpected id: {value!r}")
    return urllib.parse.quote(value, safe="")


def call(method, path, body=None):
    """One MeetStream API call. Returns (status, json)."""
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(API + path, data=data, method=method, headers={
        "Authorization": f"Token {KEY}", "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            raw = r.read()
            return r.status, json.loads(raw) if raw else None
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw) if raw else None
        except ValueError:
            return e.code, {"detail": raw.decode(errors="replace")}


def fetch_transcript(bot_id, timeout=900):
    """Resolve the transcript id (it is not in any webhook), then poll for the text."""
    bot_id = safe_id(bot_id)
    deadline = time.time() + timeout
    while time.time() < deadline:
        _, detail = call("GET", f"/bots/{bot_id}/detail")
        tid = ((detail or {}).get("bot_details") or {}).get("transcript_id")
        if tid:
            code, body = call("GET", f"/transcript/{safe_id(tid)}/get_transcript?raw=false")
            if code == 200 and body:
                return body if isinstance(body, list) else body.get("message", body)
            if code == 422:
                raise RuntimeError((body or {}).get("message", "transcript failed"))
        time.sleep(POLL)
    raise TimeoutError("transcript not ready in time")


def transcript_lines(segments):
    out = []
    for s in segments or []:
        who = (s.get("participant") or {}).get("name") or s.get("speaker") or "Unknown"
        text = s.get("transcript") or s.get("text") or " ".join(w.get("word", "") for w in s.get("words", []))
        if text:
            out.append(f"{who}: {text}")
    return out
