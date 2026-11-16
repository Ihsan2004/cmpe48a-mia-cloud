"""Shared OpenAI client singleton."""
import os
from openai import OpenAI

_openai_client = None


def get_openai_client() -> OpenAI:
    """Get or create OpenAI client."""
    global _openai_client

    if _openai_client is None:
        api_key = os.getenv("OPENAI_API_KEY")
        if not api_key:
            raise ValueError("OPENAI_API_KEY environment variable is required")
        _openai_client = OpenAI(api_key=api_key)

    return _openai_client
