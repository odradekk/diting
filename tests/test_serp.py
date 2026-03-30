"""Tests for diting.modules.serp — SerpSearchModule."""

from __future__ import annotations

from collections.abc import AsyncIterator
from unittest.mock import AsyncMock

import httpx
import pytest

from diting.models import ModuleOutput, SearchResult
from diting.modules.serp import SerpSearchModule


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------

FAKE_API_KEY = "test-api-key-123"


def _serpapi_response(organic_results: list[dict] | None = None) -> dict:
    """Build a minimal SerpAPI-shaped response dict."""
    if organic_results is None:
        return {}
    return {"organic_results": organic_results}


def _organic_items(count: int = 3) -> list[dict]:
    """Build a list of SerpAPI organic_results items."""
    return [
        {
            "title": f"Title {i}",
            "link": f"https://example.com/{i}",
            "snippet": f"Snippet for result {i}.",
        }
        for i in range(count)
    ]


def _mock_response(
    status_code: int = 200,
    json_data: dict | None = None,
) -> httpx.Response:
    """Create a fake httpx.Response."""
    if json_data is None:
        json_data = _serpapi_response(_organic_items(3))
    request = httpx.Request("GET", "https://serpapi.com/search.json")
    response = httpx.Response(
        status_code=status_code,
        json=json_data,
        request=request,
    )
    return response


@pytest.fixture()
async def module() -> AsyncIterator[SerpSearchModule]:
    """Return a SerpSearchModule with a fake API key; close on teardown."""
    mod = SerpSearchModule(api_key=FAKE_API_KEY, timeout=10)
    yield mod
    await mod.close()


@pytest.fixture()
def mock_client(module: SerpSearchModule, monkeypatch: pytest.MonkeyPatch) -> AsyncMock:
    """Replace the internal httpx.AsyncClient.get with an AsyncMock."""
    mock_get = AsyncMock(return_value=_mock_response())
    monkeypatch.setattr(module._client, "get", mock_get)
    return mock_get


# ---------------------------------------------------------------------------
# Constructor
# ---------------------------------------------------------------------------


class TestConstructor:
    def test_missing_api_key_raises_value_error(self) -> None:
        with pytest.raises(ValueError, match="must not be empty"):
            SerpSearchModule(api_key="")

    def test_module_name_is_serp(self, module: SerpSearchModule) -> None:
        assert module.name == "serp"

    async def test_default_timeout(self) -> None:
        mod = SerpSearchModule(api_key=FAKE_API_KEY)
        try:
            assert mod.timeout == 30
        finally:
            await mod.close()

    async def test_custom_timeout(self) -> None:
        mod = SerpSearchModule(api_key=FAKE_API_KEY, timeout=15)
        try:
            assert mod.timeout == 15
        finally:
            await mod.close()


# ---------------------------------------------------------------------------
# Successful search
# ---------------------------------------------------------------------------


class TestSuccessfulSearch:
    async def test_returns_list_of_search_results(
        self, module: SerpSearchModule, mock_client: AsyncMock
    ) -> None:
        results = await module._execute("python testing")

        assert len(results) == 3
        for r in results:
            assert isinstance(r, SearchResult)

    async def test_maps_link_to_url(
        self, module: SerpSearchModule, mock_client: AsyncMock
    ) -> None:
        results = await module._execute("python testing")

        assert results[0].url == "https://example.com/0"
        assert results[1].url == "https://example.com/1"

    async def test_result_fields_are_correct(
        self, module: SerpSearchModule, mock_client: AsyncMock
    ) -> None:
        results = await module._execute("python testing")

        assert results[0].title == "Title 0"
        assert results[0].snippet == "Snippet for result 0."

    async def test_correct_query_parameters(
        self, module: SerpSearchModule, mock_client: AsyncMock
    ) -> None:
        await module._execute("search query")

        mock_client.assert_called_once()
        call_kwargs = mock_client.call_args
        params = call_kwargs.kwargs.get("params") or call_kwargs[1].get("params")
        assert params["q"] == "search query"
        assert params["api_key"] == FAKE_API_KEY
        assert params["engine"] == "google"
        assert params["num"] == 20


# ---------------------------------------------------------------------------
# Empty / malformed responses
# ---------------------------------------------------------------------------


