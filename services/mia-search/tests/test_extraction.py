"""Tests for decision extraction endpoint."""
import json
import os
import pytest
from unittest.mock import patch, MagicMock
from fastapi.testclient import TestClient


@pytest.fixture(autouse=True)
def setup_env():
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

def test_extract_missing_transcript(client, auth_headers):
    """POST /extract without transcript should return 422."""
    response = client.post("/extract", json={"meeting_id": "test"}, headers=auth_headers)
    assert response.status_code == 422


def test_extract_empty_transcript(client, auth_headers):
    """POST /extract with empty transcript should return success with 0 items."""
    response = client.post(
        "/extract",
        json={"meeting_id": "test-123", "transcript": ""},
        headers=auth_headers,
    )
    assert response.status_code == 200
    data = response.json()
    assert data["success"] is True
    assert data["items"] == []


def test_extract_whitespace_transcript(client, auth_headers):
    """POST /extract with whitespace-only transcript should return 0 items."""
    response = client.post(
        "/extract",
        json={"meeting_id": "test-123", "transcript": "   \n  "},
        headers=auth_headers,
    )
    assert response.status_code == 200
    assert response.json()["items"] == []


# ==================== Happy Path ====================

@patch("app.routers.extraction.extraction_service")
def test_extract_happy_path(mock_service, client, auth_headers):
    """POST /extract with valid transcript should return extracted items."""
    mock_service.extract.return_value = {
        "items": [
            {
                "text": "API v2 ile devam edilecek",
                "item_type": "decision",
                "owner_name": "Ali",
                "source_quote": "Ali: v2 ile gidelim",
            },
            {
                "text": "Test coverage arttırılacak",
                "item_type": "action_item",
                "owner_name": "Bob",
                "source_quote": "Bob: testleri yazacağım",
            },
            {
                "text": "Bütçe konusu netleşmedi",
                "item_type": "open_topic",
                "owner_name": None,
                "source_quote": "bütçeye sonra bakalım",
            },
        ],
        "usage": {
            "usage_prompt_tokens": 800,
            "usage_completion_tokens": 300,
            "usage_total_tokens": 1100,
        },
    }

    response = client.post(
        "/extract",
        json={
            "meeting_id": "test-meeting-456",
            "transcript": "[Ali]: Bugün API konusunu konuşalım\n[Bob]: Tamam, v2 ile gidelim",
        },
        headers=auth_headers,
    )

    assert response.status_code == 200
    data = response.json()
    assert data["success"] is True
    assert len(data["items"]) == 3
    assert data["items"][0]["item_type"] == "decision"
    assert data["items"][1]["item_type"] == "action_item"
    assert data["items"][2]["item_type"] == "open_topic"
    assert data["items"][2]["owner_name"] is None
    assert data["usage_total_tokens"] == 1100


# ==================== Response Structure ====================

@patch("app.routers.extraction.extraction_service")
def test_extract_response_has_required_fields(mock_service, client, auth_headers):
    """Response should contain all required fields."""
    mock_service.extract.return_value = {
        "items": [
            {
                "text": "Karar 1",
                "item_type": "decision",
                "owner_name": "Ali",
                "source_quote": "Ali dedi ki...",
            }
        ],
        "usage": {
            "usage_prompt_tokens": 100,
            "usage_completion_tokens": 50,
            "usage_total_tokens": 150,
        },
    }

    response = client.post(
        "/extract",
        json={"meeting_id": "test", "transcript": "test transcript"},
        headers=auth_headers,
    )

    assert response.status_code == 200
    data = response.json()

    required_fields = ["success", "items"]
    for field in required_fields:
        assert field in data, f"missing required field: {field}"

    item = data["items"][0]
    item_fields = ["text", "item_type", "source_quote"]
    for field in item_fields:
        assert field in item, f"missing item field: {field}"


# ==================== Confirmed Items (Idempotency) ====================

