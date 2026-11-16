"""Repository for transcript segments and chat blocks (data access layer).

Executes parameterized PostgreSQL queries via psycopg for transcript_segments
and transcript_chat_blocks.
"""
from datetime import datetime, timedelta, timezone
from typing import Any, Dict, List, Optional
import uuid

from app.db import get_db_connection


def _parse_uuid(value: Optional[str]) -> Optional[str]:
    """Return canonical UUID string if valid, else None."""
    if not value:
        return None
    try:
        return str(uuid.UUID(str(value)))
    except (ValueError, TypeError, AttributeError):
        return None


def _serialize_timestamp(value: Any) -> Any:
    """Convert datetime values to ISO-8601 strings for downstream JSON payloads."""
    if isinstance(value, datetime):
        return value.isoformat()
    return value


def _serialize_id(value: Any) -> Any:
    """Convert UUID objects to strings for downstream JSON payloads."""
    if isinstance(value, uuid.UUID):
        return str(value)
    return value


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
        clauses: List[str] = []
        params: List[Any] = []

        parsed_id = _parse_uuid(meeting_id)
        if parsed_id:
            clauses.append("meeting_id = %s::uuid")
            params.append(parsed_id)

        if meeting_url:
            meet_code = meeting_url.split("/")[-1].split("?")[0]
            clauses.append("meeting_url = %s")
            params.append(meeting_url)
            if meet_code:
                clauses.append("meeting_url ILIKE %s")
                params.append(f"%{meet_code}%")

        if not clauses:
            if minutes_back or created_after:
                return []
            raise ValueError(
                f"No transcript segments found for meeting_id: {meeting_id}, meeting_url: {meeting_url}"
            )

        where_sql = " OR ".join(clauses)
        query = f"""
            SELECT *
            FROM public.transcript_segments
            WHERE {where_sql}
            ORDER BY start_time ASC NULLS LAST, created_at ASC
        """

        with get_db_connection() as conn:
            with conn.cursor() as cur:
                cur.execute(query, params)
                segments = cur.fetchall()

        if not segments:
            if minutes_back or created_after:
                return []
            raise ValueError(
                f"No transcript segments found for meeting_id: {meeting_id}, meeting_url: {meeting_url}"
            )

        if minutes_back or created_after:
            segments = self._filter_by_time(segments, minutes_back, created_after)
            print(f"✅ Fetched {len(segments)} filtered segments for {meeting_id}")
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
        parsed_id = _parse_uuid(meeting_id)
        if not parsed_id:
            return []

        try:
            conditions = ["meeting_id = %s::uuid"]
            params: List[Any] = [parsed_id]
            if finalized_only:
                conditions.append("is_finalized = TRUE")

            query = f"""
                SELECT *
                FROM public.transcript_chat_blocks
                WHERE {" AND ".join(conditions)}
                ORDER BY start_time ASC NULLS LAST
            """

            with get_db_connection() as conn:
                with conn.cursor() as cur:
                    cur.execute(query, params)
                    blocks = cur.fetchall()

            if blocks:
                if exclude_noise:
                    blocks = [b for b in blocks if not b.get("is_noise", False)]
                print(f"✅ Found {len(blocks)} corrected blocks.")
                return blocks

            return []
        except Exception as e:
            print(f"⚠️ Error fetching chat blocks: {e}")
            return []

    # ==================== Formatting ====================

    @staticmethod
    def format_segments_for_nlp(segments: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
        """Format raw segments for NLP processing."""
        formatted = []
        for segment in segments:
            created_at = _serialize_timestamp(segment.get("created_at"))
            formatted.append({
                "id": _serialize_id(segment.get("id")),
                "meeting_id": _serialize_id(segment.get("meeting_id")),
                "text": segment.get("text", ""),
                "transcript_chunk": segment.get("text", ""),
                "speaker": segment.get("speaker"),
                "speaker_name": segment.get("speaker"),
                "role": segment.get("role"),
                "start_time": segment.get("start_time"),
                "end_time": segment.get("end_time"),
                "duration": segment.get("duration"),
                "metadata": segment.get("metadata", {}),
                "created_at": created_at,
                "timestamp": created_at,
            })
        return formatted

    @staticmethod
    def format_blocks_for_nlp(blocks: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
        """Format chat blocks for NLP processing."""
        formatted = []
        for block in blocks:
            text = block.get("text", "") or ""
            speaker = block.get("speaker")
            created_at = _serialize_timestamp(block.get("created_at"))
            raw_ids = block.get("raw_segment_ids")
            if isinstance(raw_ids, list):
                raw_ids = [_serialize_id(item) for item in raw_ids]
            formatted.append({
                "id": _serialize_id(block.get("id")),
                "meeting_id": _serialize_id(block.get("meeting_id")),
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
                    "raw_segment_ids": raw_ids,
                },
                "created_at": created_at,
                "timestamp": created_at,
            })
        return formatted

    # ==================== Private Helpers ====================

    @staticmethod
    def _parse_datetime(value: Any) -> Optional[datetime]:
        """Parse either a datetime object or ISO timestamp string into UTC-aware datetime."""
        if value is None:
            return None
        if isinstance(value, datetime):
            return value if value.tzinfo else value.replace(tzinfo=timezone.utc)
        if not isinstance(value, str):
            return None

        clean_ts = value.replace("Z", "+00:00")
        if "T" not in clean_ts and " " in clean_ts:
            clean_ts = clean_ts.replace(" ", "T")

        if "." in clean_ts:
            main_part, fraction_and_tz = clean_ts.split(".", 1)
            if "+" in fraction_and_tz:
                frac, tz = fraction_and_tz.split("+", 1)
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

        parsed = datetime.fromisoformat(clean_ts)
        return parsed if parsed.tzinfo else parsed.replace(tzinfo=timezone.utc)

    @classmethod
    def _filter_by_time(
        cls,
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
                cutoff_time = cls._parse_datetime(created_after)
            except Exception as e:
                print(f"⚠️ Invalid info passed to created_after: {e}")

        filtered = []
        for seg in segments:
            try:
                seg_time = cls._parse_datetime(seg.get("created_at"))
                if seg_time and cutoff_time and seg_time >= cutoff_time:
                    filtered.append(seg)
            except Exception as e:
                print(f"⚠️ Timestamp parse error for '{seg.get('created_at')}': {e}")
                continue

        return filtered
