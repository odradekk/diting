"""Archive snapshot fallback — Wayback Machine + Archive.today.

When a live URL is dead, blocked, or returns thin content, public web
archives often preserve the original page.  This fetcher tries the
Wayback Machine availability API first (well-documented JSON, no
anti-bot friction), then Archive.today as a secondary source.

Only static-content URLs are attempted.  Search-results pages, API
endpoints, and feeds are skipped up front — archives don't mirror
them, and trying wastes a round-trip.
"""

from __future__ import annotations

import asyncio
from urllib.parse import urlparse

import httpx

from diting.fetch.local import _extract_markdown
from diting.fetch.tavily import FetchError, FetchResult
from diting.log import get_logger

logger = get_logger("fetch.archive")

_WAYBACK_AVAILABLE_API = "https://archive.org/wayback/available"
_ARCHIVE_TODAY_NEWEST = "https://archive.ph/newest/"
_ARCHIVE_TODAY_HOMEPAGES = frozenset(
    {"https://archive.ph", "https://archive.today", "https://archive.ph/"}
)
_DEFAULT_TIMEOUT = 15.0
_MIN_CONTENT_LENGTH = 200

# URL filter: these URL shapes are never archived, or the archive copy
# is semantically different from the live URL (search pages, feeds).
_SKIP_PATH_FRAGMENTS = ("/api/", "/graphql", "/.well-known/", "/feed", "/rss")
_SKIP_QUERY_KEYS = ("q=", "query=", "search=", "keyword=", "wd=")
_SEARCH_PATH_HINTS = ("/search",)
_API_HOST_PREFIXES = ("api.",)

_ARCHIVE_USER_AGENT = (
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)


def _is_archivable(url: str) -> bool:
    """Return True if *url* looks like static content worth archiving.

    Rejects search-results pages, API endpoints, feeds, and anything
    without a proper http(s) host.  Archive services don't mirror these
    shapes, so hitting them guarantees a miss.
    """
    try:
        parsed = urlparse(url)
    except Exception:
        return False
    if parsed.scheme not in ("http", "https"):
        return False
    if not parsed.netloc:
        return False

    host = parsed.netloc.lower()
    if any(host.startswith(p) for p in _API_HOST_PREFIXES):
        return False

    path_lower = parsed.path.lower()
    if any(frag in path_lower for frag in _SKIP_PATH_FRAGMENTS):
        return False
    if any(hint in path_lower for hint in _SEARCH_PATH_HINTS):
        return False

    query_lower = parsed.query.lower()
    if any(k in query_lower for k in _SKIP_QUERY_KEYS):
        return False

    return True


