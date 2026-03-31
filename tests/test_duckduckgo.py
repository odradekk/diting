"""Tests for diting.modules.duckduckgo — DuckDuckGoSearchModule."""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock, patch
from urllib.parse import quote

import pytest

from diting.models import ModuleOutput, SearchResult
from diting.modules.duckduckgo import DuckDuckGoSearchModule, _unwrap_ddg_redirect


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


def _ddg_html(results: list[tuple[str, str, str]]) -> str:
    """Build minimal DuckDuckGo-like HTML with result items."""
    items = ""
    for title, url, snippet in results:
        items += (
            f'<div class="result">'
            f'<h2 class="result__title"><a href="{url}">{title}</a></h2>'
            f'<a class="result__snippet">{snippet}</a>'
            f"</div>"
        )
    return f"<html><body>{items}</body></html>"


SAMPLE_RESULTS = [
    ("Python Docs", "https://docs.python.org", "Official Python documentation"),
    ("Real Python", "https://realpython.com", "Python tutorials"),
    ("PEP 8", "https://peps.python.org/pep-0008", "Style guide for Python"),
]


# ---------------------------------------------------------------------------
# URL unwrapping
# ---------------------------------------------------------------------------


class TestUnwrapDdgRedirect:
    def test_unwraps_tracking_redirect(self) -> None:
        target = "https://example.com/page?a=1"
        redirect = f"//duckduckgo.com/l/?uddg={quote(target, safe='')}&rut=abc"
        assert _unwrap_ddg_redirect(redirect) == target

    def test_returns_plain_url_unchanged(self) -> None:
        url = "https://example.com/page"
        assert _unwrap_ddg_redirect(url) == url

    def test_returns_url_without_uddg_param_unchanged(self) -> None:
        url = "//duckduckgo.com/l/?other=value"
        assert _unwrap_ddg_redirect(url) == url


# ---------------------------------------------------------------------------
# Constructor
# ---------------------------------------------------------------------------


class TestConstructor:
    def test_module_name_is_duckduckgo(self) -> None:
        module = DuckDuckGoSearchModule()
        assert module.name == "duckduckgo"

    def test_default_timeout(self) -> None:
        module = DuckDuckGoSearchModule()
        assert module.timeout == 15

    def test_custom_timeout(self) -> None:
        module = DuckDuckGoSearchModule(timeout=20)
        assert module.timeout == 20


# ---------------------------------------------------------------------------
# Successful search
# ---------------------------------------------------------------------------


class TestSuccessfulSearch:
    async def test_returns_search_results(self) -> None:
        module = DuckDuckGoSearchModule()
        html = _ddg_html(SAMPLE_RESULTS)
        mock_resp = _make_response(html)

        with patch.object(module._session, "get", new_callable=AsyncMock, return_value=mock_resp):
            results = await module._execute("python")

        assert len(results) == 3
        assert all(isinstance(r, SearchResult) for r in results)
        await module.close()

    async def test_parses_title_url_snippet(self) -> None:
        module = DuckDuckGoSearchModule()
        html = _ddg_html([("Example", "https://example.com", "A snippet")])
        mock_resp = _make_response(html)

        with patch.object(module._session, "get", new_callable=AsyncMock, return_value=mock_resp):
            results = await module._execute("query")

        assert results[0].title == "Example"
        assert results[0].url == "https://example.com"
        assert results[0].snippet == "A snippet"
        await module.close()

    async def test_unwraps_ddg_redirect_urls(self) -> None:
        module = DuckDuckGoSearchModule()
        target = "https://example.com/real"
        redirect = f"//duckduckgo.com/l/?uddg={quote(target, safe='')}"
        html = _ddg_html([("Title", redirect, "Snippet")])
        mock_resp = _make_response(html)

        with patch.object(module._session, "get", new_callable=AsyncMock, return_value=mock_resp):
            results = await module._execute("query")

        assert results[0].url == target
        await module.close()

    async def test_skips_items_without_title_tag(self) -> None:
        module = DuckDuckGoSearchModule()
        html = (
            '<html><body>'
            '<div class="result"><a class="result__snippet">No title</a></div>'
            '<div class="result"><h2 class="result__title">'
            '<a href="https://ok.com">OK</a></h2>'
            '<a class="result__snippet">Has title</a></div>'
            '</body></html>'
        )
        mock_resp = _make_response(html)

        with patch.object(module._session, "get", new_callable=AsyncMock, return_value=mock_resp):
            results = await module._execute("query")

        assert len(results) == 1
        assert results[0].title == "OK"
        await module.close()

    async def test_skips_items_without_href(self) -> None:
        module = DuckDuckGoSearchModule()
        html = (
            '<html><body>'
            '<div class="result"><h2 class="result__title"><a>No Href</a></h2>'
            '<a class="result__snippet">Snippet</a></div>'
            '</body></html>'
        )
        mock_resp = _make_response(html)

        with patch.object(module._session, "get", new_callable=AsyncMock, return_value=mock_resp):
            results = await module._execute("query")

        assert results == []
        await module.close()


# ---------------------------------------------------------------------------
# Empty results
# ---------------------------------------------------------------------------


class TestEmptyResults:
    async def test_no_result_divs_returns_empty(self) -> None:
        module = DuckDuckGoSearchModule()
        mock_resp = _make_response("<html><body></body></html>")

        with patch.object(module._session, "get", new_callable=AsyncMock, return_value=mock_resp):
            results = await module._execute("query")

        assert results == []
        await module.close()


# ---------------------------------------------------------------------------
# HTTP errors
# ---------------------------------------------------------------------------


class TestHTTPErrors:
    async def test_http_error_raises(self) -> None:
        module = DuckDuckGoSearchModule()
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
        module = DuckDuckGoSearchModule(timeout=5)
        html = _ddg_html(SAMPLE_RESULTS[:2])
        mock_resp = _make_response(html)

        with patch.object(module._session, "get", new_callable=AsyncMock, return_value=mock_resp):
            output = await module.search("test")

        assert isinstance(output, ModuleOutput)
        assert output.module == "duckduckgo"
        assert output.error is None
        assert len(output.results) == 2
        await module.close()

    async def test_search_wraps_error_in_module_output(self) -> None:
        module = DuckDuckGoSearchModule(timeout=5)
        mock_resp = _make_response("", status_code=500)

        with patch.object(module._session, "get", new_callable=AsyncMock, return_value=mock_resp):
            output = await module.search("query")

        assert output.module == "duckduckgo"
        assert output.results == []
        assert output.error is not None
        assert output.error.code == "ERROR"
        await module.close()

    async def test_search_handles_timeout(self) -> None:
        import asyncio

        module = DuckDuckGoSearchModule(timeout=1)

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
        module = DuckDuckGoSearchModule()

        with patch.object(module._session, "close", new_callable=AsyncMock) as mock_close:
            await module.close()

        mock_close.assert_awaited_once()