@patch("app.routers.extraction.extraction_service")
def test_extract_with_confirmed_items(mock_service, client, auth_headers):
    """POST /extract with confirmed_items should pass them to service."""
    mock_service.extract.return_value = {"items": [], "usage": {"usage_prompt_tokens": 0, "usage_completion_tokens": 0, "usage_total_tokens": 0}}

    response = client.post(
        "/extract",
        json={
            "meeting_id": "test",
            "transcript": "some transcript",
            "confirmed_items": ["Deploy on Wednesday", "Write tests"],
        },
        headers=auth_headers,
    )

    assert response.status_code == 200
    mock_service.extract.assert_called_once()
    call_kwargs = mock_service.extract.call_args
    assert call_kwargs.kwargs.get("confirmed_items") == ["Deploy on Wednesday", "Write tests"]


# ==================== Error Handling ====================

@patch("app.routers.extraction.extraction_service")
def test_extract_service_error(mock_service, client, auth_headers):
    """Service error should return 500."""
    mock_service.extract.side_effect = Exception("OpenAI timeout")

    response = client.post(
        "/extract",
        json={"meeting_id": "test", "transcript": "test transcript"},
        headers=auth_headers,
    )

    assert response.status_code == 500
    assert "Extraction failed" in response.json()["detail"]


# ==================== ExtractionService Unit Tests ====================

def test_extraction_service_validate_item_valid():
    """Valid items should pass validation."""
    from app.services.extraction_service import ExtractionService

    svc = ExtractionService()
    result = svc._validate_item({
        "text": "Deploy v2",
        "item_type": "decision",
        "owner_name": "Ali",
        "source_quote": "Ali said deploy v2",
    })

    assert result is not None
    assert result["text"] == "Deploy v2"
    assert result["item_type"] == "decision"


def test_extraction_service_validate_item_invalid_type():
    """Invalid item_type should return None."""
    from app.services.extraction_service import ExtractionService

    svc = ExtractionService()
    result = svc._validate_item({
        "text": "Something",
        "item_type": "question",
        "source_quote": "quote",
    })

    assert result is None


def test_extraction_service_validate_item_empty_source_quote():
    """Empty source_quote should return None."""
    from app.services.extraction_service import ExtractionService

    svc = ExtractionService()
    result = svc._validate_item({
        "text": "Deploy v2",
        "item_type": "decision",
        "source_quote": "",
    })

    assert result is None


def test_extraction_service_validate_item_empty_text():
    """Empty text should return None."""
    from app.services.extraction_service import ExtractionService

    svc = ExtractionService()
    result = svc._validate_item({
        "text": "",
        "item_type": "decision",
        "source_quote": "some quote",
    })

    assert result is None


# ==================== Chunking Unit Tests ====================

def test_chunking_short_transcript_no_split():
    """Short transcript should not be chunked."""
    from app.services.extraction_service import ExtractionService

    svc = ExtractionService()
    short = "[Ali]: Merhaba\n[Bob]: Nasılsın"
    chunks = svc._chunk_transcript(short)

    assert len(chunks) == 1
    assert chunks[0]["index"] == 0
    assert "Ali" in chunks[0]["text"]


def test_chunking_long_transcript_splits():
    """Long transcript should be split into multiple chunks."""
    from app.services.extraction_service import ExtractionService, CHUNK_TARGET_CHARS

    svc = ExtractionService()
    # Generate a long transcript (~30K chars)
    lines = []
    speakers = ["Ali", "Bob", "Charlie"]
    for i in range(300):
        speaker = speakers[i % 3]
        lines.append(f"[{speaker}]: Bu toplantıda çok önemli konular konuşuyoruz, proje planı ve timeline hakkında detaylı tartışma yapıyoruz. Satır {i}.")

    transcript = "\n".join(lines)
    assert len(transcript) > CHUNK_TARGET_CHARS * 2  # should produce 2+ chunks

    chunks = svc._chunk_transcript(transcript)

    assert len(chunks) >= 2
    # Chunks should be ordered
    for i, chunk in enumerate(chunks):
        assert chunk["index"] == i
        assert len(chunk["text"]) > 0


