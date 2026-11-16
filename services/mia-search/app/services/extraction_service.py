"""Decision extraction service — extracts decisions, actions, and open topics from transcripts.

Mirrors the layered architecture: router (thin) → service (orchestration) → LLM gateway.
Faz 1: Single-call extraction via OpenAI.
Faz 2: ADR context fields + temporal metadata.
Faz 3: Topic-aware chunking for long transcripts.
Faz 4: Parallel extraction + per-chunk retry.
"""
import json
import logging
import time as _time
from concurrent.futures import ThreadPoolExecutor, as_completed
from typing import List, Dict, Any, Optional

from app.utils.openai_gateway import create_chat_completion, extract_usage, TokenAccumulator
from app.services.nlp_json_utils import parse_json_with_retry

logger = logging.getLogger(__name__)

EXTRACTION_MODEL = "gpt-5.4"

# Chunking thresholds
SHORT_TRANSCRIPT_CHARS = 12000  # ~3000 tokens — single call
CHUNK_TARGET_CHARS = 10000      # ~2500 tokens per chunk
CHUNK_OVERLAP_LINES = 4         # overlap lines between chunks

# Parallel extraction settings
MAX_PARALLEL_CHUNKS = 3         # concurrency limit (rate limit protection)
CHUNK_MAX_RETRIES = 2           # per-chunk retry count
CHUNK_RETRY_BASE_SEC = 1.0     # exponential backoff base


