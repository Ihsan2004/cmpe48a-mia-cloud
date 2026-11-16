"""Tests for /embeddings/generate-batch usage propagation (issue #39 Step 2).

The router used to discard the service's `_usage` tuple. The Go caller
(post_meeting.go) needs the usage payload to record into usage_logs +
bump the per-user quota counter. This suite locks down the contract so a
future regression that drops the field would be caught at CI time.
"""
import os
from unittest.mock import patch

import pytest
from fastapi.testclient import TestClient


@pytest.fixture
def client():
    os.environ["INTERNAL_SERVICE_SHARED_SECRET"] = "test-secret-123"
    from app.main import app
    return TestClient(app)


def _auth_headers():
    return {"X-Internal-Service-Secret": "test-secret-123"}


def test_generate_batch_returns_usage_fields(client):
    """generate_embeddings_batch returns (embeddings, usage_dict).
    The router must surface usage_prompt_tokens / usage_total_tokens
    in the response body so the Go side can record them."""
    fake_embeddings = [[0.1] * 1536, [0.2] * 1536]
    fake_usage = {
        "usage_prompt_tokens": 42,
        "usage_completion_tokens": 0,  # embeddings have no completion side
        "usage_total_tokens": 42,
    }

    with patch(
        "app.routers.embeddings.embedding_service.generate_embeddings_batch",
        return_value=(fake_embeddings, fake_usage),
    ):
        resp = client.post(
            "/embeddings/generate-batch",
            json={"texts": ["hello world", "another one"]},
            headers=_auth_headers(),
        )

    assert resp.status_code == 200
    body = resp.json()
    assert body["count"] == 2
    assert body["usage_prompt_tokens"] == 42
    assert body["usage_completion_tokens"] == 0
    assert body["usage_total_tokens"] == 42


def test_generate_batch_zero_usage_when_service_returns_empty(client):
    """If the service produces no batches (all texts too short), the
    response still ships usage fields with zeros — not omitted, not
    null — so the Go decoder lands on a well-typed struct every time."""
    with patch(
        "app.routers.embeddings.embedding_service.generate_embeddings_batch",
        return_value=([None], {}),
    ):
        resp = client.post(
            "/embeddings/generate-batch",
            json={"texts": ["x"]},
            headers=_auth_headers(),
        )

    assert resp.status_code == 200
    body = resp.json()
    assert body["usage_prompt_tokens"] == 0
    assert body["usage_total_tokens"] == 0
