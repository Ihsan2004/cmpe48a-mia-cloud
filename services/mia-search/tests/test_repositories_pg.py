"""Unit tests for PostgreSQL (psycopg) repositories in mia-search."""
from contextlib import contextmanager
from datetime import datetime, timedelta, timezone
import uuid

from app.repositories.embedding_repo import EmbeddingRepository, _format_pgvector
from app.repositories.transcript_repo import TranscriptRepository


class FakeCursor:
    def __init__(self, rows=None, one=None):
        self.rows = rows if rows is not None else []
        self.one = one
        self.executed = []

    def execute(self, query, params=None):
        self.executed.append((query, params))

    def fetchall(self):
        return self.rows

    def fetchone(self):
        return self.one

    def __enter__(self):
        return self

    def __exit__(self, *args):
        return False


class FakeConn:
    def __init__(self, cursor: FakeCursor):
        self.cur = cursor
        self.committed = False

    def cursor(self):
        return self.cur

    def commit(self):
        self.committed = True


def _fake_db(cursor: FakeCursor):
    conn = FakeConn(cursor)

    @contextmanager
    def _cm():
        yield conn

    return _cm, conn


def test_filter_by_time_handles_native_datetime():
    now = datetime.now(timezone.utc)
    segments = [
        {"text": "old", "created_at": now - timedelta(minutes=20)},
        {"text": "recent_dt", "created_at": now - timedelta(minutes=2)},
        {"text": "recent_iso", "created_at": (now - timedelta(minutes=1)).isoformat()},
    ]
    filtered = TranscriptRepository._filter_by_time(segments, minutes_back=5, created_after=None)
    assert [s["text"] for s in filtered] == ["recent_dt", "recent_iso"]


def test_format_segments_serializes_uuid_and_datetime():
    seg_id = uuid.uuid4()
    meeting_id = uuid.uuid4()
    ts = datetime(2026, 11, 16, 12, 0, tzinfo=timezone.utc)
    out = TranscriptRepository.format_segments_for_nlp([
        {"id": seg_id, "meeting_id": meeting_id, "text": "hello", "created_at": ts}
    ])
    assert out[0]["id"] == str(seg_id)
    assert out[0]["meeting_id"] == str(meeting_id)
    assert out[0]["created_at"] == ts.isoformat()


def test_fetch_segments_skips_invalid_uuid_when_url_present(monkeypatch):
    cur = FakeCursor(rows=[{"id": uuid.uuid4(), "text": "hi", "created_at": datetime.now(timezone.utc)}])
    cm, _ = _fake_db(cur)
    monkeypatch.setattr("app.repositories.transcript_repo.get_db_connection", cm)

    repo = TranscriptRepository()
    rows = repo.fetch_segments("not-a-uuid", meeting_url="https://meet.google.com/abc-defg-hij")
    assert len(rows) == 1
    query, params = cur.executed[0]
    assert "meeting_id = %s::uuid" not in query
    assert "meeting_url = %s" in query
    assert "https://meet.google.com/abc-defg-hij" in params


def test_embedding_repo_format_and_save(monkeypatch):
    assert _format_pgvector([0.1, 0.25, -0.5]) == "[0.1,0.25,-0.5]"

    cur = FakeCursor()
    cm, conn = _fake_db(cur)
    monkeypatch.setattr("app.repositories.embedding_repo.get_db_connection", cm)

    repo = EmbeddingRepository()
    block_id = str(uuid.uuid4())
    assert repo.save_embedding(block_id, [0.5, 0.5]) is True
    assert conn.committed is True
    query, params = cur.executed[0]
    assert "SET embedding = %s::vector" in query
    assert params[0] == "[0.5,0.5]"
    assert params[3] == block_id


def test_embedding_repo_count_uses_is_distinct_from(monkeypatch):
    cur = FakeCursor(one={"cnt": 7})
    cm, _ = _fake_db(cur)
    monkeypatch.setattr("app.repositories.embedding_repo.get_db_connection", cm)

    repo = EmbeddingRepository()
    assert repo.count_blocks_without_embedding() == 7
    assert "IS DISTINCT FROM 'too_short'" in cur.executed[0][0]
