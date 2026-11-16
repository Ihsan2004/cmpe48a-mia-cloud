"""Pydantic models for request/response schemas."""
from pydantic import BaseModel, Field
from typing import List, Optional


# ==================== Catch-Up Models ====================

class CatchUpRequest(BaseModel):
    meeting_id: str = Field(..., description="UUID of the meeting")
    meeting_url: Optional[str] = Field(None, description="Meeting URL for alternative matching")
    progressive_summary: Optional[str] = Field(None, description="Previous summary checkpoint")
    last_summarized_at: Optional[str] = Field(None, description="Timestamp of last checkpoint")


class CheckpointRequest(BaseModel):
    meeting_id: str = Field(..., description="UUID of the meeting")
    meeting_url: Optional[str] = Field(None, description="Meeting URL")
    current_summary: Optional[str] = Field(None, description="Existing summary")
    last_summarized_at: Optional[str] = Field(None, description="Timestamp of last checkpoint")
    language: str = Field("auto", description="Summary language: 'auto' (match transcript), 'tr', 'en'")


class CheckpointResponse(BaseModel):
    success: bool
    summary: str
    timestamp: str
    usage_prompt_tokens: Optional[int] = None
    usage_completion_tokens: Optional[int] = None
    usage_total_tokens: Optional[int] = None


class CatchUpResponse(BaseModel):
    success: bool
    summary: str
    key_points: List[str]
    segments_analyzed: int
    timestamp: str
    usage_prompt_tokens: Optional[int] = None
    usage_completion_tokens: Optional[int] = None
    usage_total_tokens: Optional[int] = None


# ==================== Agent Catch Me Up (Go Backend Integration) ====================

class AgentCatchMeUpResponse(BaseModel):
    """Response for agent catch-me-up."""
    success: bool
    summary: str
    timestamp: str
    usage_prompt_tokens: Optional[int] = None
    usage_completion_tokens: Optional[int] = None
    usage_total_tokens: Optional[int] = None


# ==================== Extraction Models ====================

class TranscriptSegment(BaseModel):
    speaker: str
    text: str
    start_time: Optional[float] = None
    end_time: Optional[float] = None


class ExtractionRequest(BaseModel):
    meeting_id: str = Field(..., description="UUID of the meeting")
    transcript: str = Field(..., description="Full transcript (speaker-attributed lines)")
    segments: Optional[List[TranscriptSegment]] = Field(None, description="Structured segments with timing")
    confirmed_items: Optional[List[str]] = Field(None, description="Already confirmed item texts for idempotency")
    chain_items: Optional[List[dict]] = Field(None, description="Confirmed items from chain history")
    language: str = Field("tr", description="Output language")


class ExtractedItemResponse(BaseModel):
    text: str
    item_type: str  # decision, action_item, open_topic
    owner_name: Optional[str] = None
    source_quote: str
    context: Optional[str] = None
    alternatives_considered: Optional[str] = None
    timestamp_start: Optional[float] = None
    timestamp_end: Optional[float] = None
    operation: str = "ADD"  # ADD, UPDATE, DELETE (NOOP items are filtered out)
    updated_chain_idx: Optional[int] = None  # index into chain_items for UPDATE
    deleted_chain_idx: Optional[int] = None  # index into chain_items for DELETE


class ChunkTelemetry(BaseModel):
    chunk_index: int
    status: str
    latency_ms: int
    retry_count: int
    items_extracted: int
    model_used: str = "gpt-5.4"


class MergeTelemetry(BaseModel):
    raw_count: int = 0
    merged_count: int = 0
    add_count: int = 0
    update_count: int = 0
    delete_count: int = 0
    noop_count: int = 0
    merge_latency_ms: int = 0


class ExtractionResponse(BaseModel):
    success: bool
    items: List[ExtractedItemResponse]
    usage_prompt_tokens: Optional[int] = None
    usage_completion_tokens: Optional[int] = None
    usage_total_tokens: Optional[int] = None
    chunk_telemetry: Optional[List[ChunkTelemetry]] = None
    merge_telemetry: Optional[MergeTelemetry] = None


# ==================== Summarize Models ====================

class ActionItemResponse(BaseModel):
    """Structured action item."""
    description: str = Field(..., description="Action item description")
    assignee_name: Optional[str] = Field(None, description="Assigned person's name")
    commitment_level: Optional[str] = Field("candidate", description="committed/candidate")
    priority: Optional[str] = Field(None, description="Priority level if available")
    deadline: Optional[str] = Field(None, description="ISO format deadline if mentioned")
    is_optional: Optional[bool] = Field(None, description="True if optional/nice-to-have")
    tags: Optional[List[str]] = Field(None, description="Optional topic tags")
    context: Optional[str] = Field(None, description="Why this action was assigned")
    topic_indices: Optional[List[int]] = Field(None, description="Matched key topic indices")


class SummarizeRequest(BaseModel):
    meeting_id: str = Field(..., description="UUID of the meeting")
    meeting_url: Optional[str] = Field(None, description="Meeting URL for alternative matching")
    force_raw_segments: bool = Field(False, description="Bypass chat blocks, use raw segments")
    source_reason: Optional[str] = Field(None, description="Reason for forced source selection")
    language: str = Field("auto", description="Summary language: 'auto', 'tr', 'en'")


class KeyTopicResponse(BaseModel):
    """Structured key topic."""
    topic: str = Field(..., description="Topic name/title")
    topic_order: int = Field(..., description="Stable chronological topic order")


class ChunkTelemetryResponse(BaseModel):
    chunk_index: int
    status: str
    latency_ms: int
    retry_count: int
    error_code: Optional[str] = None
    model_used: Optional[str] = None
    attempt_stage: Optional[str] = None


class TopicOmissionResponse(BaseModel):
    term: str
    frequency: int


class SummarizeResponse(BaseModel):
    success: bool
    summary: str
    tldr: Optional[str] = None
    detailed_content: Optional[str] = None
    email_draft: Optional[str] = None
    key_topics: List[str]
    key_topics_structured: Optional[List[KeyTopicResponse]] = None
    action_items: List[str]
    action_items_structured: Optional[List[ActionItemResponse]] = None
    participants: List[str]
    total_chunks: int
    processing_status: Optional[str] = None
    coverage_ratio: Optional[float] = None
    successful_chunks: Optional[int] = None
    failed_chunks: Optional[int] = None
    chunk_telemetry: Optional[List[ChunkTelemetryResponse]] = None
    topic_omissions: Optional[List[TopicOmissionResponse]] = None
    warnings: Optional[List[str]] = None
    summary_source: Optional[str] = None
    usage_prompt_tokens: Optional[int] = None
    usage_completion_tokens: Optional[int] = None
    usage_total_tokens: Optional[int] = None
