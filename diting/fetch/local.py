"""Local page content fetcher — HTTP with browser escalation and dual extraction."""

from __future__ import annotations

import asyncio
from dataclasses import dataclass, field
from typing import Literal
from urllib.parse import urlparse

from curl_cffi.requests import AsyncSession
from curl_cffi.requests.exceptions import RequestException
from markdownify import markdownify
from playwright.async_api import Browser
from readability import Document
from trafilatura import extract as trafilatura_extract

from diting.fetch.tavily import FetchError, FetchResult
from diting.log import get_logger

logger = get_logger("fetch.local")

_USER_AGENT = (
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)
_HEADERS = {
    "User-Agent": _USER_AGENT,
    "Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
    "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
}
_MIN_GOOD_MARKDOWN_LENGTH = 200
_MIN_HTML_LENGTH = 5000
_MIN_BROWSER_FALLBACK_LENGTH = 80


# ---------------------------------------------------------------------------
# Pure helper functions
# ---------------------------------------------------------------------------


def _normalize_url(url: str) -> str:
    if not url.startswith(("http://", "https://")):
        return f"https://{url}"
    return url


def _detect_blockers(html: str, final_url: str) -> list[str]:
    """Detect captcha, login walls, and JS-only shells in the page."""
    haystack = f"{final_url}\n{html}".lower()
    warnings: list[str] = []

    markers: dict[str, tuple[str, ...]] = {
        "captcha": (
            "captcha",
            "showcaptcha",
            "verify you are human",
            "prove your humanity",
        ),
        "login_wall": ("sign in", "log in", "登录后查看", "登录后继续"),
        "js_shell": (
            "enable javascript",
            "javascript is disabled",
            "you need to enable javascript",
        ),
    }

    for warning, terms in markers.items():
        if any(term in haystack for term in terms):
            warnings.append(warning)

    if len(html) < _MIN_HTML_LENGTH:
        warnings.append("short_html")

    return warnings


_HARD_BLOCKERS = {"captcha", "login_wall"}


def _has_hard_blockers(warnings: list[str]) -> bool:
    return bool(_HARD_BLOCKERS.intersection(warnings))


def _should_escalate(markdown: str, warnings: list[str]) -> bool:
    """Decide whether to retry with a browser."""
    if _HARD_BLOCKERS.intersection(warnings):
        return False
    if "js_shell" in warnings:
        return True
    if len(markdown.strip()) < _MIN_BROWSER_FALLBACK_LENGTH:
        return True
    return False


def _extract_with_trafilatura(html: str) -> tuple[str, str]:
    """Extract markdown via trafilatura. Returns (title, markdown)."""
    markdown = trafilatura_extract(
        html,
        output_format="markdown",
        include_links=True,
        include_formatting=True,
        favor_precision=True,
        with_metadata=True,
    )

    if not markdown:
        return "", ""

    title = ""
    lines = [line.strip() for line in markdown.splitlines() if line.strip()]
    for line in lines:
        if line.lower().startswith("title:"):
            title = line.split(":", 1)[1].strip()
            break
    if not title:
        for line in lines:
            if line.startswith("#"):
                title = line.lstrip("# ").strip()
                break

    return title, markdown.strip()


def _extract_with_readability(html: str) -> tuple[str, str]:
    """Extract markdown via readability + markdownify. Returns (title, markdown)."""
    doc = Document(html)
    title = doc.short_title() or ""
    summary_html = doc.summary()
    markdown = markdownify(summary_html, heading_style="ATX", strip=["script", "style"])
    return title.strip(), markdown.strip()


def _extract_markdown(html: str) -> tuple[str, str, list[str]]:
    """Try trafilatura first, fall back to readability. Returns (title, markdown, warnings)."""
    warnings: list[str] = []

    title, markdown = _extract_with_trafilatura(html)
    if len(markdown) >= _MIN_GOOD_MARKDOWN_LENGTH:
        return title, markdown, warnings

    warnings.append("trafilatura_weak")
    try:
        fb_title, fb_markdown = _extract_with_readability(html)
    except Exception:
        warnings.append("readability_error")
        return title, markdown, warnings

    if len(fb_markdown) >= _MIN_GOOD_MARKDOWN_LENGTH:
        return fb_title or title, fb_markdown, warnings

    warnings.append("readability_weak")
    if not markdown and fb_markdown:
        markdown = fb_markdown
    if not title and fb_title:
        title = fb_title

    return title, markdown, warnings


def _score_quality(
    title: str, markdown: str, warnings: list[str],
) -> Literal["high", "medium", "low"]:
    cleaned = markdown.strip()
    if _HARD_BLOCKERS.intersection(warnings):
        return "low"
    if title and len(cleaned) >= 500:
        return "high"
    if title and len(cleaned) >= 150:
        return "medium"
    return "low"


# ---------------------------------------------------------------------------
# Internal result
# ---------------------------------------------------------------------------


@dataclass
class _FetchOutcome:
    url: str
    title: str
    content: str
    method: Literal["http", "browser"]
    quality: Literal["high", "medium", "low"]
    warnings: list[str] = field(default_factory=list)


# ---------------------------------------------------------------------------
# LocalFetcher
# ---------------------------------------------------------------------------


