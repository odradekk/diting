"""Tests for diting.modules.baidu — BaiduSearchModule."""

from __future__ import annotations

import json
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from diting.models import ModuleOutput, SearchResult
from diting.modules.baidu import BaiduSearchModule, _extract_url


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _make_response(html: str, status_code: int = 200) -> MagicMock:
    """Build a mock curl_cffi response with the given HTML body."""
    resp = MagicMock()
    resp.status_code = status_code
    resp.text = html
    resp.raise_for_status = MagicMock()
    if status_code >= 400:
        resp.raise_for_status.side_effect = Exception(f"HTTP {status_code}")
    return resp


def _baidu_html(results: list[dict]) -> str:
    """Build minimal Baidu-like HTML with result items.

    Each dict may have: title, url, snippet, mu, data_tools, data_landurl, href.
    """
    items = ""
    for r in results:
        attrs = 'class="result"'
        if "mu" in r:
            attrs += f' mu="{r["mu"]}"'
        if "data_tools" in r:
            escaped = json.dumps(r["data_tools"]).replace('"', "&quot;")
            attrs += f' data-tools="{escaped}"'

        href_attr = ""
        if "href" in r:
            href_attr = f' href="{r["href"]}"'
        landurl_attr = ""
        if "data_landurl" in r:
            landurl_attr = f' data-landurl="{r["data_landurl"]}"'

        title_html = ""
        if "title" in r:
            title_html = f"<h3><a{href_attr}{landurl_attr}>{r['title']}</a></h3>"

        snippet_html = ""
        if "snippet" in r:
            snippet_html = f'<div class="c-line-clamp">{r["snippet"]}</div>'

        items += f"<div {attrs}>{title_html}{snippet_html}</div>"

    return f'<html><body><div id="content_left">{items}</div></body></html>'


SAMPLE_RESULTS = [
    {"title": "Python 官方文档", "mu": "https://docs.python.org", "snippet": "Python 编程语言"},
    {"title": "菜鸟教程", "mu": "https://www.runoob.com/python", "snippet": "Python 入门教程"},
    {"title": "知乎讨论", "mu": "https://www.zhihu.com/topic/python", "snippet": "关于 Python 的讨论"},
]


# ---------------------------------------------------------------------------
# URL extraction
# ---------------------------------------------------------------------------


class TestExtractUrl:
    def test_mu_attribute(self) -> None:
        from bs4 import BeautifulSoup

        html = '<div class="result" mu="https://example.com"><h3><a>Title</a></h3></div>'
        soup = BeautifulSoup(html, "html.parser")
        item = soup.select_one(".result")
        title_tag = item.select_one("h3 a")
        assert _extract_url(item, title_tag) == "https://example.com"

    def test_data_tools_url(self) -> None:
        from bs4 import BeautifulSoup

        tools = json.dumps({"url": "https://from-tools.com"}).replace('"', "&quot;")
        html = f'<div class="result" data-tools="{tools}"><h3><a>Title</a></h3></div>'
        soup = BeautifulSoup(html, "html.parser")
        item = soup.select_one(".result")
        title_tag = item.select_one("h3 a")
        assert _extract_url(item, title_tag) == "https://from-tools.com"

    def test_data_tools_title_url(self) -> None:
        from bs4 import BeautifulSoup

        tools = json.dumps({"titleUrl": "https://title-url.com"}).replace('"', "&quot;")
        html = f'<div class="result" data-tools="{tools}"><h3><a>Title</a></h3></div>'
        soup = BeautifulSoup(html, "html.parser")
        item = soup.select_one(".result")
        title_tag = item.select_one("h3 a")
        assert _extract_url(item, title_tag) == "https://title-url.com"

    def test_data_landurl(self) -> None:
        from bs4 import BeautifulSoup

        html = '<div class="result"><h3><a data-landurl="https://land.com">Title</a></h3></div>'
        soup = BeautifulSoup(html, "html.parser")
        item = soup.select_one(".result")
        title_tag = item.select_one("h3 a")
        assert _extract_url(item, title_tag) == "https://land.com"

    def test_href_fallback(self) -> None:
        from bs4 import BeautifulSoup

        html = '<div class="result"><h3><a href="https://baidu.com/link?url=xxx">Title</a></h3></div>'
        soup = BeautifulSoup(html, "html.parser")
        item = soup.select_one(".result")
        title_tag = item.select_one("h3 a")
        assert _extract_url(item, title_tag) == "https://baidu.com/link?url=xxx"

    def test_no_url_returns_empty(self) -> None:
        from bs4 import BeautifulSoup

        html = '<div class="result"><h3><a>Title</a></h3></div>'
        soup = BeautifulSoup(html, "html.parser")
        item = soup.select_one(".result")
        title_tag = item.select_one("h3 a")
        assert _extract_url(item, title_tag) == ""

    def test_mu_takes_priority_over_data_tools(self) -> None:
        from bs4 import BeautifulSoup

        tools = json.dumps({"url": "https://tools.com"}).replace('"', "&quot;")
        html = f'<div class="result" mu="https://mu.com" data-tools="{tools}"><h3><a href="https://href.com">Title</a></h3></div>'
        soup = BeautifulSoup(html, "html.parser")
        item = soup.select_one(".result")
        title_tag = item.select_one("h3 a")
        assert _extract_url(item, title_tag) == "https://mu.com"


# ---------------------------------------------------------------------------
# Constructor
# ---------------------------------------------------------------------------


