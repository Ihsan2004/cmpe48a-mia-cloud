"""Tests for SummarizeService source selection and orchestration logic."""
import os
import pytest
from unittest.mock import MagicMock, patch


@pytest.fixture(autouse=True)
def setup_env():
    os.environ.setdefault("OPENAI_API_KEY", "sk-test-fake-key")
    yield


# ==================== Source Selection Tests ====================

def test_select_source_force_raw():
    """force_raw=True should always use raw segments."""
    from app.services.summarize_service import SummarizeService

    mock_repo = MagicMock()
    mock_repo.fetch_segments.return_value = [{"id": "1", "text": "hello"}]
    mock_repo.format_segments_for_nlp.return_value = [{"text": "hello"}]

    svc = SummarizeService(transcript_repo=mock_repo)
    chunks, source, reason = svc._select_source("meeting-1", None, force_raw=True, source_reason=None)

    assert source == "raw_segments_forced"
    assert len(chunks) == 1
    mock_repo.fetch_segments.assert_called_once()
    mock_repo.fetch_blocks.assert_not_called()


def test_select_source_finalized_blocks_preferred():
    """Finalized chat blocks should be preferred over raw segments."""
    from app.services.summarize_service import SummarizeService

    mock_repo = MagicMock()
    mock_repo.fetch_blocks.return_value = [
        {"id": "1", "text": "corrected text", "speaker": "Ali", "start_time": 0},
    ]
    mock_repo.format_blocks_for_nlp.return_value = [{"text": "corrected text"}]

    svc = SummarizeService(transcript_repo=mock_repo)
    chunks, source, reason = svc._select_source("meeting-1", None, force_raw=False, source_reason=None)

    assert source == "chat_blocks_finalized"
    assert len(chunks) == 1


def test_select_source_fallback_to_raw_when_no_blocks():
    """No blocks at all should fall back to raw segments."""
    from app.services.summarize_service import SummarizeService

    mock_repo = MagicMock()
    # First call (finalized) returns empty, second call (live) returns empty
    mock_repo.fetch_blocks.return_value = []
    mock_repo.fetch_segments.return_value = [{"id": "1", "text": "raw text"}]
    mock_repo.format_segments_for_nlp.return_value = [{"text": "raw text"}]

    svc = SummarizeService(transcript_repo=mock_repo)
    chunks, source, reason = svc._select_source("meeting-1", None, force_raw=False, source_reason=None)

    assert source == "raw_segments_fallback"
    assert reason == "no_chat_blocks_found"


def test_select_source_live_blocks_ready():
    """Live blocks with readiness >= 95% should be used."""
    from app.services.summarize_service import SummarizeService

    mock_repo = MagicMock()
    # Finalized returns empty
    def fetch_blocks_side_effect(meeting_id, finalized_only=False, exclude_noise=False):
        if finalized_only:
            return []
        return [{"id": "1", "text": "live block", "end_time": 100}]

    mock_repo.fetch_blocks.side_effect = fetch_blocks_side_effect
    mock_repo.format_blocks_for_nlp.return_value = [{"text": "live block"}]
    mock_repo.fetch_segments.return_value = [{"end_time": 100}]  # readiness = 100/100 = 1.0

    svc = SummarizeService(transcript_repo=mock_repo)
    chunks, source, reason = svc._select_source("meeting-1", None, force_raw=False, source_reason=None)

    assert source == "chat_blocks_live_ready"


def test_select_source_live_blocks_not_ready():
    """Live blocks with readiness < 95% should fall back to raw segments."""
    from app.services.summarize_service import SummarizeService

    mock_repo = MagicMock()

    def fetch_blocks_side_effect(meeting_id, finalized_only=False, exclude_noise=False):
        if finalized_only:
            return []
        return [{"id": "1", "text": "partial block", "end_time": 50}]

    mock_repo.fetch_blocks.side_effect = fetch_blocks_side_effect
    mock_repo.format_blocks_for_nlp.return_value = [{"text": "partial"}]
    mock_repo.format_segments_for_nlp.return_value = [{"text": "raw"}]
    mock_repo.fetch_segments.return_value = [{"end_time": 100}]  # readiness = 50/100 = 0.5

    svc = SummarizeService(transcript_repo=mock_repo)
    chunks, source, reason = svc._select_source("meeting-1", None, force_raw=False, source_reason=None)

    assert source == "raw_segments_fallback"
    assert reason == "live_blocks_not_ready"


# ==================== Catch-Up Tests ====================

def test_catch_me_up_empty_meeting_id():
    """Empty meeting_id should raise ValueError."""
    from app.services.summarize_service import SummarizeService

    svc = SummarizeService()
    with pytest.raises(ValueError, match="meeting_id is required"):
        svc.catch_me_up(meeting_id="", meeting_url=None, progressive_summary=None, last_summarized_at=None)


def test_catch_me_up_no_new_segments_with_existing_summary():
    """No new segments with existing summary should return existing summary."""
    from app.services.summarize_service import SummarizeService

    mock_repo = MagicMock()
    mock_repo.fetch_segments.side_effect = ValueError("No transcript segments found")

    svc = SummarizeService(transcript_repo=mock_repo)
    result = svc.catch_me_up(
        meeting_id="test-123",
        meeting_url=None,
        progressive_summary="Existing summary here",
        last_summarized_at="2026-01-01T00:00:00Z",
    )

    assert result["success"] is True
    assert result["summary"] == "Existing summary here"
    assert result["segments_analyzed"] == 0


# ==================== Checkpoint Tests ====================

def test_checkpoint_no_chunks_returns_current_summary():
    """No new chunks should return current summary."""
    from app.services.summarize_service import SummarizeService

    mock_repo = MagicMock()
    mock_repo.fetch_segments.return_value = []
    mock_repo.format_segments_for_nlp.return_value = []

    svc = SummarizeService(transcript_repo=mock_repo)
    result = svc.create_checkpoint(
        meeting_id="test-123",
        meeting_url=None,
        current_summary="Previous checkpoint",
        last_summarized_at=None,
        language="tr",
    )

    assert result["success"] is True
    assert result["summary"] == "Previous checkpoint"
