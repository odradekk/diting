"""DuckDuckGo search module via HTML scraping."""

from __future__ import annotations

from urllib.parse import parse_qs, unquote, urlparse

from curl_cffi.requests import AsyncSession
from bs4 import BeautifulSoup

from diting.models import SearchResult
from diting.modules.base import BaseSearchModule

_SEARCH_URL = "https://html.duckduckgo.com/html/"
_HEADERS = {
    "Accept-Language": "en-US,en;q=0.9",
    "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
}


def _unwrap_ddg_redirect(href: str) -> str:
    """Extract the real URL from a DuckDuckGo tracking redirect."""
    if href.startswith("//duckduckgo.com/l/?"):
        parsed = parse_qs(urlparse(f"https:{href}").query)
        uddg = parsed.get("uddg")
        if uddg:
            return unquote(uddg[0])
    return href


class DuckDuckGoSearchModule(BaseSearchModule):
    """Search module that scrapes DuckDuckGo HTML search results.

    Uses ``curl_cffi`` with browser impersonation to fetch the
    DuckDuckGo HTML-only endpoint and parses results with BeautifulSoup.

    Pagination strategy: none — the HTML endpoint has no pagination mechanism.
    Results are truncated to max_results from whatever the single page returns.
    """

    def __init__(self, timeout: int = 15, max_results: int = 20) -> None:
        super().__init__(name="duckduckgo", timeout=timeout, max_results=max_results)
        self._session = AsyncSession(
            headers=_HEADERS,
            impersonate="chrome",
        )

    async def _execute(self, query: str) -> list[SearchResult]:
        """Scrape DuckDuckGo HTML results page and return parsed results."""
        self._logger.debug("Querying DuckDuckGo: query=%r", query)

        params = {"q": query}
        response = await self._session.get(
            _SEARCH_URL,
            params=params,
            timeout=self._timeout,
            allow_redirects=True,
        )
        response.raise_for_status()

        soup = BeautifulSoup(response.text, "html.parser")
        results: list[SearchResult] = []

        for item in soup.select(".result"):
            title_tag = item.select_one(".result__title a")
            snippet_tag = item.select_one(".result__snippet")

            if not title_tag:
                continue

            title = title_tag.get_text(strip=True)
            href = str(title_tag["href"]) if title_tag.has_attr("href") else ""
            url = _unwrap_ddg_redirect(href) if href else ""
            snippet = snippet_tag.get_text(strip=True) if snippet_tag else ""

            if title and url:
                results.append(SearchResult(title=title, url=url, snippet=snippet))

        self._logger.debug("DuckDuckGo returned %d results", len(results))
        return results[:self._max_results]

    async def close(self) -> None:
        """Close the underlying session."""
        await self._session.close()
