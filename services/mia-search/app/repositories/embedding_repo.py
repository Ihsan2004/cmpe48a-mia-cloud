"""Repository for embedding-related database operations.

Handles reading/writing pgvector embeddings on transcript_chat_blocks using psycopg.
"""
from datetime import datetime, timezone
import logging
from typing import Any, Dict, List
import uuid

from app.db import get_db_connection

logger = logging.getLogger(__name__)


def _format_pgvector(values: List[float]) -> str:
    """Serialize float list to pgvector literal string '[x,y,...]'."""
    return "[" + ",".join(str(float(v)) for v in values) + "]"


def _is_valid_uuid(value: str) -> bool:
    try:
        uuid.UUID(str(value))
        return True
    except (ValueError, TypeError, AttributeError):
        return False


class EmbeddingRepository:
    """Data access for chat block embeddings."""

    def count_blocks_without_embedding(self) -> int:
        """Count chat blocks that don't have an embedding (excluding too_short)."""
        query = """
            SELECT COUNT(*) AS cnt
            FROM public.transcript_chat_blocks
            WHERE embedding IS NULL
              AND embedding_model IS DISTINCT FROM 'too_short'
        """
        with get_db_connection() as conn:
            with conn.cursor() as cur:
                cur.execute(query)
                row = cur.fetchone()
                return int(row["cnt"]) if row and row.get("cnt") is not None else 0

    def fetch_blocks_without_embedding(self, limit: int) -> List[Dict[str, Any]]:
        """Fetch a batch of blocks that need embeddings."""
        query = """
            SELECT id::text AS id, text
            FROM public.transcript_chat_blocks
            WHERE embedding IS NULL
              AND embedding_model IS DISTINCT FROM 'too_short'
            ORDER BY created_at ASC
            LIMIT %s
        """
        with get_db_connection() as conn:
            with conn.cursor() as cur:
                cur.execute(query, (int(limit),))
                return cur.fetchall() or []

    def save_embedding(self, block_id: str, embedding: List[float]) -> bool:
        """Save embedding vector to a chat block."""
        if not _is_valid_uuid(block_id):
            logger.error(f"Invalid block_id UUID: {block_id}")
            return False
        try:
            vector_literal = _format_pgvector(embedding)
            now_utc = datetime.now(timezone.utc)
            query = """
                UPDATE public.transcript_chat_blocks
                SET embedding = %s::vector,
                    embedding_model = %s,
                    embedding_generated_at = %s
                WHERE id = %s::uuid
            """
            with get_db_connection() as conn:
                with conn.cursor() as cur:
                    cur.execute(
                        query,
                        (vector_literal, "text-embedding-3-small", now_utc, str(block_id)),
                    )
                conn.commit()
            return True
        except Exception as e:
            logger.error(f"Failed to save embedding for {block_id}: {e}")
            return False

    def mark_too_short(self, block_id: str) -> None:
        """Mark a block as too short for embedding so it won't be re-fetched."""
        if not _is_valid_uuid(block_id):
            return
        try:
            query = """
                UPDATE public.transcript_chat_blocks
                SET embedding_model = 'too_short'
                WHERE id = %s::uuid
            """
            with get_db_connection() as conn:
                with conn.cursor() as cur:
                    cur.execute(query, (str(block_id),))
                conn.commit()
        except Exception as e:
            logger.warning(f"Failed to mark block {block_id} as too_short: {e}")

    def check_meeting_has_embeddings(self, meeting_id: str) -> bool:
        """Check if a meeting has at least one chat block with an embedding."""
        if not _is_valid_uuid(meeting_id):
            return False
        try:
            query = """
                SELECT id
                FROM public.transcript_chat_blocks
                WHERE meeting_id = %s::uuid
                  AND embedding IS NOT NULL
                LIMIT 1
            """
            with get_db_connection() as conn:
                with conn.cursor() as cur:
                    cur.execute(query, (str(meeting_id),))
                    return cur.fetchone() is not None
        except Exception as e:
            print(f"⚠️ Error checking embeddings: {e}")
            return False

    def semantic_search(
        self,
        meeting_id: str,
        query_embedding: List[float],
        match_count: int = 10,
        threshold: float = 0.7,
    ) -> List[Dict[str, Any]]:
        """Perform pgvector cosine similarity search on transcript_chat_blocks."""
        if not _is_valid_uuid(meeting_id):
            return []
        try:
            vector_literal = _format_pgvector(query_embedding)
            query = """
                SELECT id::text AS id,
                       meeting_id::text AS meeting_id,
                       speaker,
                       text,
                       start_time,
                       end_time,
                       1 - (embedding <=> %s::vector) AS similarity
                FROM public.transcript_chat_blocks
                WHERE meeting_id = %s::uuid
                  AND embedding IS NOT NULL
                  AND (1 - (embedding <=> %s::vector)) >= %s
                ORDER BY embedding <=> %s::vector ASC
                LIMIT %s
            """
            with get_db_connection() as conn:
                with conn.cursor() as cur:
                    cur.execute(
                        query,
                        (
                            vector_literal,
                            str(meeting_id),
                            vector_literal,
                            float(threshold),
                            vector_literal,
                            int(match_count),
                        ),
                    )
                    rows = cur.fetchall() or []

            if rows:
                print(f"✅ Semantic search found {len(rows)} matching chat blocks")
                return rows

            print("⚠️ Semantic search returned no results")
            return []
        except Exception as e:
            print(f"❌ Semantic search on chat blocks failed: {e}")
            return []
