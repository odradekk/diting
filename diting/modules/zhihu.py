"""Zhihu search module via Playwright browser automation."""

from __future__ import annotations

from urllib.parse import quote_plus

from bs4 import BeautifulSoup

from diting.models import SearchResult
from diting.modules.playwright_base import PlaywrightSearchModule, _GOTO_TIMEOUT_MS

_SEARCH_URL = "https://www.zhihu.com/search"
# Zhihu uses infinite scroll to load search results dynamically.
# Pagination strategy: scroll to bottom repeatedly, parse new results each time.
# Scroll rounds scale with max_results: max(5, max_results // 3).
# Stops when target count reached or no new results appear after a scroll.
_MIN_SCROLL_ROUNDS = 5
_SCROLL_PAUSE_MS = 2000


class ZhihuSearchModule(PlaywrightSearchModule):
    """Search module that scrapes Zhihu search results via Playwright.

    Requires authentication cookies to access search results.
    Cookies can be provided via the ``ZHIHU_COOKIE`` environment variable
    (raw header string) or a Playwright storage state file.

    Implements scroll-to-load-more to collect results up to ``max_results``.
    """

    def __init__(self, *, cookie: str = "", timeout: int = 45, max_results: int = 20) -> None:
        super().__init__(
            name="zhihu",
            cookie=cookie,
            cookie_domain=".zhihu.com",
            cookie_env="ZHIHU_COOKIE",
            js_wait_ms=6000,
            storage_state_path="config/zhihu_storage_state.json",
            timeout=timeout,
            max_results=max_results,
        )

    def _build_search_url(self, query: str) -> str:
        return f"{_SEARCH_URL}?q={quote_plus(query)}&type=content"

    async def _execute(self, query: str) -> list[SearchResult]:
        """Navigate, wait for JS, scroll to load more, then parse."""
        await self._ensure_browser()

        url = self._build_search_url(query)
        self._logger.debug("Navigating to %s (max_results=%d)", url, self._max_results)

        max_scroll_rounds = max(_MIN_SCROLL_ROUNDS, self._max_results // 3)

        page = await self._context.new_page()
        try:
            await page.goto(url, wait_until="domcontentloaded", timeout=_GOTO_TIMEOUT_MS)
            await page.wait_for_timeout(self._js_wait_ms)

            html = await page.content()
            results = self._parse_results(html)

            for _ in range(max_scroll_rounds):
                if len(results) >= self._max_results:
                    break
                previous_count = len(results)
                await page.evaluate("window.scrollTo(0, document.body.scrollHeight)")
                await page.wait_for_timeout(_SCROLL_PAUSE_MS)
                html = await page.content()
                results = self._parse_results(html)
                if len(results) <= previous_count:
                    break
        finally:
            await page.close()

        # Persist storage state for cookie refresh.
        if self._storage_state_path.parent.exists():
            try:
                await self._context.storage_state(path=str(self._storage_state_path))
            except Exception as exc:
                self._logger.debug("Failed to persist storage state: %s", exc)

        self._logger.debug("%s returned %d results", self._name, len(results))
        return results[:self._max_results]

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
