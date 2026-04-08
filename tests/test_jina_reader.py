"""Tests for diting.fetch.jina_reader — r.jina.ai reader fallback."""

from unittest.mock import AsyncMock, patch

import httpx
import pytest

from diting.fetch.jina_reader import JinaReaderFetcher
from diting.fetch.tavily import FetchError


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

_READER_URL = "https://r.jina.ai/"


def _make_response(
    text: str, status_code: int = 200, url: str = "https://example.com",
) -> httpx.Response:
    """Build a mock httpx.Response for r.jina.ai."""
    return httpx.Response(
        status_code=status_code,
        text=text,
        request=httpx.Request("GET", _READER_URL + url),
    )


# ---------------------------------------------------------------------------
# __init__ — headers and auth
# ---------------------------------------------------------------------------


class TestInitHeaders:
    """Constructor wires the right headers onto the inner httpx client."""

    def test_no_api_key_omits_authorization(self):
        fetcher = JinaReaderFetcher()
        assert "Authorization" not in fetcher._http.headers
        assert fetcher._http.headers["x-return-format"] == "markdown"

    def test_api_key_sets_bearer_token(self):
        fetcher = JinaReaderFetcher(api_key="jina_abc123")
        assert fetcher._http.headers["authorization"] == "Bearer jina_abc123"

    def test_custom_timeout(self):
        fetcher = JinaReaderFetcher(timeout=5.0)
        assert fetcher._http.timeout.connect == 5.0


# ---------------------------------------------------------------------------
# fetch() — single URL
# ---------------------------------------------------------------------------


class TestFetchSuccess:
    @pytest.mark.asyncio
    async def test_returns_markdown_body(self):
        fetcher = JinaReaderFetcher()
        body = "# Example\n\n" + "Hello world. " * 20
        with patch.object(
            fetcher._http, "get",
            new_callable=AsyncMock, return_value=_make_response(body),
        ) as mock_get:
            result = await fetcher.fetch("https://example.com/page")

        assert result == body.strip()
        mock_get.assert_awaited_once_with("https://r.jina.ai/https://example.com/page")
        await fetcher.close()

    @pytest.mark.asyncio
    async def test_strips_surrounding_whitespace(self):
        fetcher = JinaReaderFetcher()
        body = "\n\n  " + "A" * 100 + "  \n\n"
        with patch.object(
            fetcher._http, "get",
            new_callable=AsyncMock, return_value=_make_response(body),
        ):
            result = await fetcher.fetch("https://example.com")

        assert result == "A" * 100
        await fetcher.close()


class TestFetchTimeout:
    @pytest.mark.asyncio
    async def test_timeout_raises_fetch_error(self):
        fetcher = JinaReaderFetcher()
        with patch.object(
            fetcher._http, "get",
            new_callable=AsyncMock,
            side_effect=httpx.TimeoutException("timed out"),
        ):
            with pytest.raises(FetchError, match="Jina timeout"):
                await fetcher.fetch("https://example.com")
        await fetcher.close()


class TestFetchHttpError:
    @pytest.mark.asyncio
    async def test_connect_error_raises_fetch_error(self):
        fetcher = JinaReaderFetcher()
        with patch.object(
            fetcher._http, "get",
            new_callable=AsyncMock,
            side_effect=httpx.ConnectError("refused"),
        ):
            with pytest.raises(FetchError, match="Jina HTTP error"):
                await fetcher.fetch("https://example.com")
        await fetcher.close()

    @pytest.mark.asyncio
    async def test_4xx_raises_fetch_error(self):
        fetcher = JinaReaderFetcher()
        with patch.object(
            fetcher._http, "get",
            new_callable=AsyncMock,
            return_value=_make_response("forbidden", status_code=403),
        ):
            with pytest.raises(FetchError, match="Jina HTTP 403"):
                await fetcher.fetch("https://example.com")
        await fetcher.close()

    @pytest.mark.asyncio
    async def test_5xx_raises_fetch_error(self):
        fetcher = JinaReaderFetcher()
        with patch.object(
            fetcher._http, "get",
            new_callable=AsyncMock,
            return_value=_make_response("server error", status_code=503),
        ):
            with pytest.raises(FetchError, match="Jina HTTP 503"):
                await fetcher.fetch("https://example.com")
        await fetcher.close()


class TestFetchThinContent:
    @pytest.mark.asyncio
    async def test_empty_response_raises_fetch_error(self):
        fetcher = JinaReaderFetcher()
        with patch.object(
            fetcher._http, "get",
            new_callable=AsyncMock, return_value=_make_response(""),
        ):
            with pytest.raises(FetchError, match="thin content"):
                await fetcher.fetch("https://example.com")
        await fetcher.close()

    @pytest.mark.asyncio
    async def test_below_min_length_raises_fetch_error(self):
        fetcher = JinaReaderFetcher()
        with patch.object(
            fetcher._http, "get",
            new_callable=AsyncMock, return_value=_make_response("short"),
        ):
            with pytest.raises(FetchError, match="thin content"):
                await fetcher.fetch("https://example.com")
        await fetcher.close()


# ---------------------------------------------------------------------------
# fetch_many() — multiple URLs
# ---------------------------------------------------------------------------


class TestFetchMany:
    @pytest.mark.asyncio
    async def test_mixed_success_and_failure(self):
        fetcher = JinaReaderFetcher()
        good_body = "A" * 200
        responses = {
            "https://r.jina.ai/https://a.com": _make_response(good_body),
            "https://r.jina.ai/https://b.com": _make_response("x", status_code=502),
        }

        async def fake_get(url: str) -> httpx.Response:
            return responses[url]

        with patch.object(fetcher._http, "get", new=fake_get):
            results = await fetcher.fetch_many(["https://a.com", "https://b.com"])

        assert len(results) == 2
        assert results[0].url == "https://a.com"
        assert results[0].success is True
        assert results[0].content == good_body
        assert results[1].url == "https://b.com"
        assert results[1].success is False
        assert "502" in (results[1].error or "")
        await fetcher.close()

    @pytest.mark.asyncio
    async def test_empty_url_list_returns_empty(self):
        fetcher = JinaReaderFetcher()
        results = await fetcher.fetch_many([])
        assert results == []
        await fetcher.close()

    @pytest.mark.asyncio
    async def test_never_raises_on_exception(self):
        fetcher = JinaReaderFetcher()
        with patch.object(
            fetcher._http, "get",
            new_callable=AsyncMock,
            side_effect=httpx.TimeoutException("timed out"),
        ):
            results = await fetcher.fetch_many(["https://a.com"])

        assert len(results) == 1
        assert results[0].success is False
        assert "timeout" in (results[0].error or "").lower()
        await fetcher.close()


# ---------------------------------------------------------------------------
# close()
# ---------------------------------------------------------------------------


class TestClose:
    @pytest.mark.asyncio
    async def test_close_closes_http_client(self):
        fetcher = JinaReaderFetcher()
        with patch.object(
            fetcher._http, "aclose", new_callable=AsyncMock,
        ) as mock_close:
            await fetcher.close()
        mock_close.assert_awaited_once()
