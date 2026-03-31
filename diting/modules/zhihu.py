"""Zhihu search module via Playwright browser automation."""

from __future__ import annotations

from urllib.parse import quote_plus

from bs4 import BeautifulSoup

from diting.models import SearchResult
from diting.modules.playwright_base import PlaywrightSearchModule

_SEARCH_URL = "https://www.zhihu.com/search"


class ZhihuSearchModule(PlaywrightSearchModule):
    """Search module that scrapes Zhihu search results via Playwright.

    Requires authentication cookies to access search results.
    Cookies can be provided via the ``ZHIHU_COOKIE`` environment variable
    (raw header string) or a Playwright storage state file.
    """

    def __init__(self, *, cookie: str = "", timeout: int = 45) -> None:
        super().__init__(
            name="zhihu",
            cookie=cookie,
            cookie_domain=".zhihu.com",
            cookie_env="ZHIHU_COOKIE",
            js_wait_ms=6000,
            storage_state_path="config/zhihu_storage_state.json",
            timeout=timeout,
        )

    def _build_search_url(self, query: str) -> str:
        return f"{_SEARCH_URL}?q={quote_plus(query)}&type=content"

    def _parse_results(self, html: str) -> list[SearchResult]:
        soup = BeautifulSoup(html, "html.parser")
        results: list[SearchResult] = []
        seen_urls: set[str] = set()

        for link_tag in soup.select(
            'main a[href*="/question/"], main a[href*="/p/"]'
        ):
            href = str(link_tag["href"]) if link_tag.has_attr("href") else ""
            if not href:
                continue
            if href.startswith("/"):
                href = f"https://www.zhihu.com{href}"
            if href in seen_urls:
                continue

            title = link_tag.get_text(" ", strip=True)
            if not title:
                continue

            # Extract snippet from ContentItem container or parent div.
            container = link_tag.find_parent(
                class_="ContentItem"
            ) or link_tag.find_parent("div")
            snippet = "(无摘要)"
            if container:
                snippet_tag = container.select_one(".RichContent-inner .RichText")
                snippet_text = (
                    snippet_tag.get_text(" ", strip=True)
                    if snippet_tag
                    else container.get_text(" ", strip=True)
                )
                if snippet_text:
                    if snippet_text.startswith(title):
                        snippet_text = snippet_text[len(title) :].strip(" -\n\t")
                    if "：" in snippet_text:
                        snippet_text = snippet_text.split("：", 1)[1].strip()
                    snippet = snippet_text[:220] if snippet_text else "(无摘要)"

            seen_urls.add(href)
            results.append(SearchResult(title=title, url=href, snippet=snippet))

        return results
