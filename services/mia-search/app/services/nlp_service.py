"""NLP Service - Business logic for meeting analysis using OpenAI with Prompt Optimization."""
import json
from typing import List, Dict, Any, Optional, Tuple
from concurrent.futures import ThreadPoolExecutor, as_completed
import time
import re
from collections import Counter
from app.services.nlp_json_utils import parse_json_with_retry
from app.utils.openai_gateway import create_chat_completion, extract_usage, TokenAccumulator

# Model configuration - Using latest GPT-5.2 models
OPTIMIZER_MODEL = "gpt-5.4"       # GPT-5.2 for prompt optimization
MAIN_MODEL = "gpt-5.4"            # GPT-5.2 for final generation (advanced reasoning)
CORRECTOR_MODEL = "gpt-5.4"         # GPT-4o for transcript correction (faster, cost-effective)
FALLBACK_MODEL = "gpt-5.4"          # Fallback model for unstable chunks

# Shared Technical Glossary (Single Source of Truth)
GLOSSARY_TERMS = [
    "POS Payment (NOT 'Post Payment')",
    "InstitutionId",
    "RefID (Reference ID)",
    "3DS (3D Secure)",
    "QR (Quick Response)",
    "Validation",
    "Transaction",
    "Auth",
    "Deposit Income",
    "Execute Payment"
]
# PromptOptimizer class removed for performance - master_instruction provides similar functionality


