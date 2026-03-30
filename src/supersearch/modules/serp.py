"""SerpAPI search module with configurable engine."""

from __future__ import annotations

import httpx

from supersearch.models import SearchResult
from supersearch.modules.base import BaseSearchModule

_SERPAPI_URL = "https://serpapi.com/search.json"


class SerpSearchModule(BaseSearchModule):
    """Search module backed by `SerpAPI <https://serpapi.com>`_.

    Calls the SerpAPI search endpoint with a configurable engine
    (default ``"google"``) and converts ``organic_results`` into
    :class:`SearchResult` instances.
    """

    def __init__(self, api_key: str, engine: str = "google", timeout: int = 30) -> None:
        if not api_key:
            raise ValueError("SerpAPI key must not be empty")
        super().__init__(name="serp", timeout=timeout)
        self._api_key = api_key
        self._engine = engine
        self._client = httpx.AsyncClient()

    async def _execute(self, query: str) -> list[SearchResult]:
        """Call SerpAPI and return organic results."""
        params = {
            "q": query,
            "api_key": self._api_key,
            "engine": self._engine,
            "num": 20,
        }

        self._logger.debug("Requesting SerpAPI: query=%r", query)
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

        # SerpAPI may return a 200 with an error payload instead of results.
        if "error" in data:
            raise RuntimeError(f"SerpAPI error: {data['error']}")

        organic = data.get("organic_results")
        if not organic:
            return []

        results: list[SearchResult] = []
        for item in organic:
            if not isinstance(item, dict):
                continue
            title = item.get("title") or ""
            url = item.get("link") or ""
            snippet = item.get("snippet") or ""
            if title and url:
                results.append(
                    SearchResult(title=title, url=url, snippet=snippet)
                )

        self._logger.debug("SerpAPI returned %d organic results", len(results))
        return results

    async def close(self) -> None:
        """Close the underlying HTTP client."""
        await self._client.aclose()
