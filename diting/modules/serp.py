"""SerpAPI search module with configurable engine."""

from __future__ import annotations

import httpx

from diting.models import SearchResult
from diting.modules.base import BaseSearchModule

_SERPAPI_URL = "https://serpapi.com/search.json"

# SerpAPI limits: num max 100 per request (multiples of 10).
# Pagination strategy: loop with start offset (0, 100, 200, ...).
# No documented hard cap on start; bounded by Google's index depth in practice.
_MAX_NUM_PER_REQUEST = 100


class SerpSearchModule(BaseSearchModule):
    """Search module backed by `SerpAPI <https://serpapi.com>`_.

    Calls the SerpAPI search endpoint with a configurable engine
    (default ``"google"``) and converts ``organic_results`` into
    :class:`SearchResult` instances.  Paginates via ``start`` when
    more than 100 results are requested.
    """

    def __init__(self, api_key: str, engine: str = "google", timeout: int = 30, max_results: int = 20) -> None:
        if not api_key:
            raise ValueError("SerpAPI key must not be empty")
        super().__init__(name="serp", timeout=timeout, max_results=max_results)
        self._api_key = api_key
        self._engine = engine
        self._client = httpx.AsyncClient()

    async def _execute(self, query: str) -> list[SearchResult]:
        """Call SerpAPI and return organic results."""
        all_results: list[SearchResult] = []
        seen_urls: set[str] = set()
        start = 0
        num = min(self._max_results, _MAX_NUM_PER_REQUEST)

        while len(all_results) < self._max_results:
            params = {
                "q": query,
                "api_key": self._api_key,
                "engine": self._engine,
                "num": num,
                "start": start,
            }

            self._logger.debug("Requesting SerpAPI: query=%r, start=%d, num=%d", query, start, num)
            response = await self._client.get(
                _SERPAPI_URL, params=params, timeout=self._timeout
            )

            if response.status_code == 429:
                raise httpx.HTTPStatusError(
                    "Rate limited by SerpAPI (429)",
                    request=response.request,
                    response=response,
                )
            response.raise_for_status()

            data = response.json()

            if "error" in data:
                raise RuntimeError(f"SerpAPI error: {data['error']}")

            organic = data.get("organic_results")
            if not organic:
                break

            page_added = 0
            for item in organic:
                if not isinstance(item, dict):
                    continue
                title = item.get("title") or ""
                url = item.get("link") or ""
                snippet = item.get("snippet") or ""
                if title and url and url not in seen_urls:
                    seen_urls.add(url)
                    all_results.append(
                        SearchResult(title=title, url=url, snippet=snippet)
                    )
                    page_added += 1

            if page_added == 0:
                break
            if len(all_results) >= self._max_results:
                break

            start += num

        self._logger.debug("SerpAPI returned %d organic results", len(all_results))
        return all_results[:self._max_results]

    async def close(self) -> None:
        """Close the underlying HTTP client."""
        await self._client.aclose()
