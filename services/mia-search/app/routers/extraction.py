"""Decision extraction endpoint — thin HTTP layer.

All business logic lives in services/extraction_service.py.
Router only: parse request, call service, return response.
"""
from fastapi import APIRouter, HTTPException
from app.models.schemas import ExtractionRequest, ExtractionResponse
from app.services.extraction_service import ExtractionService

router = APIRouter()
extraction_service = ExtractionService()


@router.post("/extract", response_model=ExtractionResponse)
async def extract_decisions(request: ExtractionRequest):
    """Extract decisions, action items, and open topics from a meeting transcript."""
    try:
        if not request.transcript or not request.transcript.strip():
            return ExtractionResponse(
                success=True,
                items=[],
            )

        result = extraction_service.extract(
            transcript=request.transcript,
            segments=[s.model_dump() for s in request.segments] if request.segments else None,
            confirmed_items=request.confirmed_items,
            chain_items=request.chain_items,
            language=request.language,
        )

        return ExtractionResponse(
            success=True,
            items=result["items"],
            usage_prompt_tokens=result["usage"].get("usage_prompt_tokens"),
            usage_completion_tokens=result["usage"].get("usage_completion_tokens"),
            usage_total_tokens=result["usage"].get("usage_total_tokens"),
            chunk_telemetry=result.get("chunk_telemetry"),
            merge_telemetry=result.get("merge_telemetry"),
        )
    except Exception as e:
        print(f"Extraction failed: {e}")
        raise HTTPException(status_code=500, detail=f"Extraction failed: {str(e)}")
