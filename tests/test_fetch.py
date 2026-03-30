"""Tests for diting.fetch.tavily — Tavily Extract API wrapper."""

from unittest.mock import AsyncMock, patch

import httpx
import pytest

from diting.fetch.tavily import FetchError, FetchResult, TavilyFetcher


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

API_KEY = "tvly-test-key"
EXTRACT_URL = "https://api.tavily.com/extract"


def _make_extract_response(
    results: list[dict],
    failed_results: list[dict] | None = None,
    status_code: int = 200,
) -> httpx.Response:
    """Build a mock httpx.Response for the Tavily Extract endpoint."""
    body: dict = {"results": results}
    if failed_results is not None:
        body["failed_results"] = failed_results
    return httpx.Response(
        status_code=status_code,
        json=body,
        request=httpx.Request("POST", EXTRACT_URL),
    )


def _make_error_response(status_code: int, body: str = "error") -> httpx.Response:
    """Build a mock httpx.Response representing an HTTP error."""
    return httpx.Response(
        status_code=status_code,
        text=body,
        request=httpx.Request("POST", EXTRACT_URL),
    )


# ---------------------------------------------------------------------------
# fetch() — single URL
# ---------------------------------------------------------------------------


class TestFetchSuccess:
    """Successful single fetch returns content string."""

    @pytest.mark.asyncio
    async def test_fetch_returns_content(self):
        fetcher = TavilyFetcher(api_key=API_KEY)
        mock_response = _make_extract_response(
            results=[{"url": "https://example.com", "raw_content": "Hello, world!"}]
        )

        with patch.object(fetcher._http, "post", new_callable=AsyncMock, return_value=mock_response):
            result = await fetcher.fetch("https://example.com")

        assert result == "Hello, world!"
        await fetcher.close()


class TestFetchTimeout:
    """Timeout during fetch raises FetchError."""

    @pytest.mark.asyncio
    async def test_fetch_timeout_raises_fetch_error(self):
        fetcher = TavilyFetcher(api_key=API_KEY)

        with patch.object(
            fetcher._http,
            "post",
            new_callable=AsyncMock,
            side_effect=httpx.TimeoutException("Connection timed out"),
        ):
            with pytest.raises(FetchError, match="(?i)timeout"):
                await fetcher.fetch("https://example.com")

        await fetcher.close()


class TestFetchHttpError:
    """Non-2xx HTTP response raises FetchError."""

    @pytest.mark.asyncio
    async def test_fetch_http_error_raises_fetch_error(self):
        fetcher = TavilyFetcher(api_key=API_KEY)
        mock_response = _make_error_response(401, "Unauthorized")

        with patch.object(fetcher._http, "post", new_callable=AsyncMock, return_value=mock_response):
            with pytest.raises(FetchError, match="401"):
                await fetcher.fetch("https://example.com")

        await fetcher.close()

    @pytest.mark.asyncio
    async def test_fetch_server_error_raises_fetch_error(self):
        fetcher = TavilyFetcher(api_key=API_KEY)
        mock_response = _make_error_response(500, "Internal Server Error")

        with patch.object(fetcher._http, "post", new_callable=AsyncMock, return_value=mock_response):
            with pytest.raises(FetchError, match="500"):
                await fetcher.fetch("https://example.com")

        await fetcher.close()


class TestFetchEmptyContent:
    """Empty or missing content raises FetchError."""

    @pytest.mark.asyncio
    async def test_fetch_empty_raw_content(self):
        fetcher = TavilyFetcher(api_key=API_KEY)
        mock_response = _make_extract_response(
            results=[{"url": "https://example.com", "raw_content": ""}]
        )

        with patch.object(fetcher._http, "post", new_callable=AsyncMock, return_value=mock_response):
            with pytest.raises(FetchError, match="(?i)empty"):
                await fetcher.fetch("https://example.com")

        await fetcher.close()

    @pytest.mark.asyncio
    async def test_fetch_missing_raw_content(self):
        fetcher = TavilyFetcher(api_key=API_KEY)
        mock_response = _make_extract_response(
            results=[{"url": "https://example.com"}]
        )

        with patch.object(fetcher._http, "post", new_callable=AsyncMock, return_value=mock_response):
            with pytest.raises(FetchError, match="(?i)empty"):
                await fetcher.fetch("https://example.com")

        await fetcher.close()

    @pytest.mark.asyncio
    async def test_fetch_empty_results_list(self):
        fetcher = TavilyFetcher(api_key=API_KEY)
        mock_response = _make_extract_response(results=[])

        with patch.object(fetcher._http, "post", new_callable=AsyncMock, return_value=mock_response):
            with pytest.raises(FetchError, match="(?i)empty"):
                await fetcher.fetch("https://example.com")

        await fetcher.close()


# ---------------------------------------------------------------------------
# fetch_many() — multiple URLs
# ---------------------------------------------------------------------------


