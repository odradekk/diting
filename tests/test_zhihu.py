"""Tests for diting.modules.zhihu — ZhihuSearchModule."""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from diting.models import ModuleOutput, SearchResult
from diting.modules.zhihu import ZhihuSearchModule


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _zhihu_html(results: list[tuple[str, str, str]]) -> str:
    """Build minimal Zhihu-like HTML.

    Each tuple is (href, title, snippet_text).
    Uses ContentItem + RichContent-inner/RichText structure to match real Zhihu.
    """
    links = ""
    for href, title, snippet in results:
        links += (
            f'<div class="ContentItem">'
            f'<a href="{href}">{title}</a>'
            f'<div class="RichContent-inner"><span class="RichText">{snippet}</span></div>'
            f'</div>'
        )
    return f"<html><body><main>{links}</main></body></html>"


SAMPLE_RESULTS = [
    ("/question/123/answer/456", "Python 有什么优势", "Python 是一种通用编程语言"),
    ("/p/789", "异步编程入门", "异步编程是现代编程中的重要概念"),
    ("/question/101/answer/202", "Web 开发框架对比", "Django 和 Flask 是最流行的框架"),
]


def _mock_page(html: str) -> MagicMock:
    """Create a mock Playwright page."""
    page = AsyncMock()
    page.goto = AsyncMock()
    page.wait_for_timeout = AsyncMock()
    page.content = AsyncMock(return_value=html)
    page.close = AsyncMock()
    return page


def _setup_module_with_mock_browser(module: ZhihuSearchModule, page: MagicMock) -> None:
    """Inject mock browser/context/playwright into a module."""
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
        module = ZhihuSearchModule()
        assert module.name == "zhihu"

    def test_default_timeout(self) -> None:
        module = ZhihuSearchModule()
        assert module.timeout == 45

    def test_custom_timeout(self) -> None:
        module = ZhihuSearchModule(timeout=30)
        assert module.timeout == 30


# ---------------------------------------------------------------------------
# URL building
# ---------------------------------------------------------------------------


class TestBuildSearchUrl:
    def test_url_contains_query(self) -> None:
        module = ZhihuSearchModule()
        url = module._build_search_url("python 教程")
        assert "q=python" in url
        assert "zhihu.com/search" in url
        assert "type=content" in url


# ---------------------------------------------------------------------------
# Successful search
# ---------------------------------------------------------------------------


