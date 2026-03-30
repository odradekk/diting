"""Brave Search API module."""

from __future__ import annotations

import httpx

from diting.models import SearchResult
from diting.modules.base import BaseSearchModule

_BASE_URL = "https://api.search.brave.com/res/v1/web/search"
_RESULT_COUNT = 20


class BraveSearchModule(BaseSearchModule):
    """Search module backed by the Brave Search API.

    Sends web search queries to the Brave Search REST API and converts
    the response into a list of :class:`SearchResult` objects.
    """

    def __init__(self, api_key: str, timeout: int = 30) -> None:
        if not api_key:
            raise ValueError("Brave API key is required")
        super().__init__(name="brave", timeout=timeout)
        self._api_key = api_key
        self._http = httpx.AsyncClient(
            headers={
                "Accept": "application/json",
                "Accept-Encoding": "gzip",
                "X-Subscription-Token": api_key,
            },
        )

    async def _execute(self, query: str) -> list[SearchResult]:
        """Call the Brave Search API and return parsed results."""
        self._logger.debug("Querying Brave API: query=%r", query)

        response = await self._http.get(
            _BASE_URL,
            params={"q": query, "count": _RESULT_COUNT},
        )

        if response.status_code == 429:
            retry_after = response.headers.get("Retry-After", "unknown")
            raise httpx.HTTPStatusError(
                f"Rate limited by Brave API (retry after {retry_after}s)",
                request=response.request,
                response=response,
            )

        response.raise_for_status()

        data = response.json()
        web = data.get("web")
        if not web:
            return []

        raw_results = web.get("results")
        if not raw_results:
            return []

        results: list[SearchResult] = []
        for item in raw_results:
            title = item.get("title", "")
            url = item.get("url", "")
            snippet = item.get("description", "")
            if title and url:
                results.append(
                    SearchResult(title=title, url=url, snippet=snippet)
                )

        self._logger.debug("Brave API returned %d results", len(results))
        return results

    async def close(self) -> None:
        """Close the underlying HTTP client."""
        await self._http.aclose()
