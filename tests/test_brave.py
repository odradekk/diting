"""Tests for supersearch.modules.brave — BraveSearchModule."""

from __future__ import annotations

from unittest.mock import AsyncMock, patch

import httpx
import pytest

from supersearch.models import ModuleOutput, SearchResult
from supersearch.modules.brave import BraveSearchModule


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

API_KEY = "test-brave-api-key"
BRAVE_URL = "https://api.search.brave.com/res/v1/web/search"


def _make_brave_response(
    results: list[dict] | None = None,
    *,
    include_web: bool = True,
    status_code: int = 200,
    headers: dict | None = None,
) -> httpx.Response:
    """Build a mock httpx.Response mimicking the Brave Search API."""
    if include_web and results is not None:
        body = {"web": {"results": results}}
    elif include_web:
        body = {"web": {"results": []}}
    else:
        body = {}

    return httpx.Response(
        status_code=status_code,
        json=body,
        headers=headers or {},
        request=httpx.Request("GET", BRAVE_URL),
    )


def _sample_results(count: int = 3) -> list[dict]:
    """Build a list of raw Brave API result dicts."""
    return [
        {
            "title": f"Title {i}",
            "url": f"https://example.com/{i}",
            "description": f"Description for result {i}.",
        }
        for i in range(count)
    ]


# ---------------------------------------------------------------------------
# Constructor validation
# ---------------------------------------------------------------------------


class TestConstructor:
    """API key validation and basic properties."""

    def test_missing_api_key_raises_value_error(self) -> None:
        with pytest.raises(ValueError, match="(?i)api key"):
            BraveSearchModule(api_key="")

    def test_none_api_key_raises_value_error(self) -> None:
        with pytest.raises(ValueError, match="(?i)api key"):
            BraveSearchModule(api_key="")

    def test_module_name_is_brave(self) -> None:
        module = BraveSearchModule(api_key=API_KEY)
        assert module.name == "brave"

    def test_default_timeout(self) -> None:
        module = BraveSearchModule(api_key=API_KEY)
        assert module.timeout == 30

    def test_custom_timeout(self) -> None:
        module = BraveSearchModule(api_key=API_KEY, timeout=10)
        assert module.timeout == 10


# ---------------------------------------------------------------------------
# Successful search
# ---------------------------------------------------------------------------


class TestSuccessfulSearch:
    """Successful API response returns parsed SearchResult objects."""

    async def test_returns_list_of_search_results(self) -> None:
        module = BraveSearchModule(api_key=API_KEY)
        raw = _sample_results(3)
        mock_response = _make_brave_response(raw)

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=mock_response
        ):
            results = await module._execute("test query")

        assert len(results) == 3
        assert all(isinstance(r, SearchResult) for r in results)
        await module.close()

    async def test_description_maps_to_snippet(self) -> None:
        module = BraveSearchModule(api_key=API_KEY)
        raw = [
            {
                "title": "Example",
                "url": "https://example.com",
                "description": "This is the description.",
            }
        ]
        mock_response = _make_brave_response(raw)

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=mock_response
        ):
            results = await module._execute("query")

        assert results[0].snippet == "This is the description."
        assert results[0].title == "Example"
        assert results[0].url == "https://example.com"
        await module.close()

    async def test_skips_results_missing_title(self) -> None:
        module = BraveSearchModule(api_key=API_KEY)
        raw = [
            {"title": "", "url": "https://example.com/1", "description": "No title"},
            {"title": "Valid", "url": "https://example.com/2", "description": "Has title"},
        ]
        mock_response = _make_brave_response(raw)

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=mock_response
        ):
            results = await module._execute("query")

        assert len(results) == 1
        assert results[0].title == "Valid"
        await module.close()

    async def test_skips_results_missing_url(self) -> None:
        module = BraveSearchModule(api_key=API_KEY)
        raw = [
            {"title": "No URL", "url": "", "description": "Missing URL"},
            {"title": "Valid", "url": "https://example.com/2", "description": "Has URL"},
        ]
        mock_response = _make_brave_response(raw)

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=mock_response
        ):
            results = await module._execute("query")

        assert len(results) == 1
        assert results[0].title == "Valid"
        await module.close()


# ---------------------------------------------------------------------------
# Request parameters
# ---------------------------------------------------------------------------


class TestRequestParameters:
    """Correct headers and query parameters are sent."""

    async def test_headers_include_subscription_token(self) -> None:
        module = BraveSearchModule(api_key=API_KEY)
        assert module._http.headers["X-Subscription-Token"] == API_KEY
        assert module._http.headers["Accept"] == "application/json"
        assert module._http.headers["Accept-Encoding"] == "gzip"
        await module.close()

    async def test_query_parameter_is_passed(self) -> None:
        module = BraveSearchModule(api_key=API_KEY)
        mock_response = _make_brave_response(_sample_results(1))
        mock_get = AsyncMock(return_value=mock_response)

        with patch.object(module._http, "get", mock_get):
            await module._execute("python async tutorial")

        call_kwargs = mock_get.call_args
        params = call_kwargs.kwargs.get("params") or call_kwargs[1].get("params")
        assert params["q"] == "python async tutorial"
        assert params["count"] == 20
        await module.close()