def test_chunking_overlap_exists():
    """Chunks should have overlapping content (last N lines of chunk appear in next)."""
    from app.services.extraction_service import ExtractionService, CHUNK_OVERLAP_LINES

    svc = ExtractionService()
    # Generate transcript that produces exactly 2-3 chunks
    lines = []
    for i in range(200):
        lines.append(f"[Speaker{i % 3}]: Konuşma satırı {i}, önemli bir tartışma noktası burada ele alınıyor.")

    transcript = "\n".join(lines)
    chunks = svc._chunk_transcript(transcript)

    if len(chunks) >= 2:
        # Last lines of chunk 0 should appear in chunk 1
        chunk0_lines = chunks[0]["text"].split("\n")
        chunk1_text = chunks[1]["text"]

        overlap_found = False
        for line in chunk0_lines[-CHUNK_OVERLAP_LINES:]:
            if line.strip() and line.strip() in chunk1_text:
                overlap_found = True
                break

        assert overlap_found, "Expected overlap between consecutive chunks"


def test_chunking_speaker_boundary_break():
    """Chunks should prefer to break at speaker boundaries."""
    from app.services.extraction_service import ExtractionService

    svc = ExtractionService()
    # Create transcript where speaker changes are clear
    lines = []
    for i in range(200):
        speaker = "Ali" if i < 100 else "Bob"
        lines.append(f"[{speaker}]: Tartışma satırı {i}, devam eden konu burada işleniyor ve analiz ediliyor.")

    transcript = "\n".join(lines)
    chunks = svc._chunk_transcript(transcript)

    if len(chunks) >= 2:
        # Each chunk should start with a speaker tag or <reference_only> overlap wrapper
        for chunk in chunks:
            first_line = chunk["text"].strip().split("\n")[0]
            assert first_line.startswith("[") or first_line.startswith("<reference_only>"), \
                f"Chunk should start at speaker boundary or reference_only, got: {first_line[:50]}"


def test_chunking_empty_transcript():
    """Empty transcript should return single empty-ish chunk."""
    from app.services.extraction_service import ExtractionService

    svc = ExtractionService()
    chunks = svc._chunk_transcript("")
    assert len(chunks) == 1


# ==================== Parallel Extraction Tests ====================

@patch("app.services.extraction_service.create_chat_completion")
def test_parallel_extraction_multiple_chunks(mock_llm):
    """Multiple chunks should all be processed and items combined in order."""
    from app.services.extraction_service import ExtractionService
    from app.utils.openai_gateway import TokenAccumulator

    # Mock LLM to return different items per call
    call_count = {"n": 0}
    def mock_response(*args, **kwargs):
        call_count["n"] += 1
        idx = call_count["n"]
        resp = MagicMock()
        resp.choices = [MagicMock()]
        resp.choices[0].message.content = json.dumps({
            "items": [{"text": f"Karar {idx}", "item_type": "decision", "owner_name": None, "source_quote": f"quote {idx}"}]
        })
        resp.usage = MagicMock(prompt_tokens=100, completion_tokens=50, total_tokens=150)
        return resp

    mock_llm.side_effect = mock_response

    svc = ExtractionService()
    acc = TokenAccumulator()
    chunks = [
        {"text": "chunk one text", "index": 0, "time_start": None, "time_end": None},
        {"text": "chunk two text", "index": 1, "time_start": None, "time_end": None},
        {"text": "chunk three text", "index": 2, "time_start": None, "time_end": None},
    ]

    items, telemetry = svc._extract_chunked(chunks, "system prompt", acc)

    assert len(items) >= 3  # at least one item per chunk
    assert mock_llm.call_count == 3
    assert len(telemetry) == 3  # one telemetry entry per chunk
    # Items should be ordered by chunk_index
    indices = [item["chunk_index"] for item in items]
    assert indices == sorted(indices)


