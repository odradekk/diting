"""Tests for diting.modules.bing — BingSearchModule."""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from diting.models import ModuleOutput, SearchResult
from diting.modules.bing import BingSearchModule


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


def _bing_html(results: list[tuple[str, str, str]]) -> str:
    """Build minimal Bing-like HTML with organic result items."""
    items = ""
    for title, url, snippet in results:
        items += (
            f'<li class="b_algo">'
            f'<h2><a href="{url}">{title}</a></h2>'
            f'<div class="b_caption"><p>{snippet}</p></div>'
            f"</li>"
        )
    return f"<html><body><ol>{items}</ol></body></html>"


SAMPLE_RESULTS = [
    ("Python Docs", "https://docs.python.org", "Official Python documentation"),
    ("Real Python", "https://realpython.com", "Python tutorials"),
    ("PEP 8", "https://peps.python.org/pep-0008", "Style guide for Python"),
]


# ---------------------------------------------------------------------------
# Constructor
# ---------------------------------------------------------------------------


class TestConstructor:
    def test_module_name_is_bing(self) -> None:
        module = BingSearchModule()
        assert module.name == "bing"

    def test_default_timeout(self) -> None:
        module = BingSearchModule()
        assert module.timeout == 15

    def test_custom_timeout(self) -> None:
        module = BingSearchModule(timeout=10)
        assert module.timeout == 10


# ---------------------------------------------------------------------------
# Successful search
# ---------------------------------------------------------------------------


class TestSuccessfulSearch:
    async def test_returns_search_results(self) -> None:
        module = BingSearchModule()
        html = _bing_html(SAMPLE_RESULTS)
        mock_resp = _make_response(html)

        with patch.object(module._session, "get", new_callable=AsyncMock, return_value=mock_resp):
            results = await module._execute("python")

        assert len(results) == 3
        assert all(isinstance(r, SearchResult) for r in results)
        await module.close()

    async def test_parses_title_url_snippet(self) -> None:
        module = BingSearchModule()
        html = _bing_html([("Example", "https://example.com", "A snippet")])
        mock_resp = _make_response(html)

        with patch.object(module._session, "get", new_callable=AsyncMock, return_value=mock_resp):
            results = await module._execute("query")

        assert results[0].title == "Example"
        assert results[0].url == "https://example.com"
        assert results[0].snippet == "A snippet"
        await module.close()

    async def test_skips_items_without_title_tag(self) -> None:
        module = BingSearchModule()
        html = (
            '<html><body><ol>'
            '<li class="b_algo"><div class="b_caption"><p>No title</p></div></li>'
            '<li class="b_algo"><h2><a href="https://ok.com">OK</a></h2>'
            '<div class="b_caption"><p>Has title</p></div></li>'
            '</ol></body></html>'
        )
        mock_resp = _make_response(html)

        with patch.object(module._session, "get", new_callable=AsyncMock, return_value=mock_resp):
            results = await module._execute("query")

        assert len(results) == 1
        assert results[0].title == "OK"
        await module.close()

    async def test_skips_items_without_href(self) -> None:
        module = BingSearchModule()
        html = (
            '<html><body><ol>'
            '<li class="b_algo"><h2><a>No Href</a></h2>'
            '<div class="b_caption"><p>Missing href</p></div></li>'
            '</ol></body></html>'
        )
        mock_resp = _make_response(html)

        with patch.object(module._session, "get", new_callable=AsyncMock, return_value=mock_resp):
            results = await module._execute("query")

        assert results == []
        await module.close()

    async def test_missing_snippet_yields_empty_string(self) -> None:
        module = BingSearchModule()
        html = (
            '<html><body><ol>'
            '<li class="b_algo"><h2><a href="https://example.com">Title</a></h2></li>'
            '</ol></body></html>'
        )
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
    async def test_no_b_algo_items_returns_empty(self) -> None:
        module = BingSearchModule()
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
        module = BingSearchModule()
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
        module = BingSearchModule(timeout=5)
        html = _bing_html(SAMPLE_RESULTS[:2])
        mock_resp = _make_response(html)

        with patch.object(module._session, "get", new_callable=AsyncMock, return_value=mock_resp):
            output = await module.search("test")

        assert isinstance(output, ModuleOutput)
        assert output.module == "bing"
        assert output.error is None
        assert len(output.results) == 2
        await module.close()

    async def test_search_wraps_error_in_module_output(self) -> None:
        module = BingSearchModule(timeout=5)
        mock_resp = _make_response("", status_code=500)

        with patch.object(module._session, "get", new_callable=AsyncMock, return_value=mock_resp):
            output = await module.search("query")

        assert output.module == "bing"
        assert output.results == []
        assert output.error is not None
        assert output.error.code == "ERROR"
        await module.close()

    async def test_search_handles_timeout(self) -> None:
        import asyncio

        module = BingSearchModule(timeout=1)

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
        module = BingSearchModule()

        with patch.object(module._session, "close", new_callable=AsyncMock) as mock_close:
            await module.close()

        mock_close.assert_awaited_once()
