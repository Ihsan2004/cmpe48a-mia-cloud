"""Embedding generation endpoints — thin HTTP layer.

All business logic lives in services/embedding_service.py.
Routers only: parse request, call service, return response.
"""
import asyncio
from fastapi import APIRouter, HTTPException
from pydantic import BaseModel, Field
from typing import List, Optional

from app.services.embedding_service import embedding_service

router = APIRouter(prefix="/embeddings", tags=["embeddings"])


# ==================== Request/Response Models ====================

class GenerateEmbeddingRequest(BaseModel):
    text: str = Field(..., description="Text to generate embedding for")


class GenerateEmbeddingResponse(BaseModel):
    embedding: List[float]
    model: str
    dimension: int


class GenerateBatchRequest(BaseModel):
    texts: List[str] = Field(..., max_length=100, description="List of texts (max 100)")


class GenerateBatchResponse(BaseModel):
    embeddings: List[Optional[List[float]]]
    model: str
    count: int
    # Issue #39 — propagate token usage so the Go caller can record it
    # into usage_logs + bump the per-user quota counter. Optional so
    # the field stays backward compatible with older clients; zero
    # values mean "no usage was reported" (no fake billing).
    usage_prompt_tokens: int = 0
    usage_completion_tokens: int = 0
    usage_total_tokens: int = 0


class BackfillRequest(BaseModel):
    batch_size: int = Field(default=50, le=100, description="Batch size (max 100)")
    limit: int = Field(default=0, description="Max blocks to process (0 = all)")
    dry_run: bool = Field(default=False, description="Only count, don't process")


class BackfillResponse(BaseModel):
    total_without_embedding: int
    processed: int
    success_count: int
    fail_count: int
    dry_run: bool


# ==================== Endpoints ====================

@router.post("/generate", response_model=GenerateEmbeddingResponse)
async def generate_embedding(request: GenerateEmbeddingRequest):
    """Generate embedding for a single text."""
    embedding = embedding_service.generate_embedding(request.text)

    if embedding is None:
        raise HTTPException(
            status_code=400,
            detail="Text too short or embedding generation failed",
        )

    return GenerateEmbeddingResponse(
        embedding=embedding,
        model=embedding_service.MODEL,
        dimension=len(embedding),
    )


@router.post("/generate-batch", response_model=GenerateBatchResponse)
async def generate_batch(request: GenerateBatchRequest):
    """Generate embeddings for multiple texts in batch."""
    if len(request.texts) > 100:
        raise HTTPException(status_code=400, detail="Maximum 100 texts per batch request")

    embeddings, usage = embedding_service.generate_embeddings_batch(request.texts)

    # usage is the dict shape produced by TokenAccumulator.to_dict()
    # — same key names the SummarizeResponse / Go side already
    # understand. Falling back to 0 keeps the response well-typed
    # even if the service returns an empty dict (no batches issued).
    return GenerateBatchResponse(
        embeddings=embeddings,
        model=embedding_service.MODEL,
        count=len(embeddings),
        usage_prompt_tokens=usage.get("usage_prompt_tokens", 0),
        usage_completion_tokens=usage.get("usage_completion_tokens", 0),
        usage_total_tokens=usage.get("usage_total_tokens", 0),
    )


@router.post("/backfill", response_model=BackfillResponse)
async def backfill_embeddings(request: BackfillRequest):
    """Backfill embeddings for all chat blocks that don't have one."""
    try:
        result = embedding_service.backfill(
            batch_size=request.batch_size,
            limit=request.limit,
            dry_run=request.dry_run,
        )

        # Rate limiting between batches (service is sync, so we sleep here)
        if not request.dry_run and result["processed"] > 0:
            await asyncio.sleep(0)  # yield control

        return BackfillResponse(**result)
    except Exception as e:
        raise HTTPException(status_code=500, detail=f"Backfill failed: {str(e)}")
