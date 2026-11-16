"""Repository for embedding-related database operations.

Handles reading/writing embeddings to transcript_chat_blocks table.
"""
import logging
from typing import List, Dict, Any, Optional
from datetime import datetime, timezone

from app.db import get_supabase_client

logger = logging.getLogger(__name__)


class EmbeddingRepository:
    """Data access for chat block embeddings."""

    def count_blocks_without_embedding(self) -> int:
        """Count chat blocks that don't have an embedding (excluding too_short)."""
        client = get_supabase_client()
        count_resp = (
            client.table("transcript_chat_blocks")
            .select("id", count="exact")
            .is_("embedding", "null")
            .neq("embedding_model", "too_short")
            .limit(1)
            .execute()
        )
        return count_resp.count if count_resp.count is not None else 0

    def fetch_blocks_without_embedding(self, limit: int) -> List[Dict[str, Any]]:
        """Fetch a batch of blocks that need embeddings."""
        client = get_supabase_client()
        batch_resp = (
            client.table("transcript_chat_blocks")
            .select("id, text")
            .is_("embedding", "null")
            .neq("embedding_model", "too_short")
            .order("created_at", desc=False)
            .limit(limit)
            .execute()
        )
        return batch_resp.data if batch_resp.data else []

    def save_embedding(self, block_id: str, embedding: List[float]) -> bool:
        """Save embedding vector to a chat block."""
        try:
            client = get_supabase_client()
            client.table("transcript_chat_blocks").update({
                "embedding": embedding,
                "embedding_model": "text-embedding-3-small",
                "embedding_generated_at": datetime.now(timezone.utc).isoformat(),
            }).eq("id", block_id).execute()
            return True
        except Exception as e:
            logger.error(f"Failed to save embedding for {block_id}: {e}")
            return False

    def mark_too_short(self, block_id: str) -> None:
        """Mark a block as too short for embedding so it won't be re-fetched."""
        try:
            client = get_supabase_client()
            client.table("transcript_chat_blocks").update({
                "embedding_model": "too_short",
            }).eq("id", block_id).execute()
        except Exception:
            pass

    def check_meeting_has_embeddings(self, meeting_id: str) -> bool:
        """Check if a meeting has at least one chat block with an embedding."""
        try:
            client = get_supabase_client()
            response = (
                client.table("transcript_chat_blocks")
                .select("id")
                .eq("meeting_id", meeting_id)
                .not_.is_("embedding", "null")
                .limit(1)
                .execute()
            )
            return len(response.data) > 0
        except Exception as e:
            print(f"\u26a0\ufe0f Error checking embeddings: {e}")
            return False

    def semantic_search(
        self,
        meeting_id: str,
        query_embedding: List[float],
        match_count: int = 10,
        threshold: float = 0.7,
    ) -> List[Dict[str, Any]]:
        """Perform semantic search on transcript_chat_blocks using RPC."""
        try:
            client = get_supabase_client()
            result = client.rpc(
                "match_chat_blocks",
                {
                    "query_embedding": query_embedding,
                    "meeting_id": meeting_id,
                    "match_threshold": threshold,
                    "match_count": match_count,
                },
            ).execute()

            if result.data:
                print(f"\u2705 Semantic search found {len(result.data)} matching chat blocks")
                return result.data

            print("\u26a0\ufe0f Semantic search returned no results")
            return []
        except Exception as e:
            print(f"\u274c Semantic search on chat blocks failed: {e}")
            return []