@patch("app.services.extraction_service.create_chat_completion")
def test_parallel_extraction_partial_failure(mock_llm):
    """If one chunk fails, others should still succeed."""
    from app.services.extraction_service import ExtractionService
    from app.utils.openai_gateway import TokenAccumulator

    call_count = {"n": 0}
    def mock_response(*args, **kwargs):
        call_count["n"] += 1
        if call_count["n"] == 2:
            raise Exception("Simulated OpenAI timeout")
        resp = MagicMock()
        resp.choices = [MagicMock()]
        resp.choices[0].message.content = json.dumps({
            "items": [{"text": f"Karar {call_count['n']}", "item_type": "decision", "owner_name": None, "source_quote": f"quote {call_count['n']}"}]
        })
        resp.usage = MagicMock(prompt_tokens=100, completion_tokens=50, total_tokens=150)
        return resp

    mock_llm.side_effect = mock_response

    svc = ExtractionService()
    acc = TokenAccumulator()
    chunks = [
        {"text": "chunk one", "index": 0, "time_start": None, "time_end": None},
        {"text": "chunk two (will fail)", "index": 1, "time_start": None, "time_end": None},
        {"text": "chunk three", "index": 2, "time_start": None, "time_end": None},
    ]

    items, _ = svc._extract_chunked(chunks, "system prompt", acc)

    # Chunk 1 fails after retries, but chunks 0 and 2 should produce items
    assert len(items) >= 1  # at least some items survived


# ==================== Merge Tests ====================

def test_fallback_merge_dedup():
    """Fallback merge should remove exact text duplicates, keep last chunk."""
    from app.services.extraction_service import ExtractionService

    svc = ExtractionService()
    raw_items = [
        {"text": "API v2 ile devam", "item_type": "decision", "source_quote": "q1", "chunk_index": 0},
        {"text": "Test yazılacak", "item_type": "action_item", "source_quote": "q2", "chunk_index": 0},
        {"text": "API v2 ile devam", "item_type": "decision", "source_quote": "q3", "chunk_index": 1},  # duplicate
    ]

    merged = svc._fallback_merge(raw_items, None)
    texts = [m["text"] for m in merged]
    assert len(merged) == 2
    assert texts.count("API v2 ile devam") == 1
    assert all(m["operation"] == "ADD" for m in merged)


def test_fallback_merge_chain_dedup():
    """Fallback merge should skip items already in chain."""
    from app.services.extraction_service import ExtractionService

    svc = ExtractionService()
    raw_items = [
        {"text": "API v2 ile devam", "item_type": "decision", "source_quote": "q1", "chunk_index": 0},
        {"text": "Yeni feature", "item_type": "decision", "source_quote": "q2", "chunk_index": 0},
    ]
    chain_items = [{"text": "API v2 ile devam", "item_type": "decision"}]

    merged = svc._fallback_merge(raw_items, chain_items)
    assert len(merged) == 1
    assert merged[0]["text"] == "Yeni feature"


def test_merge_single_item_no_chain():
    """Single item with no chain should get ADD without LLM call."""
    from app.services.extraction_service import ExtractionService
    from app.utils.openai_gateway import TokenAccumulator

    svc = ExtractionService()
    acc = TokenAccumulator()
    raw_items = [{"text": "Tek karar", "item_type": "decision", "source_quote": "q"}]

    merged = svc._merge_items(raw_items, None, acc)
    assert len(merged) == 1
    assert merged[0]["operation"] == "ADD"
    assert acc.total_tokens == 0  # no LLM call


@patch("app.services.extraction_service.create_chat_completion")
def test_merge_with_llm_operations(mock_llm):
    """LLM merge should assign ADD/UPDATE/DELETE/NOOP operations."""
    from app.services.extraction_service import ExtractionService
    from app.utils.openai_gateway import TokenAccumulator

    # Mock merge LLM response
    resp = MagicMock()
    resp.choices = [MagicMock()]
    resp.choices[0].message.content = json.dumps({
        "results": [
            {"idx": 0, "operation": "ADD", "updated_chain_idx": None, "deleted_chain_idx": None},
            {"idx": 1, "operation": "UPDATE", "updated_chain_idx": 0, "deleted_chain_idx": None},
            {"idx": 2, "operation": "NOOP", "updated_chain_idx": None, "deleted_chain_idx": None},
            {"idx": 3, "operation": "DELETE", "updated_chain_idx": None, "deleted_chain_idx": 1},
        ]
    })
    resp.usage = MagicMock(prompt_tokens=100, completion_tokens=50, total_tokens=150)
    mock_llm.return_value = resp

    svc = ExtractionService()
    acc = TokenAccumulator()
    raw_items = [
        {"text": "Yeni karar", "item_type": "decision", "source_quote": "q1"},
        {"text": "API v2.1", "item_type": "decision", "source_quote": "q2"},
        {"text": "Zaten var", "item_type": "decision", "source_quote": "q3"},
        {"text": "X iptal", "item_type": "decision", "source_quote": "q4"},
    ]
    chain_items = [
        {"text": "API v2", "item_type": "decision"},
        {"text": "X projesi", "item_type": "decision"},
    ]

    merged = svc._merge_items(raw_items, chain_items, acc)

    # NOOP filtered out → 3 items
    assert len(merged) == 3
    ops = [m["operation"] for m in merged]
    assert "ADD" in ops
    assert "UPDATE" in ops
    assert "DELETE" in ops
    assert "NOOP" not in ops

    # UPDATE should have chain index
    update_item = next(m for m in merged if m["operation"] == "UPDATE")
    assert update_item["updated_chain_idx"] == 0

    # DELETE should have chain index
    delete_item = next(m for m in merged if m["operation"] == "DELETE")
    assert delete_item["deleted_chain_idx"] == 1


