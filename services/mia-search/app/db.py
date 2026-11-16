"""Database connection helper for PostgreSQL (Cloud SQL / Docker pgvector).

Reads DATABASE_URL from environment and yields psycopg connections configured
with dict_row so repository methods return plain dictionaries.
"""
from contextlib import contextmanager
import os
from typing import Iterator

import psycopg
from psycopg.rows import dict_row


def get_database_url() -> str:
    """Return DATABASE_URL from environment or raise a clear configuration error."""
    database_url = os.getenv("DATABASE_URL")
    if not database_url:
        raise ValueError("DATABASE_URL environment variable is required.")
    return database_url


@contextmanager
def get_db_connection() -> Iterator[psycopg.Connection]:
    """Open and yield a psycopg PostgreSQL connection with dict_row factory."""
    conn = psycopg.connect(get_database_url(), row_factory=dict_row)
    try:
        yield conn
    finally:
        conn.close()
