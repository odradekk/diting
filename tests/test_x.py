"""Tests for diting.modules.x — XSearchModule."""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from diting.models import ModuleOutput, SearchResult
from diting.modules.x import XSearchModule


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _x_html(tweets: list[tuple[str, str, str]]) -> str:
    """Build minimal X-like HTML with tweet articles.

    Each tuple is (status_path, author_name, tweet_text).
    """
    articles = ""
    for path, author, text in tweets:
        articles += (
            f'<article>'
            f'<a href="{path}">link</a>'
            f'<div data-testid="User-Name"><span>{author}</span></div>'
            f'<div data-testid="tweetText">{text}</div>'
            f'</article>'
        )
    return f"<html><body>{articles}</body></html>"


LOGIN_WALL_HTML = '<html><body>Log in to X<a href="/i/flow/login">Login</a></body></html>'

SAMPLE_TWEETS = [
    ("/user1/status/123", "Alice", "First tweet about Python"),
    ("/user2/status/456", "Bob", "Second tweet about async"),
    ("/user3/status/789", "Charlie", "Third tweet about web scraping"),
]


def _mock_page(html: str) -> MagicMock:
    """Create a mock Playwright page."""
    page = AsyncMock()
    page.goto = AsyncMock()
    page.wait_for_timeout = AsyncMock()
    page.content = AsyncMock(return_value=html)
    page.evaluate = AsyncMock()
    page.close = AsyncMock()
    return page


def _setup_module_with_mock_browser(module: XSearchModule, page: MagicMock) -> None:
    """Inject mock browser/context/playwright into a module to skip real launch."""
    module._playwright = MagicMock()
    module._browser = MagicMock()
    module._context = AsyncMock()
    module._context.new_page = AsyncMock(return_value=page)
    module._context.storage_state = AsyncMock()


# ---------------------------------------------------------------------------
# Constructor
# ---------------------------------------------------------------------------


class TestConstructor:
    def test_module_name(self) -> None:
        module = XSearchModule()
        assert module.name == "x"

    def test_default_timeout(self) -> None:
        module = XSearchModule()
        assert module.timeout == 45

    def test_custom_timeout(self) -> None:
        module = XSearchModule(timeout=30)
        assert module.timeout == 30


# ---------------------------------------------------------------------------
# URL building
# ---------------------------------------------------------------------------


class TestBuildSearchUrl:
    def test_url_contains_query(self) -> None:
        module = XSearchModule()
        url = module._build_search_url("python async")
        assert "q=python+async" in url
        assert "x.com/search" in url
        assert "f=live" in url


# ---------------------------------------------------------------------------
# Successful search
# ---------------------------------------------------------------------------


class TestSuccessfulSearch:
    async def test_returns_search_results(self) -> None:
        module = XSearchModule()
        page = _mock_page(_x_html(SAMPLE_TWEETS))
        _setup_module_with_mock_browser(module, page)

        results = await module._execute("python")

        assert len(results) == 3
        assert all(isinstance(r, SearchResult) for r in results)

    async def test_parses_author_and_url(self) -> None:
        module = XSearchModule()
        page = _mock_page(_x_html([("/user/status/111", "TestAuthor", "Hello world")]))
        _setup_module_with_mock_browser(module, page)

        results = await module._execute("hello")

        assert results[0].title == "TestAuthor"
        assert results[0].url == "https://x.com/user/status/111"
        assert results[0].snippet == "Hello world"

    async def test_title_falls_back_to_snippet_without_author(self) -> None:
        module = XSearchModule()
        html = (
            "<html><body>"
            "<article><a href='/u/status/1'>link</a>"
            '<div data-testid="tweetText">Some tweet text here</div></article>'
            "</body></html>"
        )
        page = _mock_page(html)
        _setup_module_with_mock_browser(module, page)

        results = await module._execute("query")

        assert results[0].title == "Some tweet text here"

    async def test_deduplicates_by_url(self) -> None:
        module = XSearchModule()
        page = _mock_page(_x_html([
            ("/user/status/1", "Alice", "First"),
            ("/user/status/1", "Alice", "Duplicate"),
        ]))
        _setup_module_with_mock_browser(module, page)

        results = await module._execute("query")

        assert len(results) == 1

    async def test_skips_articles_without_status_link(self) -> None:
        module = XSearchModule()
        html = (
            "<html><body>"
            "<article><a href='/other'>link</a>"
            '<div data-testid="tweetText">No status</div></article>'
            "<article><a href='/u/status/1'>link</a>"
            '<div data-testid="User-Name"><span>Author</span></div>'
            '<div data-testid="tweetText">Has status</div></article>'
            "</body></html>"
        )
        page = _mock_page(html)
        _setup_module_with_mock_browser(module, page)

        results = await module._execute("query")

        assert len(results) == 1
        assert results[0].title == "Author"

    async def test_skips_articles_without_tweet_text(self) -> None:
        module = XSearchModule()
        html = (
            "<html><body>"
            "<article><a href='/u/status/1'>link</a></article>"
            "</body></html>"
        )
        page = _mock_page(html)
        _setup_module_with_mock_browser(module, page)

        results = await module._execute("query")

        assert results == []


# ---------------------------------------------------------------------------
# Login wall detection
# ---------------------------------------------------------------------------


class TestLoginWall:
    async def test_raises_on_login_wall(self) -> None:
        module = XSearchModule()
        page = _mock_page(LOGIN_WALL_HTML)
        _setup_module_with_mock_browser(module, page)

        with pytest.raises(RuntimeError, match="login wall"):
            await module._execute("query")


# ---------------------------------------------------------------------------
# Empty results
# ---------------------------------------------------------------------------


class TestEmptyResults:
    async def test_no_articles_returns_empty(self) -> None:
        module = XSearchModule()
        page = _mock_page("<html><body></body></html>")
        _setup_module_with_mock_browser(module, page)

        results = await module._execute("query")

        assert results == []


# ---------------------------------------------------------------------------
# Base class integration
# ---------------------------------------------------------------------------


class TestBaseClassIntegration:
    async def test_search_returns_module_output(self) -> None:
        module = XSearchModule(timeout=60)
        page = _mock_page(_x_html(SAMPLE_TWEETS[:2]))
        _setup_module_with_mock_browser(module, page)

        output = await module.search("test")

        assert isinstance(output, ModuleOutput)
        assert output.module == "x"
        assert output.error is None
        assert len(output.results) == 2

    async def test_login_wall_becomes_module_error(self) -> None:
        module = XSearchModule(timeout=60)
        page = _mock_page(LOGIN_WALL_HTML)
        _setup_module_with_mock_browser(module, page)

        output = await module.search("query")

        assert output.module == "x"
        assert output.results == []
        assert output.error is not None
        assert output.error.code == "ERROR"
        assert "login wall" in output.error.message


# ---------------------------------------------------------------------------
# Close
# ---------------------------------------------------------------------------


class TestClose:
    async def test_close_cleans_up_browser(self) -> None:
        module = XSearchModule()
        mock_browser = AsyncMock()
        mock_pw = AsyncMock()
        module._browser = mock_browser
        module._playwright = mock_pw

        await module.close()

        mock_browser.close.assert_awaited_once()
        mock_pw.stop.assert_awaited_once()
        assert module._browser is None
        assert module._playwright is None

    async def test_close_noop_when_not_initialized(self) -> None:
        module = XSearchModule()
        await module.close()  # should not raise
