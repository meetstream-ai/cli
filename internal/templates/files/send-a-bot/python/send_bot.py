"""Send a MeetStream bot to a meeting, follow it, print the transcript.

Usage: python send_bot.py <meeting-url>
"""
import os
import sys
import time

from meetstream_api import POLL, call, fetch_transcript, safe_id, transcript_lines

PROVIDERS = {
    "meetstream": {"meetstream": {"language": "auto", "translate": False}},
    "deepgram": {"deepgram": {"model": "nova-3", "language": "en", "diarize": True}},
    "assemblyai": {"assemblyai": {"speech_models": ["best"], "language_code": "en_us", "speaker_labels": True}},
}
PROVIDER = os.environ.get("TRANSCRIPTION_PROVIDER", "[[.Transcription]]")
TERMINAL = ("stopped", "done", "notallowed", "denied", "failed", "error")


def main():
    if len(sys.argv) != 2:
        sys.exit("usage: python send_bot.py <meeting-url>")
    body = {"meeting_link": sys.argv[1], "bot_name": "[[.BotName]]",
            "recording_config": {"transcript": {"provider": PROVIDERS.get(PROVIDER, {PROVIDER: {}})}}}
    code, bot = call("POST", "/bots/create_bot", body)
    if code >= 300:
        sys.exit(f"create_bot failed ({code}): {bot}")
    bot_id = safe_id(bot["bot_id"])
    print(f"bot {bot_id} sent")
    last = ""
    while True:
        _, st = call("GET", f"/bots/{bot_id}/status")
        status = (st or {}).get("status", "")
        if status != last:
            print(f"  {time.strftime('%H:%M:%S')}  {status}")
            last = status
        if status.lower() in TERMINAL:
            break
        time.sleep(POLL)
    if last.lower() not in ("stopped", "done"):
        sys.exit(f"bot ended without recording: {last}")
    print("waiting for the transcript ...")
    for line in transcript_lines(fetch_transcript(bot_id)):
        print("  " + line)


if __name__ == "__main__":
    main()
