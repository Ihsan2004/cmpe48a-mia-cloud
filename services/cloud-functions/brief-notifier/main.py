"""Google Cloud Function (2nd Gen): brief-notifier.

Stateless serverless HTTP handler invoked by `mia-backend` to deliver
pre-meeting briefs and post-meeting summaries via email (SMTP) or dry-run mode.
"""

from email.header import decode_header, make_header
from email.message import EmailMessage
import hmac
import json
import os
import smtplib
import functions_framework

BLOCKED_EXTRA_HEADERS = {
    "subject",
    "from",
    "to",
    "cc",
    "bcc",
    "content-type",
    "mime-version",
    "content-transfer-encoding",
}


def _json_response(body_dict: dict, status: int):
    return (json.dumps(body_dict), status, {"Content-Type": "application/json"})


def _has_crlf(value: str) -> bool:
    return "\r" in value or "\n" in value


def _decode_subject(raw_subject: str) -> str:
    """Decode RFC-2047 encoded words if present so EmailMessage avoids double-encoding."""
    try:
        return str(make_header(decode_header(raw_subject)))
    except Exception:
        return raw_subject


@functions_framework.http
def brief_notifier(request):
    """Handle health checks (GET) and brief notification dispatches (POST)."""
    if request.method == "GET" and request.path.rstrip("/") == "/health":
        return _json_response({"status": "healthy", "service": "brief-notifier"}, 200)

    if request.method != "POST":
        return _json_response({"error": "Method not allowed"}, 405)

    expected_secret = os.getenv("INTERNAL_SERVICE_SHARED_SECRET", "")
    if expected_secret:
        provided = request.headers.get("X-Internal-Service-Secret", "")
        if not hmac.compare_digest(provided, expected_secret):
            return _json_response({"error": "Unauthorized"}, 401)

    payload = request.get_json(silent=True)
    if not isinstance(payload, dict):
        return _json_response({"error": "Request body must be a JSON object"}, 400)

    channel = payload.get("channel") or "email"
    if not isinstance(channel, str) or channel != "email":
        return _json_response({"error": "Unsupported channel; only 'email' is supported"}, 400)

    recipient = payload.get("recipient")
    raw_subject = payload.get("subject")
    subject = _decode_subject(raw_subject if isinstance(raw_subject, str) and raw_subject else "MIA Meeting Brief")
    body = payload.get("body") or ""
    html_body = payload.get("html") or ""
    from_addr = payload.get("from") or os.getenv("SMTP_FROM_EMAIL", "noreply@example.com")
    extra_headers = payload.get("extra_headers")
    if extra_headers is None:
        extra_headers = {}

    if not isinstance(recipient, str) or not recipient.strip():
        return _json_response({"error": "Missing or invalid 'recipient'"}, 400)
    if not isinstance(body, str) or not isinstance(html_body, str) or (not body and not html_body):
        return _json_response({"error": "Missing or invalid 'body'/'html'"}, 400)
    if not isinstance(from_addr, str) or not isinstance(extra_headers, dict):
        return _json_response({"error": "Invalid 'from' or 'extra_headers'"}, 400)
    if _has_crlf(recipient) or _has_crlf(subject) or _has_crlf(from_addr):
        return _json_response({"error": "Invalid newline characters in email header"}, 400)

    smtp_host = os.getenv("SMTP_HOST", "").strip()
    if smtp_host:
        port_raw = (os.getenv("SMTP_PORT") or "587").strip()
        try:
            smtp_port = int(port_raw)
        except ValueError:
            return _json_response({"error": "Invalid SMTP_PORT configuration"}, 500)

        msg = EmailMessage()
        msg["Subject"] = subject
        msg["From"] = from_addr
        msg["To"] = recipient.strip()
        for header_name, header_val in extra_headers.items():
            if not isinstance(header_name, str) or not isinstance(header_val, str):
                continue
            if header_name.lower() in BLOCKED_EXTRA_HEADERS:
                continue
            if _has_crlf(header_name) or _has_crlf(header_val):
                return _json_response({"error": f"Invalid header characters in {header_name}"}, 400)
            if header_val.strip():
                msg[header_name] = header_val.strip()

        msg.set_content(body or " ")
        if html_body:
            msg.add_alternative(html_body, subtype="html")

        if smtp_port == 465:
            with smtplib.SMTP_SSL(smtp_host, smtp_port, timeout=10) as smtp:
                if os.getenv("SMTP_USER") and os.getenv("SMTP_PASSWORD"):
                    smtp.login(os.getenv("SMTP_USER"), os.getenv("SMTP_PASSWORD"))
                smtp.send_message(msg)
        else:
            with smtplib.SMTP(smtp_host, smtp_port, timeout=10) as smtp:
                if smtp.has_extn("starttls"):
                    smtp.starttls()
                if os.getenv("SMTP_USER") and os.getenv("SMTP_PASSWORD"):
                    smtp.login(os.getenv("SMTP_USER"), os.getenv("SMTP_PASSWORD"))
                smtp.send_message(msg)
        return _json_response({"status": "sent", "channel": "email"}, 200)

    # Dry-run response when SMTP_HOST is unconfigured (local Docker Compose / Locust benchmarks)
    return _json_response({"status": "accepted", "channel": "email", "dry_run": True}, 200)