class ExtractionService:
    """Extracts decisions, action items, and open topics from meeting transcripts."""

    def extract(
        self,
        transcript: str,
        segments: Optional[List[Dict[str, Any]]] = None,
        confirmed_items: Optional[List[str]] = None,
        chain_items: Optional[List[Dict[str, Any]]] = None,
        language: str = "tr",
    ) -> Dict[str, Any]:
        """
        Extract decisions, actions, and open topics from a transcript.

        Short transcripts (<12K chars) → single LLM call.
        Long transcripts → chunked with overlap, each chunk extracted separately.

        Args:
            transcript: Flat transcript text (speaker-attributed lines)
            segments: Optional structured segments with timing from Go
            confirmed_items: Previously confirmed item texts (for retry idempotency)
            chain_items: Confirmed items from chain history (for future phases)
            language: Output language (default: Turkish)

        Returns:
            Dict with 'items' list and 'usage' dict
        """
        if not transcript or not transcript.strip():
            logger.info("Empty transcript, skipping extraction")
            return {"items": [], "usage": _empty_usage()}

        system_prompt = self._build_system_prompt(confirmed_items, language)
        acc = TokenAccumulator()

        chunk_telemetry = []

        # Short transcript: single call (Faz 1 behavior)
        if len(transcript) <= SHORT_TRANSCRIPT_CHARS:
            items = self._extract_single(transcript, system_prompt, acc)
        else:
            # Long transcript: chunk and extract each (with timing from segments)
            chunks = self._chunk_transcript(transcript, segments)
            logger.info("Long transcript (%d chars) split into %d chunks", len(transcript), len(chunks))
            items, chunk_telemetry = self._extract_chunked(chunks, system_prompt, acc)

        # Merge/dedupe: resolve intra-meeting overrides + cross-meeting updates
        merge_start = _time.monotonic()
        merged_items = self._merge_items(items, chain_items, acc)
        merge_latency_ms = int((_time.monotonic() - merge_start) * 1000)

        # Extract completed indices from merge result metadata
        completed_chain_indices = []
        for m in merged_items:
            if "_completed_chain_indices" in m:
                completed_chain_indices = m.pop("_completed_chain_indices")
                break
        # Remove _META sentinel if present
        merged_items = [m for m in merged_items if m.get("operation") != "_META"]

        # Build merge telemetry
        merge_telemetry = {
            "raw_count": len(items),
            "merged_count": len(merged_items),
            "add_count": sum(1 for m in merged_items if m.get("operation") == "ADD"),
            "update_count": sum(1 for m in merged_items if m.get("operation") == "UPDATE"),
            "delete_count": sum(1 for m in merged_items if m.get("operation") == "DELETE"),
            "complete_count": len(completed_chain_indices),
            "completed_chain_indices": completed_chain_indices,
            "noop_count": len(items) - len(merged_items),
            "merge_latency_ms": merge_latency_ms,
        }

        logger.info(
            "Extraction completed: %d raw → %d merged (decisions=%d, actions=%d, topics=%d)",
            len(items), len(merged_items),
            sum(1 for i in merged_items if i["item_type"] == "decision"),
            sum(1 for i in merged_items if i["item_type"] == "action_item"),
            sum(1 for i in merged_items if i["item_type"] == "open_topic"),
        )

        return {
            "items": merged_items,
            "usage": acc.to_dict(),
            "chunk_telemetry": chunk_telemetry,
            "merge_telemetry": merge_telemetry,
        }

    # ==================== Extraction Calls ====================

    def _extract_single(
        self, transcript: str, system_prompt: str, acc: TokenAccumulator
    ) -> List[Dict[str, Any]]:
        """Single-call extraction for short transcripts."""
        user_prompt = self._build_user_prompt(transcript)

        response = create_chat_completion(
            model=EXTRACTION_MODEL,
            messages=[
                {"role": "system", "content": system_prompt},
                {"role": "user", "content": user_prompt},
            ],
            max_completion_tokens=4000,
            temperature=0.3,
            response_format={"type": "json_object"},
        )

        acc.add(extract_usage(response, EXTRACTION_MODEL))
        result = parse_json_with_retry(response.choices[0].message.content.strip())

        items = []
        for item in result.get("items", []):
            normalized = self._validate_item(item)
            if normalized:
                items.append(normalized)
        return items

    def _extract_chunked(
        self, chunks: List[Dict[str, Any]], system_prompt: str, acc: TokenAccumulator
    ) -> tuple:
        """Parallel extraction with per-chunk retry and telemetry. Returns (items, telemetry)."""
        chunk_results: Dict[int, List[Dict[str, Any]]] = {}
        telemetry: List[Dict[str, Any]] = []

        def process_chunk(chunk: Dict[str, Any]) -> Dict[str, Any]:
            """Extract from a single chunk with retry. Returns telemetry dict."""
            chunk_index = chunk["index"]
            chunk_text = chunk["text"]
            time_start = chunk.get("time_start")
            time_end = chunk.get("time_end")
            start_ts = _time.monotonic()
            retries_used = 0
            status = "success"
            items_found = []

            for attempt in range(CHUNK_MAX_RETRIES + 1):
                try:
                    user_prompt = self._build_user_prompt(chunk_text, is_chunk=True)
                    response = create_chat_completion(
                        model=EXTRACTION_MODEL,
                        messages=[
                            {"role": "system", "content": system_prompt},
                            {"role": "user", "content": user_prompt},
                        ],
                        max_completion_tokens=4000,
                        temperature=0.3,
                        response_format={"type": "json_object"},
                    )

                    acc.add(extract_usage(response, EXTRACTION_MODEL))
                    result = parse_json_with_retry(response.choices[0].message.content.strip())

                    for item in result.get("items", []):
                        normalized = self._validate_item(item)
                        if normalized:
                            normalized["chunk_index"] = chunk_index
                            normalized["timestamp_start"] = time_start
                            normalized["timestamp_end"] = time_end
                            items_found.append(normalized)

                    break  # success — exit retry loop

                except Exception as e:
                    retries_used = attempt + 1
                    if attempt < CHUNK_MAX_RETRIES:
                        backoff = CHUNK_RETRY_BASE_SEC * (2 ** attempt)
                        logger.warning(
                            "Chunk %d attempt %d failed, retrying in %.1fs: %s",
                            chunk_index, attempt + 1, backoff, e,
                        )
                        _time.sleep(backoff)
                    else:
                        status = "failed"
                        logger.error("Chunk %d failed after %d retries: %s", chunk_index, CHUNK_MAX_RETRIES, e)

            latency_ms = int((_time.monotonic() - start_ts) * 1000)

            return {
                "chunk_index": chunk_index,
                "status": status,
                "latency_ms": latency_ms,
                "retry_count": retries_used,
                "items_extracted": len(items_found),
                "model_used": EXTRACTION_MODEL,
                "items": items_found,
            }

        # Parallel execution with concurrency limit
        logger.info("Extracting %d chunks in parallel (max_workers=%d)", len(chunks), MAX_PARALLEL_CHUNKS)

        with ThreadPoolExecutor(max_workers=MAX_PARALLEL_CHUNKS) as executor:
            futures = {executor.submit(process_chunk, chunk): chunk["index"] for chunk in chunks}

            for future in as_completed(futures):
                chunk_idx = futures[future]
                try:
                    result = future.result()
                    chunk_results[result["chunk_index"]] = result["items"]
                    telemetry.append({k: v for k, v in result.items() if k != "items"})
                except Exception as e:
                    logger.error("Chunk %d future failed: %s", chunk_idx, e)
                    telemetry.append({
                        "chunk_index": chunk_idx,
                        "status": "failed",
                        "latency_ms": 0,
                        "retry_count": 0,
                        "items_extracted": 0,
                    })

        # Combine items in chunk order (important for Faz 5 merge/override)
        all_items = []
        for idx in sorted(chunk_results.keys()):
            all_items.extend(chunk_results[idx])

        # Log telemetry summary
        successful = sum(1 for t in telemetry if t["status"] == "success")
        total_items = sum(t["items_extracted"] for t in telemetry)
        logger.info(
            "Parallel extraction done: %d/%d chunks succeeded, %d items, telemetry=%s",
            successful, len(chunks), total_items,
            [{k: v for k, v in t.items()} for t in sorted(telemetry, key=lambda x: x["chunk_index"])],
        )

        # Total failure: all chunks failed — raise instead of returning empty
        if successful == 0 and len(chunks) > 0:
            raise RuntimeError(
                f"All {len(chunks)} chunks failed extraction. "
                f"Telemetry: {telemetry}"
            )

        sorted_telemetry = sorted(telemetry, key=lambda x: x["chunk_index"])
        return all_items, sorted_telemetry

    # ==================== Merge / Dedupe ====================

    def _merge_items(
        self,
        raw_items: List[Dict[str, Any]],
        chain_items: Optional[List[Dict[str, Any]]],
        acc: TokenAccumulator,
    ) -> List[Dict[str, Any]]:
        """
        Merge extracted items: intra-meeting dedup + cross-meeting operations.

        If fewer than 2 items and no chain_items, skip merge (no work to do).
        Uses LLM merge call with fallback to rule-based dedup.

        Each returned item has an 'operation' field: ADD, UPDATE, DELETE, NOOP.
        """
        if len(raw_items) <= 1 and not chain_items:
            # Nothing to merge — tag as ADD
            for item in raw_items:
                item["operation"] = "ADD"
            return raw_items

        try:
            return self._merge_with_llm(raw_items, chain_items, acc)
        except Exception as e:
            logger.warning("LLM merge failed, falling back to rule-based: %s", e)
            return self._fallback_merge(raw_items, chain_items)

    def _merge_with_llm(
        self,
        raw_items: List[Dict[str, Any]],
        chain_items: Optional[List[Dict[str, Any]]],
        acc: TokenAccumulator,
    ) -> List[Dict[str, Any]]:
        """LLM-based merge with ADD/UPDATE/DELETE/NOOP operations."""
        # Build compact item summaries for the merge prompt
        raw_summary = []
        for i, item in enumerate(raw_items):
            entry = {
                "idx": i,
                "text": item["text"],
                "item_type": item["item_type"],
                "owner": item.get("owner_name"),
                "source_quote": item.get("source_quote", ""),
                "context": item.get("context"),
                "alternatives": item.get("alternatives_considered"),
                "chunk": item.get("chunk_index", 0),
            }
            raw_summary.append(entry)

        chain_summary = []
        if chain_items:
            for i, item in enumerate(chain_items):
                chain_summary.append({
                    "idx": i,
                    "text": item.get("text", ""),
                    "item_type": item.get("item_type", ""),
                    "owner": item.get("owner_name"),
                    "context": item.get("context"),
                })

        system_prompt = (
            "Sen bir toplantı karar birleştirme asistanısın. "
            "İki liste verilecek: bu toplantıdan çıkarılan yeni kararlar (A) ve "
            "önceki toplantılardan onaylanmış kararlar (B).\n\n"
            "İki tür operasyon var:\n\n"
            "A) Her yeni item (A listesi) için bir operasyon belirle:\n"
            "- ADD: Tamamen yeni (B'de benzeri yok, A içinde tekrar yok)\n"
            "- UPDATE: B'deki bir kararın güncellenmiş hali (updated_chain_idx ile B indeksini belirt)\n"
            "- DELETE: B'deki bir kararın bu toplantıda açıkça iptal edildiği (deleted_chain_idx ile belirt)\n"
            "- NOOP: A içinde aynı kararın paraphrase'i veya B'de zaten aynısı var (çıkarılır)\n\n"
            "B) COMPLETE tespiti — B listesindeki item'lar için:\n"
            "- Transcript'te bir B item'ının tamamlandığı/yapıldığı belirtiliyorsa "
            "(örn. 'bunu yaptık', 'tamamlandı', 'hallettik', 'gönderildi', 'done', 'completed'), "
            "o item'ı COMPLETE olarak işaretle (completed_chain_indices listesinde B indeksini belirt)\n"
            "- Sadece AÇIKÇA tamamlandığı söylenen item'lar — çıkarım yapma\n"
            "- Decision'lar genelde COMPLETE olmaz (kararlar kalıcıdır), ama action_item ve open_topic COMPLETE olabilir\n\n"
            "INTRA-MEETING DEDUP / COMPOSE (A içi — çok önemli):\n"
            "- (Saf paraphrase) İki A item'ı aynı somut kararı/aksiyonu farklı kelimelerle ifade ediyorsa, daha net ve eksiksiz formüle edileni TUT, diğerini NOOP yap. Yeni metin yazma.\n"
            "- (COMPOSE) İki A item'ı aynı kararın farklı facet'lerini söylüyorsa (örn. biri 'Backend Go olacak', diğeri 'service/handler/repository mimarisi olacak'), TEK item'a birleştir:\n"
            "    1. Tutulan item için yeni `text` ve (varsa) yeni `context` alanlarını response'da döndür\n"
            "    2. Diğer item'ı NOOP yap\n"
            "    3. YALNIZCA her iki item'da AÇIKÇA geçen olguları birleştir — yeni bilgi EKLEME, çıkarım yapma\n"
            "- DİKKAT: Aynı toplantıda alınmış FARKLI kararlar (örn. 'Backend Go' ve 'Frontend React') ayrı item olarak kalır, COMPOSE DEĞİLDİR. Sadece gerçekten AYNI KARARIN parçaları compose edilir.\n\n"
            "UPDATE kuralları (cross-meeting, A vs B — çok önemli):\n"
            "- decision UPDATE: aynı KONU hakkında yön/karar değişmişse UPDATE. Örnek: 'Stripe mı Iyzico mu açık' → 'Iyzico ile başlanacak'\n"
            "- action_item UPDATE: sadece AYNI SOMUT GÖREV'in deadline, scope veya detayı değişmişse UPDATE. "
            "Aynı kişiye atanmış FARKLI görevler UPDATE DEĞİLDİR — her biri ayrı ADD olmalı. "
            "Örnek YANLIŞ: 'Ali toplantıya katılacak' → 'Ali demo yapacak' (farklı görev = ADD). "
            "Örnek DOĞRU: 'API Cuma'ya teslim' → 'API Pazartesi'ye ertelendi' (aynı görev, tarih değişti = UPDATE)\n"
            "- open_topic UPDATE: açık konu karara dönüşmüşse UPDATE\n\n"
            "DELETE kuralları:\n"
            "- Sadece açıkça 'iptal', 'vazgeçtik', 'artık yapmıyoruz' denilen kararlar için DELETE\n"
            "- 'Phase 2'ye bırakıldı' veya 'ertelendi' DELETE DEĞİLDİR — bunlar prioritization kararı (ADD)\n\n"
            "Ek kurallar:\n"
            "Çakışma kuralları:\n"
            "- Aynı B index'i hem DELETE hem UPDATE alamaz. Eski karar iptal edilip yerine yeni karar geliyorsa → UPDATE (eski→yeni evrim). Sadece 'iptal, yerine bir şey yok' → DELETE\n"
            "- A içinde aynı konuda birden fazla karar varsa SON chunk'takini koru (override)\n\n"
            "UPDATE hata örnekleri (bunları YAPMA):\n"
            "- YANLIŞ: 'Grafik kütüphanesi henüz net değil' → 'HTTPS yarına tamamlanacak' (farklı konu = ADD)\n"
            "- YANLIŞ: 'Ali toplantıya katılacak' → 'Ali demo yapacak' (farklı görev = ADD)\n"
            "- DOĞRU: 'Iyzico ile başlanacak' → 'Stripe ile devam edilecek' (aynı konu: payment provider = UPDATE)\n\n"
            "- Emin değilsen ADD olarak işaretle\n"
            "- Tüm çıktılar Türkçe"
        )

        user_prompt = (
            f"A) BU TOPLANTIDAN ÇIKARILAN KARARLAR:\n{json.dumps(raw_summary, ensure_ascii=False)}\n\n"
            f"B) ONAYLANMIŞ KARARLAR (önceki toplantılardan):\n{json.dumps(chain_summary, ensure_ascii=False) if chain_summary else '(yok)'}\n\n"
            "Her A item'ı için operasyon belirle. JSON formatında döndür:\n"
            '{"results": [{"idx": 0, "operation": "ADD|UPDATE|DELETE|NOOP", '
            '"updated_chain_idx": null, "deleted_chain_idx": null, '
            '"text": null, "context": null}], '
            '"completed_chain_indices": []}\n\n'
            "Not:\n"
            "- `text` ve `context` alanları yalnızca COMPOSE durumunda doldurulur "
            "(birleştirilmiş item için). Diğer durumlarda null bırak.\n"
            "- `completed_chain_indices`: Transcript'te tamamlandığı açıkça belirtilen B item'larının "
            "indekslerini listele. Hiçbiri yoksa boş liste [] döndür."
        )

        response = create_chat_completion(
            model=EXTRACTION_MODEL,
            messages=[
                {"role": "system", "content": system_prompt},
                {"role": "user", "content": user_prompt},
            ],
            max_completion_tokens=2000,
            temperature=0.2,
            response_format={"type": "json_object"},
        )

        acc.add(extract_usage(response, EXTRACTION_MODEL))
        merge_result = parse_json_with_retry(response.choices[0].message.content.strip())

        results = merge_result.get("results", [])
        completed_indices = merge_result.get("completed_chain_indices", [])

        # Build index lookup
        result_map = {}
        for r in results:
            idx = r.get("idx")
            if idx is not None:
                result_map[idx] = r

        # Apply operations to raw items
        merged = []
        for i, item in enumerate(raw_items):
            op_info = result_map.get(i, {"operation": "ADD"})
            operation = op_info.get("operation", "ADD").upper()

            if operation == "NOOP":
                continue  # skip duplicate/already-exists

            item["operation"] = operation
            if operation == "UPDATE":
                item["updated_chain_idx"] = op_info.get("updated_chain_idx")
            elif operation == "DELETE":
                item["deleted_chain_idx"] = op_info.get("deleted_chain_idx")

            # COMPOSE: apply text/context overrides if model returned them
            override_text = op_info.get("text")
            override_context = op_info.get("context")
            if override_text and isinstance(override_text, str) and override_text.strip():
                item["text"] = override_text.strip()
                item["composed"] = True
            if override_context and isinstance(override_context, str) and override_context.strip():
                item["context"] = override_context.strip()

            merged.append(item)

        # Log completed items from chain
        if completed_indices:
            logger.info(
                "LLM merge: %d chain items marked COMPLETE: indices %s",
                len(completed_indices), completed_indices,
            )

        logger.info(
            "LLM merge: %d raw → %d merged (ADD=%d, UPDATE=%d, DELETE=%d, NOOP=%d, COMPLETE=%d)",
            len(raw_items), len(merged),
            sum(1 for m in merged if m.get("operation") == "ADD"),
            sum(1 for m in merged if m.get("operation") == "UPDATE"),
            sum(1 for m in merged if m.get("operation") == "DELETE"),
            len(raw_items) - len(merged),
            len(completed_indices),
        )

        # Attach completed_chain_indices to the first merged item as metadata
        # so the caller can access it (avoids breaking the return type)
        if merged:
            merged[0]["_completed_chain_indices"] = completed_indices
        elif completed_indices:
            # No new items but some chain items completed — return a sentinel
            merged.append({
                "operation": "_META",
                "_completed_chain_indices": completed_indices,
            })

        return merged

    @staticmethod
    def _fallback_merge(
        raw_items: List[Dict[str, Any]],
        chain_items: Optional[List[Dict[str, Any]]],
    ) -> List[Dict[str, Any]]:
        """
        Rule-based fallback merge: exact text dedup + last-chunk-wins.
        No UPDATE/DELETE detection — everything is ADD.
        """
        seen_texts = set()
        chain_texts = set()
        if chain_items:
            for item in chain_items:
                chain_texts.add(item.get("text", "").strip().lower())

        # Process in reverse (last chunk first = override wins)
        merged = []
        for item in reversed(raw_items):
            text_key = item["text"].strip().lower()
            if text_key in seen_texts:
                continue  # intra-meeting duplicate
            if text_key in chain_texts:
                continue  # already confirmed in chain
            seen_texts.add(text_key)
            item["operation"] = "ADD"
            merged.append(item)

        merged.reverse()  # restore chronological order
        logger.info("Fallback merge: %d raw → %d merged (all ADD)", len(raw_items), len(merged))
        return merged

    # ==================== Chunking ====================

    def _chunk_transcript(
        self, transcript: str, segments: Optional[List[Dict[str, Any]]] = None,
    ) -> List[Dict[str, Any]]:
        """
        Split long transcript into overlapping chunks at speaker boundaries.

        Each chunk is a dict with:
        - text: chunk content
        - index: chunk order (0-based)
        - time_start/time_end: derived from structured segments (if provided)
        """
        lines = transcript.split("\n")
        if not lines:
            return [{"text": transcript, "index": 0, "time_start": None, "time_end": None}]

        # Build line → time mapping from segments (if available)
        line_times = self._build_line_time_map(lines, segments)

        chunks = []
        current_lines: List[str] = []
        current_line_indices: List[int] = []  # track original line indices for timing
        current_chars = 0
        chunk_index = 0
        current_leading_overlap = 0  # how many leading lines came from previous chunk's tail

        def _make_chunk(
            chunk_lines: List[str], indices: List[int], idx: int, leading_overlap: int = 0
        ) -> Dict[str, Any]:
            """Build chunk dict. Wraps any leading overlap lines in <reference_only> markers."""
            t_start = None
            t_end = None
            if line_times:
                starts = [line_times[i][0] for i in indices if i < len(line_times) and line_times[i][0] is not None]
                ends = [line_times[i][1] for i in indices if i < len(line_times) and line_times[i][1] is not None]
                if starts:
                    t_start = min(starts)
                if ends:
                    t_end = max(ends)

            if leading_overlap > 0 and leading_overlap < len(chunk_lines):
                ref_block = "\n".join(chunk_lines[:leading_overlap])
                body_block = "\n".join(chunk_lines[leading_overlap:])
                text = f"<reference_only>\n{ref_block}\n</reference_only>\n{body_block}".strip()
            else:
                text = "\n".join(chunk_lines).strip()

            return {
                "text": text,
                "index": idx,
                "time_start": t_start,
                "time_end": t_end,
            }

        for i, line in enumerate(lines):
            current_lines.append(line)
            current_line_indices.append(i)
            current_chars += len(line) + 1

            if current_chars >= CHUNK_TARGET_CHARS:
                break_at = self._find_speaker_break(current_lines)

                if break_at is not None and break_at > len(current_lines) // 2:
                    chunk_text_lines = current_lines[:break_at]
                    chunk_indices = current_line_indices[:break_at]
                    remaining_lines = current_lines[break_at:]
                    remaining_indices = current_line_indices[break_at:]
                else:
                    chunk_text_lines = current_lines
                    chunk_indices = current_line_indices
                    remaining_lines = []
                    remaining_indices = []

                chunks.append(_make_chunk(
                    chunk_text_lines, chunk_indices, chunk_index,
                    leading_overlap=current_leading_overlap,
                ))
                chunk_index += 1

                # Overlap: last N lines become reference_only of next chunk
                overlap_n = min(CHUNK_OVERLAP_LINES, len(chunk_text_lines))
                overlap_lines = chunk_text_lines[-overlap_n:]
                overlap_indices = chunk_indices[-overlap_n:]

                current_lines = overlap_lines + remaining_lines
                current_line_indices = overlap_indices + remaining_indices
                current_chars = sum(len(l) + 1 for l in current_lines)
                current_leading_overlap = overlap_n

        # Final chunk
        if current_lines:
            final_text = "\n".join(current_lines).strip()
            if final_text:
                chunks.append(_make_chunk(
                    current_lines, current_line_indices, chunk_index,
                    leading_overlap=current_leading_overlap,
                ))

        if len(chunks) <= 1:
            t_start = None
            t_end = None
            if line_times:
                starts = [t[0] for t in line_times if t[0] is not None]
                ends = [t[1] for t in line_times if t[1] is not None]
                if starts:
                    t_start = min(starts)
                if ends:
                    t_end = max(ends)
            return [{"text": transcript.strip(), "index": 0, "time_start": t_start, "time_end": t_end}]

        return chunks

    @staticmethod
    def _find_speaker_break(lines: List[str]) -> Optional[int]:
        """Find the last speaker boundary ([Name]:) in the last third of lines."""
        search_start = max(len(lines) * 2 // 3, 1)
        for i in range(len(lines) - 1, search_start - 1, -1):
            if lines[i].strip().startswith("["):
                return i
        return None

    @staticmethod
    def _build_line_time_map(
        lines: List[str], segments: Optional[List[Dict[str, Any]]]
    ) -> Optional[List[tuple]]:
        """
        Map each transcript line to (start_time, end_time) using structured segments.

        Lines are matched to segments by order — line i corresponds to segment i.
        If segments aren't provided or counts differ, returns None.

        Returns:
            List of (start_time, end_time) tuples, or None
        """
        if not segments:
            return None

        # Segments and lines should correspond 1:1 (each segment = one [Speaker]: text line)
        # If counts differ, best-effort: map as many as possible
        time_map = []
        for i in range(len(lines)):
            if i < len(segments):
                seg = segments[i]
                time_map.append((seg.get("start_time"), seg.get("end_time")))
            else:
                time_map.append((None, None))

        return time_map

    # ==================== Prompt Building ====================

    def _build_system_prompt(
        self,
        confirmed_items: Optional[List[str]],
        language: str,
    ) -> str:
        exclusion_note = ""
        if confirmed_items:
            exclusion_note = "\n\nAŞAĞIDAKİ KARARLAR ZATEN ONAYLANDI — bunları tekrar çıkarma:\n"
            for text in confirmed_items:
                exclusion_note += f"- {text}\n"

        output_lang = "İngilizce" if (language or "tr").lower().startswith("en") else "Türkçe"

        return (
            "Sen bir toplantı karar analistisin. Verilen toplantı transcript'inden "
            "kararları, aksiyonları ve açık konuları çıkar.\n\n"
            "GRANÜLARİTE EŞİĞİ (en önemli kural):\n"
            "- Sadece toplantıdan bağımsız olarak takip edilebilir, somut çıktısı olan kararları/aksiyonları çıkar.\n"
            "- Bir item çıkarmadan önce kendine sor: \"Toplantı bittikten 1 hafta sonra biri bu item'a bakıp 'evet bunu takip ediyoruz' diyebilir mi?\" Cevap net evet değilse ÇIKARMA.\n"
            "- Genel niyetler (\"bakmamız lazım\", \"düşünelim\"), koşullu/spekülatif ifadeler (\"değerlendirilebilir\", \"olabilir\", \"belki\"), meta yorumlar (\"toplantıyı erken bitirelim\", \"iyi geçti\") item DEĞİLDİR.\n"
            "- Şüphe varsa ÇIKARMA. Az çıkarmak fazla çıkarmaktan iyidir.\n\n"
            "Item türleri:\n"
            '- "decision": Net, alınmış karar ("yapalım", "karar verdik", "şöyle ilerleyelim", scope/prioritization: "MVP\'de değil", "Phase 2\'ye bırakalım")\n'
            '- "action_item": Somut görev — kim, ne, (varsa) ne zaman ("X yapacak", "Y\'ye kadar tamamlanacak")\n'
            '- "open_topic": Somut bir problem, seçim veya çözülmemiş konu (sadece dile getirilmiş, henüz karara bağlanmamış). Genel erteleme veya sohbet open_topic DEĞİLDİR.\n\n'
            "Alan kuralları:\n"
            "- source_quote: ZORUNLU. Transcript'ten birebir alıntı.\n"
            "- owner_name: Varsa belirt, yoksa null. SPK_X gibi ham ID kullanma — gerçek isim transcript'te geçmiyorsa null bırak.\n"
            "- context: Kararın alınma sebebi (transcript'te açıkça geçiyorsa, 1-2 cümle). Çıkarım YAPMA, sadece söyleneni yaz. Yoksa null.\n"
            "- alternatives_considered: Tartışılan ama reddedilen alternatifler (transcript'te geçiyorsa). Yoksa null.\n\n"
            "ÖRNEKLER (granülarite eşiğini bunlara göre kalibre et):\n\n"
            "Örnek 1 — ÇIKAR (net karar):\n"
            "  Konuşma: \"Tamam o zaman backend Go ile yazalım, herkes hemfikir mi? — Evet.\"\n"
            "  → {\"text\": \"Backend Go ile yazılacak\", \"item_type\": \"decision\", \"source_quote\": \"Tamam o zaman backend Go ile yazalım\"}\n\n"
            "Örnek 2 — ÇIKAR (somut görev):\n"
            "  Konuşma: \"Ali Cuma'ya kadar API endpoint'lerini hazırlayacak.\"\n"
            "  → {\"text\": \"API endpoint'leri Cuma'ya kadar hazırlanacak\", \"item_type\": \"action_item\", \"owner_name\": \"Ali\", \"source_quote\": \"Ali Cuma'ya kadar API endpoint'lerini hazırlayacak\"}\n\n"
            "Örnek 3 — ÇIKARMA (genel niyet):\n"
            "  Konuşma: \"Performans tarafına da bakmamız lazım bir ara.\"\n"
            "  → ÇIKARMA. Niyet beyanı, takip edilebilir somut bir aksiyon yok.\n\n"
            "Örnek 4 — ÇIKARMA (koşullu/spekülatif):\n"
            "  Konuşma: \"Belki Dovetail kullanmayı da değerlendirebiliriz, henüz karar vermedik.\"\n"
            "  → ÇIKARMA. Karar yok, sadece bir ihtimal.\n\n"
            "Örnek 5 — ÇIKARMA (meta yorum):\n"
            "  Konuşma: \"Sanırım toplantıyı planlanandan biraz erken bitirebiliriz.\"\n"
            "  → ÇIKARMA. Toplantı meta'sı, ürün/proje çıktısı değil.\n\n"
            "Örnek 6 — ÇIKAR (gerçek open_topic):\n"
            "  Konuşma: \"Stripe mı Iyzico mu kullanacağız hâlâ net değil, önümüzdeki hafta karar vereceğiz.\"\n"
            "  → {\"text\": \"Payment provider seçimi: Stripe vs Iyzico\", \"item_type\": \"open_topic\", \"source_quote\": \"Stripe mı Iyzico mu kullanacağız hâlâ net değil\"}\n\n"
            "Reference-only bloklar:\n"
            "- Transcript içinde <reference_only>...</reference_only> bloğu görürsen, o satırlar ÖNCEKİ chunk'tan bağlam için kopyalanmıştır. O satırları sadece bağlamı anlamak için OKU, ORADAN ITEM ÇIKARMA. Item'lar yalnızca bu blokların DIŞINDAKİ satırlardan gelmelidir.\n\n"
            f"- Tüm çıktılar {output_lang}"
            + exclusion_note
        )

    @staticmethod
    def _build_user_prompt(transcript: str, is_chunk: bool = False) -> str:
        chunk_note = ""
        if is_chunk:
            chunk_note = (
                "NOT: Bu, daha uzun bir toplantının bir BÖLÜMÜDÜR. "
                "Bu bölümden en fazla ~5 ana karar/aksiyon çıkar — 0 da olabilir, "
                "doldurmaya çalışma. Yalnızca granülarite eşiğini geçen, "
                "açık ve takip edilebilir item'ları al.\n\n"
            )
        return (
            chunk_note
            + "Aşağıdaki toplantı transcript'inden kararları, aksiyonları ve açık konuları çıkar.\n\n"
            f"TRANSCRIPT:\n{transcript}\n\n"
            "JSON formatında döndür:\n"
            "{\n"
            '  "items": [\n'
            "    {\n"
            '      "text": "Karar/aksiyon/konu metni — kısa ve net",\n'
            '      "item_type": "decision" | "action_item" | "open_topic",\n'
            '      "owner_name": "Kişi adı veya null",\n'
            '      "source_quote": "Transcript\'ten ilgili alıntı",\n'
            '      "context": "Kararın sebebi (varsa, yoksa null)",\n'
            '      "alternatives_considered": "Tartışılan alternatifler (varsa, yoksa null)"\n'
            "    }\n"
            "  ]\n"
            "}"
        )

    # ==================== Validation ====================

    @staticmethod
    def _validate_item(item: Dict[str, Any]) -> Optional[Dict[str, Any]]:
        """Validate and normalize a single extracted item. Returns None if invalid."""
        valid_types = {"decision", "action_item", "open_topic"}

        item_type = item.get("item_type", "")
        text = (item.get("text") or "").strip()
        source_quote = (item.get("source_quote") or "").strip()

        if item_type not in valid_types:
            logger.warning("Invalid item_type '%s', skipping: %s", item_type, text[:50])
            return None
        if not text:
            return None
        if not source_quote:
            logger.warning("Empty source_quote, skipping: %s", text[:50])
            return None

        return {
            "text": text,
            "item_type": item_type,
            "owner_name": item.get("owner_name"),
            "source_quote": source_quote,
            "context": item.get("context") or None,
            "alternatives_considered": item.get("alternatives_considered") or None,
        }


def _empty_usage() -> Dict[str, int]:
    return {
        "usage_prompt_tokens": 0,
        "usage_completion_tokens": 0,
        "usage_total_tokens": 0,
    }