class TestSuccessfulSearch:
    async def test_returns_search_results(self) -> None:
        module = ZhihuSearchModule()
        page = _mock_page(_zhihu_html(SAMPLE_RESULTS))
        _setup_module_with_mock_browser(module, page)

        results = await module._execute("python")

        assert len(results) == 3
        assert all(isinstance(r, SearchResult) for r in results)

    async def test_parses_title_url_snippet(self) -> None:
        module = ZhihuSearchModule()
        page = _mock_page(_zhihu_html([
            ("/question/1/answer/2", "Test Title", "Test snippet content"),
        ]))
        _setup_module_with_mock_browser(module, page)

        results = await module._execute("test")

        assert results[0].title == "Test Title"
        assert results[0].url == "https://www.zhihu.com/question/1/answer/2"
        assert "Test snippet" in results[0].snippet

    async def test_prepends_zhihu_domain_to_relative_urls(self) -> None:
        module = ZhihuSearchModule()
        page = _mock_page(_zhihu_html([("/p/123", "Article", "Content")]))
        _setup_module_with_mock_browser(module, page)

        results = await module._execute("query")

        assert results[0].url == "https://www.zhihu.com/p/123"

    async def test_deduplicates_by_url(self) -> None:
        module = ZhihuSearchModule()
        page = _mock_page(_zhihu_html([
            ("/question/1/answer/2", "Title A", "Snippet A"),
            ("/question/1/answer/2", "Title B", "Snippet B"),
        ]))
        _setup_module_with_mock_browser(module, page)

        results = await module._execute("query")

        assert len(results) == 1

    async def test_skips_links_without_title(self) -> None:
        module = ZhihuSearchModule()
        html = (
            '<html><body><main>'
            '<div><a href="/question/1/answer/2"></a></div>'
            '<div><a href="/p/3">Has Title</a><p>Snippet</p></div>'
            '</main></body></html>'
        )
        page = _mock_page(html)
        _setup_module_with_mock_browser(module, page)

        results = await module._execute("query")

        assert len(results) == 1
        assert results[0].title == "Has Title"

    async def test_strips_title_prefix_from_snippet(self) -> None:
        module = ZhihuSearchModule()
        html = (
            '<html><body><main>'
            '<div class="ContentItem">'
            '<a href="/question/1/answer/2">My Title</a>'
            '<div class="RichContent-inner">'
            '<span class="RichText">My Title - actual snippet here</span>'
            '</div></div>'
            '</main></body></html>'
        )
        page = _mock_page(html)
        _setup_module_with_mock_browser(module, page)

        results = await module._execute("query")

        assert "My Title" not in results[0].snippet
        assert "actual snippet here" in results[0].snippet

    async def test_strips_author_prefix_from_snippet(self) -> None:
        module = ZhihuSearchModule()
        html = (
            '<html><body><main>'
            '<div class="ContentItem">'
            '<a href="/question/1/answer/2">Title</a>'
            '<div class="RichContent-inner">'
            '<span class="RichText">某作者：这是实际内容</span>'
            '</div></div>'
            '</main></body></html>'
        )
        page = _mock_page(html)
        _setup_module_with_mock_browser(module, page)

        results = await module._execute("query")

        assert results[0].snippet == "这是实际内容"

    async def test_fallback_snippet_without_richtext(self) -> None:
        module = ZhihuSearchModule()
        html = (
            '<html><body><main>'
            '<div><a href="/question/1/answer/2">Title</a>'
            '<p>Fallback content here</p></div>'
            '</main></body></html>'
        )
        page = _mock_page(html)
        _setup_module_with_mock_browser(module, page)

        results = await module._execute("query")

        assert "Fallback content" in results[0].snippet


# ---------------------------------------------------------------------------
# Empty results
# ---------------------------------------------------------------------------


class TestEmptyResults:
    async def test_no_matching_links_returns_empty(self) -> None:
        module = ZhihuSearchModule()
        page = _mock_page("<html><body><main></main></body></html>")
        _setup_module_with_mock_browser(module, page)

        results = await module._execute("query")

        assert results == []


# ---------------------------------------------------------------------------
# Base class integration
# ---------------------------------------------------------------------------


class TestBaseClassIntegration:
    async def test_search_returns_module_output(self) -> None:
        module = ZhihuSearchModule(timeout=60)
        page = _mock_page(_zhihu_html(SAMPLE_RESULTS[:2]))
        _setup_module_with_mock_browser(module, page)

        output = await module.search("test")

        assert isinstance(output, ModuleOutput)
        assert output.module == "zhihu"
        assert output.error is None
        assert len(output.results) == 2

    async def test_search_handles_timeout(self) -> None:
        import asyncio

        module = ZhihuSearchModule(timeout=1)
        module._playwright = MagicMock()
        module._browser = MagicMock()
        module._context = AsyncMock()

        async def _slow_goto(*args, **kwargs):
            await asyncio.sleep(10)

        page = AsyncMock()
        page.goto = _slow_goto
        page.close = AsyncMock()
        module._context.new_page = AsyncMock(return_value=page)

        output = await module.search("slow")

        assert output.error is not None
        assert output.error.code == "TIMEOUT"


# ---------------------------------------------------------------------------
# Close
# ---------------------------------------------------------------------------


class TestClose:
    async def test_close_cleans_up(self) -> None:
        module = ZhihuSearchModule()
        mock_browser = AsyncMock()
        mock_pw = AsyncMock()
        module._browser = mock_browser
        module._playwright = mock_pw

        await module.close()

        mock_browser.close.assert_awaited_once()
        mock_pw.stop.assert_awaited_once()

    async def test_close_noop_when_not_initialized(self) -> None:
        module = ZhihuSearchModule()
        await module.close()