class NLPService:
    """Service class for NLP operations using OpenAI."""

    def __init__(self):
        # When non-None, every LLM response in this service feeds the
        # active accumulator via _track_usage. generate_comprehensive_summary
        # sets/clears this around its full pipeline so map+reduce+correction
        # tokens are all captured in one number — previously only the reduce
        # phase landed in the returned dict (undercount per issue #39 review).
        self._comprehensive_acc: Optional[TokenAccumulator] = None

    def _track_usage(self, response: Any, model: str) -> None:
        """Forward a response's token usage to the active accumulator.

        No-op when no comprehensive_summary is in flight (standalone
        endpoints like generate_progressive_summary / catch_me_up extract
        usage on their own). TokenAccumulator.add is thread-safe so the
        parallel map-phase ThreadPoolExecutor workers can call this
        directly without coordination here.
        """
        acc = self._comprehensive_acc
        if acc is None:
            return
        acc.add(extract_usage(response, model))
    
    def extract_text_from_chunks(self, chunks: List[Dict[str, Any]]) -> str:
        """Combine chunks into a single text for analysis."""
        texts = []
        for chunk in chunks:
            speaker = chunk.get('speaker_name') or chunk.get('speaker') or 'Konuşmacı'
            text = chunk.get('transcript_chunk') or chunk.get('text') or chunk.get('content', '')
            if text:
                texts.append(f"[{speaker}]: {text}")
        return '\n'.join(texts)

    def _safe_float(self, value: Any) -> float:
        """Best-effort conversion for numeric time values."""
        if value is None:
            return None
        try:
            return float(value)
        except (TypeError, ValueError):
            return None

    def _build_transcript_with_timing(self, chunks: List[Dict[str, Any]]) -> Tuple[str, List[Dict[str, float]]]:
        """
        Build transcript text with one logical line per source chunk and retain line-level time mapping.
        This enables topic timing alignment to real transcript blocks/segments.
        """
        lines = []
        line_times = []
        for chunk in chunks:
            speaker = chunk.get('speaker_name') or chunk.get('speaker') or 'Konuşmacı'
            text = chunk.get('transcript_chunk') or chunk.get('text') or chunk.get('content', '')
            if not text:
                continue

            # Keep a single line per source unit so topic line indices stay alignable.
            normalized_text = str(text).replace('\n', ' ').strip()
            if not normalized_text:
                continue

            lines.append(f"[{speaker}]: {normalized_text}")
            line_times.append({
                "start_time": self._safe_float(chunk.get("start_time")),
                "end_time": self._safe_float(chunk.get("end_time")),
            })

        return '\n'.join(lines), line_times

    def _resolve_chunk_time_range(
        self,
        line_times: List[Dict[str, float]],
        start_line: int,
        end_line: int,
    ) -> Tuple[float, float]:
        """Resolve chunk-level source time window from line-level timing map."""
        if not line_times:
            return None, None
        if start_line < 0 or end_line <= start_line:
            return None, None
        if start_line >= len(line_times):
            return None, None

        safe_end = min(end_line, len(line_times))
        time_slice = line_times[start_line:safe_end]
        if not time_slice:
            return None, None

        starts = [t.get("start_time") for t in time_slice if t.get("start_time") is not None]
        ends = [t.get("end_time") for t in time_slice if t.get("end_time") is not None]
        if not starts or not ends:
            return None, None

        return min(starts), max(ends)
    
    def generate_progressive_summary(self, chunks: List[Dict[str, Any]], previous_summary: str = None, language: str = "auto") -> Tuple[str, List[str], dict]:
        """
        Generate a progressive summary for catch-me-up.
        Uses single-stage generation for low latency.
        """
        transcript = self.extract_text_from_chunks(chunks)

        if not transcript:
            return ("No content yet." if language == "en" else "Henüz içerik bulunmuyor."), [], {}

        try:
            # Truncate if too long (Catch Me Up still uses raw transcript for speed)
            max_chars = 15000
            if len(transcript) > max_chars:
                transcript = transcript[:max_chars] + ("\n...[truncated]" if language == "en" else "\n...[devamı kısaltıldı]")

            # Build language-aware prompts
            if language == "en":
                lang_rule = "Write in English, clearly and professionally."
                system_msg = "You are a senior meeting analyst and summarization expert. Always respond in valid JSON format. Write in English."
            elif language == "auto":
                lang_rule = "Write in the same language as the transcript."
                system_msg = "You are a senior meeting analyst and summarization expert. Always respond in valid JSON format. Write in the same language as the transcript."
            else:
                lang_rule = "Türkçe, net ve profesyonel yaz."
                system_msg = "Sen üst düzey bir toplantı analisti ve özet uzmanısın. Yanıtlarını HER ZAMAN geçerli JSON formatında ver. Türkçe yaz."

            # Strategy: Single-Stage Dictionary-based Prompting
            if previous_summary:
                # INCREMENTAL UPDATE PROMPT
                final_prompt = f"""Sen bir toplantı asistanısın. Mevcut toplantı özetini, yeni gelen konuşmalarla güncellemen gerekiyor.

MEVCUT ÖZET:
{previous_summary}

YENİ KONUŞMALAR:
{transcript}

GÖREVLER:
1. Mevcut özeti, yeni bilgilerle birleştirerek güncelle.
2. Yeni konuşmalardaki önemli noktaları ekle.
3. {lang_rule}

YANIT FORMATI (JSON):
{{"summary": "güncellenmiş özet", "key_points": ["önemli madde 1", "önemli madde 2"]}}"""
            else:
                # FRESH SUMMARY PROMPT
                final_prompt = f"""Aşağıdaki toplantı transkriptini analiz et. Toplantıya geç katılan biri için:

1. Şu ana kadar konuşulanları 2-3 cümlede özetle
2. En kritik 3-5 noktayı bullet point olarak listele
3. {lang_rule}

TRANSKRİPT:
{transcript}

YANIT FORMATI (JSON):
{{"summary": "özet", "key_points": ["madde1", "madde2"]}}"""

            print(f"🚀 Sending to {MAIN_MODEL} for catch-up generation...")

            # Stage 2: Generate with powerful model
            response = create_chat_completion(
                model=MAIN_MODEL,
                messages=[
                    {"role": "system", "content": system_msg},
                    {"role": "user", "content": final_prompt}
                ]
            )
            usage = extract_usage(response, MAIN_MODEL)

            response_text = response.choices[0].message.content.strip()

            # Parse JSON with retry
            result = parse_json_with_retry(response_text)

            summary = result.get("summary", "Özet oluşturulamadı.")
            key_points = result.get("key_points", [])

            # Normalize to strings (GPT-5.1 might return objects instead of strings)
            def to_string(item):
                if isinstance(item, str):
                    return item
                elif isinstance(item, dict):
                    return item.get("point") or item.get("description") or item.get("item") or str(item)
                else:
                    return str(item)

            key_points = [to_string(p) for p in key_points]

            # Add header
            header = f"📊 {len(chunks)} segment analiz edildi | Model: {MAIN_MODEL}\n\n"

            print(f"✅ Catch-up generated successfully")
            usage_dict = {"usage_prompt_tokens": usage.prompt_tokens, "usage_completion_tokens": usage.completion_tokens, "usage_total_tokens": usage.total_tokens}
            return header + summary, key_points, usage_dict
            
        except json.JSONDecodeError as e:
            print(f"JSON parse error: {e}")
            print(f"Response was: {response_text[:500] if 'response_text' in locals() else 'N/A'}")
            return f"Toplantı devam ediyor - {len(chunks)} segment. (JSON parse hatası)", [], {}
        except Exception as e:
            print(f"OpenAI API error in progressive summary: {e}")
            return f"Toplantı devam ediyor - {len(chunks)} segment. (Hata: {str(e)[:100]})", [], {}
    
    def generate_agent_catch_me_up(self, existing_summary: str, recent_transcript: str, instructions: str = None, language: str = "auto") -> Tuple[str, dict]:
        """
        Generate catch-me-up summary from pre-prepared data (from Go backend).
        This is a simplified version that doesn't fetch from DB.
        """
        try:
            # Build language-aware defaults
            if language == "en":
                default_instructions = "You are MIA. Your task is to answer 'What's being discussed right now?' for users who joined late or lost focus."
                truncation_suffix = "\n...[truncated]"
                task_with_summary = 'TASK: Using the existing summary as context, summarize the current status from the recent conversations in one paragraph. Answer "What\'s being discussed right now?"'
                task_without_summary = 'TASK: Summarize the current status from these conversations in one paragraph. Answer "What\'s being discussed right now?"'
                existing_label, recent_label = "EXISTING SUMMARY", "RECENT CONVERSATIONS"
            elif language == "auto":
                default_instructions = "Sen MIA'sın. Toplantıya sonradan dahil olan veya dikkatini kaybeden kullanıcıya 'Şu an ne konuşuluyor?' sorusunun cevabını veriyorsun. Transkriptin dilinde yanıt ver."
                truncation_suffix = "\n...[devamı kısaltıldı]"
                task_with_summary = 'GÖREV: Mevcut özeti bağlam olarak kullanarak, son konuşmalardan şu anki durumu tek paragrafta netçe özetle. Kullanıcıya "Şu an ne konuşuluyor?" sorusunun cevabını ver. Transkriptin dilinde yanıt ver.'
                task_without_summary = 'GÖREV: Bu konuşmalardan şu anki durumu tek paragrafta netçe özetle. Kullanıcıya "Şu an ne konuşuluyor?" sorusunun cevabını ver. Transkriptin dilinde yanıt ver.'
                existing_label, recent_label = "MEVCUT ÖZET", "SON KONUŞMALAR"
            else:  # tr
                default_instructions = "Sen MIA'sın. Toplantıya sonradan dahil olan veya dikkatini kaybeden kullanıcıya 'Şu an ne konuşuluyor?' sorusunun cevabını veriyorsun."
                truncation_suffix = "\n...[devamı kısaltıldı]"
                task_with_summary = 'GÖREV: Mevcut özeti bağlam olarak kullanarak, son konuşmalardan şu anki durumu tek paragrafta netçe özetle. Kullanıcıya "Şu an ne konuşuluyor?" sorusunun cevabını ver.'
                task_without_summary = 'GÖREV: Bu konuşmalardan şu anki durumu tek paragrafta netçe özetle. Kullanıcıya "Şu an ne konuşuluyor?" sorusunun cevabını ver.'
                existing_label, recent_label = "MEVCUT ÖZET", "SON KONUŞMALAR"

            system_instructions = instructions or default_instructions

            # Truncate if too long
            max_chars = 15000
            if len(recent_transcript) > max_chars:
                recent_transcript = recent_transcript[:max_chars] + truncation_suffix

            # Build prompt based on whether we have existing summary
            if existing_summary and existing_summary.strip():
                user_prompt = f"""{existing_label}:
{existing_summary}

{recent_label}:
{recent_transcript}

{task_with_summary}"""
            else:
                user_prompt = f"""{recent_label}:
{recent_transcript}

{task_without_summary}"""

            print(f"🚀 Agent Catch-Me-Up: Sending to {MAIN_MODEL}...")
            
            response = create_chat_completion(
                model=MAIN_MODEL,
                messages=[
                    {"role": "system", "content": system_instructions},
                    {"role": "user", "content": user_prompt}
                ]
            )
            usage = extract_usage(response, MAIN_MODEL)

            summary = response.choices[0].message.content.strip()
            print(f"✅ Agent Catch-Me-Up generated successfully")
            usage_dict = {"usage_prompt_tokens": usage.prompt_tokens, "usage_completion_tokens": usage.completion_tokens, "usage_total_tokens": usage.total_tokens}
            return summary, usage_dict
            
        except Exception as e:
            print(f"Agent Catch-Me-Up error: {e}")
            raise RuntimeError(f"Catch-me-up failed: {str(e)[:200]}") from e
    
    def generate_checkpoint_summary(self, chunks: List[Dict[str, Any]], current_summary: str = None, language: str = "auto") -> Tuple[str, dict]:
        """
        Generate a checkpoint summary for background processing.
        Returns (summary_text, usage_dict).
        """
        summary, _, usage_dict = self.generate_progressive_summary(chunks, previous_summary=current_summary, language=language)
        # Remove header if present
        if "segment analiz edildi" in summary:
            parts = summary.split("\n\n", 1)
            if len(parts) > 1:
                return parts[1], usage_dict
        return summary, usage_dict
    
    def correct_transcript(self, transcript: str) -> str:
        """
        Layer 1: Correct and clean the raw transcript.
        - Fixes punctuation, capitalization, and grammar.
        - Identifies and standardizes speaker names (if implied).
        - Removes filler words (uh, um, things like that) but keeps meaning.
        
        For long transcripts, splits into chunks and corrects each IN PARALLEL.
        """
        if not transcript or len(transcript) < 50:
            return transcript

        print(f"🧹 Layer 1: Correcting transcript ({len(transcript)} chars)...")

        chunk_limit = 25000  # Max characters per correction chunk (~20% increase)

        # PRE-SCAN: Detect terminology inconsistencies before correction
        dynamic_glossary = self._prescan_terminology(transcript)

        # If transcript fits in one chunk, process directly
        if len(transcript) <= chunk_limit:
            return self._correct_single_chunk(transcript, dynamic_glossary)

        # Split into chunks for long transcripts
        correction_chunks = self._split_for_correction(transcript, chunk_limit)
        print(f"📦 Transcript split into {len(correction_chunks)} correction chunks")

        # PARALLEL PROCESSING for correction chunks (all receive the same dynamic glossary)
        print(f"⚡ Correcting {len(correction_chunks)} chunks in parallel...")
        corrected_results = {}

        with ThreadPoolExecutor(max_workers=8) as executor:
            futures = {
                executor.submit(self._correct_single_chunk, chunk, dynamic_glossary): idx
                for idx, chunk in enumerate(correction_chunks)
            }

            for future in as_completed(futures):
                idx = futures[future]
                try:
                    corrected_results[idx] = future.result(timeout=300)
                    print(f"✅ Correction chunk {idx + 1}/{len(correction_chunks)} completed")
                except Exception as e:
                    print(f"⚠️ Correction chunk {idx + 1} failed, using raw: {e}")
                    corrected_results[idx] = correction_chunks[idx]

        # Combine in order
        corrected_parts = [corrected_results[i] for i in range(len(correction_chunks))]
        combined = "\n".join(corrected_parts)
        print(f"✅ All {len(correction_chunks)} chunks corrected and combined.")
        return combined
    
    def _split_for_correction(self, transcript: str, chunk_size: int) -> list:
        """Split transcript into chunks for correction, trying to break at speaker tags."""
        if len(transcript) <= chunk_size:
            return [transcript]
        
        chunks = []
        current_pos = 0
        
        while current_pos < len(transcript):
            end_pos = current_pos + chunk_size
            
            if end_pos >= len(transcript):
                chunks.append(transcript[current_pos:])
                break
            
            # Try to find a speaker tag boundary [Name]: for cleaner splits
            search_start = max(current_pos + chunk_size - 1000, current_pos)
            search_end = min(current_pos + chunk_size + 500, len(transcript))
            search_region = transcript[search_start:search_end]
            
            # Look for speaker tag pattern
            best_break = -1
            import re
            speaker_matches = list(re.finditer(r'\n\[', search_region))
            if speaker_matches:
                # Use the last match before chunk_size boundary
                for match in speaker_matches:
                    pos = match.start()
                    if pos > best_break:
                        best_break = pos
            
            # Fallback to paragraph or sentence boundaries
            if best_break == -1:
                for sep in ['\n\n', '.\n', '. ', '\n']:
                    pos = search_region.rfind(sep)
                    if pos > best_break:
                        best_break = pos
                        break
            
            if best_break > 0:
                end_pos = search_start + best_break
            
            chunks.append(transcript[current_pos:end_pos].strip())
            current_pos = end_pos
        
        return chunks
    
    def _prescan_terminology(self, transcript: str) -> str:
        """
        Pre-scan: Analyze full transcript to detect inconsistent terms before parallel correction.
        Returns a meeting-specific terminology guide to inject into each correction chunk.
        """
        # Sample the transcript (first + middle + last sections) to keep prompt short
        sample_size = 3000
        if len(transcript) <= sample_size * 2:
            sample = transcript
        else:
            third = len(transcript) // 3
            sample = (
                transcript[:sample_size] +
                "\n[...]\n" +
                transcript[third:third + sample_size] +
                "\n[...]\n" +
                transcript[-sample_size:]
            )

        prompt = f"""Aşağıdaki toplantı transkript örneklemini analiz et. STT (Speech-to-Text) sistemi bazı kelimeleri yanlış tanımış olabilir.

GÖREV: Tutarsız veya şüpheli terimleri tespit et. Frekans analizi ve bağlam kontrolü yaparak her terim için DOĞRU formu belirle.

Örnek STT hataları:
- Aynı kavram farklı yazılmış olabilir (ör: "API" vs "IP", "state" vs "istade")
- Teknik terimler bozulmuş olabilir (ör: "Fatura" → "Nokolay", "Telefon")
- Kişi isimleri tutarsız olabilir (ör: "Fatih" vs "Fatık")

TRANSKRİPT ÖRNEKLEMİ:
{sample}

YANIT FORMATI (JSON):
{{
  "corrections": [
    {{"wrong": "yanlış terim", "correct": "doğru terim", "reason": "kısa açıklama"}}
  ]
}}

Eğer tutarsızlık yoksa boş liste döndür: {{"corrections": []}}
Sadece JSON döndür."""

        try:
            response = create_chat_completion(
                model=CORRECTOR_MODEL,
                messages=[
                    {"role": "system", "content": "Sen bir STT hata analisti sin. Transkriptteki terminoloji tutarsızlıklarını tespit edersin. Sadece JSON döndür."},
                    {"role": "user", "content": prompt}
                ],
                max_completion_tokens=500
            )
            self._track_usage(response, CORRECTOR_MODEL)

            response_text = response.choices[0].message.content.strip()
            result = parse_json_with_retry(response_text)
            corrections = result.get("corrections", [])

            if not corrections:
                print("✅ Pre-scan: No terminology inconsistencies found.")
                return ""

            # Build a terminology guide string
            guide_lines = []
            for c in corrections:
                guide_lines.append(f"- \"{c['wrong']}\" → \"{c['correct']}\" ({c.get('reason', '')})")

            guide = "\n".join(guide_lines)
            print(f"🔍 Pre-scan: Found {len(corrections)} terminology corrections:\n{guide}")
            return guide

        except Exception as e:
            print(f"⚠️ Pre-scan failed, continuing without: {e}")
            return ""

    def _correct_single_chunk(self, chunk: str, dynamic_glossary: str = "") -> str:
        """Correct a single transcript chunk."""
        if not chunk or len(chunk) < 50:
            return chunk

        glossary_text = ", ".join(GLOSSARY_TERMS)

        # Build dynamic glossary section
        dynamic_section = ""
        if dynamic_glossary:
            dynamic_section = f"""
TOPLANTIYA ÖZGÜ TERİM DÜZELTMELERİ (Pre-scan ile tespit edildi, bunları uygula):
{dynamic_glossary}
"""

        prompt = f"""Sen dünyaca ünlü bir redaktör ve editörsün. Aşağıdaki ham toplantı transkriptini temizle ve düzelt.

GÖREVLER:
1. Yazım yanlışlarını, noktalama işaretlerini ve büyük/küçük harf hatalarını düzelt.
2. Anlam bozukluğu yaratan "eee", "ııı", "şey" gibi dolgu kelimelerini (filler words) çıkar.
3. Konuşma akışını bozmadan cümleleri daha kurallı hale getir.
4. Konuşmacı etiketlerini ([Ahmet]: vb.) ASLA DEĞİŞTİRME.
5. İçeriğin anlamını asla değiştirme.

ÖZEL TEKNİK SÖZLÜK (Bunları doğru duyduğuna emin ol):
{glossary_text}
{dynamic_section}
KURALLAR:
- "Pos Payment" terimini "Post Payment" olarak değiştirme.
- Teknik terimleri olduğu gibi koru.
- Aynı terim transkript boyunca TUTARLI kullanılmalı. Eğer bir terim hem doğru hem yanlış yazılmışsa, DOĞRU olan versiyonu her yerde kullan.

HAM TRANSKRİPT:
{chunk}

Sadece düzeltilmiş metni döndür, başka hiçbir şey yazma."""

        try:
            response = create_chat_completion(
                model=CORRECTOR_MODEL,
                messages=[
                    {"role": "system", "content": "Sen bir redaktörsün. Sadece düzeltilmiş metni ver."},
                    {"role": "user", "content": prompt}
                ]
            )
            self._track_usage(response, CORRECTOR_MODEL)

            return response.choices[0].message.content.strip()

        except Exception as e:
            print(f"⚠️ Chunk correction failed, using raw: {e}")
            return chunk

    def generate_master_instruction(self, title: str, participants: List[str], duration_minutes: int) -> str:
        """
        Metadata-based Optimizer: Runs ONCE at the start.
        Input: Meeting title, participants, duration.
        Output: Instruction set for all chunk summarizations.
        """
        # Skip API call for generic/uninformative titles — fallback is sufficient
        generic_titles = {"toplantı", "meeting", "görüşme", "sync", "haftalık", "weekly", "daily", "standup", "günlük"}
        title_lower = title.strip().lower()
        if not title or title_lower in generic_titles or len(title_lower) < 4:
            print(f"⏭️ Skipping master instruction for generic title: '{title}' — using default")
            return "Genel toplantı özeti çıkar. Kararları, aksiyonları ve deadline'ları vurgula. Teknik detayları koru."

        print(f"🎯 Generating master instruction for: {title}")
        
        prompt = f"""Sen toplantı analisti MIA'sın. Aşağıdaki toplantı metadata'sına bakarak, bu toplantının özetini çıkarırken kullanılacak TALİMAT SETİ üret.

TOPLANTI BİLGİLERİ:
- Başlık: {title}
- Katılımcılar: {', '.join(participants) if participants else 'Belirtilmemiş'}
- Süre: {duration_minutes} dakika

GÖREV:
Bu toplantıyı parça parça özetleyeceğim. Her parçayı özetlerken neye odaklanmalıyım? Neyi görmezden gelmeliyim?

KURALLAR:
- Sadece 2-3 cümlelik direktif ver.
- Toplantı tipini tahmin et (standup, planning, retrospective, teknik, strateji vs.)
- Önemli olabilecek anahtar kelimeleri belirt.

Örnek çıktı: "Bu bir teknik planlama toplantısı. Kod değişkenlerine, veritabanı şemalarına ve deadline tarihlerine odaklan. Sosyal sohbetleri görmezden gel."

TALİMAT SETİNİ VER:"""

        try:
            response = create_chat_completion(
                model=OPTIMIZER_MODEL,
                messages=[
                    {"role": "system", "content": "Sen toplantı analisti MIA'sın. Sadece talimat setini ver, başka açıklama yapma."},
                    {"role": "user", "content": prompt}
                ],
                max_completion_tokens=200
            )
            self._track_usage(response, OPTIMIZER_MODEL)

            instruction = response.choices[0].message.content.strip()
            print(f"✅ Master instruction generated: {instruction[:100]}...")
            return instruction

        except Exception as e:
            print(f"⚠️ Master instruction generation failed: {e}")
            return "Genel toplantı özeti çıkar. Kararları ve aksiyonları vurgula."

    def segment_transcript_by_topic_with_boundaries(self, transcript: str) -> List[Dict[str, Any]]:
        """
        Topic-aware segmentation with line boundaries.
        Returns chunks as dicts: {"text", "start_line", "end_line"} where end_line is exclusive.
        """
        lines = transcript.split('\n') if transcript else []

        # Guard: Empty transcript
        if not transcript or len(transcript.strip()) < 1000:
            return [{
                "text": transcript or "",
                "start_line": 0,
                "end_line": max(len(lines), 1),
            }]

        print(f"🧠 Segmenting transcript by topic (Length: {len(transcript)} chars)...")

        numbered_lines = [f"{i}: {line}" for i, line in enumerate(lines)]
        numbered_text = "\n".join(numbered_lines)

        prompt = f"""Sen bir Toplantı Segmentasyon Uzmanısın. Aşağıdaki transkripti analiz et ve KONU DEĞİŞİMLERİNİN olduğu satır numaralarını belirle.

KURALLAR:
1. Toplantıyı mantıksal "bölümlere" (chapters) ayır.
2. Her bölüm tek bir ana konuyu ele almalı.
3. Çok uzun konuları (200+ satır) mantıksal duraklama noktalarından böl.
4. Asla cümle ortasında bölme.
5. Yanıtı SADECE JSON formatında tamsayı (integer) listesi olarak ver.
6. İlk satır (0) her zaman başlangıçtır, listeye eklemene gerek yok.

Örnek Yanıt: [45, 120, 250, 400]
(Bu, 0-44 arası Konu 1, 45-119 arası Konu 2, vb. demektir)

TRANSKRİPT:
{numbered_text}

SADECE JSON LISTESI DÖNDÜR:"""

        try:
            response = create_chat_completion(
                model="gpt-5.4",
                messages=[
                    {"role": "system", "content": "Sen bir JSON makinesisn. Sadece [10, 25, 44] formatında liste döndür."},
                    {"role": "user", "content": prompt}
                ],
                max_completion_tokens=500
            )
            self._track_usage(response, "gpt-5.4")

            response_text = response.choices[0].message.content.strip()
            split_indices = parse_json_with_retry(response_text)
            if not isinstance(split_indices, list):
                raise ValueError(f"Segmentation returned invalid format: {type(split_indices)}")

            split_indices = sorted([int(x) for x in split_indices if isinstance(x, (int, float))])

            chunk_ranges = []
            current_start = 0
            for split_idx in split_indices:
                if split_idx <= current_start or split_idx >= len(lines):
                    continue
                chunk_ranges.append((current_start, split_idx))
                current_start = split_idx
            if current_start < len(lines):
                chunk_ranges.append((current_start, len(lines)))

            boundary_chunks = []
            for start_line, end_line in chunk_ranges:
                chunk_text = "\n".join(lines[start_line:end_line]).strip()
                if chunk_text:
                    boundary_chunks.append({
                        "text": chunk_text,
                        "start_line": start_line,
                        "end_line": end_line,
                    })

            print(f"✅ Transcript segmented into {len(boundary_chunks)} topic-based chunks (Raw)")
            optimized = self._optimize_chunk_boundaries(boundary_chunks, lines)
            print(f"✅ Optimized into {len(optimized)} chunks (Boundary Safety Net Applied)")
            return optimized

        except Exception as e:
            print(f"⚠️ Topic segmentation failed, falling back to legacy chunking: {e}")
            fallback_chunks = self.chunk_transcript_legacy(transcript)
            return [
                {"text": chunk_text, "start_line": 0, "end_line": len(lines)}
                for chunk_text in fallback_chunks if chunk_text
            ]

    def segment_transcript_by_topic(self, transcript: str) -> List[str]:
        """
        Backward-compatible wrapper that returns only chunk texts.
        """
        return [chunk.get("text", "") for chunk in self.segment_transcript_by_topic_with_boundaries(transcript)]

    def _optimize_chunk_boundaries(self, chunks: List[Dict[str, Any]], lines: List[str]) -> List[Dict[str, Any]]:
        """
        Boundary-preserving safety net:
        1) Split oversized chunks by line boundaries.
        2) Merge tiny chunks to reduce context fragmentation.
        """
        MAX_CHUNK_SIZE = 16000
        MIN_CHUNK_SIZE = 1000

        split_chunks = []
        for chunk in chunks:
            text = chunk.get("text", "")
            start_line = int(chunk.get("start_line", 0))
            end_line = int(chunk.get("end_line", 0))
            if len(text) <= MAX_CHUNK_SIZE:
                split_chunks.append(chunk)
                continue

            print(f"✂️ Splitting oversized chunk with boundaries ({len(text)} chars)...")
            local_start = start_line
            buffer_lines = []
            buffer_len = 0

            for line_idx in range(start_line, end_line):
                line = lines[line_idx]
                add_len = len(line) + (1 if buffer_lines else 0)
                if buffer_lines and (buffer_len + add_len > MAX_CHUNK_SIZE):
                    chunk_text = "\n".join(buffer_lines).strip()
                    if chunk_text:
                        split_chunks.append({
                            "text": chunk_text,
                            "start_line": local_start,
                            "end_line": line_idx,
                        })
                    local_start = line_idx
                    buffer_lines = [line]
                    buffer_len = len(line)
                else:
                    buffer_lines.append(line)
                    buffer_len += add_len

            if buffer_lines:
                chunk_text = "\n".join(buffer_lines).strip()
                if chunk_text:
                    split_chunks.append({
                        "text": chunk_text,
                        "start_line": local_start,
                        "end_line": end_line,
                    })

        merged_chunks = []
        for chunk in split_chunks:
            text = chunk.get("text", "")
            if not merged_chunks:
                merged_chunks.append(chunk)
                continue

            prev = merged_chunks[-1]
            prev_text = prev.get("text", "")
            if len(text) < MIN_CHUNK_SIZE and len(prev_text) + len(text) + 1 <= MAX_CHUNK_SIZE:
                prev["text"] = (prev_text + "\n" + text).strip()
                prev["end_line"] = chunk.get("end_line", prev.get("end_line"))
            else:
                merged_chunks.append(chunk)

        return merged_chunks

    def _optimize_chunk_sizes(self, chunks: List[str]) -> List[str]:
        """
        Safety Net:
        1. Split chunks > 16.000 chars (Prevent GPT-5.2 Timeout)
        2. Merge chunks < 1.000 chars (Prevent Context Fragmentation)
        """
        MAX_CHUNK_SIZE = 16000
        MIN_CHUNK_SIZE = 1000
        
        # Pass 1: Split Oversized Chunks
        split_chunks = []
        for chunk in chunks:
            if len(chunk) > MAX_CHUNK_SIZE:
                print(f"✂️ Splitting oversized chunk ({len(chunk)} chars)...")
                # Use legacy splitter for safe segmentation
                sub_chunks = self.chunk_transcript_legacy(chunk, chunk_size=MAX_CHUNK_SIZE)
                split_chunks.extend(sub_chunks)
            else:
                split_chunks.append(chunk)
        
        # Pass 2: Merge Undersized Chunks
        merged_chunks = []
        current_buffer = ""
        
        for chunk in split_chunks:
            # If buffer + chunk is still small, keep accumulating
            if len(current_buffer) + len(chunk) < MIN_CHUNK_SIZE:
                current_buffer += "\n" + chunk if current_buffer else chunk
                continue
            
            # If buffer exists, prepend it to current chunk
            if current_buffer:
                combined = current_buffer + "\n" + chunk
                # Check if combined is now too big (rare but possible)
                if len(combined) > MAX_CHUNK_SIZE:
                    # Flush buffer as is, take chunk as is
                    merged_chunks.append(current_buffer)
                    merged_chunks.append(chunk)
                else:
                    merged_chunks.append(combined)
                current_buffer = ""
            else:
                merged_chunks.append(chunk)
        
        # Flush remaining buffer
        if current_buffer:
             # Merge into last chunk if possible
             if merged_chunks and len(merged_chunks[-1]) + len(current_buffer) <= MAX_CHUNK_SIZE:
                 merged_chunks[-1] += "\n" + current_buffer
             else:
                 merged_chunks.append(current_buffer)
                 
        return merged_chunks

    def chunk_transcript_legacy(self, transcript: str, chunk_size: int = 16000) -> List[str]:
        """
        Legacy char-based chunking (Fixed size).
        Kept as fallback.
        """
        # Guard: Empty or whitespace-only transcript
        if not transcript or len(transcript.strip()) == 0:
            return []
        
        if len(transcript) <= chunk_size:
            return [transcript]
        
        chunks = []
        current_pos = 0
        
        while current_pos < len(transcript):
            end_pos = current_pos + chunk_size
            
            if end_pos >= len(transcript):
                chunks.append(transcript[current_pos:])
                break
            
            # Try to find a good break point
            # Priority 1: Speaker change [Name]: (using \n[ as marker)
            # Priority 2: Paragraph (double newline)
            # Priority 3: Sentence ending
            
            search_start = max(current_pos + chunk_size - 1000, current_pos)
            search_end = min(current_pos + chunk_size + 500, len(transcript))
            search_region = transcript[search_start:search_end]
            
            best_break = -1
            
            # Check for speaker tag pattern first
            import re
            # Look for newline followed by [ (likely speaker start)
            speaker_matches = list(re.finditer(r'\n\[', search_region))
            if speaker_matches:
                 # Use the last speaker change within reasonable range
                 for match in speaker_matches:
                    pos = match.start()
                    # Prefer not to leave a tiny chunk at the end
                    if pos > 0: 
                        best_break = pos
            
            # Fallback to standard delimiters if no speaker tag found
            if best_break == -1:
                for sep in ['\n\n', '.\n', '. ', '\n']:
                    pos = search_region.rfind(sep)
                    if pos > best_break:
                        best_break = pos
                        break
            
            if best_break > 0:
                end_pos = search_start + best_break + 1
            
            chunks.append(transcript[current_pos:end_pos].strip())
            current_pos = end_pos
        
        print(f"📦 Transcript split into {len(chunks)} chunks (Legacy)")
        return chunks

    def _normalize_actions(self, raw_actions: List[Any], commitment_level: str = "committed") -> List[Dict[str, Any]]:
        """Normalize action payloads into a stable dict structure for reduce phase."""
        normalized_level = commitment_level if commitment_level in ("committed", "candidate") else "candidate"
        structured_actions = []
        for item in raw_actions:
            if isinstance(item, dict):
                item_level = item.get("commitment_level", normalized_level)
                if item_level not in ("committed", "candidate"):
                    item_level = normalized_level
                structured_actions.append({
                    "assignee": item.get("assignee", "Belirsiz"),
                    "task": item.get("task", ""),
                    "context": item.get("context", ""),
                    "priority": item.get("priority", "medium"),
                    "deadline": item.get("deadline"),
                    "is_optional": item.get("is_optional", False),
                    "commitment_level": item_level,
                })
            else:
                structured_actions.append({
                    "assignee": "Belirsiz",
                    "task": str(item),
                    "context": "",
                    "priority": "medium",
                    "deadline": None,
                    "is_optional": False,
                    "commitment_level": normalized_level,
                })
        return structured_actions

    def _build_segment_prompt(self, chunk: str, master_instruction: str, chunk_index: int, reduced_schema: bool = False) -> str:
        """Builds prompt variants for strict and reduced-schema recovery attempts."""
        from datetime import datetime
        import_date_note = datetime.now().strftime("%Y-%m-%d")

        if reduced_schema:
            return f"""{master_instruction}

Aşağıdaki TOPLANTI BÖLÜMÜNÜ (Topic Segment {chunk_index + 1}) analiz et.
Sadece kritik alanları çıkar. JSON dışında hiçbir şey döndürme.

TRANSKRİPT PARÇASI:
{chunk}

KURALLAR:
- Halüsinasyon yapma, sadece metinde geçenleri kullan.
- TEKNİK SÖZLÜK: {", ".join(GLOSSARY_TERMS)}
- Tarih geçerse ISO formatına çevir (bugün {import_date_note}).
- AKSİYON KALİTE FİLTRESİ: Somut bir çıktı/deliverable veya uygulanabilir next step YOKSA aksiyon olarak listeleme.

YANIT FORMATI (JSON):
{{
  "topic_title": "Kısa başlık",
  "executive_summary": "2-4 cümle özlü özet",
  "audit": {{
    "decisions": ["..."],
        "committed_actions": [
          {{
            "assignee": "İsim veya Belirsiz",
            "task": "Görev",
            "context": "Kısa bağlam",
            "priority": "high/medium/low",
            "deadline": "ISO tarih veya null",
        "is_optional": false,
        "commitment_level": "committed"
      }}
    ],
        "candidate_actions": [
          {{
            "assignee": "İsim veya Belirsiz",
            "task": "Görev",
            "context": "Kısa bağlam",
            "priority": "high/medium/low",
            "deadline": "ISO tarih veya null",
        "is_optional": false,
        "commitment_level": "candidate"
      }}
    ],
    "debates": ["..."],
    "uncertainties": ["..."]
  }},
  "sentiment": "positive/neutral/negative/heated"
}}"""

        return f"""{master_instruction}

Aşağıdaki TOPLANTI BÖLÜMÜNÜ (Topic Segment {chunk_index + 1}) analiz et. Bu bölüm tek bir ana konuyu içeriyor.

GÖREVLER (Önem Sırasına Göre):

1. 🛑 Denetçi Modu (The Audit):
   - KARARLAR: Kesinleşen kararları "Kim, Ne zaman, Ne" formatında çıkar.
   - COMMITTED AKSİYONLAR: owner + net görev + uygulanabilir fiil + çelişkiyle geri alınmamış aksiyonlar.
   - CANDIDATE AKSİYONLAR: aksiyon olma ihtimali yüksek ama owner/deadline/scope gibi kritik alanı eksik maddeler.
   - ⚠️ AKSİYON KALİTE FİLTRESİ: Somut bir çıktı (deliverable), obje veya uygulanabilir next step YOKSA aksiyon olarak listeleme. "Bakalım", "düşünelim", "bir bakayım" gibi ifadeler ancak somut bir hedef belirtilmişse aksiyon sayılır.
   - TARTIŞMALAR: Konuşulan ama bir sonuca bağlanamayan ucu açık konuları not et.
   - ÇELİŞKİLER: Bu bölümün kendi içinde çelişen ifadeler varsa yakala (örn: önce "hazır" denip sonra "bug var" denilmesi).

2. ✍️ Yazman Modu (The Scribe):
   - Bu bölümün hikayesini anlatan, akıcı bir paragraf yaz.
   - "Ahmet şunu dedi, Mehmet bunu dedi" gibi transkript tekrarı YAPMA.
   - Olayın özünü ve sonucunu anlat.

3. ⚠️ HALÜSİNASYON KORUMASI:
   - SADECE transkriptte açıkça geçen bilgileri yaz. Çıkarım yapma, bilgi uydurma.
   - Transkriptte bahsedilmeyen bir konu, karar veya aksiyon ASLA ekleme.
   - Eğer bir bilgiden emin değilsen, "uncertainties" alanına yaz.
   - TEKNİK SÖZLÜĞE UY: {", ".join(GLOSSARY_TERMS)}

TRANSKRİPT PARÇASI:
{chunk}

YANIT FORMATI (JSON):
{{
  "topic_title": "Bu bölüm için kısa ve açıklayıcı bir başlık (örn: Bütçe Revizyonu)",
  "executive_summary": "Yazman modu ile yazılmış akıcı özet paragrafı",
  "audit": {{
    "decisions": ["Karar 1", "Karar 2"],
        "committed_actions": [
          {{ 
            "assignee": "İsim (veya 'Belirsiz')", 
            "task": "Görev", 
            "context": "Neden atandı?",
            "priority": "high/medium/low",
            "deadline": "ISO tarih (ör: 2025-02-10) veya null",
        "is_optional": false,
        "commitment_level": "committed"
      }}
    ],
        "candidate_actions": [
          {{ 
            "assignee": "İsim (veya 'Belirsiz')", 
            "task": "Görev", 
            "context": "Neden atandı?",
            "priority": "high/medium/low",
            "deadline": "ISO tarih (ör: 2025-02-10) veya null",
        "is_optional": false,
        "commitment_level": "candidate"
      }}
    ],
    "debates": ["Tartışma 1", "Tartışma 2"],
    "uncertainties": ["Netleşmeyen noktalar"]
  }},
  "sentiment": "positive/neutral/negative/heated"
}}

AKSİYON ALANLARI TALİMATI:
- priority: "high" = Kritik/Acil işler (bloklayan, deadline'ı yakın), "medium" = Standart görevler, "low" = Rutin/Zamanı esnek işler
- is_optional: True = Görev metninde "(opsiyonel)", "(düşük öncelik)", "(nice to have)" gibi ifadeler varsa veya bağlamda zorunlu olmadığı belirtilmişse. Bu durumda parantez içi ifadeyi görev metninden SİL.
- deadline: Eğer transkriptte "yarın", "cuma", "15 Şubat" gibi tarih geçiyorsa ISO formatına çevir (bugün {import_date_note} olarak kabul edilebilir). Yoksa null.
- commitment_level: "committed" veya "candidate"
"""

    def _analyze_topic_segment_once(self, chunk: str, master_instruction: str, chunk_index: int, model: str, reduced_schema: bool) -> Dict[str, Any]:
        """Run one model attempt and normalize output."""
        prompt = self._build_segment_prompt(chunk, master_instruction, chunk_index, reduced_schema=reduced_schema)

        response = create_chat_completion(
            model=model,
            messages=[
                {"role": "system", "content": "Sen kıdemli bir Toplantı Denetçisi ve Yazmanısın. Çıktıların kesin, net ve JSON formatındadır."},
                {"role": "user", "content": prompt},
            ],
            max_completion_tokens=900 if reduced_schema else 1500,
        )
        self._track_usage(response, model)

        response_text = response.choices[0].message.content.strip()
        result = parse_json_with_retry(response_text)
        audit = result.get("audit", {})
        committed_actions = audit.get("committed_actions", [])
        candidate_actions = audit.get("candidate_actions", [])

        # Backward compatibility for older prompt responses.
        if not committed_actions and not candidate_actions:
            committed_actions = audit.get("action_items", [])

        normalized_actions = []
        normalized_actions.extend(self._normalize_actions(committed_actions, commitment_level="committed"))
        normalized_actions.extend(self._normalize_actions(candidate_actions, commitment_level="candidate"))

        return {
            "index": chunk_index,
            "summary": result.get("executive_summary", ""),
            "topic_title": result.get("topic_title", ""),
            "decisions": audit.get("decisions", []),
            "debates": audit.get("debates", []),
            "actions": normalized_actions,
            "uncertainties": audit.get("uncertainties", []),
            "sentiment": result.get("sentiment", "neutral"),
        }

    def _recover_chunk_with_subchunks(self, chunk: str, master_instruction: str, chunk_index: int) -> Dict[str, Any]:
        """Final recovery: split problematic chunk and merge sub-results."""
        if len(chunk) < 5000:
            raise RuntimeError("chunk_too_small_for_subchunk_recovery")

        sub_chunks = self.chunk_transcript_legacy(chunk, chunk_size=max(4000, min(8000, len(chunk) // 2)))
        if len(sub_chunks) <= 1:
            raise RuntimeError("subchunk_split_not_effective")

        print(f"🧩 Chunk {chunk_index + 1}: sub-chunk recovery with {len(sub_chunks)} parts")
        recovered = []
        for sub_idx, sub_chunk in enumerate(sub_chunks):
            sub_error = None
            for model in (MAIN_MODEL, FALLBACK_MODEL):
                try:
                    sub_result = self._analyze_topic_segment_once(
                        sub_chunk,
                        master_instruction,
                        chunk_index,
                        model=model,
                        reduced_schema=True,
                    )
                    recovered.append(sub_result)
                    sub_error = None
                    break
                except Exception as err:
                    sub_error = err
            if sub_error is not None:
                raise RuntimeError(f"subchunk_{sub_idx + 1}_failed: {sub_error}")

        merged_summary = " ".join([r.get("summary", "") for r in recovered if r.get("summary")]).strip()
        merged_decisions = []
        merged_debates = []
        merged_actions = []
        merged_uncertainties = []
        for r in recovered:
            merged_decisions.extend(r.get("decisions", []))
            merged_debates.extend(r.get("debates", []))
            merged_actions.extend(r.get("actions", []))
            merged_uncertainties.extend(r.get("uncertainties", []))

        return {
            "index": chunk_index,
            "summary": merged_summary or f"[Bölüm {chunk_index + 1} kısmi kurtarıldı]",
            "topic_title": recovered[0].get("topic_title", f"Bölüm {chunk_index + 1}"),
            "decisions": merged_decisions,
            "debates": merged_debates,
            "actions": merged_actions,
            "uncertainties": merged_uncertainties,
            "sentiment": "neutral",
        }

    def analyze_topic_segment(self, chunk: str, master_instruction: str, chunk_index: int) -> Dict[str, Any]:
        """
        Phase 2: Specialized Analysis (The Scribe & The Audit).
        Analyzes a topic segment to extract structured data + narrative summary.
        """
        print(f"🕵️ Analyzing Topic Segment {chunk_index + 1}...")
        start = time.perf_counter()
        attempts = [
            {"name": "strict_schema", "model": MAIN_MODEL, "reduced_schema": False},
            {"name": "reduced_schema", "model": MAIN_MODEL, "reduced_schema": True},
            {"name": "fallback_model", "model": FALLBACK_MODEL, "reduced_schema": True},
        ]
        errors = []

        for retry_count, attempt in enumerate(attempts):
            try:
                result = self._analyze_topic_segment_once(
                    chunk=chunk,
                    master_instruction=master_instruction,
                    chunk_index=chunk_index,
                    model=attempt["model"],
                    reduced_schema=attempt["reduced_schema"],
                )
                result["chunk_status"] = "success"
                result["latency_ms"] = int((time.perf_counter() - start) * 1000)
                result["retry_count"] = retry_count
                result["error_code"] = None
                result["model_used"] = attempt["model"]
                result["attempt_stage"] = attempt["name"]
                print(f"✅ Segment {chunk_index + 1} analyzed: {result.get('topic_title')} ({attempt['name']})")
                return result
            except Exception as err:
                errors.append(err)
                print(f"⚠️ Segment {chunk_index + 1} attempt '{attempt['name']}' failed: {err}")

        # Final recovery: split failing chunk and re-run with reduced schema.
        try:
            recovered = self._recover_chunk_with_subchunks(chunk, master_instruction, chunk_index)
            recovered["chunk_status"] = "success"
            recovered["latency_ms"] = int((time.perf_counter() - start) * 1000)
            recovered["retry_count"] = len(attempts)
            recovered["error_code"] = None
            recovered["model_used"] = "subchunk_recovery"
            recovered["attempt_stage"] = "subchunk_recovery"
            print(f"✅ Segment {chunk_index + 1} recovered via sub-chunk fallback")
            return recovered
        except Exception as err:
            errors.append(err)
            error_code = type(errors[-1]).__name__ if errors else "unknown_error"
            print(f"⚠️ Segment {chunk_index + 1} analysis failed: {errors[-1]}")
            return {
                "index": chunk_index,
                "summary": "",
                "topic_title": f"Bölüm {chunk_index + 1}",
                "decisions": [],
                "debates": [],
                "actions": [],
                "uncertainties": [],
                "sentiment": "neutral",
                "chunk_status": "failed",
                "latency_ms": int((time.perf_counter() - start) * 1000),
                "retry_count": len(attempts) + 1,
                "error_code": error_code,
                "model_used": "none",
                "attempt_stage": "failed",
            }

    def _extract_high_frequency_terms(self, source_text: str, min_frequency: int = 3, top_k: int = 12) -> List[Tuple[str, int]]:
        """Extract recurring technical-ish terms as omission detector candidates."""
        if not source_text:
            return []

        stopwords = {
            "ve", "veya", "ile", "bu", "şu", "bir", "için", "ama", "gibi", "daha", "çok", "az", "olan",
            "the", "and", "for", "with", "that", "this", "from", "have", "will", "they", "then", "about",
            "biz", "siz", "ben", "sen", "onu", "bunu", "şeyi", "yani", "işte", "evet", "tamam",
        }

        tokens = re.findall(r"[A-Za-zÇĞİÖŞÜçğıöşü0-9][A-Za-zÇĞİÖŞÜçğıöşü0-9_-]{2,}", source_text.lower())
        counts = Counter()
        for token in tokens:
            if len(token) < 4:
                continue
            if token in stopwords:
                continue
            if token.isdigit():
                continue
            counts[token] += 1

        frequent = [(term, freq) for term, freq in counts.items() if freq >= min_frequency]
        frequent.sort(key=lambda x: (-x[1], -len(x[0]), x[0]))
        return frequent[:top_k]

    def _detect_topic_omissions(self, source_text: str, topics: List[str]) -> List[Dict[str, Any]]:
        """
        Detect high-frequency terms present in transcript but absent from extracted topic list.
        This is a warning signal, not a hard failure.
        """
        if not source_text or not topics:
            return []

        topics_text = " ".join(topics).lower()
        omissions = []
        for term, freq in self._extract_high_frequency_terms(source_text):
            if term not in topics_text:
                omissions.append({"term": term, "frequency": freq})

        return omissions[:8]

    # ── Call A: Narrative Reduce ──────────────────────────────────────
    def _reduce_narrative(
        self,
        combined_topics_text: str,
        master_instruction: str,
        style_guide: str,
        duration_minutes: int,
        language_instruction: str = "",
    ) -> str:
        """
        Reduce Call A: Synthesize chunk summaries into a cohesive markdown narrative.
        Returns plain markdown (no JSON parse risk).
        Fallback: combined_topics_text (chunk summaries concatenated).
        """
        prompt = f"""{master_instruction}

# ROL
Sen MIA, toplantı analizi uzmanısın. Görevin: Parçalara ayrılmış toplantı segmentlerini birleştirip ZAMANA DUYARLI, tutarlı bir detaylı özet oluşturmak.

# KRİTİK KURAL: KRONOLOJİK GÜNCELLEME PROTOKOLÜ
⚠️ BU KURAL TÜM DİĞERLERİNDEN ÖNCE GELİR ⚠️

Aynı konu hakkında birden fazla konuşma varsa:
1. SONRADAN söylenen bilgi, ÖNCEDEN söyleneni GEÇERSİZ KILAR
2. Özette SADECE SON HALİ yer almalıdır
3. Eski bilgiyi "daha önce X denilmişti ama..." diye belirtme
4. Çelişki varsa, segment indeksine bak → DAHA SONRA gelen segment kazanır

--- KONU ÖZETLERİ (Segment indeksi = kronolojik sıra, büyük indeks = daha yeni) ---
{combined_topics_text}

# GÖREV
Yukarıdaki konu özetlerini sentezleyerek TEK bir tutarlı detaylı rapor oluştur.

KURALLAR:
- {style_guide}
- "Önce X denildi sonra Y denildi" gibi SÜREÇ ANLATIMI YAPMA. Sadece ŞU ANKİ DURUMU yaz.
- ASLA "Giriş", "Gelişme" gibi başlıklar atma.
- TEKNİK SÖZLÜĞE UY: {", ".join(GLOSSARY_TERMS)}
- ⚠️ HALÜSİNASYON YASAĞI: Transkriptte GEÇMİYEN bir bilgiyi ASLA uydurma.

⚠️ YASAKLAR — AŞAĞIDAKİLERİ KESİNLİKLE YAZMA:
- "Yönetici Özeti", "TL;DR", "Özet" gibi başlık veya bölüm YAZMA — bunlar ayrı üretiliyor.
- Bölüm sonlarına **Karar:**, **Kararlar:**, **Aksiyon:**, **Deadline:** blokları KOYMA — bunlar ayrı structured çıktı olarak üretiliyor.
- Bullet list halinde karar veya aksiyon maddesi listeleme — sadece konunun bağlamını anlat.
- Senin görevin SADECE konuların detaylı anlatımını yapmak, karar/aksiyon/özet üretmek DEĞİL.

{language_instruction}

YANIT FORMATI: Düz markdown metin. JSON KULLANMA. Başlıklar için ### kullan."""

        try:
            response = create_chat_completion(
                model="gpt-5.4",
                messages=[
                    {"role": "system", "content": "Sen MIA, toplantı analizi uzmanısın. Düz markdown formatında detaylı toplantı özeti yazarsın."},
                    {"role": "user", "content": prompt}
                ],
                max_completion_tokens=4000 if duration_minutes >= 30 else 2500
            )
            self._last_narrative_usage = extract_usage(response, "gpt-5.4")
            self._track_usage(response, "gpt-5.4")
            narrative = response.choices[0].message.content.strip()
            if narrative:
                print(f"✅ Narrative reduce complete ({len(narrative)} chars)")
                return narrative
            else:
                print("⚠️ Narrative reduce returned empty, using fallback")
                return combined_topics_text
        except Exception as e:
            print(f"⚠️ Narrative reduce failed: {e}, using fallback")
            return combined_topics_text

    # ── Call B: Structured Reduce ──────────────────────────────────────
    def _reduce_structured(
        self,
        chunk_results: List[Dict[str, Any]],
        all_actions: List[Dict[str, Any]],
        all_debates: List[Any],
        all_uncertainties: List[Any],
        master_instruction: str,
        duration_minutes: int = 30,
        language_instruction: str = "",
    ) -> Dict[str, Any]:
        """
        Reduce Call B: Merge chunk data into structured JSON (topics, actions, tldr).
        Input is lightweight: chunk titles + actions (already short).
        Fallback: extract from chunk_results directly.
        """
        # Build compact input: just titles + action bullets
        compact_chunks = []
        for res in sorted(chunk_results, key=lambda x: x.get("index", 0)):
            title = res.get("topic_title", f"Bölüm {res.get('index', 0)+1}")
            summary = (res.get("summary") or "").strip()
            # Take first 2-3 sentences for context
            sentences = [s.strip() for s in summary.split(".") if s.strip()]
            brief = ". ".join(sentences[:3]) + "." if sentences else ""
            compact_chunks.append(f"- **{title}**: {brief}")

        compact_text = "\n".join(compact_chunks)

        # Dynamic action limits based on meeting duration
        if duration_minutes <= 30:
            max_committed = 5
            max_candidate = 3
        else:
            duration_hours = duration_minutes / 60
            max_committed = min(10, round(duration_hours * 8))
            max_candidate = min(5, round(duration_hours * 4))

        prompt = f"""{master_instruction}

# ROL
Sen MIA, toplantı analizi uzmanısın. Görevin: Parçalara ayrılmış toplantı verilerini birleştirip yapısal çıktı üretmek.

# KRİTİK KURAL: KRONOLOJİK GÜNCELLEME PROTOKOLÜ
Aynı konu hakkında birden fazla bilgi varsa SONRADAN söylenen geçerlidir.

--- KONU ÖZETLERİ ---
{compact_text}

--- HAM AKSİYONLAR ---
{json.dumps(all_actions, ensure_ascii=False)}

--- HAM BELİRSİZLİKLER/TARTIŞMALAR ---
{json.dumps(all_debates + all_uncertainties, ensure_ascii=False)}

# GÖREV
Aşağıdaki yapısal çıktıyı oluştur:

1. **tldr:** Toplantının şu anki net durumu (2-3 cümle). Sadece en kritik çıktılar.
2. **key_topics:** Ana gündem maddeleri (5-7 madde, string listesi).
3. **action_items:** Deduplike edilmiş aksiyon listesi. ÜST SINIRLAR (doldurmak zorunda DEĞİLSİN — sadece gerçekten somut olanları yaz):
   - committed: EN FAZLA {max_committed} madde. owner + net görev + geri alınmamış.
   - candidate: EN FAZLA {max_candidate} madde. owner/deadline/scope eksik.
   - ⚠️ KALİTE FİLTRESİ: Somut bir çıktı (deliverable), obje veya uygulanabilir next step YOKSA aksiyon olarak YAZMA.
   - BİRLEŞTİRME KURALI: Aynı owner + aynı deliverable/hedef → TEK maddede birleştir. (Farklı owner = farklı aksiyon.)
   - Opsiyonel olanları 'is_optional: true' işaretle.
4. **pending_items:** SADECE gerçekten karara bağlanmamış, havada kalan konular. EN FAZLA 5 madde.
   - ⚠️ Bir konu hakkında açık KARAR varsa (key_topics veya action_items'ta yer alıyorsa) onu pending_items'a KOYMA.
   - Genel belirsizlik veya spekülasyon yazma — sadece toplantıda açıkça tartışılıp sonuca bağlanamamış somut konuları yaz.
   - Hiç açık nokta yoksa boş liste döndür: []

TEKNİK SÖZLÜĞE UY: {", ".join(GLOSSARY_TERMS)}
⚠️ HALÜSİNASYON YASAĞI: Ham verilerde OLMAYAN bir bilgiyi ASLA uydurma.
{language_instruction}

YANIT FORMATI (JSON):
{{
  "tldr": "...",
  "key_topics": ["..."],
  "action_items": [
    {{
      "task": "Net ve kısa görev açıklaması",
      "assignee": "İsim veya null",
      "commitment_level": "committed|candidate",
      "priority": "high/medium/low",
      "deadline": "ISO tarih veya null",
      "is_optional": false,
      "tags": ["tag1", "tag2"]
    }}
  ],
  "pending_items": ["..."]
}}"""

        try:
            response = create_chat_completion(
                model="gpt-5.4",
                messages=[
                    {"role": "system", "content": "Sen MIA, toplantı analizi uzmanısın. Yapısal veri birleştirme yaparsın. Yanıtın JSON formatındadır."},
                    {"role": "user", "content": prompt}
                ],
                max_completion_tokens=2500
            )
            self._last_structured_usage = extract_usage(response, "gpt-5.4")
            self._track_usage(response, "gpt-5.4")
            response_text = response.choices[0].message.content.strip()
            result = parse_json_with_retry(response_text)

            # Schema validation
            result.setdefault("tldr", "")
            result.setdefault("key_topics", [])
            result.setdefault("action_items", [])
            result.setdefault("committed_actions", [])
            result.setdefault("candidate_actions", [])
            result.setdefault("pending_items", [])

            print(f"✅ Structured reduce complete (topics: {len(result.get('key_topics', []))}, actions: {len(result.get('action_items', []))})")
            return result

        except Exception as e:
            print(f"⚠️ Structured reduce failed: {e}, using smart fallback")
            return None  # Signals caller to use smart fallback

    # ── Main Reduce Orchestrator ───────────────────────────────────────
    def reduce_summaries(
        self,
        chunk_results: List[Dict[str, Any]],
        master_instruction: str,
        participants: List[str],
        duration_minutes: int,
        source_text: str = "",
        language: str = "auto",
    ) -> Dict[str, Any]:
        """
        Phase 3: Parallel Reduce — runs narrative + structured merge concurrently.
        Each call has independent fallback so partial failures don't lose everything.
        """
        print(f"🔗 Reducing {len(chunk_results)} chunk summaries... (Duration: {duration_minutes} min, Language: {language})")

        try:
            return self._reduce_summaries_impl(chunk_results, master_instruction, participants, duration_minutes, source_text, language)
        except Exception as e:
            print(f"❌ Reduce orchestrator failed: {e}")

            # Top-level smart fallback
            fallback_topics = []
            seen_topics = set()
            topic_summaries = []
            for res in sorted(chunk_results, key=lambda x: x.get("index", 0)):
                topic_title = (res.get("topic_title") or "").strip()
                summary = res.get("summary", "")
                if summary:
                    topic_summaries.append(f"### {topic_title or 'Bölüm'}\n{summary}")
                if topic_title and topic_title.lower() not in seen_topics:
                    seen_topics.add(topic_title.lower())
                    fallback_topics.append(topic_title)

            first_sentences = []
            for res in sorted(chunk_results, key=lambda x: x.get("index", 0)):
                summary = (res.get("summary") or "").strip()
                if summary:
                    first_sentence = summary.split(".")[0].strip()
                    if first_sentence:
                        first_sentences.append(first_sentence + ".")
            fallback_tldr = " ".join(first_sentences[:3]) if first_sentences else "Toplantı özeti oluşturuldu."

            # Extract raw actions from chunks for fallback
            fallback_actions = []
            fallback_structured = []
            for res in chunk_results:
                for a in res.get("actions", []):
                    if isinstance(a, dict):
                        task = a.get("task", a.get("description", ""))
                        assignee = a.get("assignee", a.get("assignee_name"))
                        if assignee in ("Belirsiz", "", None):
                            assignee = None
                        commitment_level = a.get("commitment_level")
                        if commitment_level not in ("committed", "candidate"):
                            commitment_level = "committed" if assignee else "candidate"
                        fallback_actions.append(f"{task} (Sahibi: {assignee or 'Belirsiz'})")
                        fallback_structured.append({
                            "description": task,
                            "assignee_name": assignee,
                            "commitment_level": commitment_level,
                            "priority": a.get("priority", "medium"),
                            "deadline": a.get("deadline"),
                            "is_optional": a.get("is_optional", False),
                            "context": a.get("context", ""),
                            "topic_indices": []
                        })

            combined = "\n\n".join(topic_summaries)
            return {
                "summary": combined,
                "tldr": fallback_tldr,
                "detailed_content": combined,
                "email_draft": fallback_tldr,
                "key_topics": fallback_topics,
                "key_topics_structured": [{"topic": t, "topic_order": i} for i, t in enumerate(fallback_topics)],
                "action_items": fallback_actions,
                "action_items_structured": fallback_structured,
                "participants": participants,
                "topic_omissions": [],
                "warnings": ["Reduce orchestrator failed, top-level fallback used."],
            }

    def _reduce_summaries_impl(
        self,
        chunk_results: List[Dict[str, Any]],
        master_instruction: str,
        participants: List[str],
        duration_minutes: int,
        source_text: str = "",
        language: str = "auto",
    ) -> Dict[str, Any]:
        """Internal implementation of parallel reduce."""
        # 1. Aggregate Structured Data
        all_debates = []
        all_actions = []
        all_uncertainties = []
        topic_summaries = []

        # Sort by index
        chunk_results.sort(key=lambda x: x["index"])

        for res in chunk_results:
            all_debates.extend(res.get("debates", []))
            all_actions.extend(res.get("actions", []))
            all_uncertainties.extend(res.get("uncertainties", []))

            title = res.get("topic_title", f"Bölüm {res['index']+1}")
            summary = res.get("summary", "")
            if summary:
                topic_summaries.append(f"### {title}\n{summary}")

        combined_topics_text = "\n\n".join(topic_summaries)

        # 2. Strategy Selection
        if duration_minutes < 15:
            style_guide = "KISA TOPLANTI KURALI: SADECE tek bir paragraf yaz (4-5 cümle). Başlık KULLANMA. Bölümlere AYIRMA. ### işareti KULLANMA. Lafı uzatma, doğrudan sonucu anlat."
        else:
            style_guide = "Format: Hiyerarşik Yapı (Konu Başlıkları Altında). Her konu için EN FAZLA 1 paragraf (3-5 cümle). Gereksiz detay verme, sadece konunun özünü anlat."

        # 3. Build language instruction for prompts
        if language == "auto":
            language_instruction = "YANIT DİLİ: Transkriptin yazıldığı dilde yanıt ver. Transkript hangi dildeyse özeti de o dilde yaz."
        elif language == "en":
            language_instruction = "YANIT DİLİ: Tüm çıktıyı İNGİLİZCE yaz."
        else:
            language_instruction = "YANIT DİLİ: Tüm çıktıyı TÜRKÇE yaz."

        # 4. Run both reduce calls in PARALLEL
        narrative_result = None
        structured_result = None

        with ThreadPoolExecutor(max_workers=2) as executor:
            narrative_future = executor.submit(
                self._reduce_narrative,
                combined_topics_text,
                master_instruction,
                style_guide,
                duration_minutes,
                language_instruction,
            )
            structured_future = executor.submit(
                self._reduce_structured,
                chunk_results,
                all_actions,
                all_debates,
                all_uncertainties,
                master_instruction,
                duration_minutes,
                language_instruction,
            )

            try:
                narrative_result = narrative_future.result(timeout=120)
            except Exception as e:
                print(f"⚠️ Narrative future failed: {e}, using chunk concatenation")
                narrative_result = combined_topics_text

            try:
                structured_result = structured_future.result(timeout=120)
            except Exception as e:
                print(f"⚠️ Structured future failed: {e}, will use smart fallback")
                structured_result = None

        # 4. Build final output — merge results from both calls

        # --- Narrative (Call A) ---
        # narrative_result is always a string (fallback = combined_topics_text)
        detailed = narrative_result

        # --- Structured (Call B) ---
        # If structured_result is None, use smart fallback
        if structured_result is None:
            # Smart fallback for structured data
            fallback_topics = []
            seen_topics = set()
            for res in sorted(chunk_results, key=lambda x: x.get("index", 0)):
                topic_title = (res.get("topic_title") or "").strip()
                if not topic_title:
                    continue
                topic_key = topic_title.lower()
                if topic_key in seen_topics:
                    continue
                seen_topics.add(topic_key)
                fallback_topics.append(topic_title)

            first_sentences = []
            for res in sorted(chunk_results, key=lambda x: x.get("index", 0)):
                summary = (res.get("summary") or "").strip()
                if summary:
                    first_sentence = summary.split(".")[0].strip()
                    if first_sentence:
                        first_sentences.append(first_sentence + ".")
            fallback_tldr = " ".join(first_sentences[:3]) if first_sentences else "Toplantı özeti oluşturuldu."

            tldr = fallback_tldr
            topics = fallback_topics
            actions = []
            pending = []

            # Build fallback action items from raw chunk actions
            for a in all_actions:
                if isinstance(a, dict):
                    actions.append(a)

            print(f"⚠️ Structured smart fallback used (Topics: {len(topics)}, Actions: {len(actions)})")
        else:
            tldr = structured_result.get("tldr", "")
            topics = structured_result.get("key_topics", [])
            actions = structured_result.get("action_items", [])
            if not actions:
                committed = structured_result.get("committed_actions", [])
                candidate = structured_result.get("candidate_actions", [])
                actions = []
                for item in committed:
                    if isinstance(item, dict):
                        item["commitment_level"] = item.get("commitment_level", "committed")
                    actions.append(item)
                for item in candidate:
                    if isinstance(item, dict):
                        item["commitment_level"] = item.get("commitment_level", "candidate")
                    actions.append(item)
            pending = structured_result.get("pending_items", [])

        # Fallback if topics still empty: infer from chunk titles
        if not topics:
            inferred_topics = []
            seen_topics = set()
            for res in sorted(chunk_results, key=lambda x: x.get("index", 0)):
                topic_title = (res.get("topic_title") or "").strip()
                if not topic_title:
                    continue
                topic_key = topic_title.lower()
                if topic_key in seen_topics:
                    continue
                seen_topics.add(topic_key)
                inferred_topics.append(topic_title)
                if len(inferred_topics) >= 7:
                    break
            topics = inferred_topics

        # Format Pending for display
        pending_text = ""
        if pending:
            pending_text = "\n\n#### ❓ Açık Kalan Noktalar\n" + "\n".join([f"- {p}" for p in pending])

        # Assemble Full Body (Backward Compatibility)
        full_summary_body = f"{detailed}{pending_text}".lstrip("\n")

        # Keep key topics in stable chronological order
        key_topics_structured = [
            {"topic": topic, "topic_order": idx}
            for idx, topic in enumerate(topics)
        ]

        # Build action_items_structured
        action_items_structured = []
        legacy_actions = []
        for a in actions:
            if isinstance(a, dict):
                task = a.get("task", a.get("description", ""))
                assignee = a.get("assignee", a.get("assignee_name"))
                if assignee in ("Belirsiz", "", None):
                    assignee = None
                commitment_level = a.get("commitment_level")
                if commitment_level not in ("committed", "candidate"):
                    commitment_level = "committed" if assignee else "candidate"

                # Topic matching via keyword heuristic
                action_text = task.lower()
                matched_topic_ids = []
                for topic_idx, topic in enumerate(topics):
                    topic_lower = topic.lower()
                    if topic_lower in action_text or \
                       any(word in action_text for word in topic_lower.split() if len(word) > 3):
                        matched_topic_ids.append(topic_idx)

                action_items_structured.append({
                    "description": task,
                    "assignee_name": assignee,
                    "commitment_level": commitment_level,
                    "priority": a.get("priority", "medium"),
                    "deadline": a.get("deadline"),
                    "is_optional": a.get("is_optional", False),
                    "tags": a.get("tags", []),
                    "topic_indices": matched_topic_ids
                })
                assignee_label = assignee or "Belirsiz"
                if commitment_level == "candidate":
                    legacy_actions.append(f"[Aday] {task} (Sahibi: {assignee_label})")
                else:
                    legacy_actions.append(f"{task} (Sahibi: {assignee_label})")
            elif isinstance(a, str):
                legacy_actions.append(a)
                action_items_structured.append({
                    "description": a,
                    "assignee_name": None,
                    "commitment_level": "candidate",
                    "priority": "medium",
                    "deadline": None,
                    "is_optional": False,
                    "topic_indices": []
                })

        topic_omissions = self._detect_topic_omissions(source_text, topics)
        warnings = []
        if topic_omissions:
            sampled = ", ".join([f"{o['term']}({o['frequency']})" for o in topic_omissions[:4]])
            warnings.append(f"Potential topic omissions detected: {sampled}")
        if structured_result is None:
            warnings.append("Structured reduce failed, smart fallback used.")

        print(
            f"✅ Final synthesis complete (Topics: {len(topics)}, Actions: {len(action_items_structured)}, "
            f"Omissions: {len(topic_omissions)})"
        )

        return {
            "summary": full_summary_body,
            "tldr": tldr,
            "detailed_content": detailed,
            "email_draft": tldr,
            "key_topics": topics,
            "key_topics_structured": key_topics_structured,
            "action_items": legacy_actions,
            "action_items_structured": action_items_structured,
            "participants": participants,
            "topic_omissions": topic_omissions,
            "warnings": warnings,
        }

    def generate_comprehensive_summary(
        self,
        chunks: List[Dict[str, Any]],
        title: str = "Toplantı",
        duration_minutes: int = 60,
        transcript_pre_corrected: bool = False,
        language: str = "auto",
    ) -> Dict[str, Any]:
        """
        Generate comprehensive summary using Map-Reduce architecture with parallel processing.

        Pipeline:
        1. Extract metadata and participants
        2. Validate transcript size (Prevent anomalous costs)
        3. Generate master instruction (ONCE, metadata-based)
        4. Correct and chunk the transcript
        5. Process all chunks in PARALLEL
        6. Reduce chunk summaries into final Tiered Summary
        """
        print(f"🚀 Starting Map-Reduce summary generation for: {title}")

        # --- VALIDATION LAYER ---
        # Dynamic limit: 25 chunks/min (1500 chunks/hour)
        # Minimum 2000 chunks (80 min buffer) to prevent false positives for short meetings
        dynamic_limit = max(2000, int(duration_minutes * 25))
        
        MAX_CHUNKS_PER_MEETING = dynamic_limit
        MAX_TOTAL_CHARS = 300000     # ~85k tokens, roughly max for GPT-5.2 context safety

        if len(chunks) > MAX_CHUNKS_PER_MEETING:
            error_msg = f"Chunk count anomaly: {len(chunks)} > {MAX_CHUNKS_PER_MEETING} (duration: {duration_minutes} min) (possible duplicate issue)"
            print(f"🚨 {error_msg}")
            raise RuntimeError(error_msg)

        raw_transcript, source_line_times = self._build_transcript_with_timing(chunks)

        if len(raw_transcript) > MAX_TOTAL_CHARS:
            error_msg = f"Transcript size anomaly: {len(raw_transcript)} chars exceeds limit of {MAX_TOTAL_CHARS}"
            print(f"🚨 {error_msg}")
            raise RuntimeError(error_msg)
        # ------------------------

        # Extract participants
        participants = []
        for chunk in chunks:
            speaker = chunk.get('speaker_name') or chunk.get('speaker')
            if speaker and speaker not in participants and speaker != 'Konuşmacı':
                participants.append(speaker)
        
        if not raw_transcript:
            return {
                "summary": "No meeting content found." if language == "en" else "Toplantı içeriği bulunamadı.",
                "key_topics": [],
                "action_items": [],
                "participants": participants,
                "processing_status": "failed",
                "coverage_ratio": 0.0,
                "successful_chunks": 0,
                "failed_chunks": 0,
                "chunk_telemetry": [],
            }

        # Single accumulator captures every LLM response across the whole
        # pipeline (master instruction + correction pre-scan + per-chunk
        # correction + segmentation + parallel map + reduce). _track_usage
        # — called from each LLM site — consults this field; nil-when-not-set
        # means standalone endpoints (catch_me_up, progressive summary) bypass.
        # finally: clears the field even if any phase raises, so the next
        # call on the same NLPService instance starts clean.
        self._comprehensive_acc = TokenAccumulator()
        try:
            # PHASE 1: Generate Master Instruction (runs ONCE with metadata only)
            master_instruction = self.generate_master_instruction(title, participants, duration_minutes)
            
            # PHASE 2: Correction (skip for finalized chat blocks to avoid duplicate correction)
            if transcript_pre_corrected:
                print("⏭️ Skipping Layer 1 correction (source=finalized_chat_blocks)")
                corrected_transcript = raw_transcript
            else:
                corrected_transcript = self.correct_transcript(raw_transcript)
            
            # PHASE 3: Chunk the transcript with boundary metadata for timing alignment
            chunk_boundaries = [
                item for item in self.segment_transcript_by_topic_with_boundaries(corrected_transcript)
                if item.get("text")
            ]
            text_chunks = [item.get("text", "") for item in chunk_boundaries]

            if not text_chunks or not chunk_boundaries:
                raise RuntimeError("No topic chunks generated from transcript")

            # If correction changed line count, precise source timing alignment becomes unsafe.
            corrected_line_count = len(corrected_transcript.split('\n'))
            timing_alignment_available = len(source_line_times) == corrected_line_count
            if not timing_alignment_available:
                print(
                    f"⚠️ Source timing alignment degraded: source_lines={len(source_line_times)} "
                    f"!= corrected_lines={corrected_line_count}"
                )

            # PHASE 4: Parallel Map - Process all chunks concurrently
            chunk_results = []
            if len(text_chunks) == 1:
                print("📄 Single chunk - using direct processing")
                single_result = self.analyze_topic_segment(text_chunks[0], master_instruction, 0)
                if timing_alignment_available:
                    start_time, end_time = self._resolve_chunk_time_range(
                        source_line_times,
                        chunk_boundaries[0].get("start_line", 0),
                        chunk_boundaries[0].get("end_line", corrected_line_count),
                    )
                    single_result["source_start_time"] = start_time
                    single_result["source_end_time"] = end_time
                else:
                    single_result["source_start_time"] = None
                    single_result["source_end_time"] = None
                chunk_results.append(single_result)
            else:
                print(f"⚡ Processing {len(text_chunks)} chunks in parallel...")
                with ThreadPoolExecutor(max_workers=8) as executor:
                    futures = {
                        executor.submit(self.analyze_topic_segment, chunk, master_instruction, idx): idx
                        for idx, chunk in enumerate(text_chunks)
                    }

                    for future in as_completed(futures):
                        idx = futures[future]
                        source_start_time = None
                        source_end_time = None
                        if timing_alignment_available and idx < len(chunk_boundaries):
                            source_start_time, source_end_time = self._resolve_chunk_time_range(
                                source_line_times,
                                chunk_boundaries[idx].get("start_line", 0),
                                chunk_boundaries[idx].get("end_line", corrected_line_count),
                            )
                        try:
                            result = future.result(timeout=360)  # 6 min timeout per chunk
                            result["source_start_time"] = source_start_time
                            result["source_end_time"] = source_end_time
                            chunk_results.append(result)
                        except Exception as e:
                            print(f"⚠️ Chunk {idx + 1} failed at executor level: {e}")
                            chunk_results.append({
                                "index": idx,
                                "summary": "",
                                "topic_title": f"Bölüm {idx + 1}",
                                "decisions": [],
                                "debates": [],
                                "actions": [],
                                "uncertainties": [],
                                "sentiment": "neutral",
                                "chunk_status": "failed",
                                "latency_ms": 0,
                                "retry_count": 0,
                                "error_code": type(e).__name__,
                                "model_used": "executor_timeout",
                                "attempt_stage": "executor_failure",
                                "source_start_time": source_start_time,
                                "source_end_time": source_end_time,
                            })

            chunk_results.sort(key=lambda x: x.get("index", 0))
            total_chunks = len(text_chunks)
            successful_results = [r for r in chunk_results if r.get("chunk_status") == "success"]
            failed_results = [r for r in chunk_results if r.get("chunk_status") != "success"]
            successful_chunks = len(successful_results)
            failed_chunks = len(failed_results)
            coverage_ratio = successful_chunks / max(total_chunks, 1)

            chunk_telemetry = [
                {
                    "chunk_index": r.get("index"),
                    "status": r.get("chunk_status", "failed"),
                    "latency_ms": r.get("latency_ms", 0),
                    "retry_count": r.get("retry_count", 0),
                    "error_code": r.get("error_code"),
                    "model_used": r.get("model_used"),
                    "attempt_stage": r.get("attempt_stage"),
                }
                for r in chunk_results
            ]

            if coverage_ratio < 0.85:
                raise RuntimeError(
                    f"Coverage gate failed: {successful_chunks}/{total_chunks} chunks succeeded "
                    f"(coverage={coverage_ratio:.2f}, threshold=0.85)"
                )

            # PHASE 5: Reduce - Combine successful chunk summaries only.
            # _last_*_usage instance fields are now vestigial — _track_usage
            # in _reduce_narrative / _reduce_structured already feeds the
            # pipeline-wide self._comprehensive_acc. Keeping the resets +
            # writes for any external diagnostic still reading them.
            self._last_narrative_usage = None
            self._last_structured_usage = None

            final_result = self.reduce_summaries(
                successful_results,
                master_instruction,
                participants,
                duration_minutes,
                source_text=corrected_transcript,
                language=language,
            )

            # Pipeline-wide accumulator (master instruction + correction
            # pre-scan + per-chunk correction + segmentation + parallel
            # map + reduce calls). Previously this dict only included the
            # two reduce calls — issue #39 review found ~80% of comprehensive
            # cost was being undercounted in usage_logs.
            final_result.update(self._comprehensive_acc.to_dict())

            processing_status = "completed" if coverage_ratio >= 0.90 else "completed_with_gaps"
            warnings = list(final_result.get("warnings", []))
            if processing_status == "completed_with_gaps":
                warning = (
                    f"Summary generated with gaps: {failed_chunks} chunk failed "
                    f"({successful_chunks}/{total_chunks} chunks succeeded)."
                )
                warnings.append(warning)

            final_result.update({
                "processing_status": processing_status,
                "coverage_ratio": coverage_ratio,
                "successful_chunks": successful_chunks,
                "failed_chunks": failed_chunks,
                "chunk_telemetry": chunk_telemetry,
                "warnings": warnings,
                "total_chunks": total_chunks,
            })

            print(
                f"✅ Map-Reduce summary completed ({total_chunks} chunks, "
                f"coverage={coverage_ratio:.2f}, status={processing_status})"
            )
            return final_result

        except Exception as e:
            print(f"❌ Map-Reduce summary failed: {e}")
            # Raise exception instead of returning error placeholder as summary
            # Go backend will catch this and log to summary_errors table
            raise RuntimeError(f"Map-Reduce summary failed: {str(e)[:200]}")
        finally:
            # Always clear so a follow-up call on the same NLPService
            # instance (singleton via app factory) starts fresh — and so
            # any later standalone endpoint (progressive / catch_me_up)
            # running on the same instance bypasses _track_usage.
            self._comprehensive_acc = None