class TestConstructor:
    def test_module_name_is_baidu(self) -> None:
        module = BaiduSearchModule()
        assert module.name == "baidu"

    def test_default_timeout(self) -> None:
        module = BaiduSearchModule()
        assert module.timeout == 15

    def test_custom_timeout(self) -> None:
        module = BaiduSearchModule(timeout=10)
        assert module.timeout == 10


# ---------------------------------------------------------------------------
# Successful search
# ---------------------------------------------------------------------------


class TestSuccessfulSearch:
    async def test_returns_search_results(self) -> None:
        module = BaiduSearchModule()
        html = _baidu_html(SAMPLE_RESULTS)
        mock_resp = _make_response(html)

        with patch.object(module._session, "get", new_callable=AsyncMock, return_value=mock_resp):
            results = await module._execute("python")

        assert len(results) == 3
        assert all(isinstance(r, SearchResult) for r in results)
        await module.close()

    async def test_parses_title_url_snippet(self) -> None:
        module = BaiduSearchModule()
        html = _baidu_html([{"title": "Example", "mu": "https://example.com", "snippet": "A snippet"}])
        mock_resp = _make_response(html)

        with patch.object(module._session, "get", new_callable=AsyncMock, return_value=mock_resp):
            results = await module._execute("query")

        assert results[0].title == "Example"
        assert results[0].url == "https://example.com"
        assert results[0].snippet == "A snippet"
        await module.close()

    async def test_skips_items_without_title_tag(self) -> None:
        module = BaiduSearchModule()
        html = _baidu_html([
            {"mu": "https://no-title.com", "snippet": "No title here"},
            {"title": "OK", "mu": "https://ok.com", "snippet": "Has title"},
        ])
        mock_resp = _make_response(html)

        with patch.object(module._session, "get", new_callable=AsyncMock, return_value=mock_resp):
            results = await module._execute("query")

        assert len(results) == 1
        assert results[0].title == "OK"
        await module.close()

    async def test_skips_items_without_url(self) -> None:
        module = BaiduSearchModule()
        # No mu, no data-tools, no href → empty URL → skipped
        html = _baidu_html([{"title": "No URL", "snippet": "Missing URL"}])
        mock_resp = _make_response(html)

        with patch.object(module._session, "get", new_callable=AsyncMock, return_value=mock_resp):
            results = await module._execute("query")

        assert results == []
        await module.close()

    async def test_missing_snippet_yields_empty_string(self) -> None:
        module = BaiduSearchModule()
        html = _baidu_html([{"title": "Title", "mu": "https://example.com"}])
        mock_resp = _make_response(html)

        with patch.object(module._session, "get", new_callable=AsyncMock, return_value=mock_resp):
            results = await module._execute("query")

        assert len(results) == 1
        assert results[0].snippet == ""
        await module.close()


# ---------------------------------------------------------------------------
# Empty results
# ---------------------------------------------------------------------------


class TestEmptyResults:
    async def test_no_result_items_returns_empty(self) -> None:
        module = BaiduSearchModule()
        mock_resp = _make_response('<html><body><div id="content_left"></div></body></html>')

        with patch.object(module._session, "get", new_callable=AsyncMock, return_value=mock_resp):
            results = await module._execute("query")

        assert results == []
        await module.close()


# ---------------------------------------------------------------------------
# HTTP errors
# ---------------------------------------------------------------------------


class TestHTTPErrors:
    async def test_http_error_raises(self) -> None:
        module = BaiduSearchModule()
        mock_resp = _make_response("", status_code=500)

        with patch.object(module._session, "get", new_callable=AsyncMock, return_value=mock_resp):
            with pytest.raises(Exception, match="500"):
                await module._execute("query")

        await module.close()


# ---------------------------------------------------------------------------
# Base class integration
# ---------------------------------------------------------------------------


class TestBaseClassIntegration:
    async def test_search_returns_module_output(self) -> None:
        module = BaiduSearchModule(timeout=5)
        html = _baidu_html(SAMPLE_RESULTS[:2])
        mock_resp = _make_response(html)

        with patch.object(module._session, "get", new_callable=AsyncMock, return_value=mock_resp):
            output = await module.search("test")

        assert isinstance(output, ModuleOutput)
        assert output.module == "baidu"
        assert output.error is None
        assert len(output.results) == 2
        await module.close()

    async def test_search_wraps_error_in_module_output(self) -> None:
        module = BaiduSearchModule(timeout=5)
        mock_resp = _make_response("", status_code=500)

        with patch.object(module._session, "get", new_callable=AsyncMock, return_value=mock_resp):
            output = await module.search("query")

        assert output.module == "baidu"
        assert output.results == []
        assert output.error is not None
        assert output.error.code == "ERROR"
        await module.close()

    async def test_search_handles_timeout(self) -> None:
        import asyncio

        module = BaiduSearchModule(timeout=1)

        async def _slow(*args, **kwargs):
            await asyncio.sleep(10)

        with patch.object(module._session, "get", side_effect=_slow):
            output = await module.search("slow")

        assert output.error is not None
        assert output.error.code == "TIMEOUT"
        assert output.error.retryable is True
        await module.close()


# ---------------------------------------------------------------------------
# Close
# ---------------------------------------------------------------------------


class TestClose:
    async def test_close_calls_session_close(self) -> None:
        module = BaiduSearchModule()

        with patch.object(module._session, "close", new_callable=AsyncMock) as mock_close:
            await module.close()

        mock_close.assert_awaited_once()
