"""X (Twitter) search module via Playwright browser automation."""

from __future__ import annotations

from urllib.parse import quote_plus

from bs4 import BeautifulSoup

from diting.models import SearchResult
from diting.modules.playwright_base import PlaywrightSearchModule, _GOTO_TIMEOUT_MS

_SEARCH_URL = "https://x.com/search"
_LOGIN_WALL_MARKERS = (
    "Log in to X",
    "Sign in to X",
    "登录 X",
    'href="/i/flow/login"',
)
# X (Twitter) uses infinite scroll to load results dynamically.
# Pagination strategy: scroll to bottom repeatedly, parse new results each time.
# Scroll rounds scale with max_results: max(8, max_results // 3).
# Stops when target count reached or no new results appear after a scroll.
_MIN_SCROLL_ROUNDS = 8
_SCROLL_PAUSE_MS = 2500


class XSearchModule(PlaywrightSearchModule):
    """Search module that scrapes X.com search results via Playwright.

    Requires authentication cookies to bypass the login wall.
    Cookies can be provided via the ``X_COOKIE`` environment variable
    (raw header string) or a Playwright storage state file.

    Implements scroll-to-load-more to collect results up to ``max_results``.
    """

    def __init__(self, *, cookie: str = "", timeout: int = 45, max_results: int = 20) -> None:
        super().__init__(
            name="x",
            cookie=cookie,
            cookie_domain=".x.com",
            cookie_env="X_COOKIE",
            js_wait_ms=8000,
            storage_state_path="config/x_storage_state.json",
            timeout=timeout,
            max_results=max_results,
        )

    def _build_search_url(self, query: str) -> str:
        return f"{_SEARCH_URL}?q={quote_plus(query)}&src=typed_query&f=live"

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
        if any(marker in html for marker in _LOGIN_WALL_MARKERS):
            raise RuntimeError("X login wall detected — cookies may be expired")

        soup = BeautifulSoup(html, "html.parser")
        results: list[SearchResult] = []
        seen_urls: set[str] = set()

        for article in soup.select("article"):
            link_tag = article.select_one("a[href*='/status/']")
            text_tag = article.select_one("div[data-testid='tweetText']")
            author_tag = article.select_one('[data-testid="User-Name"] span')

            if not link_tag or not text_tag:
                continue

            snippet = text_tag.get_text(" ", strip=True)
            title = author_tag.get_text(" ", strip=True) if author_tag else snippet[:40]
            href = str(link_tag["href"]) if link_tag.has_attr("href") else ""
            if href.startswith("/"):
                href = f"https://x.com{href}"
            if not href or href in seen_urls:
                continue

            seen_urls.add(href)
            results.append(SearchResult(title=title, url=href, snippet=snippet))

        return results
