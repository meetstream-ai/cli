"""Send the notes with the provider you chose at init ([[.Email]])."""
import json
import os
import smtplib
import urllib.request
from email.message import EmailMessage


def _post(url, headers, body):
    req = urllib.request.Request(url, data=json.dumps(body).encode(), headers={"Content-Type": "application/json", **headers})
    with urllib.request.urlopen(req, timeout=30) as r:
        return r.status


def send_notes(bot_id, notes):
    provider = os.environ.get("EMAIL_PROVIDER", "[[.Email]]")
    to, sender = os.environ.get("EMAIL_TO"), os.environ.get("EMAIL_FROM")
    subject = f"Meeting notes ({bot_id})"
    if provider == "resend" and os.environ.get("RESEND_API_KEY") and to and sender:
        _post("https://api.resend.com/emails", {"Authorization": "Bearer " + os.environ["RESEND_API_KEY"]},
              {"from": sender, "to": [to], "subject": subject, "text": notes})
    elif provider == "sendgrid" and os.environ.get("SENDGRID_API_KEY") and to and sender:
        _post("https://api.sendgrid.com/v3/mail/send", {"Authorization": "Bearer " + os.environ["SENDGRID_API_KEY"]},
              {"personalizations": [{"to": [{"email": to}]}], "from": {"email": sender}, "subject": subject,
               "content": [{"type": "text/plain", "value": notes}]})
    elif provider == "smtp" and os.environ.get("SMTP_HOST") and to and sender:
        msg = EmailMessage()
        msg["Subject"], msg["From"], msg["To"] = subject, sender, to
        msg.set_content(notes)
        with smtplib.SMTP(os.environ["SMTP_HOST"], int(os.environ.get("SMTP_PORT", "587"))) as s:
            s.starttls()
            if os.environ.get("SMTP_USER"):
                s.login(os.environ["SMTP_USER"], os.environ.get("SMTP_PASSWORD", ""))
            s.send_message(msg)
    else:
        print(f"[{bot_id}] notes (email not set up, printing):\n{notes}", flush=True)
        return
    print(f"[{bot_id}] notes sent to {to}", flush=True)
