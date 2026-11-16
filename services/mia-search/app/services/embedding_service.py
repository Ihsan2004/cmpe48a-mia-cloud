"""Embedding generation service for semantic search."""
from typing import List, Optional, Tuple
import logging
from app.utils.openai_gateway import create_embedding, extract_usage, TokenAccumulator
from app.repositories.embedding_repo import EmbeddingRepository

logger = logging.getLogger(__name__)


class EmbeddingService:
    """Service for generating text embeddings using OpenAI."""

    MODEL = "text-embedding-3-small"  # 1536 dimensions, $0.02/1M tokens
    MIN_TEXT_LENGTH = 10  # Skip embedding for very short texts

    def __init__(self, embedding_repo: EmbeddingRepository = None):
        self.repo = embedding_repo or EmbeddingRepository()

    def generate_embedding(self, text: str) -> Optional[List[float]]:
        """
        Generate embedding for a single text.

        Args:
            text: Text content to embed

        Returns:
            List of floats (1536 dimensions) or None if failed/too short
        """
        if len(text.strip()) < self.MIN_TEXT_LENGTH:
            logger.warning(f"Text too short for embedding: {len(text)} chars")
            return None

        try:
            response = create_embedding(
                model=self.MODEL,
                input=text
            )
            usage = extract_usage(response, self.MODEL)
            if usage.total_tokens > 0:
                logger.debug(f"Embedding usage: {usage.total_tokens} tokens")
            return response.data[0].embedding
        except Exception as e:
            logger.error(f"Embedding generation failed: {e}")
            return None

    def generate_embeddings_batch(
        self,
        texts: List[str],
        batch_size: int = 100
    ) -> Tuple[List[Optional[List[float]]], dict]:
        """
        Generate embeddings for multiple texts in batches.

        Args:
            texts: List of text strings to embed
            batch_size: Number of texts per API request (max 100)

        Returns:
            Tuple of (list of embeddings, usage_dict)
        """
        results = [None] * len(texts)
        acc = TokenAccumulator()

        # Filter valid texts and track their original indices
        valid_indices = []
        valid_texts = []
        for idx, text in enumerate(texts):
            if len(text.strip()) >= self.MIN_TEXT_LENGTH:
                valid_indices.append(idx)
                valid_texts.append(text)

        if not valid_texts:
            return results, acc.to_dict()

        for i in range(0, len(valid_texts), batch_size):
            batch = valid_texts[i:i+batch_size]
            batch_indices = valid_indices[i:i+batch_size]

            try:
                response = create_embedding(
                    model=self.MODEL,
                    input=batch
                )
                acc.add(extract_usage(response, self.MODEL))
                for j, r in enumerate(response.data):
                    results[batch_indices[j]] = r.embedding
                logger.info(f"Generated {len(batch)} embeddings (batch {i//batch_size + 1})")
            except Exception as e:
                logger.error(f"Batch embedding failed for batch {i//batch_size + 1}: {e}")

        return results, acc.to_dict()

    def backfill(self, batch_size: int, limit: int, dry_run: bool) -> dict:
        """
        Backfill embeddings for all chat blocks that don't have one.

        Args:
            batch_size: Number of blocks per batch
            limit: Max blocks to process (0 = all)
            dry_run: Only count, don't process

        Returns:
            Dict with total_without_embedding, processed, success_count, fail_count, dry_run
        """
        total_count = self.repo.count_blocks_without_embedding()
        logger.info(f"\U0001f4ca Found {total_count} blocks without embedding")

        if total_count == 0 or dry_run:
            return {
                "total_without_embedding": total_count,
                "processed": 0,
                "success_count": 0,
                "fail_count": 0,
                "dry_run": dry_run,
            }

        processed = 0
        success_count = 0
        fail_count = 0
        process_limit = limit if limit > 0 else total_count

        while processed < process_limit:
            fetch_size = min(batch_size, process_limit - processed)
            blocks = self.repo.fetch_blocks_without_embedding(fetch_size)

            if not blocks:
                break

            texts = [b["text"] for b in blocks]
            embeddings, _batch_usage = self.generate_embeddings_batch(texts)

            for i, embedding in enumerate(embeddings):
                if embedding is None:
                    self.repo.mark_too_short(blocks[i]["id"])
                    fail_count += 1
                    continue

                if self.repo.save_embedding(blocks[i]["id"], embedding):
                    success_count += 1
                else:
                    fail_count += 1

            processed += len(blocks)
            logger.info(
                f"\u2705 Progress: {processed}/{process_limit} - "
                f"Success: {success_count}, Failed: {fail_count}"
            )

        logger.info(f"\U0001f389 Backfill completed: {success_count} success, {fail_count} failed")

        return {
            "total_without_embedding": total_count,
            "processed": processed,
            "success_count": success_count,
            "fail_count": fail_count,
            "dry_run": False,
        }


# Singleton instance
embedding_service = EmbeddingService()
