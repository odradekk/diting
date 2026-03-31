"""Bing Web Search module via HTML scraping."""

from __future__ import annotations

from curl_cffi.requests import AsyncSession
from bs4 import BeautifulSoup

from diting.models import SearchResult
from diting.modules.base import BaseSearchModule

_SEARCH_URL = "https://www.bing.com/search"
_RESULT_COUNT = 20
_HEADERS = {
    "Accept-Language": "en-US,en;q=0.9",
    "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
}


class BingSearchModule(BaseSearchModule):
    """Search module that scrapes Bing web search results.

    Uses ``curl_cffi`` with browser impersonation to fetch Bing HTML
    and parses organic results with BeautifulSoup.
    """

    def __init__(self, timeout: int = 15) -> None:
        super().__init__(name="bing", timeout=timeout)
        self._session = AsyncSession(
            headers=_HEADERS,
            impersonate="chrome131",
        )

    async def _execute(self, query: str) -> list[SearchResult]:
        """Scrape Bing search results page and return parsed results."""
        self._logger.debug("Querying Bing: query=%r", query)

        params = {"q": query, "count": str(_RESULT_COUNT)}
        response = await self._session.get(
            _SEARCH_URL,
            params=params,
            timeout=self._timeout,
            allow_redirects=True,
        )
        response.raise_for_status()

        soup = BeautifulSoup(response.text, "html.parser")
        results: list[SearchResult] = []

        for item in soup.select("li.b_algo"):
            title_tag = item.select_one("h2 a")
            snippet_tag = item.select_one(".b_caption p") or item.select_one("p")

            if not title_tag:
                continue

            title = title_tag.get_text(strip=True)
            url = str(title_tag["href"]) if title_tag.has_attr("href") else ""
            snippet = snippet_tag.get_text(strip=True) if snippet_tag else ""

            if title and url:
                results.append(SearchResult(title=title, url=url, snippet=snippet))

        self._logger.debug("Bing returned %d results", len(results))
        return results

    async def close(self) -> None:
        """Close the underlying session."""
        await self._session.close()
