"""Google Cloud Function (2nd Gen): brief-notifier.

Stateless serverless HTTP handler invoked by `mia-backend` to deliver
pre-meeting briefs and post-meeting summaries via email (SMTP) or Slack.
"""

import json
import os
import smtplib
from email.message import EmailMessage
import functions_framework


@functions_framework.http
def brief_notifier(request):
    """Handle HTTP POST brief notification requests."""
    if request.method != "POST":
        return (json.dumps({"error": "Method not allowed"}), 405, {"Content-Type": "application/json"})

    expected_secret = os.getenv("INTERNAL_SERVICE_SHARED_SECRET", "")
    if expected_secret:
        provided = request.headers.get("X-Internal-Service-Secret", "")
        if provided != expected_secret:
            return (json.dumps({"error": "Unauthorized"}), 401, {"Content-Type": "application/json"})

    payload = request.get_json(silent=True) or {}
    channel = payload.get("channel", "email")
    recipient = payload.get("recipient")
    subject = payload.get("subject", "MIA Meeting Brief")
    body = payload.get("body", "")

    if not recipient or not body:
        return (
            json.dumps({"error": "Missing required fields: recipient, body"}),
            400,
            {"Content-Type": "application/json"},
        )

    if channel == "email" and os.getenv("SMTP_HOST"):
        msg = EmailMessage()
        msg["Subject"] = subject
        msg["From"] = os.getenv("SMTP_FROM_EMAIL", "noreply@example.com")
        msg["To"] = recipient
        msg.set_content(body)
        with smtplib.SMTP(os.getenv("SMTP_HOST"), int(os.getenv("SMTP_PORT", "587")), timeout=10) as smtp:
            smtp.starttls()
            if os.getenv("SMTP_USER") and os.getenv("SMTP_PASSWORD"):
                smtp.login(os.getenv("SMTP_USER"), os.getenv("SMTP_PASSWORD"))
            smtp.send_message(msg)
        return (json.dumps({"status": "sent", "channel": "email"}), 200, {"Content-Type": "application/json"})

    # Dry-run / placeholder response when external SMTP/Slack delivery is disabled (e.g., load tests)
    return (
        json.dumps({"status": "accepted", "channel": channel, "dry_run": True}),
        200,
        {"Content-Type": "application/json"},
    )
