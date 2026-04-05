"""Brave Search API module."""

from __future__ import annotations

import httpx

from diting.models import SearchResult
from diting.modules.base import BaseSearchModule
from diting.modules.manifest import ModuleManifest

_BASE_URL = "https://api.search.brave.com/res/v1/web/search"

# Brave API limits: count max 20 per request, offset max 9.
# Pagination strategy: loop with offset 0..9, each fetching up to 20 results.
# Theoretical ceiling: ~200 results (10 offsets * 20 per page).
_MAX_COUNT_PER_REQUEST = 20
_MAX_OFFSET = 9


class BraveSearchModule(BaseSearchModule):
    """Search module backed by the Brave Search API.

    Sends web search queries to the Brave Search REST API and converts
    the response into a list of :class:`SearchResult` objects.
    Paginates via ``offset`` (max 9) when more results are requested.
    """

    MANIFEST = ModuleManifest(
        domains=["general", "independent-index"],
        languages=["en", "zh", "*"],
        cost_tier="cheap",
        latency_tier="fast",
        result_type="general",
        scope=(
            "General web search backed by an independent index, with good "
            "coverage of English technical content, independent blogs, and "
            "long-tail sites. Stable quality, suitable as a paid baseline."
        ),
    )

    def __init__(self, api_key: str, timeout: int = 30, max_results: int = 20) -> None:
        if not api_key:
            raise ValueError("Brave API key is required")
        super().__init__(name="brave", timeout=timeout, max_results=max_results)
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
        self._logger.debug("Querying Brave API: query=%r, max_results=%d", query, self._max_results)

        all_results: list[SearchResult] = []
        seen_urls: set[str] = set()

        for offset in range(_MAX_OFFSET + 1):
            count = min(self._max_results - len(all_results), _MAX_COUNT_PER_REQUEST)
            if count <= 0:
                break

            params: dict[str, str | int] = {"q": query, "count": count}
            if offset > 0:
                params["offset"] = offset

            response = await self._http.get(_BASE_URL, params=params)

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
                break

            raw_results = web.get("results")
            if not raw_results:
                break

            page_added = 0
            for item in raw_results:
                title = item.get("title", "")
                url = item.get("url", "")
                snippet = item.get("description", "")
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

        self._logger.debug("Brave API returned %d results", len(all_results))
        return all_results[:self._max_results]

    async def close(self) -> None:
        """Close the underlying HTTP client."""
        await self._http.aclose()