@patch("app.services.extraction_service.create_chat_completion")
@patch("app.services.extraction_service.CHUNK_MAX_RETRIES", 1)
@patch("app.services.extraction_service.CHUNK_RETRY_BASE_SEC", 0.01)
def test_parallel_extraction_retry_then_succeed(mock_llm):
    """Chunk should retry on failure and succeed on second attempt."""
    from app.services.extraction_service import ExtractionService
    from app.utils.openai_gateway import TokenAccumulator

    attempt = {"n": 0}
    def mock_response(*args, **kwargs):
        attempt["n"] += 1
        if attempt["n"] == 1:
            raise Exception("Transient error")
        resp = MagicMock()
        resp.choices = [MagicMock()]
        resp.choices[0].message.content = json.dumps({
            "items": [{"text": "Retry başarılı", "item_type": "decision", "owner_name": None, "source_quote": "test"}]
        })
        resp.usage = MagicMock(prompt_tokens=100, completion_tokens=50, total_tokens=150)
        return resp

    mock_llm.side_effect = mock_response

    svc = ExtractionService()
    acc = TokenAccumulator()
    chunks = [{"text": "single chunk", "index": 0, "time_start": None, "time_end": None}]

    items, _ = svc._extract_chunked(chunks, "system prompt", acc)

    assert len(items) == 1
    assert items[0]["text"] == "Retry başarılı"
    assert mock_llm.call_count == 2  # first fail + retry success


# ==================== Total Failure Tests ====================

@patch("app.services.extraction_service.create_chat_completion")
@patch("app.services.extraction_service.CHUNK_MAX_RETRIES", 0)
@patch("app.services.extraction_service.CHUNK_RETRY_BASE_SEC", 0.01)
def test_total_chunk_failure_raises_error(mock_llm):
    """All chunks failing should raise RuntimeError, not return empty list."""
    from app.services.extraction_service import ExtractionService
    from app.utils.openai_gateway import TokenAccumulator

    mock_llm.side_effect = Exception("OpenAI down")

    svc = ExtractionService()
    acc = TokenAccumulator()
    chunks = [
        {"text": "chunk one", "index": 0, "time_start": None, "time_end": None},
        {"text": "chunk two", "index": 1, "time_start": None, "time_end": None},
    ]

    with pytest.raises(RuntimeError, match="All.*chunks failed"):
        svc._extract_chunked(chunks, "system prompt", acc)


# ==================== Line Time Map Tests ====================

def test_build_line_time_map_with_segments():
    """Line time map should map lines to segment timestamps."""
    from app.services.extraction_service import ExtractionService

    svc = ExtractionService()
    lines = ["[Ali]: Merhaba", "[Bob]: Selam", "[Ali]: Devam edelim"]
    segments = [
        {"speaker": "Ali", "text": "Merhaba", "start_time": 0.0, "end_time": 5.0},
        {"speaker": "Bob", "text": "Selam", "start_time": 5.0, "end_time": 8.0},
        {"speaker": "Ali", "text": "Devam edelim", "start_time": 8.0, "end_time": 12.0},
    ]

    time_map = svc._build_line_time_map(lines, segments)

    assert time_map is not None
    assert len(time_map) == 3
    assert time_map[0] == (0.0, 5.0)
    assert time_map[1] == (5.0, 8.0)
    assert time_map[2] == (8.0, 12.0)


