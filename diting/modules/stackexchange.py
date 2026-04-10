"""StackExchange Q&A search module (Stack Overflow)."""

from __future__ import annotations

import html

import httpx

from diting.models import SearchResult
from diting.modules.base import BaseSearchModule
from diting.modules.manifest import ModuleManifest

_BASE_URL = "https://api.stackexchange.com/2.3/search/advanced"

# StackExchange caps pagesize at 100 per request.
_MAX_PAGESIZE = 100


class StackExchangeSearchModule(BaseSearchModule):
    """Search module backed by the StackExchange v2.3 API.

    Queries Stack Overflow for programming Q&A and converts the response
    into a list of :class:`SearchResult` objects.
    """

    MANIFEST = ModuleManifest(
        domains=["qa", "programming"],
        languages=["en"],
        cost_tier="free",
        latency_tier="fast",
        result_type="qa",
        scope=(
            "Programming Q&A from Stack Overflow. Strong for specific "
            "technical questions, error messages, API usage, and code "
            "examples. Weak for conceptual overviews or non-programming topics."
        ),
    )

    def __init__(self, timeout: int = 15, max_results: int = 20) -> None:
        super().__init__(name="stackexchange", timeout=timeout, max_results=max_results)
        # timeout=None: BaseSearchModule.search() is the single source of
        # truth for timeouts (via asyncio.wait_for).  Matches arxiv /
        # github / wikipedia conventions.
        self._http = httpx.AsyncClient(timeout=None)

    async def _execute(self, query: str) -> list[SearchResult]:
        """Call the StackExchange API and return parsed results."""
        self._logger.debug(
            "Querying StackExchange API: query=%r, max_results=%d",
            query,
            self._max_results,
        )

        params: dict[str, str | int] = {
            "q": query,
            "site": "stackoverflow",
            "order": "desc",
            "sort": "relevance",
            "pagesize": min(self._max_results, _MAX_PAGESIZE),
            "page": 1,
            "filter": "!nNPvSNdWme",
        }

        response = await self._http.get(_BASE_URL, params=params)

        if response.status_code == 429:
            raise httpx.HTTPStatusError(
                "StackExchange API rate limit exceeded (300 requests/day without key)",
                request=response.request,
                response=response,
            )

        # Try to parse JSON before raise_for_status so we can detect
        # SE-specific throttle errors (error_id 502) that arrive as HTTP 400.
        # Non-JSON error bodies (e.g. 5xx HTML) fall through to raise_for_status;
        # non-JSON 2xx responses are re-raised as they indicate upstream corruption.
        try:
            data = response.json()
        except ValueError:
            if response.is_error:
                response.raise_for_status()
            raise

        if data.get("error_id") == 502:
            raise httpx.HTTPStatusError(
                f"StackExchange API throttle: {data.get('error_message', 'too many requests')}",
                request=response.request,
                response=response,
            )

        response.raise_for_status()

        items = data.get("items", [])

        all_results: list[SearchResult] = []
        seen_urls: set[str] = set()

        for item in items:
            title = html.unescape(item.get("title", ""))
            url = item.get("url") or item.get("link", "")
            snippet = item.get("body_excerpt", "")
            if not snippet:
                tags = item.get("tags", [])
                snippet = ", ".join(tags) if tags else ""

            if title and url and url not in seen_urls:
                seen_urls.add(url)
                all_results.append(
                    SearchResult(title=title, url=url, snippet=snippet)
                )

        self._logger.debug("StackExchange API returned %d results", len(all_results))
        return all_results[: self._max_results]

    async def close(self) -> None:
        """Close the underlying HTTP client."""
        await self._http.aclose()
