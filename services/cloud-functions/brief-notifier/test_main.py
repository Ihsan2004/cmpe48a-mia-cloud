"""Unit tests for the brief-notifier Cloud Function."""
import json
from types import SimpleNamespace

from main import brief_notifier


def _make_req(method="POST", path="/", headers=None, payload=None):
    return SimpleNamespace(
        method=method,
        path=path,
        headers=headers or {},
        get_json=lambda silent=True: payload,
    )


def test_health_endpoint():
    body, status, _ = brief_notifier(_make_req(method="GET", path="/health"))
    assert status == 200
    assert json.loads(body)["status"] == "healthy"


def test_rejects_unauthorized_when_secret_set(monkeypatch):
    monkeypatch.setenv("INTERNAL_SERVICE_SHARED_SECRET", "top-secret")
    body, status, _ = brief_notifier(_make_req(payload={"recipient": "a@b.com", "body": "hi"}))
    assert status == 401


def test_rejects_non_dict_json_and_unsupported_channel(monkeypatch):
    monkeypatch.delenv("INTERNAL_SERVICE_SHARED_SECRET", raising=False)
    _, status_list, _ = brief_notifier(_make_req(payload=["bad"]))
    assert status_list == 400

    _, status_chan, _ = brief_notifier(_make_req(payload={"channel": "sms", "recipient": "a@b.com", "body": "hi"}))
    assert status_chan == 400


def test_rejects_crlf_injection(monkeypatch):
    monkeypatch.delenv("INTERNAL_SERVICE_SHARED_SECRET", raising=False)
    _, status, _ = brief_notifier(
        _make_req(payload={"recipient": "a@b.com\r\nBcc: evil@x.com", "body": "hi"})
    )
    assert status == 400


def test_dry_run_accepts_html_and_rfc2047_subject(monkeypatch):
    monkeypatch.delenv("INTERNAL_SERVICE_SHARED_SECRET", raising=False)
    monkeypatch.delenv("SMTP_HOST", raising=False)
    body, status, _ = brief_notifier(
        _make_req(
            payload={
                "channel": "email",
                "recipient": "a@b.com",
                "subject": "=?utf-8?q?Toplant=C4=B1_Brief?=",
                "body": "Plain text",
                "html": "<p>HTML</p>",
            }
        )
    )
    assert status == 200
    assert json.loads(body)["dry_run"] is True


def test_smtp_send_filters_blocked_headers(monkeypatch):
    monkeypatch.delenv("INTERNAL_SERVICE_SHARED_SECRET", raising=False)
    monkeypatch.setenv("SMTP_HOST", "smtp.example.local")
    monkeypatch.setenv("SMTP_PORT", "")

    sent_messages = []

    class FakeSMTP:
        def __init__(self, host, port, timeout=10):
            self.host = host
            self.port = port

        def __enter__(self):
            return self

        def __exit__(self, *args):
            return False

        def has_extn(self, name):
            return False

        def send_message(self, msg):
            sent_messages.append((self.host, self.port, msg))

    monkeypatch.setattr("main.smtplib.SMTP", FakeSMTP)

    body, status, _ = brief_notifier(
        _make_req(
            payload={
                "channel": "email",
                "recipient": "user@example.com",
                "subject": "Brief Ready",
                "body": "Plain summary",
                "html": "<p>HTML summary</p>",
                "extra_headers": {
                    "Reply-To": "reply@example.com",
                    "Bcc": "attacker@evil.com",
                    "List-Unsubscribe": "<https://example.com/unsub>",
                },
            }
        )
    )
    assert status == 200
    assert json.loads(body)["status"] == "sent"
    host, port, msg = sent_messages[0]
    assert host == "smtp.example.local" and port == 587
    assert msg["Reply-To"] == "reply@example.com"
    assert msg["List-Unsubscribe"] == "<https://example.com/unsub>"
    assert msg["Bcc"] is None