def test_build_line_time_map_no_segments():
    """No segments should return None."""
    from app.services.extraction_service import ExtractionService

    svc = ExtractionService()
    result = svc._build_line_time_map(["line1", "line2"], None)
    assert result is None


def test_build_line_time_map_mismatched_counts():
    """More lines than segments should fill with None tuples."""
    from app.services.extraction_service import ExtractionService

    svc = ExtractionService()
    lines = ["line1", "line2", "line3"]
    segments = [{"start_time": 1.0, "end_time": 2.0}]

    time_map = svc._build_line_time_map(lines, segments)

    assert time_map is not None
    assert len(time_map) == 3
    assert time_map[0] == (1.0, 2.0)
    assert time_map[1] == (None, None)
    assert time_map[2] == (None, None)


# ==================== Chunk Timestamp Propagation ====================

def test_chunk_with_segments_gets_time_range():
    """Chunks built from segments should have time_start/time_end."""
    from app.services.extraction_service import ExtractionService

    svc = ExtractionService()

    # Build a long transcript with segments
    lines = []
    segments = []
    for i in range(200):
        lines.append(f"[Speaker{i % 3}]: This is line number {i} with enough text to fill space quickly")
        segments.append({
            "speaker": f"Speaker{i % 3}",
            "text": f"This is line number {i} with enough text to fill space quickly",
            "start_time": float(i * 10),
            "end_time": float(i * 10 + 9),
        })

    transcript = "\n".join(lines)
    chunks = svc._chunk_transcript(transcript, segments)

    # Should have multiple chunks for 200 lines
    assert len(chunks) >= 2

    # First chunk should have time range from segments
    assert chunks[0]["time_start"] is not None
    assert chunks[0]["time_end"] is not None
    assert chunks[0]["time_start"] == 0.0  # first segment starts at 0
    assert chunks[0]["time_end"] > 0


# ==================== Merge Fallback Trigger ====================

@patch("app.services.extraction_service.create_chat_completion")
def test_merge_falls_back_on_llm_error(mock_llm):
    """If merge LLM call fails, fallback merge should be used."""
    from app.services.extraction_service import ExtractionService
    from app.utils.openai_gateway import TokenAccumulator

    mock_llm.side_effect = Exception("Merge LLM timeout")

    svc = ExtractionService()
    acc = TokenAccumulator()
    raw_items = [
        {"text": "Karar A", "item_type": "decision", "source_quote": "q1", "chunk_index": 0},
        {"text": "Karar B", "item_type": "decision", "source_quote": "q2", "chunk_index": 1},
    ]
    chain_items = [{"text": "Eski karar", "item_type": "decision"}]

    # Should not raise — falls back to rule-based merge
    merged = svc._merge_items(raw_items, chain_items, acc)

    assert len(merged) == 2
    assert all(m["operation"] == "ADD" for m in merged)  # fallback = all ADD


# ==================== Merge Telemetry ====================

@patch("app.services.extraction_service.create_chat_completion")
def test_extract_returns_merge_telemetry(mock_service):
    """Full extract should return merge_telemetry in result."""
    from app.services.extraction_service import ExtractionService

    # Mock single-call extraction
    resp = MagicMock()
    resp.choices = [MagicMock()]
    resp.choices[0].message.content = json.dumps({
        "items": [
            {"text": "Karar 1", "item_type": "decision", "owner_name": None, "source_quote": "q1"},
            {"text": "Karar 2", "item_type": "action_item", "owner_name": "Ali", "source_quote": "q2"},
        ]
    })
    resp.usage = MagicMock(prompt_tokens=200, completion_tokens=100, total_tokens=300)
    mock_service.return_value = resp

    svc = ExtractionService()
    result = svc.extract(
        transcript="[Ali]: Kısa toplantı transcript",
        confirmed_items=None,
        chain_items=None,
    )

    assert "merge_telemetry" in result
    assert result["merge_telemetry"]["raw_count"] == 2
    assert result["merge_telemetry"]["merged_count"] == 2
    assert result["merge_telemetry"]["add_count"] == 2