class LocalFetcher:
    """Async local page fetcher with HTTP + browser escalation.

    Uses ``curl_cffi`` for HTTP and an externally-managed Playwright
    ``Browser`` for JS-rendered pages.  Content is extracted via
    trafilatura (primary) with readability + markdownify as fallback.
    """

    def __init__(
        self,
        browser: Browser | None = None,
        http_timeout: int = 20,
        browser_timeout_ms: int = 30_000,
        browser_settle_ms: int = 1200,
    ) -> None:
        self._browser = browser
        self._browser_timeout_ms = browser_timeout_ms
        self._browser_settle_ms = browser_settle_ms
        self._session = AsyncSession(
            headers=_HEADERS,
            impersonate="chrome",
            timeout=http_timeout,
        )

    # ------------------------------------------------------------------
    # Public interface (matches Fetcher protocol)
    # ------------------------------------------------------------------

    async def fetch(self, url: str) -> str:
        """Fetch and extract markdown from *url*. Raises ``FetchError`` on failure."""
        outcome = await self._fetch_single(url)
        if not outcome.content:
            raise FetchError(f"Empty content for {url} (warnings: {outcome.warnings})")
        return outcome.content

    async def fetch_many(self, urls: list[str]) -> list[FetchResult]:
        """Fetch multiple URLs concurrently. Never raises."""
        tasks = [self._fetch_single_safe(u) for u in urls]
        return await asyncio.gather(*tasks)

    async def close(self) -> None:
        """Close the HTTP session. Does NOT close the browser (externally managed)."""
        await self._session.close()

    # ------------------------------------------------------------------
    # Core fetch logic
    # ------------------------------------------------------------------

    async def _fetch_single(self, url: str) -> _FetchOutcome:
        """Fetch one URL with auto-escalation from HTTP to browser."""
        normalized = _normalize_url(url)
        html = ""
        final_url = normalized
        method: Literal["http", "browser"] = "http"
        warnings: list[str] = []

        # --- HTTP attempt ---
        try:
            final_url, html = await self._fetch_via_http(normalized)
        except RequestException as exc:
            warnings.append(f"http_error:{exc.__class__.__name__}")
            logger.debug("HTTP fetch failed for %s: %s", url, exc)
            if self._browser:
                final_url, html = await self._fetch_via_browser(normalized)
                method = "browser"
            else:
                return _FetchOutcome(
                    url=normalized, title="", content="", method="http",
                    quality="low", warnings=warnings,
                )

        # --- Blocker detection ---
        warnings.extend(_detect_blockers(html, final_url))
        if _has_hard_blockers(warnings):
            logger.info("Hard blocker detected for %s: %s", url, warnings)
            title = urlparse(final_url).netloc or final_url
            return _FetchOutcome(
                url=normalized, title=title, content="", method=method,
                quality="low", warnings=warnings,
            )

        # --- Content extraction ---
        title, markdown, extract_warnings = await asyncio.to_thread(
            _extract_markdown, html,
        )
        warnings.extend(extract_warnings)

        # --- Browser escalation on thin content ---
        if _should_escalate(markdown, warnings) and self._browser and method == "http":
            logger.info("Escalating to browser for %s", url)
            final_url, html = await self._fetch_via_browser(normalized)
            method = "browser"

            warnings = _detect_blockers(html, final_url)
            if _has_hard_blockers(warnings):
                title = urlparse(final_url).netloc or final_url
                return _FetchOutcome(
                    url=normalized, title=title, content="", method=method,
                    quality="low", warnings=warnings,
                )

            title, markdown, extract_warnings = await asyncio.to_thread(
                _extract_markdown, html,
            )
            warnings.extend(extract_warnings)

        if not title:
            parsed = urlparse(final_url)
            title = parsed.netloc or parsed.path or final_url

        quality = _score_quality(title, markdown, warnings)
        logger.info(
            "Fetched %s (%s, %s, %d chars)", url, method, quality, len(markdown),
        )
        return _FetchOutcome(
            url=normalized, title=title, content=markdown, method=method,
            quality=quality, warnings=warnings,
        )

    async def _fetch_single_safe(self, url: str) -> FetchResult:
        """Wrap ``_fetch_single`` to never raise, returning a ``FetchResult``."""
        try:
            outcome = await self._fetch_single(url)
            if outcome.content:
                return FetchResult(url=url, content=outcome.content, success=True)
            return FetchResult(
                url=url, content="", success=False,
                error=f"Empty content (warnings: {outcome.warnings})",
            )
        except Exception as exc:
            logger.warning("Fetch failed for %s: %s", url, exc)
            return FetchResult(url=url, content="", success=False, error=str(exc))

    # ------------------------------------------------------------------
    # Transport
    # ------------------------------------------------------------------

    async def _fetch_via_http(self, url: str) -> tuple[str, str]:
        """HTTP fetch via curl_cffi. Returns (final_url, html)."""
        response = await self._session.get(url, allow_redirects=True)
        response.raise_for_status()
        return str(response.url), response.text

    async def _fetch_via_browser(self, url: str) -> tuple[str, str]:
        """Browser fetch via Playwright. Returns (final_url, html).

        Creates an isolated context per request; always cleans up via finally.
        """
        if not self._browser:
            raise FetchError(f"No browser available for {url}")

        context = await self._browser.new_context(
            locale="zh-CN",
            user_agent=_USER_AGENT,
            viewport={"width": 1440, "height": 1024},
            extra_http_headers={"Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8"},
        )
        page = await context.new_page()
        try:
            await page.goto(
                url, wait_until="domcontentloaded",
                timeout=self._browser_timeout_ms,
            )
            await page.wait_for_timeout(self._browser_settle_ms)
            html = await page.content()
            final_url = page.url
            return final_url, html
        finally:
            await page.close()
            await context.close()
