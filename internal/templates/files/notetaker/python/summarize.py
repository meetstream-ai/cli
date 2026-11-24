"""Summarize a transcript with the model you chose at init ([[.LLM]])."""
import json
import os
import urllib.request

PROMPT = "Write short meeting notes: a 2-line summary, then decisions, then action items with owners.\n\n"


def _post(url, headers, body):
    req = urllib.request.Request(url, data=json.dumps(body).encode(), headers={"Content-Type": "application/json", **headers})
    with urllib.request.urlopen(req, timeout=120) as r:
        return json.loads(r.read())


def summarize(transcript):
    provider = os.environ.get("LLM_PROVIDER", "[[.LLM]]")
    if provider == "anthropic" and os.environ.get("ANTHROPIC_API_KEY"):
        out = _post("https://api.anthropic.com/v1/messages",
                    {"x-api-key": os.environ["ANTHROPIC_API_KEY"], "anthropic-version": "2023-06-01"},
                    {"model": os.environ.get("ANTHROPIC_MODEL", "claude-sonnet-5-5"), "max_tokens": 800,
                     "messages": [{"role": "user", "content": PROMPT + transcript}]})
        return out["content"][0]["text"]
    if provider == "openai" and os.environ.get("OPENAI_API_KEY") and os.environ.get("OPENAI_MODEL"):
        out = _post("https://api.openai.com/v1/chat/completions",
                    {"Authorization": "Bearer " + os.environ["OPENAI_API_KEY"]},
                    {"model": os.environ["OPENAI_MODEL"], "messages": [{"role": "user", "content": PROMPT + transcript}]})
        return out["choices"][0]["message"]["content"]
    # No model key yet: plain notes, so the app still works end to end.
    lines = transcript.splitlines()
    return "No summary model key set. First lines of the transcript:\n" + "\n".join(lines[:5])
