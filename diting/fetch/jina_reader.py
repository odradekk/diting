"""r.jina.ai Reader API wrapper — second-layer fetch fallback.

When direct HTTP (curl_cffi / browser) fails, r.jina.ai renders the page
server-side and returns extracted markdown directly.  This clears a large
share of Cloudflare / DataDome protected sites without paying the cost
of a full local browser escalation.

The service is free for anonymous use.  Supplying ``JINA_API_KEY`` raises
the per-IP rate limit — keyless mode is still production-viable, so the
key is strictly optional.
"""

from __future__ import annotations

import asyncio

import httpx

from diting.fetch.tavily import FetchError, FetchResult
from diting.log import get_logger

logger = get_logger("fetch.jina_reader")

_READER_BASE = "https://r.jina.ai/"
_DEFAULT_TIMEOUT = 15.0
_MIN_CONTENT_LENGTH = 50  # anything shorter is almost certainly an error page


class JinaReaderFetcher:
    """Async wrapper around the r.jina.ai reader service.

    r.jina.ai accepts ``https://r.jina.ai/<URL>`` and returns the markdown
    of the extracted main content.  An optional bearer token (sent as
    ``Authorization: Bearer <key>``) lifts anonymous rate limits.

    Implements the :class:`~diting.fetch.base.Fetcher` protocol so it can
    be dropped into a :class:`~diting.fetch.composite.CompositeFetcher`.
    """

    def __init__(
        self,
        api_key: str = "",
        timeout: float = _DEFAULT_TIMEOUT,
    ) -> None:
        headers: dict[str, str] = {
            "Accept": "text/plain",
            "X-Return-Format": "markdown",
        }
        if api_key:
            headers["Authorization"] = f"Bearer {api_key}"

        self._http = httpx.AsyncClient(
            headers=headers,
            timeout=httpx.Timeout(timeout),
            follow_redirects=True,
        )

    # ------------------------------------------------------------------
    # Public API (matches Fetcher protocol)
    # ------------------------------------------------------------------

    async def fetch(self, url: str) -> str:
        """Fetch markdown for *url* via r.jina.ai.

        Args:
            url: The URL to extract content from.

        Returns:
            The extracted markdown content.

        Raises:
            FetchError: On timeout, HTTP error, or thin/empty response.
        """
        reader_url = _READER_BASE + url
        logger.debug("Jina reader fetch: %s", url)

        try:
            response = await self._http.get(reader_url)
        except httpx.TimeoutException as exc:
            logger.warning("Jina timeout for %s: %s", url, exc)
            raise FetchError(f"Jina timeout for {url}: {exc}") from exc
        except httpx.HTTPError as exc:
            logger.warning("Jina HTTP error for %s: %s", url, exc)
            raise FetchError(f"Jina HTTP error for {url}: {exc}") from exc

        if response.status_code >= 400:
            logger.warning(
                "Jina HTTP %d for %s: %s",
                response.status_code, url, response.text[:200],
            )
            raise FetchError(
                f"Jina HTTP {response.status_code} for {url}"
            )

        content = response.text.strip()
        if len(content) < _MIN_CONTENT_LENGTH:
            logger.warning(
                "Jina returned thin content for %s (%d chars)",
                url, len(content),
            )
            raise FetchError(
                f"Jina returned thin content for {url} ({len(content)} chars)"
            )

        logger.info("Jina fetched %s (%d chars)", url, len(content))
        return content

    async def fetch_many(self, urls: list[str]) -> list[FetchResult]:
        """Fetch multiple URLs concurrently.  Never raises on per-URL failure."""
        tasks = [self._fetch_safe(u) for u in urls]
        return await asyncio.gather(*tasks)

    async def close(self) -> None:
        """Close the underlying HTTP client."""
        await self._http.aclose()

    # ------------------------------------------------------------------
    # Internals
    # ------------------------------------------------------------------

    async def _fetch_safe(self, url: str) -> FetchResult:
        """``fetch`` variant that never raises — captures errors on the result."""
        try:
            content = await self.fetch(url)
            return FetchResult(url=url, content=content, success=True)
        except Exception as exc:
            return FetchResult(
                url=url, content="", success=False, error=str(exc),
            )


__all__ = ["JinaReaderFetcher"]
