"""Base class for Playwright-based search modules requiring JS rendering and cookies."""

from __future__ import annotations

import os
from abc import abstractmethod
from pathlib import Path

from diting.models import SearchResult
from diting.modules.base import BaseSearchModule

_USER_AGENT = (
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)
_GOTO_TIMEOUT_MS = 30_000


def _parse_cookie_header(header: str, domain: str) -> list[dict]:
    """Parse a raw cookie header string into Playwright cookie dicts."""
    cookies: list[dict] = []
    for part in header.split("; "):
        if "=" not in part:
            continue
        name, value = part.split("=", 1)
        cookies.append({
            "name": name,
            "value": value,
            "domain": domain,
            "path": "/",
            "secure": True,
            "sameSite": "None",
        })
    return cookies


class PlaywrightSearchModule(BaseSearchModule):
    """Base class for search modules that need a headless browser.

    Manages Playwright browser lifecycle, cookie loading from storage
    state files or environment variables, and page navigation with
    configurable JS wait times.

    Subclasses must implement:
    - :meth:`_build_search_url` — construct the search URL for a query
    - :meth:`_parse_results` — parse HTML into SearchResult list
    - :attr:`_cookie_domain` — domain for cookie injection (e.g. ``.x.com``)
    - :attr:`_cookie_env` — env var name for cookie header string
    - :attr:`_js_wait_ms` — milliseconds to wait for JS rendering
    - :attr:`_storage_state_path` — path to Playwright storage state JSON
    """

    def __init__(
        self,
        name: str,
        *,
        cookie: str = "",
        cookie_domain: str,
        cookie_env: str,
        js_wait_ms: int,
        storage_state_path: str,
        timeout: int = 45,
    ) -> None:
        super().__init__(name=name, timeout=timeout)
        self._cookie = cookie
        self._cookie_domain = cookie_domain
        self._cookie_env = cookie_env
        self._js_wait_ms = js_wait_ms
        self._storage_state_path = Path(storage_state_path)
        self._playwright = None
        self._browser = None
        self._context = None

    async def _ensure_browser(self) -> None:
        """Lazily initialize Playwright browser and context on first use."""
        if self._browser is not None:
            return

        from playwright.async_api import async_playwright

        self._playwright = await async_playwright().start()
        self._browser = await self._playwright.chromium.launch(headless=True)

        # Try storage state file first.
        if self._storage_state_path.exists():
            self._logger.info("Loading storage state from %s", self._storage_state_path)
            self._context = await self._browser.new_context(
                locale="zh-CN",
                user_agent=_USER_AGENT,
                storage_state=str(self._storage_state_path),
            )
            return

        # Fall back to cookie header (constructor arg → env var).
        self._context = await self._browser.new_context(
            locale="zh-CN",
            user_agent=_USER_AGENT,
        )
        cookie_header = self._cookie or os.environ.get(self._cookie_env, "").strip()
        if cookie_header:
            cookies = _parse_cookie_header(cookie_header, self._cookie_domain)
            self._logger.info("Injecting %d cookies from header", len(cookies))
            await self._context.add_cookies(cookies)
        else:
            self._logger.warning("No cookies available for %s — may hit auth wall", self._name)

    async def _execute(self, query: str) -> list[SearchResult]:
        """Navigate to search page, wait for JS, parse results."""
        await self._ensure_browser()

        url = self._build_search_url(query)
        self._logger.debug("Navigating to %s", url)

        page = await self._context.new_page()
        try:
            await page.goto(url, wait_until="domcontentloaded", timeout=_GOTO_TIMEOUT_MS)
            await page.wait_for_timeout(self._js_wait_ms)
            html = await page.content()
        finally:
            await page.close()

        # Persist storage state for cookie refresh.
        if self._storage_state_path.parent.exists():
            try:
                await self._context.storage_state(path=str(self._storage_state_path))
            except Exception as exc:
                self._logger.debug("Failed to persist storage state: %s", exc)

        results = self._parse_results(html)
        self._logger.debug("%s returned %d results", self._name, len(results))
        return results

    @abstractmethod
    def _build_search_url(self, query: str) -> str:
        """Construct the full search URL for *query*."""

    @abstractmethod
    def _parse_results(self, html: str) -> list[SearchResult]:
        """Parse rendered HTML into a list of SearchResult."""

    async def close(self) -> None:
        """Shut down browser and Playwright."""
        if self._browser:
            await self._browser.close()
            self._browser = None
        if self._playwright:
            await self._playwright.stop()
            self._playwright = None
        self._context = None
