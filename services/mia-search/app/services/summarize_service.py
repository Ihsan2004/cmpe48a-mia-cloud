"""Summarization service — orchestrates transcript fetching, source selection, and NLP calls.

All business logic that was previously in routers/summarize.py lives here.
Router is now a thin HTTP layer.
"""
from datetime import datetime
from typing import Dict, Any, Optional, Tuple, List

from app.repositories.transcript_repo import TranscriptRepository
from app.services.nlp_service import NLPService


class SummarizeService:
    """Orchestrates summarization workflows."""

    def __init__(
        self,
        transcript_repo: TranscriptRepository = None,
        nlp_service: NLPService = None,
    ):
        self.repo = transcript_repo or TranscriptRepository()
        self.nlp = nlp_service or NLPService()

    # ==================== Catch-Up ====================

    def catch_me_up(
        self,
        meeting_id: str,
        meeting_url: Optional[str],
        progressive_summary: Optional[str],
        last_summarized_at: Optional[str],
    ) -> Dict[str, Any]:
        """
        Progressive summarization for late joiners.

        Returns:
            Dict with success, summary, key_points, segments_analyzed, timestamp, usage
        """
        if not meeting_id:
            raise ValueError("meeting_id is required")

        created_after = last_summarized_at if progressive_summary else None

        try:
            segments = self.repo.fetch_segments(
                meeting_id, meeting_url, created_after=created_after
            )
            chunks = self.repo.format_segments_for_nlp(segments)
        except ValueError as e:
            if progressive_summary and "No transcript segments found" in str(e):
                return {
                    "success": True,
                    "summary": progressive_summary,
                    "key_points": ["Toplantıda yeni bir gelişme olmadı."],
                    "segments_analyzed": 0,
                    "timestamp": datetime.utcnow().isoformat(),
                }
            raise

        if not chunks and not progressive_summary:
            raise ValueError("No transcript data found for this meeting")

        if progressive_summary:
            print(f"\u23e9 Incremental Catch-Up with {len(chunks)} new chunks")
            summary, key_points, usage_dict = self.nlp.generate_progressive_summary(
                chunks, previous_summary=progressive_summary
            )
        else:
            print(f"\U0001f504 Full Catch-Up with {len(chunks)} chunks")
            summary, key_points, usage_dict = self.nlp.generate_progressive_summary(chunks)

        return {
            "success": True,
            "summary": summary,
            "key_points": key_points,
            "segments_analyzed": len(chunks),
            "timestamp": datetime.utcnow().isoformat(),
            "usage_prompt_tokens": usage_dict.get("usage_prompt_tokens"),
            "usage_completion_tokens": usage_dict.get("usage_completion_tokens"),
            "usage_total_tokens": usage_dict.get("usage_total_tokens"),
        }

    # ==================== Agent Catch-Me-Up ====================

    def agent_catch_me_up(
        self,
        existing_summary: str,
        recent_transcript: str,
        instructions: str,
        language: str,
    ) -> Dict[str, Any]:
        """
        Agent catch-me-up — processes pre-prepared data from Go backend.

        Returns:
            Dict with success, summary, timestamp, usage
        """
        if not recent_transcript and not existing_summary:
            return {
                "success": True,
                "summary": (
                    "Henüz transkript verisi bulunmuyor."
                    if language != "en"
                    else "No transcript data available yet."
                ),
                "timestamp": datetime.utcnow().isoformat(),
            }

        summary, usage_dict = self.nlp.generate_agent_catch_me_up(
            existing_summary=existing_summary,
            recent_transcript=recent_transcript,
            instructions=instructions,
            language=language,
        )

        return {
            "success": True,
            "summary": summary,
            "timestamp": datetime.utcnow().isoformat(),
            "usage_prompt_tokens": usage_dict.get("usage_prompt_tokens"),
            "usage_completion_tokens": usage_dict.get("usage_completion_tokens"),
            "usage_total_tokens": usage_dict.get("usage_total_tokens"),
        }

    # ==================== Comprehensive Summarize ====================

    def summarize(
        self,
        meeting_id: str,
        meeting_url: Optional[str],
        force_raw_segments: bool,
        source_reason: Optional[str],
        language: str,
    ) -> Dict[str, Any]:
        """
        Comprehensive meeting summarization with source selection.

        Source priority:
        1. Finalized chat blocks
        2. Live chat blocks if readiness >= 95%
        3. Raw transcript segments fallback

        Returns:
            Full summary result dict matching SummarizeResponse fields.
        """
        if not meeting_id:
            raise ValueError("meeting_id is required")

        chunks, source, source_reason = self._select_source(
            meeting_id, meeting_url, force_raw_segments, source_reason
        )

        if not chunks:
            raise ValueError("No transcript data found for this meeting")

        result = self.nlp.generate_comprehensive_summary(
            chunks,
            transcript_pre_corrected=(source in ("chat_blocks_finalized", "chat_blocks_live_ready")),
            language=language,
        )

        warnings = list(result.get("warnings") or [])
        if force_raw_segments:
            warnings.append(
                f"Raw transcript fallback was forced for this run ({source_reason or 'unspecified'})."
            )
        result["warnings"] = warnings
        result["summary_source"] = source

        return result

    # ==================== Checkpoint ====================

    def create_checkpoint(
        self,
        meeting_id: str,
        meeting_url: Optional[str],
        current_summary: Optional[str],
        last_summarized_at: Optional[str],
        language: str,
    ) -> Dict[str, Any]:
        """
        Generate an incremental checkpoint summary.

        Returns:
            Dict with success, summary, timestamp, usage
        """
        created_after = last_summarized_at if current_summary else None

        try:
            segments = self.repo.fetch_segments(
                meeting_id, meeting_url, created_after=created_after
            )
            chunks = self.repo.format_segments_for_nlp(segments)
        except Exception:
            chunks = []

        if not chunks:
            return {
                "success": True,
                "summary": current_summary or "",
                "timestamp": datetime.utcnow().isoformat(),
            }

        new_summary, usage_dict = self.nlp.generate_checkpoint_summary(
            chunks, current_summary=current_summary, language=language
        )

        return {
            "success": True,
            "summary": new_summary,
            "timestamp": datetime.utcnow().isoformat(),
            "usage_prompt_tokens": usage_dict.get("usage_prompt_tokens") if usage_dict else None,
            "usage_completion_tokens": usage_dict.get("usage_completion_tokens") if usage_dict else None,
            "usage_total_tokens": usage_dict.get("usage_total_tokens") if usage_dict else None,
        }

    # ==================== Private: Source Selection ====================

    def _select_source(
        self,
        meeting_id: str,
        meeting_url: Optional[str],
        force_raw: bool,
        source_reason: Optional[str],
    ) -> Tuple[List[Dict[str, Any]], str, Optional[str]]:
        """
        Select the best transcript source for summarization.

        Returns:
            (chunks, source_label, source_reason)
        """
        if force_raw:
            segments = self.repo.fetch_segments(meeting_id, meeting_url)
            chunks = self.repo.format_segments_for_nlp(segments)
            source_reason = source_reason or "force_raw_segments"
            print(
                f"\u26a0\ufe0f Summarize source forced to raw transcript segments "
                f"({len(chunks)} segments, reason={source_reason})"
            )
            return chunks, "raw_segments_forced", source_reason

        # 1. Try finalized chat blocks
        try:
            blocks = self.repo.fetch_blocks(meeting_id, finalized_only=True, exclude_noise=True)
        except Exception:
            blocks = []

        if blocks:
            chunks = self.repo.format_blocks_for_nlp(blocks)
            print(f"\u2705 Summarize source: finalized chat blocks ({len(blocks)} blocks)")
            return chunks, "chat_blocks_finalized", source_reason or "chat_blocks_finalized"

        # 2. Try live chat blocks with readiness check
        try:
            live_blocks = self.repo.fetch_blocks(meeting_id, finalized_only=False, exclude_noise=True)
        except Exception:
            live_blocks = []

        if live_blocks:
            segments = self.repo.fetch_segments(meeting_id, meeting_url)

            max_segment_end = max([seg.get("end_time") or 0 for seg in segments], default=0.0)
            max_block_end = max([block.get("end_time") or 0 for block in live_blocks], default=0.0)
            readiness_ratio = (max_block_end / max_segment_end) if max_segment_end > 0 else 0.0

            if readiness_ratio >= 0.95:
                chunks = self.repo.format_blocks_for_nlp(live_blocks)
                print(
                    f"\u2705 Summarize source: live chat blocks (readiness={readiness_ratio:.2f}, "
                    f"blocks={len(live_blocks)})"
                )
                return chunks, "chat_blocks_live_ready", source_reason or "chat_blocks_live_ready"
            else:
                chunks = self.repo.format_segments_for_nlp(segments)
                print(
                    f"\u26a0\ufe0f Live blocks below readiness threshold ({readiness_ratio:.2f} < 0.95). "
                    f"Falling back to raw transcript segments ({len(chunks)} segments)"
                )
                return chunks, "raw_segments_fallback", source_reason or "live_blocks_not_ready"

        # 3. Raw segments fallback
        segments = self.repo.fetch_segments(meeting_id, meeting_url)
        chunks = self.repo.format_segments_for_nlp(segments)
        print(f"\u26a0\ufe0f Summarize source fallback: raw transcript segments ({len(chunks)} segments)")
        return chunks, "raw_segments_fallback", source_reason or "no_chat_blocks_found"
