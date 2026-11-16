"""Database connection singleton.

Only provides the Supabase client. All query logic lives in repositories/.
"""
import os
from typing import Optional
from supabase import create_client, Client

_supabase_client: Optional[Client] = None


def get_supabase_client() -> Client:
    """Get or create Supabase client."""
    global _supabase_client

    if _supabase_client is None:
        supabase_url = os.getenv("SUPABASE_URL")
        supabase_key = os.getenv("SUPABASE_KEY")

        if not supabase_url or not supabase_key:
            raise ValueError(
                "SUPABASE_URL and SUPABASE_KEY environment variables are required."
            )

        _supabase_client = create_client(supabase_url, supabase_key)
        print(f"Connected to Supabase: {supabase_url}")

    return _supabase_client
