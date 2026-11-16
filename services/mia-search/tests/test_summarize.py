"""Tests for summarize endpoint with mocked dependencies."""
import os
import pytest
from unittest.mock import patch, MagicMock
from fastapi.testclient import TestClient


@pytest.fixture(autouse=True)
def setup_env():
    """Set required env vars before importing app."""
    os.environ["INTERNAL_SERVICE_SHARED_SECRET"] = "test-secret"
    os.environ.setdefault("OPENAI_API_KEY", "sk-test-fake-key")
    yield


@pytest.fixture
def auth_headers():
    return {"X-Internal-Service-Secret": "test-secret"}


@pytest.fixture
def client():
    from app.main import app
    return TestClient(app)


# ==================== Request Validation ====================

def test_summarize_missing_meeting_id(client, auth_headers):
    """POST /summarize without meeting_id should return 422."""
    response = client.post("/summarize", json={}, headers=auth_headers)
    assert response.status_code == 422


def test_summarize_empty_meeting_id(client, auth_headers):
    """POST /summarize with empty meeting_id should return 400."""
    response = client.post(
        "/summarize",
        json={"meeting_id": ""},
        headers=auth_headers,
    )
    # Router checks `if not request.meeting_id` → 400
    assert response.status_code == 400
    assert "meeting_id is required" in response.json()["detail"]


# ==================== Summarize with Mock ====================

@patch("app.routers.summarize.summarize_service")
def test_summarize_happy_path(mock_service, client, auth_headers):
    """POST /summarize with valid data should return summary."""
    mock_service.summarize.return_value = {
        "success": True,
        "summary": "Toplantıda proje planı tartışıldı ve öncelikler belirlendi.",
        "email_draft": "Kısa özet: proje planı konuşuldu.",
        "key_topics": ["proje planı", "öncelikler"],
        "key_topics_structured": [
            {"topic": "proje planı", "topic_order": 1},
            {"topic": "öncelikler", "topic_order": 2},
        ],
        "action_items": ["Öncelikleri belirle"],
        "action_items_structured": [
            {"description": "Öncelikleri belirle", "assignee_name": "Bob", "commitment_level": "committed"}
        ],
        "participants": ["Alice", "Bob"],
        "total_chunks": 1,
        "processing_status": "completed",
        "coverage_ratio": 1.0,
        "successful_chunks": 1,
        "failed_chunks": 0,
        "chunk_telemetry": [],
        "warnings": [],
        "summary_source": "chat_blocks_finalized",
        "usage_prompt_tokens": 500,
        "usage_completion_tokens": 200,
        "usage_total_tokens": 700,
    }

    response = client.post(
        "/summarize",
        json={"meeting_id": "test-meeting-123"},
        headers=auth_headers,
    )

    assert response.status_code == 200
    data = response.json()
    assert data["success"] is True
    assert "proje" in data["summary"].lower() or len(data["summary"]) > 0
    assert isinstance(data["key_topics"], list)
    assert isinstance(data["action_items"], list)
    assert isinstance(data["participants"], list)


@patch("app.routers.summarize.summarize_service")
def test_summarize_no_transcript(mock_service, client, auth_headers):
    """POST /summarize when no transcript exists should return 404."""
    mock_service.summarize.side_effect = ValueError("No transcript data found for this meeting")

    response = client.post(
        "/summarize",
        json={"meeting_id": "nonexistent-meeting"},
        headers=auth_headers,
    )

    assert response.status_code == 404


# ==================== Response Structure ====================

@patch("app.routers.summarize.summarize_service")
def test_summarize_response_has_required_fields(mock_service, client, auth_headers):
    """Response should contain all required SummarizeResponse fields."""
    mock_service.summarize.return_value = {
        "success": True,
        "summary": "Test toplantı özeti burada yer almaktadır.",
        "email_draft": "Kısa özet",
        "key_topics": ["test"],
        "key_topics_structured": [],
        "action_items": [],
        "action_items_structured": [],
        "participants": ["A"],
        "total_chunks": 1,
        "processing_status": "completed",
        "coverage_ratio": 1.0,
        "successful_chunks": 1,
        "failed_chunks": 0,
        "chunk_telemetry": [],
        "warnings": [],
        "summary_source": "chat_blocks_finalized",
        "usage_prompt_tokens": 100,
        "usage_completion_tokens": 50,
        "usage_total_tokens": 150,
    }

    response = client.post(
        "/summarize",
        json={"meeting_id": "test-meeting"},
        headers=auth_headers,
    )

    assert response.status_code == 200
    data = response.json()

    # Required fields from SummarizeResponse schema
    required_fields = ["success", "summary", "key_topics", "action_items", "participants"]
    for field in required_fields:
        assert field in data, f"missing required field: {field}"


# ==================== NLP Service Unit Tests ====================

def test_nlp_service_extract_text_from_chunks():
    """NLPService.extract_text_from_chunks should combine chunks into text."""
    from app.services.nlp_service import NLPService

    svc = NLPService()
    chunks = [
        {"speaker": "Alice", "text": "Merhaba"},
        {"speaker": "Bob", "text": "Nasılsın"},
    ]

    result = svc.extract_text_from_chunks(chunks)
    assert "[Alice]: Merhaba" in result
    assert "[Bob]: Nasılsın" in result


def test_nlp_service_extract_text_empty_chunks():
    """Empty chunks should return empty string."""
    from app.services.nlp_service import NLPService

    svc = NLPService()
    result = svc.extract_text_from_chunks([])
    assert result == ""


def test_nlp_service_extract_text_missing_speaker():
    """Missing speaker should default to 'Konuşmacı'."""
    from app.services.nlp_service import NLPService

    svc = NLPService()
    chunks = [{"text": "Merhaba"}]  # no speaker field

    result = svc.extract_text_from_chunks(chunks)
    assert "[Konuşmacı]: Merhaba" in result


def test_nlp_service_safe_float():
    """_safe_float should handle various input types."""
    from app.services.nlp_service import NLPService

    svc = NLPService()

    assert svc._safe_float(3.14) == 3.14
    assert svc._safe_float("2.5") == 2.5
    assert svc._safe_float(0) == 0.0
    assert svc._safe_float(None) is None
    assert svc._safe_float("not-a-number") is None
