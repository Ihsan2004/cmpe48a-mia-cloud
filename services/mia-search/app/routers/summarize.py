"""Summarization routers — thin HTTP layer.

All business logic lives in services/summarize_service.py.
Routers only: parse request, call service, return response.
"""
from fastapi import APIRouter, HTTPException
from app.models.schemas import (
    CatchUpRequest, CatchUpResponse,
    SummarizeRequest, SummarizeResponse,
    CheckpointRequest, CheckpointResponse,
    AgentCatchMeUpResponse,
)
from app.services.summarize_service import SummarizeService

router = APIRouter()
summarize_service = SummarizeService()


@router.post("/catch-up", response_model=CatchUpResponse)
async def catch_me_up(request: CatchUpRequest):
    """Progressive summarization for late joiners."""
    try:
        result = summarize_service.catch_me_up(
            meeting_id=request.meeting_id,
            meeting_url=request.meeting_url,
            progressive_summary=request.progressive_summary,
            last_summarized_at=request.last_summarized_at,
        )
        return CatchUpResponse(**result)
    except ValueError as e:
        raise HTTPException(status_code=404, detail=str(e))
    except Exception as e:
        raise HTTPException(status_code=500, detail=f"Catch-up failed: {str(e)}")


@router.post("/catch-me-up")
async def agent_catch_me_up(request: dict):
    """Agent catch-me-up — processes pre-prepared data from Go backend."""
    try:
        data = request.get("data", {})
        system_config = request.get("system_config", {})

        result = summarize_service.agent_catch_me_up(
            existing_summary=data.get("existing_summary", ""),
            recent_transcript=data.get("recent_transcript", ""),
            instructions=system_config.get("instructions", ""),
            language=system_config.get("language", "auto"),
        )
        return AgentCatchMeUpResponse(**result)
    except Exception as e:
        print(f"Agent Catch Me Up failed: {e}")
        raise HTTPException(status_code=500, detail=f"Agent catch-me-up failed: {str(e)}")


@router.post("/summarize", response_model=SummarizeResponse)
async def summarize_meeting(request: SummarizeRequest):
    """Comprehensive summarization of the entire meeting."""
    try:
        if not request.meeting_id:
            raise HTTPException(status_code=400, detail="meeting_id is required")

        result = summarize_service.summarize(
            meeting_id=request.meeting_id,
            meeting_url=request.meeting_url,
            force_raw_segments=bool(request.force_raw_segments),
            source_reason=request.source_reason,
            language=request.language,
        )

        return SummarizeResponse(
            success=True,
            summary=result["summary"],
            tldr=result.get("tldr"),
            detailed_content=result.get("detailed_content"),
            email_draft=result.get("email_draft"),
            key_topics=result["key_topics"],
            key_topics_structured=result.get("key_topics_structured"),
            action_items=result["action_items"],
            action_items_structured=result.get("action_items_structured"),
            participants=result["participants"],
            total_chunks=result.get("total_chunks", 0),
            processing_status=result.get("processing_status"),
            coverage_ratio=result.get("coverage_ratio"),
            successful_chunks=result.get("successful_chunks"),
            failed_chunks=result.get("failed_chunks"),
            chunk_telemetry=result.get("chunk_telemetry"),
            topic_omissions=result.get("topic_omissions"),
            warnings=result.get("warnings"),
            summary_source=result.get("summary_source"),
            usage_prompt_tokens=result.get("usage_prompt_tokens"),
            usage_completion_tokens=result.get("usage_completion_tokens"),
            usage_total_tokens=result.get("usage_total_tokens"),
        )
    except ValueError as e:
        raise HTTPException(status_code=404, detail=str(e))
    except HTTPException:
        raise
    except Exception as e:
        raise HTTPException(status_code=500, detail=f"Summarization failed: {str(e)}")


@router.post("/summarize/checkpoint", response_model=CheckpointResponse)
async def create_checkpoint(request: CheckpointRequest):
    """Generate an incremental checkpoint summary."""
    try:
        result = summarize_service.create_checkpoint(
            meeting_id=request.meeting_id,
            meeting_url=request.meeting_url,
            current_summary=request.current_summary,
            last_summarized_at=request.last_summarized_at,
            language=request.language,
        )
        return CheckpointResponse(**result)
    except Exception as e:
        print(f"Checkpoint failed: {e}")
        return CheckpointResponse(
            success=False,
            summary=request.current_summary or "",
            timestamp=__import__("datetime").datetime.utcnow().isoformat(),
        )