class TestEmptyResponses:
    async def test_missing_organic_results_key(
        self, module: SerpSearchModule, mock_client: AsyncMock
    ) -> None:
        mock_client.return_value = _mock_response(json_data={})

        results = await module._execute("empty query")

        assert results == []

    async def test_empty_organic_results_array(
        self, module: SerpSearchModule, mock_client: AsyncMock
    ) -> None:
        mock_client.return_value = _mock_response(
            json_data=_serpapi_response([])
        )

        results = await module._execute("empty query")

        assert results == []

    async def test_items_without_title_are_skipped(
        self, module: SerpSearchModule, mock_client: AsyncMock
    ) -> None:
        mock_client.return_value = _mock_response(
            json_data=_serpapi_response(
                [
                    {"link": "https://example.com/0", "snippet": "no title"},
                    {"title": "Valid", "link": "https://example.com/1", "snippet": "ok"},
                ]
            )
        )

        results = await module._execute("partial query")

        assert len(results) == 1
        assert results[0].title == "Valid"

    async def test_non_dict_items_are_skipped(
        self, module: SerpSearchModule, mock_client: AsyncMock
    ) -> None:
        mock_client.return_value = _mock_response(
            json_data=_serpapi_response(
                [
                    "not a dict",
                    {"title": "Valid", "link": "https://example.com/1", "snippet": "ok"},
                ]
            )
        )

        results = await module._execute("malformed query")

        assert len(results) == 1
        assert results[0].title == "Valid"

    async def test_null_snippet_treated_as_empty(
        self, module: SerpSearchModule, mock_client: AsyncMock
    ) -> None:
        mock_client.return_value = _mock_response(
            json_data=_serpapi_response(
                [{"title": "Title", "link": "https://example.com/0", "snippet": None}]
            )
        )

        results = await module._execute("null snippet query")

        assert len(results) == 1
        assert results[0].snippet == ""

    async def test_error_body_on_200_raises(
        self, module: SerpSearchModule, mock_client: AsyncMock
    ) -> None:
        mock_client.return_value = _mock_response(
            json_data={"error": "Invalid API key."}
        )

        with pytest.raises(RuntimeError, match="SerpAPI error"):
            await module._execute("error body query")


# ---------------------------------------------------------------------------
# HTTP errors
# ---------------------------------------------------------------------------


class TestHTTPErrors:
    async def test_429_raises_http_status_error(
        self, module: SerpSearchModule, mock_client: AsyncMock
    ) -> None:
        mock_client.return_value = _mock_response(status_code=429, json_data={})

        with pytest.raises(httpx.HTTPStatusError, match="429"):
            await module._execute("rate limited query")

    async def test_500_raises_http_status_error(
        self, module: SerpSearchModule, mock_client: AsyncMock
    ) -> None:
        mock_client.return_value = _mock_response(status_code=500, json_data={})

        with pytest.raises(httpx.HTTPStatusError):
            await module._execute("server error query")


# ---------------------------------------------------------------------------
# Integration with base class
# ---------------------------------------------------------------------------


class TestBaseClassIntegration:
    async def test_search_returns_module_output_on_success(
        self, module: SerpSearchModule, mock_client: AsyncMock
    ) -> None:
        output = await module.search("integration query")

        assert isinstance(output, ModuleOutput)
        assert output.module == "serp"
        assert output.error is None
        assert len(output.results) == 3

    async def test_search_returns_module_output_on_error(
        self, module: SerpSearchModule, mock_client: AsyncMock
    ) -> None:
        mock_client.return_value = _mock_response(status_code=500, json_data={})

        output = await module.search("failing query")

        assert isinstance(output, ModuleOutput)
        assert output.module == "serp"
        assert output.results == []
        assert output.error is not None
        assert output.error.code == "ERROR"

    async def test_search_wraps_429_in_module_output(
        self, module: SerpSearchModule, mock_client: AsyncMock
    ) -> None:
        mock_client.return_value = _mock_response(status_code=429, json_data={})

        output = await module.search("rate limited query")

        assert isinstance(output, ModuleOutput)
        assert output.module == "serp"
        assert output.results == []
        assert output.error is not None
        assert "429" in output.error.message

    async def test_search_wraps_error_body_in_module_output(
        self, module: SerpSearchModule, mock_client: AsyncMock
    ) -> None:
        mock_client.return_value = _mock_response(
            json_data={"error": "Invalid API key."}
        )

        output = await module.search("error body query")

        assert isinstance(output, ModuleOutput)
        assert output.results == []
        assert output.error is not None
        assert "SerpAPI error" in output.error.message

    async def test_search_handles_timeout(
        self, module: SerpSearchModule, mock_client: AsyncMock
    ) -> None:
        import asyncio

        async def slow_get(*args, **kwargs):
            await asyncio.sleep(20)
            return _mock_response()

        mock_client.side_effect = slow_get

        output = await module.search("timeout query")

        assert output.module == "serp"
        assert output.results == []
        assert output.error is not None
        assert output.error.code == "TIMEOUT"
        assert output.error.retryable is True


# ---------------------------------------------------------------------------
# Close
# ---------------------------------------------------------------------------


class TestClose:
    async def test_close_calls_aclose(
        self, module: SerpSearchModule, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        mock_aclose = AsyncMock()
        monkeypatch.setattr(module._client, "aclose", mock_aclose)

        await module.close()

        mock_aclose.assert_called_once()