class ArchiveFetcher:
    """Fetch content from public web archives when the live site fails.

    Tries Wayback Machine's availability API first, then Archive.today
    as a secondary source.  Non-archivable URLs (search pages, API
    endpoints) are rejected up front with ``FetchError``.

    Implements the :class:`~diting.fetch.base.Fetcher` protocol so it
    can drop into a :class:`~diting.fetch.composite.CompositeFetcher`.
    """

    def __init__(self, timeout: float = _DEFAULT_TIMEOUT) -> None:
        self._http = httpx.AsyncClient(
            timeout=httpx.Timeout(timeout),
            follow_redirects=True,
            headers={
                "User-Agent": _ARCHIVE_USER_AGENT,
                "Accept": "text/html,application/xhtml+xml,*/*;q=0.8",
            },
        )

    # ------------------------------------------------------------------
    # Public API (matches Fetcher protocol)
    # ------------------------------------------------------------------

    async def fetch(self, url: str) -> str:
        """Fetch *url* content from a public archive snapshot.

        Raises:
            FetchError: If the URL is not archivable, no snapshots exist,
                or extraction from the snapshot produces thin content.
        """
        if not _is_archivable(url):
            raise FetchError(f"Archive skipped non-static URL: {url}")

        logger.debug("Archive fetch: %s", url)

        wayback_error: str | None = None
        try:
            content = await self._try_wayback(url)
            if content:
                return content
        except FetchError as exc:
            wayback_error = str(exc)
            logger.debug("Wayback miss for %s: %s", url, exc)

        try:
            content = await self._try_archive_today(url)
            if content:
                return content
        except FetchError as exc:
            logger.debug("Archive.today miss for %s: %s", url, exc)

        detail = f" (wayback: {wayback_error})" if wayback_error else ""
        raise FetchError(f"No archive snapshot available for {url}{detail}")

    async def fetch_many(self, urls: list[str]) -> list[FetchResult]:
        """Fetch multiple URLs concurrently. Never raises on per-URL failure."""
        tasks = [self._fetch_safe(u) for u in urls]
        return await asyncio.gather(*tasks)

    async def close(self) -> None:
        await self._http.aclose()

    # ------------------------------------------------------------------
    # Wayback
    # ------------------------------------------------------------------

    async def _try_wayback(self, url: str) -> str:
        snapshot_url = await self._lookup_wayback(url)
        if not snapshot_url:
            raise FetchError(f"No Wayback snapshot for {url}")
        html = await self._fetch_html(snapshot_url)
        return await self._extract(html, snapshot_url)

    async def _lookup_wayback(self, url: str) -> str | None:
        """Return a raw-content snapshot URL from Wayback, or None on miss."""
        try:
            resp = await self._http.get(
                _WAYBACK_AVAILABLE_API, params={"url": url},
            )
        except httpx.TimeoutException as exc:
            raise FetchError(f"Wayback lookup timeout: {exc}") from exc
        except httpx.HTTPError as exc:
            raise FetchError(f"Wayback lookup HTTP error: {exc}") from exc

        if resp.status_code >= 400:
            raise FetchError(f"Wayback lookup HTTP {resp.status_code}")

        try:
            data = resp.json()
        except ValueError as exc:
            raise FetchError(f"Wayback invalid JSON: {exc}") from exc

        snapshots = data.get("archived_snapshots") or {}
        closest = snapshots.get("closest") or {}
        if not closest.get("available"):
            return None

        # Skip archived error pages — Wayback sometimes stores 404/5xx copies.
        status_str = str(closest.get("status", ""))
        if status_str and not status_str.startswith("2"):
            return None

        timestamp = closest.get("timestamp", "")
        if not timestamp:
            return None

        # `id_` suffix returns the raw original HTML without Wayback's wrapper.
        return f"https://web.archive.org/web/{timestamp}id_/{url}"

    # ------------------------------------------------------------------
    # Archive.today
    # ------------------------------------------------------------------

    async def _try_archive_today(self, url: str) -> str:
        """GET archive.ph/newest/<url>, follow the redirect, extract content.

        Archive.today has no documented JSON API, so we rely on its
        ``/newest/`` redirect behaviour: a hit lands on an archive page
        (different hostname or path), a miss redirects back to the
        homepage.
        """
        snapshot_url = _ARCHIVE_TODAY_NEWEST + url
        try:
            resp = await self._http.get(snapshot_url)
        except httpx.TimeoutException as exc:
            raise FetchError(f"Archive.today timeout: {exc}") from exc
        except httpx.HTTPError as exc:
            raise FetchError(f"Archive.today HTTP error: {exc}") from exc

        if resp.status_code >= 400:
            raise FetchError(f"Archive.today HTTP {resp.status_code}")

        final_url = str(resp.url).rstrip("/")
        # Archive.today redirects misses to its homepage.
        if (
            final_url in _ARCHIVE_TODAY_HOMEPAGES
            or final_url + "/" in _ARCHIVE_TODAY_HOMEPAGES
        ):
            raise FetchError(f"No Archive.today snapshot for {url}")

        return await self._extract(resp.text, str(resp.url))

    # ------------------------------------------------------------------
    # Shared
    # ------------------------------------------------------------------

    async def _fetch_html(self, snapshot_url: str) -> str:
        try:
            resp = await self._http.get(snapshot_url)
        except httpx.TimeoutException as exc:
            raise FetchError(f"Snapshot timeout: {exc}") from exc
        except httpx.HTTPError as exc:
            raise FetchError(f"Snapshot HTTP error: {exc}") from exc

        if resp.status_code >= 400:
            raise FetchError(f"Snapshot HTTP {resp.status_code}")

        return resp.text

    async def _extract(self, html: str, snapshot_url: str) -> str:
        """Run trafilatura on *html*; raise if result is too thin."""
        extracted = await asyncio.to_thread(_extract_markdown, html)
        markdown = extracted[1]
        if len(markdown) < _MIN_CONTENT_LENGTH:
            raise FetchError(
                f"Archive snapshot {snapshot_url} thin content "
                f"({len(markdown)} chars)"
            )
        logger.info(
            "Archive extracted %d chars from %s", len(markdown), snapshot_url,
        )
        return markdown

    async def _fetch_safe(self, url: str) -> FetchResult:
        try:
            content = await self.fetch(url)
            return FetchResult(url=url, content=content, success=True)
        except Exception as exc:
            return FetchResult(url=url, content="", success=False, error=str(exc))


__all__ = ["ArchiveFetcher"]
