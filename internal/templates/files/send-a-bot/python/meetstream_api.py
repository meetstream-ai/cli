"""Small MeetStream API helpers (standard library only)."""
import json, os, re, subprocess, time, urllib.error, urllib.parse, urllib.request
from pathlib import Path


def load_env():
    """Read .env next to this file (no packages needed)."""
    p = Path(__file__).with_name(".env")
    if p.exists():
        for line in p.read_text().splitlines():
            if "=" in line and not line.lstrip().startswith("#"):
                k, v = line.split("=", 1)
                os.environ.setdefault(k.strip(), v.strip())


# The API URL from your shell, before .env is read: the CLI keeps using it, so a
# project file cannot send your sign-in to another host.
SHELL_API_URL = os.environ.get("MEETSTREAM_API_URL")
load_env()
API = os.environ.get("MEETSTREAM_API_URL", "https://api.meetstream.ai/api/v1").rstrip("/")
KEY = os.environ.get("MEETSTREAM_API_KEY", "")
_target = API if KEY else (SHELL_API_URL or "")
POLL = 1 if ("127.0.0.1" in _target or "localhost" in _target) else 5


_ID = re.compile(r"^[A-Za-z0-9_-]{1,128}$")


def safe_id(value):
    """Check an id from a webhook or an API answer before it goes into a URL path."""
    if not isinstance(value, str) or not _ID.match(value):
        raise ValueError(f"unexpected id: {value!r}")
    return urllib.parse.quote(value, safe="")


def _call_via_cli(method, path, body):
    """No key in .env (your laptop): the meetstream CLI makes the call with your sign-in."""
    env = dict(os.environ)
    if SHELL_API_URL:
        env["MEETSTREAM_API_URL"] = SHELL_API_URL
    else:
        env.pop("MEETSTREAM_API_URL", None)
    args = ["meetstream", "api", path, "-X", method, "--envelope", "--no-input"]
    if body is not None:
        args += ["-d", "-"]
    try:
        r = subprocess.run(args, input=json.dumps(body) if body is not None else None,
                           capture_output=True, text=True, env=env, timeout=60)
    except FileNotFoundError:
        raise RuntimeError("no MeetStream key in .env and the meetstream CLI is not installed "
                           "(install it, or run: meetstream env --with-key)") from None
    if r.returncode != 0:
        raise RuntimeError("meetstream api failed: " + (r.stdout or r.stderr).strip()[:300] +
                           " (signed in? run: meetstream login)")
    out = json.loads(r.stdout)
    return out["status"], out["body"]


def call(method, path, body=None):
    """One MeetStream API call. Returns (status, json).

    With MEETSTREAM_API_KEY in .env (a server) it calls the API directly.
    Without it (your laptop) it goes through the CLI and your sign-in.
    """
    if not KEY:
        return _call_via_cli(method, path, body)
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


def segment_text(s):
    """words[].text, then punctuated_word, then word, then the flat transcript or text."""
    words = " ".join(
        t for t in ((w or {}).get("text") or (w or {}).get("punctuated_word") or (w or {}).get("word") or "" for w in s.get("words") or []) if t
    )
    return words or s.get("transcript") or s.get("text") or ""


def transcript_lines(segments):
    out = []
    for s in segments or []:
        who = (s.get("participant") or {}).get("name") or s.get("speaker") or "Unknown"
        text = segment_text(s)
        if text:
            out.append(f"{who}: {text}")
    return out
