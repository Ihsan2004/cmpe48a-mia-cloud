"""JSON parsing/repair helpers for NLP service responses."""
import json
import re
from typing import Any, Dict

from app.utils.openai_gateway import create_chat_completion


def parse_json_with_retry(
    response_text: str,
    max_retries: int = 2,
    repair_model: str = "gpt-5.4",
) -> Dict[str, Any]:
    """Parse JSON with lightweight cleanup and LLM-based self-repair fallback."""
    cleaned = _strip_markdown_json(response_text)

    first_error = None
    try:
        return json.loads(cleaned)
    except json.JSONDecodeError as err:
        first_error = err
        print(f"⚠️ JSON parse failed: {first_error}")

    try:
        match = re.search(r"\{.*\}", cleaned, re.DOTALL)
        if match:
            potential_json = match.group(0)
            print("🔍 Optimization: Attempting regex JSON extraction...")
            return json.loads(potential_json)
    except Exception:
        pass

    last_error = first_error or json.JSONDecodeError("Invalid JSON", cleaned, 0)
    for attempt in range(max_retries):
        print(f"🔄 JSON fix attempt {attempt + 1}/{max_retries}...")
        try:
            fix_prompt = f"""Aşağıdaki JSON yanıtında syntax hatası var. Düzelt ve SADECE geçerli JSON döndür.

HATALI YANIT:
{cleaned[:2000]}

HATA MESAJI:
{str(last_error)}

KURALLAR:
1. Sadece geçerli JSON döndür
2. Açıklama ekleme
3. Markdown code block kullanma
4. Eksik virgül, parantez veya tırnak işaretlerini düzelt"""

            response = create_chat_completion(
                model=repair_model,
                messages=[
                    {"role": "system", "content": "Sen JSON düzeltici bir asistansın. Sadece geçerli JSON döndür."},
                    {"role": "user", "content": fix_prompt},
                ],
                max_completion_tokens=2000,
            )

            fixed_text = _strip_markdown_json(response.choices[0].message.content.strip())
            result = json.loads(fixed_text)
            print(f"✅ JSON fixed on attempt {attempt + 1}")
            return result
        except json.JSONDecodeError as err:
            last_error = err
            print(f"⚠️ Fix attempt {attempt + 1} failed: {err}")
            continue
        except Exception as err:
            print(f"⚠️ LLM correction failed: {err}")
            break

    raise last_error


def _strip_markdown_json(text: str) -> str:
    cleaned = (text or "").strip()
    if cleaned.startswith("```"):
        parts = cleaned.split("```")
        if len(parts) >= 2:
            cleaned = parts[1]
            if cleaned.startswith("json"):
                cleaned = cleaned[4:]
        cleaned = cleaned.strip()
    return cleaned
