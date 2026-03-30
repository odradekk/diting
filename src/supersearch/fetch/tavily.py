"""Tavily Extract API wrapper for fetching page content."""

from __future__ import annotations

import httpx
from pydantic import BaseModel

from supersearch.log import get_logger

logger = get_logger("fetch.tavily")

_EXTRACT_URL = "https://api.tavily.com/extract"


class FetchError(Exception):
    """Error raised when a page fetch fails."""


class FetchResult(BaseModel):
    """Result of a single URL fetch attempt."""

    url: str
    content: str
    success: bool
    error: str | None = None


class TavilyFetcher:
    """Async wrapper around the Tavily Extract API.

    Uses ``httpx.AsyncClient`` for HTTP requests.  The caller is responsible
    for calling :meth:`close` when the fetcher is no longer needed.
    """

    def __init__(self, api_key: str, timeout: int = 15) -> None:
        self._api_key = api_key
        self._http = httpx.AsyncClient(
            headers={"Content-Type": "application/json"},
            timeout=httpx.Timeout(timeout),
        )

    # ------------------------------------------------------------------
    # Public API
    # ------------------------------------------------------------------

    async def fetch(self, url: str) -> str:
        """Fetch page text content for a single URL via Tavily Extract.

        Args:
            url: The URL to extract content from.

        Returns:
            The raw text content of the page.

        Raises:
            FetchError: On timeout, HTTP error, or empty/missing content.
        """
        logger.debug("Fetching URL: %s", url)

        try:
            response = await self._http.post(
                _EXTRACT_URL,
                json={"api_key": self._api_key, "urls": [url]},
            )
        except httpx.TimeoutException as exc:
            logger.warning("Timeout fetching %s: %s", url, exc)
            raise FetchError(f"Timeout fetching {url}: {exc}") from exc

        if response.status_code >= 400:
            logger.warning(
                "HTTP %d fetching %s: %s",
                response.status_code,
                url,
                response.text,
            )
            raise FetchError(
                f"HTTP {response.status_code} fetching {url}: {response.text}"
            )

        data = response.json()
        results = data.get("results", [])

        if not results or not results[0].get("raw_content"):
            logger.warning("Empty content for %s", url)
            raise FetchError(f"Empty content for {url}")

        content = results[0]["raw_content"]
        logger.info("Fetched %s (%d chars)", url, len(content))
        return content

    async def fetch_many(self, urls: list[str]) -> list[FetchResult]:
        """Fetch page content for multiple URLs in a single API call.

        Unlike :meth:`fetch`, this method never raises on individual failures.
        Errors are captured in each :class:`FetchResult`.

        Args:
            urls: List of URLs to extract content from.

        Returns:
            A list of :class:`FetchResult` objects, one per input URL.
        """
        logger.debug("Fetching %d URLs", len(urls))

        try:
            response = await self._http.post(
                _EXTRACT_URL,
                json={"api_key": self._api_key, "urls": urls},
            )
        except httpx.TimeoutException as exc:
            logger.warning("Timeout fetching %d URLs: %s", len(urls), exc)
            return [
                FetchResult(
                    url=u,
                    content="",
                    success=False,
                    error=f"Timeout: {exc}",
                )
                for u in urls
            ]

        if response.status_code >= 400:
            logger.warning(
                "HTTP %d fetching %d URLs: %s",
                response.status_code,
                len(urls),
                response.text,
            )
            return [
                FetchResult(
                    url=u,
                    content="",
                    success=False,
                    error=f"HTTP {response.status_code}: {response.text}",
                )
                for u in urls
            ]

        data = response.json()

        # Build lookup from successful results.
        success_map: dict[str, str] = {}
        for item in data.get("results", []):
            raw = item.get("raw_content", "")
            if raw:
                success_map[item["url"]] = raw

        # Build lookup from failed results.
        failed_map: dict[str, str] = {}
        for item in data.get("failed_results", []):
            failed_map[item.get("url", "")] = item.get(
                "error", "Unknown error"
            )

        # Assemble output in input order.
        out: list[FetchResult] = []
        for u in urls:
            if u in success_map:
                logger.info("Fetched %s (%d chars)", u, len(success_map[u]))
                out.append(
                    FetchResult(url=u, content=success_map[u], success=True)
                )
            else:
                error_msg = failed_map.get(u, "No content returned")
                logger.warning("Failed to fetch %s: %s", u, error_msg)
                out.append(
                    FetchResult(
                        url=u, content="", success=False, error=error_msg
                    )
                )

        return out

    async def close(self) -> None:
        """Close the underlying HTTP client."""
        await self._http.aclose()
