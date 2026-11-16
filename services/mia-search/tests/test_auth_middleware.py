"""Tests for internal service authentication middleware."""
import os
import pytest
from fastapi.testclient import TestClient


@pytest.fixture
def client():
    """Create test client with auth secret configured."""
    os.environ["INTERNAL_SERVICE_SHARED_SECRET"] = "test-secret-123"

    from app.main import app
    return TestClient(app)


@pytest.fixture
def client_no_secret():
    """Create test client without auth secret (simulates misconfiguration)."""
    os.environ.pop("INTERNAL_SERVICE_SHARED_SECRET", None)
    os.environ["INTERNAL_SERVICE_SHARED_SECRET"] = ""

    from app.main import app
    return TestClient(app)


def test_public_paths_bypass_auth(client):
    """Public paths (/health, /) should not require auth."""
    response = client.get("/health")
    assert response.status_code == 200

    response = client.get("/")
    assert response.status_code == 200


def test_protected_path_without_header(client):
    """Protected paths without auth header should return 401."""
    response = client.post("/summarize", json={"meeting_id": "test"})
    assert response.status_code == 401
    assert "Unauthorized" in response.json()["detail"]


def test_protected_path_with_wrong_secret(client):
    """Protected paths with wrong secret should return 401."""
    response = client.post(
        "/summarize",
        json={"meeting_id": "test"},
        headers={"X-Internal-Service-Secret": "wrong-secret"},
    )
    assert response.status_code == 401


def test_protected_path_with_correct_secret(client):
    """Protected paths with correct secret should pass auth (may fail on business logic, not auth)."""
    response = client.post(
        "/summarize",
        json={"meeting_id": "test-meeting-id"},
        headers={"X-Internal-Service-Secret": "test-secret-123"},
    )
    # Should NOT be 401 — auth passed. Might be 400/422/500 from business logic.
    assert response.status_code != 401


def test_no_secret_configured_returns_503(client_no_secret):
    """When INTERNAL_SERVICE_SHARED_SECRET is not set, protected paths return 503."""
    response = client_no_secret.post(
        "/summarize",
        json={"meeting_id": "test"},
        headers={"X-Internal-Service-Secret": "any-value"},
    )
    assert response.status_code == 503
    assert "not configured" in response.json()["detail"]
