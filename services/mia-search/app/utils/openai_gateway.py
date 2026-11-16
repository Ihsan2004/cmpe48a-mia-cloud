"""OpenAI gateway with shared timeout/retry policy.

This module centralizes OpenAI chat/embedding calls so every service
follows the same resiliency rules.
"""
import logging
import os
import random
import threading
import time
from dataclasses import dataclass
from typing import Any, Optional

from app.utils.openai_client import get_openai_client

logger = logging.getLogger(__name__)


@dataclass
class TokenUsage:
    """Token usage info extracted from an OpenAI API response."""
    prompt_tokens: int = 0
    completion_tokens: int = 0
    total_tokens: int = 0
    model: str = ""


class TokenAccumulator:
    """Accumulates token usage across multiple API calls.

    Thread-safe: ``add`` and ``to_dict`` hold an internal lock so a
    single accumulator can be fed from ThreadPoolExecutor workers
    (comprehensive_summary fan-out). Without the lock, ``x += n`` is
    NOT atomic in CPython (LOAD_ATTR → BINARY_ADD → STORE_ATTR spans
    multiple bytecodes), so concurrent map-phase chunks could lose
    increments under contention.
    """

    def __init__(self):
        self._lock = threading.Lock()
        self.prompt_tokens: int = 0
        self.completion_tokens: int = 0
        self.total_tokens: int = 0

    def add(self, usage: TokenUsage):
        with self._lock:
            self.prompt_tokens += usage.prompt_tokens
            self.completion_tokens += usage.completion_tokens
            self.total_tokens += usage.total_tokens

    def to_dict(self) -> dict:
        with self._lock:
            return {
                "usage_prompt_tokens": self.prompt_tokens,
                "usage_completion_tokens": self.completion_tokens,
                "usage_total_tokens": self.total_tokens,
            }


def extract_usage(response: Any, model: str = "") -> TokenUsage:
    """Extract token usage from an OpenAI response object."""
    usage = getattr(response, "usage", None)
    if usage is None:
        return TokenUsage(model=model)
    return TokenUsage(
        prompt_tokens=getattr(usage, "prompt_tokens", 0) or 0,
        completion_tokens=getattr(usage, "completion_tokens", 0) or 0,
        total_tokens=getattr(usage, "total_tokens", 0) or 0,
        model=getattr(response, "model", model) or model,
    )

DEFAULT_CHAT_TIMEOUT_SEC = float(os.getenv("OPENAI_CHAT_TIMEOUT_SEC", "120"))
DEFAULT_EMBED_TIMEOUT_SEC = float(os.getenv("OPENAI_EMBED_TIMEOUT_SEC", "60"))
DEFAULT_MAX_RETRIES = int(os.getenv("OPENAI_API_MAX_RETRIES", "2"))
DEFAULT_RETRY_BASE_SEC = float(os.getenv("OPENAI_API_RETRY_BASE_SEC", "1.0"))
DEFAULT_RETRY_MAX_SEC = float(os.getenv("OPENAI_API_RETRY_MAX_SEC", "8.0"))


def create_chat_completion(
    *,
    model: str,
    messages: list[dict[str, Any]],
    timeout: Optional[float] = None,
    max_retries: Optional[int] = None,
    **kwargs: Any,
) -> Any:
    """Call chat.completions.create with standard retry/timeout behavior."""
    timeout_sec = timeout if timeout is not None else DEFAULT_CHAT_TIMEOUT_SEC
    retries = DEFAULT_MAX_RETRIES if max_retries is None else max(0, max_retries)

    def _invoke() -> Any:
        client = get_openai_client()
        return client.chat.completions.create(
            model=model,
            messages=messages,
            timeout=timeout_sec,
            **kwargs,
        )

    return _call_with_retry(_invoke, call_type="chat", model=model, retries=retries)


def create_embedding(
    *,
    model: str,
    input: Any,
    timeout: Optional[float] = None,
    max_retries: Optional[int] = None,
    **kwargs: Any,
) -> Any:
    """Call embeddings.create with standard retry/timeout behavior."""
    timeout_sec = timeout if timeout is not None else DEFAULT_EMBED_TIMEOUT_SEC
    retries = DEFAULT_MAX_RETRIES if max_retries is None else max(0, max_retries)

    def _invoke() -> Any:
        client = get_openai_client()
        return client.embeddings.create(
            model=model,
            input=input,
            timeout=timeout_sec,
            **kwargs,
        )

    return _call_with_retry(_invoke, call_type="embedding", model=model, retries=retries)


def _call_with_retry(fn, *, call_type: str, model: str, retries: int) -> Any:
    last_error = None

    for attempt in range(retries + 1):
        try:
            return fn()
        except Exception as exc:  # noqa: BLE001 - OpenAI SDK raises multiple exception types.
            last_error = exc
            retryable = _is_retryable(exc)
            is_last_attempt = attempt >= retries

            if not retryable or is_last_attempt:
                raise

            delay = _backoff_delay(attempt)
            logger.warning(
                "OpenAI %s call failed (model=%s, attempt=%d/%d): %s. Retrying in %.2fs",
                call_type,
                model,
                attempt + 1,
                retries + 1,
                exc,
                delay,
            )
            time.sleep(delay)

    # Defensive fallback; loop should either return or raise.
    if last_error is not None:
        raise last_error
    raise RuntimeError("OpenAI gateway failed without error context")


def _is_retryable(exc: Exception) -> bool:
    error_name = exc.__class__.__name__
    if error_name in {
        "APITimeoutError",
        "APIConnectionError",
        "RateLimitError",
        "InternalServerError",
        "ServiceUnavailableError",
    }:
        return True

    status_code = getattr(exc, "status_code", None)
    if status_code in {408, 409, 429, 500, 502, 503, 504}:
        return True

    message = str(exc).lower()
    if any(token in message for token in ("timeout", "temporarily unavailable", "rate limit", "connection reset")):
        return True

    return False


def _backoff_delay(attempt: int) -> float:
    # attempt=0 -> base, attempt=1 -> base*2, ...
    exp = DEFAULT_RETRY_BASE_SEC * (2 ** attempt)
    jitter = random.uniform(0, DEFAULT_RETRY_BASE_SEC)
    return min(DEFAULT_RETRY_MAX_SEC, exp + jitter)
