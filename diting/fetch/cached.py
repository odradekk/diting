"""Cache-aware fetcher decorator.

:class:`CachedFetcher` wraps any object satisfying the :class:`Fetcher`
protocol and adds a read-through / write-through cache layer.  On every
call path:

1. Look up the URL in :class:`~diting.fetch.cache.ContentCache`.
2. On miss, delegate to the inner fetcher.
3. Validate the fetched content with :func:`is_cacheable`.
4. Only write back to the cache if validation passes — login walls,
   Cloudflare challenges, and short garbage are dropped on the floor.

The wrapper is the single point where "cache hit / miss / reject" signals
live in the fetch stack, making it the natural place to log that signal.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from diting.fetch.content_validator import is_cacheable
from diting.fetch.tavily import FetchError, FetchResult
from diting.log import get_logger

if TYPE_CHECKING:
    from diting.fetch.base import Fetcher
    from diting.fetch.cache import ContentCache

logger = get_logger("fetch.cached")


class CachedFetcher:
    """Read-through / write-through cache decorator around any Fetcher."""

    def __init__(self, inner: Fetcher, cache: ContentCache) -> None:
        self._inner = inner
        self._cache = cache

    async def fetch(self, url: str) -> str:
        """Return cached content if fresh; otherwise fetch and store it.

        Raises :class:`FetchError` when the inner fetcher fails.  A cache
        hit is always a success even if the inner fetcher would have
        failed — that is the entire point.
        """
        cached = self._cache.get(url)
        if cached is not None:
            logger.info("cache_hit=true url=%s chars=%d", url, len(cached))
            return cached

        content = await self._inner.fetch(url)
        self._maybe_store(url, content)
        return content

    async def fetch_many(self, urls: list[str]) -> list[FetchResult]:
        """Serve cache hits locally and delegate only the misses to *inner*.

        The result list preserves input order so positional callers in the
        pipeline see no behaviour change.
        """
        results: list[FetchResult | None] = [None] * len(urls)
        misses_idx: list[int] = []
        misses_urls: list[str] = []

        for i, url in enumerate(urls):
            cached = self._cache.get(url)
            if cached is not None:
                logger.info("cache_hit=true url=%s chars=%d", url, len(cached))
                results[i] = FetchResult(url=url, content=cached, success=True)
            else:
                misses_idx.append(i)
                misses_urls.append(url)

        if misses_urls:
            fetched = await self._inner.fetch_many(misses_urls)
            if len(fetched) != len(misses_urls):
                logger.warning(
                    "inner.fetch_many returned %d results for %d URLs",
                    len(fetched), len(misses_urls),
                )
                for idx in misses_idx[len(fetched):]:
                    results[idx] = FetchResult(
                        url=urls[idx], content="", success=False,
                        error="inner fetcher result count mismatch",
                    )
            for idx, fr in zip(misses_idx, fetched):
                results[idx] = fr
                if fr.success and fr.content:
                    self._maybe_store(fr.url, fr.content)

        # Every slot should be populated; synthesize failures for any gaps.
        return [
            r if r is not None
            else FetchResult(url=urls[i], content="", success=False, error="slot unfilled")
            for i, r in enumerate(results)
        ]

    async def close(self) -> None:
        """Close the inner fetcher; the cache connection is owned elsewhere."""
        await self._inner.close()

    # ------------------------------------------------------------------
    # Internals
    # ------------------------------------------------------------------

    def _maybe_store(self, url: str, content: str) -> None:
        """Validate *content* and cache it only when it passes the gate."""
        ok, reason = is_cacheable(content)
        if ok:
            self._cache.put(url, content)
            logger.debug("cache_stored url=%s chars=%d", url, len(content))
        else:
            logger.info(
                "cache_rejected url=%s reason=%s chars=%d",
                url, reason, len(content),
            )


__all__ = ["CachedFetcher", "FetchError"]
