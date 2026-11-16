"""Tests for health check endpoints."""
import os
import pytest
from fastapi.testclient import TestClient


@pytest.fixture
def client():
    """Create test client with auth bypass for public paths."""
    # Set a dummy secret so the middleware doesn't return 503
    os.environ["INTERNAL_SERVICE_SHARED_SECRET"] = "test-secret"

    # Import after env is set
    from app.main import app
    return TestClient(app)


def test_health_endpoint(client):
    """GET /health should return healthy status."""
    response = client.get("/health")
    assert response.status_code == 200

    data = response.json()
    assert data["status"] == "healthy"
    assert data["service"] == "mia-search"
    assert "timestamp" in data


def test_root_endpoint(client):
    """GET / should return service info."""
    response = client.get("/")
    assert response.status_code == 200

    data = response.json()
    assert data["service"] == "MIA Search Service"
    assert data["status"] == "running"
    assert "endpoints" in data
