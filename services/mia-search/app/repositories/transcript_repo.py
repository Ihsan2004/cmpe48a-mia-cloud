"""Repository for transcript segments and chat blocks (data access layer).

Wraps all Supabase queries for transcript_segments and transcript_chat_blocks.
Logic moved from db.py flat functions into a class for testability.
"""
from typing import List, Dict, Any, Optional
from datetime import datetime, timedelta, timezone

from app.db import get_supabase_client


class TranscriptRepository:
    """Data access for transcript segments and chat blocks."""

    def fetch_segments(
        self,
        meeting_id: str,
        meeting_url: str = None,
        minutes_back: int = None,
        created_after: str = None,
    ) -> List[Dict[str, Any]]:
        """
        Fetch transcript segments for a meeting.

        Args:
            meeting_id: UUID string of the meeting
            meeting_url: Optional meeting URL for alternative matching
            minutes_back: Filter segments created in last N minutes
            created_after: ISO timestamp to filter segments after

        Returns:
            List of transcript segment dicts

        Raises:
            ValueError: If no segments found (and no time filter applied)
        """
        client = get_supabase_client()

        meeting_id_no_hyphens = meeting_id.replace("-", "")
        conditions = [
            f"meeting_id.eq.{meeting_id}",
            f"meeting_id.eq.{meeting_id_no_hyphens}",
        ]

        if meeting_url:
            meet_code = meeting_url.split("/")[-1].split("?")[0] if meeting_url else ""
            conditions.append(f"meeting_url.eq.{meeting_url}")
            if meet_code:
                conditions.append(f"meeting_url.ilike.%{meet_code}%")

        or_filter = ",".join(conditions)
        print(f"\U0001f50d Searching transcript_segments with filter: {or_filter[:200]}...")

        response = (
            client.table("transcript_segments")
            .select("*")
            .or_(or_filter)
            .order("start_time", desc=False)
            .order("created_at", desc=False)
            .execute()
        )

        if not response.data:
            debug_response = (
                client.table("transcript_segments")
                .select("meeting_id, meeting_url")
                .limit(5)
                .execute()
            )
            sample_ids = [r.get("meeting_id", "N/A") for r in debug_response.data] if debug_response.data else []
            print(f"\u26a0\ufe0f No match found. Sample meeting_ids in table: {sample_ids}")

            if minutes_back or created_after:
                return []
            raise ValueError(f"No transcript segments found for meeting_id: {meeting_id}, meeting_url: {meeting_url}")

        segments = response.data

        if minutes_back or created_after:
            segments = self._filter_by_time(segments, minutes_back, created_after)
            print(f"\u2705 Fetched {len(segments)} filtered segments for {meeting_id}")
            return segments

        print(f"Fetched {len(segments)} transcript segments for meeting {meeting_id}")
        return segments

    def fetch_blocks(
        self,
        meeting_id: str,
        finalized_only: bool = False,
        exclude_noise: bool = False,
    ) -> List[Dict[str, Any]]:
        """
        Fetch corrected transcript blocks from transcript_chat_blocks.

        Args:
            meeting_id: UUID of the meeting
            finalized_only: Only return finalized blocks
            exclude_noise: Exclude noise-flagged blocks

        Returns:
            List of chat block dicts (empty list if none found)
        """
        try:
            client = get_supabase_client()
            print(f"\U0001f50d Checking transcript_chat_blocks for meeting: {meeting_id}")

            query = (
                client.table("transcript_chat_blocks")
                .select("*")
                .eq("meeting_id", meeting_id)
            )
            if finalized_only:
                query = query.eq("is_finalized", True)

            response = query.order("start_time", desc=False).execute()

            if response.data:
                blocks = response.data
                if exclude_noise:
                    blocks = [b for b in blocks if not b.get("is_noise", False)]
                print(f"\u2705 Found {len(blocks)} corrected blocks.")
                return blocks

            return []
        except Exception as e:
            print(f"\u26a0\ufe0f Error fetching chat blocks: {e}")
            return []

    # ==================== Formatting ====================

    @staticmethod
    def format_segments_for_nlp(segments: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
        """Format raw segments for NLP processing."""
        formatted = []
        for segment in segments:
            formatted.append({
                "id": segment.get("id"),
                "meeting_id": segment.get("meeting_id"),
                "text": segment.get("text", ""),
                "transcript_chunk": segment.get("text", ""),
                "speaker": segment.get("speaker"),
                "speaker_name": segment.get("speaker"),
                "role": segment.get("role"),
                "start_time": segment.get("start_time"),
                "end_time": segment.get("end_time"),
                "duration": segment.get("duration"),
                "metadata": segment.get("metadata", {}),
                "created_at": segment.get("created_at"),
                "timestamp": segment.get("created_at"),
            })
        return formatted

    @staticmethod
    def format_blocks_for_nlp(blocks: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
        """Format chat blocks for NLP processing."""
        formatted = []
        for block in blocks:
            text = block.get("text", "") or ""
            speaker = block.get("speaker")
            formatted.append({
                "id": block.get("id"),
                "meeting_id": block.get("meeting_id"),
                "text": text,
                "transcript_chunk": text,
                "speaker": speaker,
                "speaker_name": speaker,
                "role": None,
                "start_time": block.get("start_time"),
                "end_time": block.get("end_time"),
                "duration": None,
                "metadata": {
                    "source": "transcript_chat_blocks",
                    "is_finalized": block.get("is_finalized"),
                    "source_segment_count": block.get("source_segment_count"),
                    "raw_segment_ids": block.get("raw_segment_ids"),
                },
                "created_at": block.get("created_at"),
                "timestamp": block.get("created_at"),
            })
        return formatted

    # ==================== Private Helpers ====================

    @staticmethod
    def _filter_by_time(
        segments: List[Dict[str, Any]],
        minutes_back: Optional[int],
        created_after: Optional[str],
    ) -> List[Dict[str, Any]]:
        """Filter segments by time window."""
        cutoff_time = None
        if minutes_back:
            cutoff_time = datetime.now(timezone.utc) - timedelta(minutes=minutes_back)
        elif created_after:
            try:
                clean_str = created_after.replace("Z", "+00:00")
                cutoff_time = datetime.fromisoformat(clean_str)
            except Exception as e:
                print(f"\u26a0\ufe0f Invalid info passed to created_after: {e}")

        filtered = []
        for seg in segments:
            try:
                created_at_str = seg.get("created_at")
                if not created_at_str:
                    continue

                clean_ts = created_at_str.replace("Z", "+00:00")
                if "T" not in clean_ts and " " in clean_ts:
                    clean_ts = clean_ts.replace(" ", "T")

                if "." in clean_ts:
                    parts = clean_ts.split(".")
                    main_part = parts[0]
                    fraction_and_tz = parts[1]

                    if "+" in fraction_and_tz:
                        frac, tz = fraction_and_tz.split("+")
                        tz = "+" + tz
                    elif "-" in fraction_and_tz:
                        last_minus = fraction_and_tz.rfind("-")
                        frac = fraction_and_tz[:last_minus]
                        tz = fraction_and_tz[last_minus:]
                    else:
                        frac = fraction_and_tz
                        tz = ""

                    if len(frac) != 6:
                        frac = (frac + "000000")[:6]
                        clean_ts = f"{main_part}.{frac}{tz}"

                seg_time = datetime.fromisoformat(clean_ts)
                if cutoff_time and seg_time >= cutoff_time:
                    filtered.append(seg)
            except Exception as e:
                print(f"\u26a0\ufe0f Timestamp parse error for '{seg.get('created_at')}': {e}")
                continue

        return filtered
