"""Bing Web Search module via HTML scraping."""

from __future__ import annotations

import base64
from urllib.parse import parse_qs, urlparse

from curl_cffi.requests import AsyncSession
from bs4 import BeautifulSoup

from diting.models import SearchResult
from diting.modules.base import BaseSearchModule


def _resolve_bing_url(href: str) -> str:
    """Decode actual destination from a ``bing.com/ck/a`` tracking URL.

    Bing wraps result links in ``/ck/a?...&u=a1<base64>...`` redirects.
    The ``u`` parameter starts with ``a1`` followed by the base64-encoded
    target URL.  Returns the original *href* unchanged when decoding fails
    or the URL is already a direct link.
    """
    parsed = urlparse(href)
    hostname = parsed.hostname or ""
    if (hostname == "www.bing.com" or hostname == "bing.com") and parsed.path == "/ck/a":
        u_values = parse_qs(parsed.query).get("u")
        if u_values:
            token = u_values[0]
            if token.startswith("a1"):
                try:
                    raw = token[2:]
                    # Normalize padding for unpadded base64.
                    raw += "=" * (-len(raw) % 4)
                    return base64.urlsafe_b64decode(raw).decode("utf-8")
                except Exception:
                    pass
    return href

_SEARCH_URL = "https://www.bing.com/search"

# Bing HTML scraping: count param controls results per page (up to ~50).
# Pagination strategy: loop with first offset (1-based: 1, 51, 101, ...).
# Stops when no new results are found on a page.
_MAX_COUNT_PER_PAGE = 50
_HEADERS = {
    "Accept-Language": "en-US,en;q=0.9",
    "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
}


class BingSearchModule(BaseSearchModule):
    """Search module that scrapes Bing web search results.

    Uses ``curl_cffi`` with browser impersonation to fetch Bing HTML
    and parses organic results with BeautifulSoup.  Paginates via the
    ``first`` parameter when more results are requested.
    """

    def __init__(self, timeout: int = 15, max_results: int = 20) -> None:
        super().__init__(name="bing", timeout=timeout, max_results=max_results)
        self._session = AsyncSession(
            headers=_HEADERS,
            impersonate="chrome",
        )

    async def _execute(self, query: str) -> list[SearchResult]:
        """Scrape Bing search results pages and return parsed results."""
        self._logger.debug("Querying Bing: query=%r, max_results=%d", query, self._max_results)

        all_results: list[SearchResult] = []
        seen_urls: set[str] = set()
        first = 1  # Bing uses 1-based offset

        while len(all_results) < self._max_results:
            count = min(self._max_results - len(all_results), _MAX_COUNT_PER_PAGE)
            params: dict[str, str] = {"q": query, "count": str(count)}
            if first > 1:
                params["first"] = str(first)

            response = await self._session.get(
                _SEARCH_URL,
                params=params,
                timeout=self._timeout,
                allow_redirects=True,
            )
            response.raise_for_status()

            soup = BeautifulSoup(response.text, "html.parser")
            page_added = 0

            for item in soup.select("li.b_algo"):
                title_tag = item.select_one("h2 a")
                snippet_tag = item.select_one(".b_caption p") or item.select_one("p")

                if not title_tag:
                    continue

                title = title_tag.get_text(strip=True)
                raw_href = str(title_tag["href"]) if title_tag.has_attr("href") else ""
                url = _resolve_bing_url(raw_href)
                snippet = snippet_tag.get_text(strip=True) if snippet_tag else ""

                if title and url and url not in seen_urls:
                    seen_urls.add(url)
                    all_results.append(SearchResult(title=title, url=url, snippet=snippet))
                    page_added += 1

            if page_added == 0:
                break

            first += count

        self._logger.debug("Bing returned %d results", len(all_results))
        return all_results[:self._max_results]

    async def close(self) -> None:
        """Close the underlying session."""
        await self._session.close()