# ---------------------------------------------------------------------------
# Empty / missing results
# ---------------------------------------------------------------------------


class TestEmptyResults:
    """Graceful handling of empty or missing response data."""

    async def test_no_web_key_returns_empty_list(self) -> None:
        module = BraveSearchModule(api_key=API_KEY)
        mock_response = _make_brave_response(include_web=False)

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=mock_response
        ):
            results = await module._execute("query")

        assert results == []
        await module.close()

    async def test_empty_results_array_returns_empty_list(self) -> None:
        module = BraveSearchModule(api_key=API_KEY)
        mock_response = _make_brave_response([])

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=mock_response
        ):
            results = await module._execute("query")

        assert results == []
        await module.close()

    async def test_web_key_with_null_results_returns_empty_list(self) -> None:
        module = BraveSearchModule(api_key=API_KEY)
        response = httpx.Response(
            status_code=200,
            json={"web": {"results": None}},
            request=httpx.Request("GET", BRAVE_URL),
        )

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=response
        ):
            results = await module._execute("query")

        assert results == []
        await module.close()


# ---------------------------------------------------------------------------
# HTTP errors
# ---------------------------------------------------------------------------


class TestHTTPErrors:
    """HTTP error responses raise appropriate exceptions."""

    async def test_429_raises_http_status_error(self) -> None:
        module = BraveSearchModule(api_key=API_KEY)
        mock_response = httpx.Response(
            status_code=429,
            json={"error": "rate limited"},
            headers={"Retry-After": "30"},
            request=httpx.Request("GET", BRAVE_URL),
        )

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=mock_response
        ):
            with pytest.raises(httpx.HTTPStatusError, match="(?i)rate limited"):
                await module._execute("query")

        await module.close()

    async def test_429_includes_retry_after(self) -> None:
        module = BraveSearchModule(api_key=API_KEY)
        mock_response = httpx.Response(
            status_code=429,
            json={"error": "rate limited"},
            headers={"Retry-After": "60"},
            request=httpx.Request("GET", BRAVE_URL),
        )

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=mock_response
        ):
            with pytest.raises(httpx.HTTPStatusError, match="60"):
                await module._execute("query")

        await module.close()

    async def test_500_raises_http_status_error(self) -> None:
        module = BraveSearchModule(api_key=API_KEY)
        mock_response = httpx.Response(
            status_code=500,
            text="Internal Server Error",
            request=httpx.Request("GET", BRAVE_URL),
        )

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=mock_response
        ):
            with pytest.raises(httpx.HTTPStatusError):
                await module._execute("query")

        await module.close()


# ---------------------------------------------------------------------------
# Base class integration
# ---------------------------------------------------------------------------


class TestBaseClassIntegration:
    """Integration with BaseSearchModule.search() timeout and error wrapping."""

    async def test_search_returns_module_output_on_success(self) -> None:
        module = BraveSearchModule(api_key=API_KEY, timeout=5)
        raw = _sample_results(2)
        mock_response = _make_brave_response(raw)

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=mock_response
        ):
            output = await module.search("test query")

        assert isinstance(output, ModuleOutput)
        assert output.module == "brave"
        assert output.error is None
        assert len(output.results) == 2
        await module.close()

    async def test_search_wraps_http_error_in_module_output(self) -> None:
        module = BraveSearchModule(api_key=API_KEY, timeout=5)
        mock_response = httpx.Response(
            status_code=500,
            text="Internal Server Error",
            request=httpx.Request("GET", BRAVE_URL),
        )

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=mock_response
        ):
            output = await module.search("query")

        assert output.module == "brave"
        assert output.results == []
        assert output.error is not None
        assert output.error.code == "ERROR"
        await module.close()

    async def test_search_handles_timeout(self) -> None:
        module = BraveSearchModule(api_key=API_KEY, timeout=1)

        async def _slow_get(*args, **kwargs):  # noqa: ARG001
            import asyncio

            await asyncio.sleep(10)

        with patch.object(module._http, "get", side_effect=_slow_get):
            output = await module.search("slow query")

        assert output.module == "brave"
        assert output.results == []
        assert output.error is not None
        assert output.error.code == "TIMEOUT"
        assert output.error.retryable is True
        await module.close()


# ---------------------------------------------------------------------------
# Close
# ---------------------------------------------------------------------------


class TestClose:
    """close() delegates to the underlying httpx client."""

    async def test_close_calls_aclose(self) -> None:
        module = BraveSearchModule(api_key=API_KEY)

        with patch.object(module._http, "aclose", new_callable=AsyncMock) as mock_aclose:
            await module.close()

        mock_aclose.assert_awaited_once()
