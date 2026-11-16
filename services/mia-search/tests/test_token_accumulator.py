"""Token accumulator + NLPService._track_usage wiring tests (issue #39).

Covers:
  - TokenAccumulator is thread-safe (concurrent add() converges exactly).
  - NLPService._track_usage is a no-op when no comprehensive summary is
    in flight (standalone progressive / catch_me_up calls don't pollute
    the next comprehensive call's accumulator).
  - When _comprehensive_acc is set, every wired call site contributes —
    correction phase + map phase + reduce phase all show up in the
    final usage_*_tokens output, NOT just the reduce phase (the
    pre-fix bug).
"""
import threading
from types import SimpleNamespace
from unittest.mock import MagicMock, patch

import pytest

from app.utils.openai_gateway import TokenAccumulator, TokenUsage


# ==================== TokenAccumulator thread-safety ====================

def test_accumulator_concurrent_add_converges_exactly():
    """200 worker goroutines, 100 add() each, every add = (10, 5, 15).

    Without the internal lock, Python's int += would lose increments
    under contention (LOAD_ATTR / BINARY_ADD / STORE_ATTR isn't atomic).
    """
    acc = TokenAccumulator()
    usage = TokenUsage(prompt_tokens=10, completion_tokens=5, total_tokens=15)

    def worker():
        for _ in range(100):
            acc.add(usage)

    threads = [threading.Thread(target=worker) for _ in range(200)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()

    expected_calls = 200 * 100
    snap = acc.to_dict()
    assert snap["usage_prompt_tokens"] == expected_calls * 10
    assert snap["usage_completion_tokens"] == expected_calls * 5
    assert snap["usage_total_tokens"] == expected_calls * 15


# ==================== _track_usage no-op when no comprehensive in flight ===

def _fake_response(prompt=100, completion=50, total=None, model="gpt-5.4", content="{}"):
    """Mimic openai.ChatCompletion shape — both .usage (for extract_usage)
    and .choices[0].message.content (which every call site accesses
    after create_chat_completion returns)."""
    if total is None:
        total = prompt + completion
    return SimpleNamespace(
        model=model,
        usage=SimpleNamespace(
            prompt_tokens=prompt,
            completion_tokens=completion,
            total_tokens=total,
        ),
        choices=[SimpleNamespace(message=SimpleNamespace(content=content))],
    )


def test_track_usage_noop_outside_comprehensive():
    """Standalone endpoints (progressive summary, catch_me_up) call
    _track_usage as part of the shared LLM site, but should NOT have
    their tokens accumulated anywhere — the caller-side already
    extracts usage on its own."""
    from app.services.nlp_service import NLPService

    svc = NLPService()
    assert svc._comprehensive_acc is None

    # Call _track_usage — must be silent no-op.
    svc._track_usage(_fake_response(), "gpt-5.4")

    # _comprehensive_acc is still None; nothing leaked.
    assert svc._comprehensive_acc is None


def test_track_usage_accumulates_when_comprehensive_active():
    """When generate_comprehensive_summary has set the accumulator,
    every _track_usage call lands in it."""
    from app.services.nlp_service import NLPService

    svc = NLPService()
    svc._comprehensive_acc = TokenAccumulator()

    # Simulate 5 LLM responses across phases.
    for _ in range(5):
        svc._track_usage(_fake_response(prompt=100, completion=50), "gpt-5.4")

    snap = svc._comprehensive_acc.to_dict()
    assert snap["usage_prompt_tokens"] == 500
    assert snap["usage_completion_tokens"] == 250
    assert snap["usage_total_tokens"] == 750


# ==================== Wired call sites feed the accumulator =================

def test_all_phases_contribute_to_comprehensive_acc():
    """Patch create_chat_completion so every call returns 100/50 tokens.
    Drive each of the wired internal methods directly and assert the
    pipeline-wide accumulator captures ALL phases — not just reduce
    (which was the pre-fix bug)."""
    from app.services.nlp_service import NLPService

    svc = NLPService()
    svc._comprehensive_acc = TokenAccumulator()

    fake = _fake_response(prompt=100, completion=50)

    with patch("app.services.nlp_service.create_chat_completion", return_value=fake) as mock_call, \
         patch("app.services.nlp_service.parse_json_with_retry") as mock_json:
        # parse_json_with_retry stubs vary per call site — set up
        # reasonable returns for the methods we'll invoke.
        mock_json.side_effect = [
            {"corrections": []},                  # _prescan_terminology
            {"audit": {}, "executive_summary": "", "topic_title": "T"},  # _analyze_topic_segment_once
        ]

        # Phase: correction pre-scan
        svc._prescan_terminology("a" * 6500)  # long enough to skip the < sample_size early return
        # Phase: per-chunk correction
        svc._correct_single_chunk("x" * 200, dynamic_glossary="")
        # Phase: master instruction (non-generic title → real call)
        svc.generate_master_instruction("Q3 Strategy Review", ["Ali", "Veli"], 60)
        # Phase: map
        svc._analyze_topic_segment_once("chunk text", "instr", 0, "gpt-5.4", reduced_schema=False)

    # 4 distinct phases × 150 total = 600 total tokens.
    snap = svc._comprehensive_acc.to_dict()
    assert mock_call.call_count == 4
    assert snap["usage_total_tokens"] == 600, (
        f"Expected ALL phases (correction pre-scan + per-chunk correction + "
        f"master instruction + map) to contribute to acc; got {snap}"
    )
    assert snap["usage_prompt_tokens"] == 400
    assert snap["usage_completion_tokens"] == 200
