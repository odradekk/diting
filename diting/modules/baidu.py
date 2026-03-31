"""Baidu Web Search module via HTML scraping."""

from __future__ import annotations

import json

from curl_cffi.requests import AsyncSession
from bs4 import BeautifulSoup, Tag

from diting.models import SearchResult
from diting.modules.base import BaseSearchModule

_SEARCH_URL = "https://www.baidu.com/s"
_HEADERS = {
    "Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
    "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
}

_SNIPPET_SELECTOR = (
    "div[class*='c-line-clamp'], "
    "div[class*='summary'], "
    ".c-span-last p, "
    ".content-right_8Zs40"
)


def _extract_url(item: Tag, title_tag: Tag) -> str:
    """Extract the real URL from a Baidu result element.

    Baidu stores the canonical URL in several possible locations:
    1. ``mu`` attribute on the result container
    2. ``data-tools`` JSON attribute (``url`` or ``titleUrl`` key)
    3. ``data-landurl`` attribute on the title link
    4. ``href`` attribute on the title link (Baidu redirect URL, fallback)
    """
    if item.has_attr("mu"):
        return str(item["mu"])

    if item.has_attr("data-tools"):
        try:
            tools = json.loads(str(item["data-tools"]))
            url = tools.get("url") or tools.get("titleUrl")
            if url:
                return str(url)
        except (json.JSONDecodeError, TypeError):
            pass

    if title_tag.has_attr("data-landurl"):
        return str(title_tag["data-landurl"])

    if title_tag.has_attr("href"):
        return str(title_tag["href"])

    return ""


class BaiduSearchModule(BaseSearchModule):
    """Search module that scrapes Baidu web search results.

    Uses ``curl_cffi`` with browser impersonation to fetch Baidu HTML
    and parses organic results with BeautifulSoup. Extracts canonical
    URLs from Baidu's various attribute formats.
    """

    def __init__(self, timeout: int = 15) -> None:
        super().__init__(name="baidu", timeout=timeout)
        self._session = AsyncSession(
            headers=_HEADERS,
            impersonate="chrome131",
        )

    async def _execute(self, query: str) -> list[SearchResult]:
        """Scrape Baidu search results page and return parsed results."""
        self._logger.debug("Querying Baidu: query=%r", query)

        params = {"wd": query}
        response = await self._session.get(
            _SEARCH_URL,
            params=params,
            timeout=self._timeout,
            allow_redirects=True,
        )
        response.raise_for_status()

        soup = BeautifulSoup(response.text, "html.parser")
        results: list[SearchResult] = []

        for item in soup.select("#content_left > .result, #content_left > .result-op"):
            title_tag = item.select_one("h3 a")
            snippet_tag = item.select_one(_SNIPPET_SELECTOR)

            if not title_tag:
                continue

            title = title_tag.get_text(" ", strip=True)
            url = _extract_url(item, title_tag)
            snippet = snippet_tag.get_text(" ", strip=True) if snippet_tag else ""

            if title and url:
                results.append(SearchResult(title=title, url=url, snippet=snippet))

        self._logger.debug("Baidu returned %d results", len(results))
        return results

    async def close(self) -> None:
        """Close the underlying session."""
        await self._session.close()
