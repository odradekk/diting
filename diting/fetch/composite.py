"""Composite fetcher — tries a primary fetcher, falls back to a secondary."""

from __future__ import annotations

from typing import TYPE_CHECKING

from diting.fetch.tavily import FetchError, FetchResult
from diting.log import get_logger

if TYPE_CHECKING:
    from diting.fetch.base import Fetcher

logger = get_logger("fetch.composite")


class CompositeFetcher:
    """Try *primary* first; on failure, delegate to *fallback*.

    Both ``fetch`` and ``fetch_many`` follow the same strategy:
    the primary fetcher runs first, and the fallback handles any
    URLs that the primary could not serve.
    """

    def __init__(self, primary: Fetcher, fallback: Fetcher) -> None:
        self._primary = primary
        self._fallback = fallback

    async def fetch(self, url: str) -> str:
        """Fetch *url* from primary; on ``FetchError``, try fallback."""
        try:
            return await self._primary.fetch(url)
        except FetchError:
            logger.info("Primary fetch failed for %s, trying fallback", url)
            return await self._fallback.fetch(url)

    async def fetch_many(self, urls: list[str]) -> list[FetchResult]:
        """Fetch *urls* from primary, retry failures via fallback in a single batch."""
        results = await self._primary.fetch_many(urls)

        # Collect failed indices and their URLs for positional merge.
        failed_indices = [i for i, r in enumerate(results) if not r.success]
        if not failed_indices:
            return results

        failed_urls = [results[i].url for i in failed_indices]
        logger.info("Retrying %d/%d failed URLs with fallback", len(failed_urls), len(urls))
        fallback_results = await self._fallback.fetch_many(failed_urls)

        # Positional merge: replace each failed slot with its fallback result.
        merged = list(results)
        for idx, fb_result in zip(failed_indices, fallback_results):
            merged[idx] = fb_result
        return merged

    async def close(self) -> None:
        """Close both inner fetchers. Fallback is always closed even if primary raises."""
        try:
            await self._primary.close()
        finally:
            await self._fallback.close()