class TestFetchManySuccess:
    """Successful multi-fetch returns list of FetchResults."""

    @pytest.mark.asyncio
    async def test_fetch_many_all_success(self):
        fetcher = TavilyFetcher(api_key=API_KEY)
        mock_response = _make_extract_response(
            results=[
                {"url": "https://a.com", "raw_content": "Content A"},
                {"url": "https://b.com", "raw_content": "Content B"},
            ]
        )

        with patch.object(fetcher._http, "post", new_callable=AsyncMock, return_value=mock_response):
            results = await fetcher.fetch_many(["https://a.com", "https://b.com"])

        assert len(results) == 2
        assert results[0].url == "https://a.com"
        assert results[0].content == "Content A"
        assert results[0].success is True
        assert results[0].error is None
        assert results[1].url == "https://b.com"
        assert results[1].content == "Content B"
        assert results[1].success is True
        await fetcher.close()


class TestFetchManyMixed:
    """fetch_many handles mixed success/failure without raising."""

    @pytest.mark.asyncio
    async def test_fetch_many_mixed_results(self):
        fetcher = TavilyFetcher(api_key=API_KEY)
        mock_response = _make_extract_response(
            results=[
                {"url": "https://a.com", "raw_content": "Content A"},
            ],
            failed_results=[
                {"url": "https://b.com", "error": "Page not found"},
            ],
        )

        with patch.object(fetcher._http, "post", new_callable=AsyncMock, return_value=mock_response):
            results = await fetcher.fetch_many(["https://a.com", "https://b.com"])

        assert len(results) == 2
        assert results[0].success is True
        assert results[0].content == "Content A"
        assert results[1].success is False
        assert results[1].content == ""
        assert results[1].error == "Page not found"
        await fetcher.close()


class TestFetchManyCaptures:
    """fetch_many captures errors without raising."""

    @pytest.mark.asyncio
    async def test_fetch_many_timeout_captures_error(self):
        fetcher = TavilyFetcher(api_key=API_KEY)

        with patch.object(
            fetcher._http,
            "post",
            new_callable=AsyncMock,
            side_effect=httpx.TimeoutException("Connection timed out"),
        ):
            results = await fetcher.fetch_many(["https://a.com", "https://b.com"])

        assert len(results) == 2
        for r in results:
            assert r.success is False
            assert r.content == ""
            assert "Timeout" in r.error
        await fetcher.close()

    @pytest.mark.asyncio
    async def test_fetch_many_http_error_captures_error(self):
        fetcher = TavilyFetcher(api_key=API_KEY)
        mock_response = _make_error_response(429, "Rate limited")

        with patch.object(fetcher._http, "post", new_callable=AsyncMock, return_value=mock_response):
            results = await fetcher.fetch_many(["https://a.com"])

        assert len(results) == 1
        assert results[0].success is False
        assert "429" in results[0].error
        await fetcher.close()

    @pytest.mark.asyncio
    async def test_fetch_many_no_content_captures_error(self):
        """URL present in neither results nor failed_results gets a fallback error."""
        fetcher = TavilyFetcher(api_key=API_KEY)
        mock_response = _make_extract_response(results=[], failed_results=[])

        with patch.object(fetcher._http, "post", new_callable=AsyncMock, return_value=mock_response):
            results = await fetcher.fetch_many(["https://missing.com"])

        assert len(results) == 1
        assert results[0].success is False
        assert results[0].error == "No content returned"
        await fetcher.close()


class TestFetchManyPreservesOrder:
    """fetch_many returns results in the same order as input URLs."""

    @pytest.mark.asyncio
    async def test_fetch_many_order_preserved(self):
        fetcher = TavilyFetcher(api_key=API_KEY)
        # API returns results in reverse order.
        mock_response = _make_extract_response(
            results=[
                {"url": "https://c.com", "raw_content": "C"},
                {"url": "https://a.com", "raw_content": "A"},
                {"url": "https://b.com", "raw_content": "B"},
            ]
        )

        with patch.object(fetcher._http, "post", new_callable=AsyncMock, return_value=mock_response):
            results = await fetcher.fetch_many(
                ["https://a.com", "https://b.com", "https://c.com"]
            )

        assert [r.url for r in results] == [
            "https://a.com",
            "https://b.com",
            "https://c.com",
        ]
        assert [r.content for r in results] == ["A", "B", "C"]
        await fetcher.close()


# ---------------------------------------------------------------------------
# close()
# ---------------------------------------------------------------------------


class TestClose:
    """close() delegates to the underlying httpx client's aclose()."""

    @pytest.mark.asyncio
    async def test_close(self):
        fetcher = TavilyFetcher(api_key=API_KEY)

        with patch.object(fetcher._http, "aclose", new_callable=AsyncMock) as mock_aclose:
            await fetcher.close()

        mock_aclose.assert_awaited_once()


# ---------------------------------------------------------------------------
# FetchResult model
# ---------------------------------------------------------------------------


class TestFetchResultModel:
    """FetchResult Pydantic model behaves correctly."""

    def test_fetch_result_success(self):
        result = FetchResult(url="https://example.com", content="text", success=True)
        assert result.url == "https://example.com"
        assert result.content == "text"
        assert result.success is True
        assert result.error is None

    def test_fetch_result_failure(self):
        result = FetchResult(
            url="https://example.com",
            content="",
            success=False,
            error="Timed out",
        )
        assert result.success is False
        assert result.error == "Timed out"
        assert result.content == ""
