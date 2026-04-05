"""Local content cache — SQLite backed, per-domain TTL.

Caches the *output* of the fetch layer (extracted page content), not the
raw search results.  A single process-wide :class:`ContentCache` is shared
across fetchers through the :class:`diting.fetch.cached.CachedFetcher`
wrapper.

Design decisions
----------------

- **SQLite** over a pickle/dbm file: batteries-included, durable, handles
  concurrent readers cleanly, easy to inspect by hand.
- **Schema versioned**: on mismatch the whole table is rebuilt.  A
  corrupted or stale DB never blocks startup — the worst case is one
  round of re-fetching.
- **Per-URL TTL, derived at insert time**: the record stores an absolute
  expiry timestamp so ``get`` is a single SELECT with a cheap comparison.
- **Domain-tier defaults**: a hand-curated table gives arxiv/wiki long
  TTLs, news sites short TTLs, and everything else a 7-day default.
  This is a first-pass seed list; it will grow with user feedback.
"""

from __future__ import annotations

import pathlib
import sqlite3
import time
from urllib.parse import urlparse

from diting.log import get_logger

logger = get_logger("fetch.cache")

# Schema version — bump when the table layout changes.
_SCHEMA_VERSION = "1"

_DEFAULT_TTL_DAYS = 7

# Domain suffix → TTL in days.  Ordered longest-match-first so that more
# specific rules win.  A suffix matches when the URL host equals it or
# ends with ``.suffix``.
_DOMAIN_TTL_RULES: tuple[tuple[str, int], ...] = (
    # Academic / reference — essentially immutable.
    ("arxiv.org", 36500),
    ("openalex.org", 36500),
    ("doi.org", 36500),
    ("crossref.org", 36500),
    ("wikipedia.org", 30),
    # Code hosting — stable enough to cache for a month.
    ("github.com", 30),
    ("stackoverflow.com", 30),
    ("stackexchange.com", 30),
    # News — stale within a day.
    ("nytimes.com", 1),
    ("bbc.com", 1),
    ("bbc.co.uk", 1),
    ("reuters.com", 1),
    ("cnn.com", 1),
    ("bloomberg.com", 1),
    ("theguardian.com", 1),
    ("wsj.com", 1),
)


def default_cache_path() -> pathlib.Path:
    """Return the default on-disk cache path: ``~/.cache/diting/content.db``."""
    return pathlib.Path.home() / ".cache" / "diting" / "content.db"


def _ttl_days_for_url(url: str) -> int:
    """Return the TTL in days for *url*, falling back to the default."""
    host = (urlparse(url).hostname or "").lower()
    if not host:
        return _DEFAULT_TTL_DAYS
    for suffix, days in _DOMAIN_TTL_RULES:
        if host == suffix or host.endswith("." + suffix):
            return days
    return _DEFAULT_TTL_DAYS


class ContentCache:
    """SQLite-backed URL → content cache with per-URL TTL.

    The class is safe to construct and discard cheaply; it keeps a single
    long-lived connection internally.  All methods are synchronous —
    SQLite is fast enough for this workload that async wrapping would
    add complexity without meaningful gain.
    """

    def __init__(self, db_path: pathlib.Path | str | None = None) -> None:
        path = pathlib.Path(db_path) if db_path is not None else default_cache_path()
        path.parent.mkdir(parents=True, exist_ok=True)
        self._path = path
        # ``isolation_level=None`` → autocommit; each statement is its
        # own transaction.  Simplest correct behaviour for this workload.
        self._conn = sqlite3.connect(str(path), isolation_level=None)
        self._conn.execute("PRAGMA journal_mode=WAL")
        self._ensure_schema()

    # ------------------------------------------------------------------
    # Public API
    # ------------------------------------------------------------------

    def get(self, url: str) -> str | None:
        """Return cached content for *url*, or ``None`` when missing/expired."""
        row = self._conn.execute(
            "SELECT content, expires_at FROM content WHERE url = ?",
            (url,),
        ).fetchone()
        if row is None:
            return None
        content, expires_at = row
        if expires_at <= time.time():
            # Delete the expired row opportunistically.
            self._conn.execute("DELETE FROM content WHERE url = ?", (url,))
            return None
        return content

    def put(self, url: str, content: str, *, ttl_days: int | None = None) -> None:
        """Store *content* for *url* with a domain-derived or explicit TTL."""
        if ttl_days is None:
            ttl_days = _ttl_days_for_url(url)
        fetched_at = time.time()
        expires_at = fetched_at + ttl_days * 86400
        self._conn.execute(
            "INSERT OR REPLACE INTO content "
            "(url, content, fetched_at, expires_at) VALUES (?, ?, ?, ?)",
            (url, content, fetched_at, expires_at),
        )

    def delete(self, url: str) -> None:
        """Remove the cached entry for *url* if present."""
        self._conn.execute("DELETE FROM content WHERE url = ?", (url,))

    def purge_expired(self) -> int:
        """Delete every expired entry; returns the number removed."""
        cur = self._conn.execute(
            "DELETE FROM content WHERE expires_at <= ?", (time.time(),)
        )
        return cur.rowcount

    def size(self) -> int:
        """Return the number of rows currently stored."""
        row = self._conn.execute("SELECT COUNT(*) FROM content").fetchone()
        return int(row[0]) if row else 0

    def close(self) -> None:
        """Close the underlying SQLite connection."""
        self._conn.close()

    # ------------------------------------------------------------------
    # Internals
    # ------------------------------------------------------------------

    def _ensure_schema(self) -> None:
        """Create the schema or rebuild it on a version mismatch."""
        self._conn.execute(
            "CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT)"
        )
        row = self._conn.execute(
            "SELECT value FROM meta WHERE key = 'schema_version'"
        ).fetchone()
        existing_version = row[0] if row else None

        if existing_version != _SCHEMA_VERSION:
            if existing_version is not None:
                logger.info(
                    "Cache schema version changed (%s → %s) — rebuilding",
                    existing_version, _SCHEMA_VERSION,
                )
            self._conn.execute("DROP TABLE IF EXISTS content")
            self._conn.execute(
                "CREATE TABLE content ("
                "  url        TEXT PRIMARY KEY,"
                "  content    TEXT NOT NULL,"
                "  fetched_at REAL NOT NULL,"
                "  expires_at REAL NOT NULL"
                ")"
            )
            self._conn.execute(
                "INSERT OR REPLACE INTO meta (key, value) VALUES "
                "('schema_version', ?)",
                (_SCHEMA_VERSION,),
            )
