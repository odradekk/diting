"""Tests for diting.modules.github — GitHubSearchModule."""

from __future__ import annotations

from unittest.mock import AsyncMock, patch

import httpx
import pytest

from diting.models import SearchResult
from diting.modules.github import GitHubSearchModule


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

GITHUB_URL = "https://api.github.com/search/repositories"


def _make_github_response(
    items: list[dict] | None = None,
    *,
    total_count: int = 100,
    status_code: int = 200,
    headers: dict | None = None,
) -> httpx.Response:
    """Build a mock httpx.Response mimicking the GitHub Search API."""
    body = {
        "total_count": total_count,
        "items": items if items is not None else [],
    }
    return httpx.Response(
        status_code=status_code,
        json=body,
        headers=headers or {},
        request=httpx.Request("GET", GITHUB_URL),
    )


def _sample_items(count: int = 3) -> list[dict]:
    """Build a list of raw GitHub API repository dicts."""
    return [
        {
            "full_name": f"owner/repo-{i}",
            "html_url": f"https://github.com/owner/repo-{i}",
            "description": f"Description for repo {i}.",
        }
        for i in range(count)
    ]


# ---------------------------------------------------------------------------
# Basic search
# ---------------------------------------------------------------------------


class TestBasicSearch:
    """Successful API response returns parsed SearchResult objects."""

    async def test_basic_search(self) -> None:
        module = GitHubSearchModule()
        items = [
            {
                "full_name": "torvalds/linux",
                "html_url": "https://github.com/torvalds/linux",
                "description": "Linux kernel source tree",
            },
            {
                "full_name": "rust-lang/rust",
                "html_url": "https://github.com/rust-lang/rust",
                "description": "Empowering everyone to build reliable software.",
            },
            {
                "full_name": "python/cpython",
                "html_url": "https://github.com/python/cpython",
                "description": "The Python programming language",
            },
        ]
        mock_response = _make_github_response(items)
        mock_get = AsyncMock(return_value=mock_response)

        with patch.object(module._http, "get", mock_get):
            results = await module._execute("linux kernel")

        # Verify request parameters
        call_kwargs = mock_get.call_args
        params = call_kwargs.kwargs.get("params") or call_kwargs[1].get("params")
        assert params["q"] == "linux kernel"
        assert params["sort"] == "stars"
        assert params["order"] == "desc"
        assert params["per_page"] == 20
        assert params["page"] == 1

        assert len(results) == 3
        assert all(isinstance(r, SearchResult) for r in results)
        assert results[0].title == "torvalds/linux"
        assert results[0].url == "https://github.com/torvalds/linux"
        assert results[0].snippet == "Linux kernel source tree"
        assert results[1].title == "rust-lang/rust"
        assert results[2].title == "python/cpython"
        await module.close()

    async def test_null_description(self) -> None:
        module = GitHubSearchModule()
        items = [
            {
                "full_name": "user/no-desc",
                "html_url": "https://github.com/user/no-desc",
                "description": None,
            },
        ]
        mock_response = _make_github_response(items)

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=mock_response
        ):
            results = await module._execute("no description repo")

        assert len(results) == 1
        assert results[0].snippet == ""
        await module.close()

    async def test_empty_items(self) -> None:
        module = GitHubSearchModule()
        mock_response = _make_github_response([], total_count=0)

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=mock_response
        ):
            results = await module._execute("nonexistent-xyz-abc")

        assert results == []
        await module.close()


# ---------------------------------------------------------------------------
# Authentication headers
# ---------------------------------------------------------------------------


class TestAuthHeaders:
    """Token handling for Authorization header."""

    async def test_token_sets_auth_header(self) -> None:
        module = GitHubSearchModule(token="ghp_test123")
        assert module._http.headers["Authorization"] == "Bearer ghp_test123"
        await module.close()

    async def test_no_token_no_auth_header(self) -> None:
        module = GitHubSearchModule()
        assert "Authorization" not in module._http.headers
        await module.close()


# ---------------------------------------------------------------------------
# max_results
# ---------------------------------------------------------------------------


class TestMaxResults:
    """max_results caps the returned result count."""

    async def test_max_results_respected(self) -> None:
        module = GitHubSearchModule(max_results=5)
        items = _sample_items(20)
        mock_response = _make_github_response(items)

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=mock_response
        ):
            results = await module._execute("popular repos")

        assert len(results) == 5
        await module.close()


# ---------------------------------------------------------------------------
# HTTP errors
# ---------------------------------------------------------------------------


class TestHTTPErrors:
    """HTTP error responses raise appropriate exceptions."""

    async def test_rate_limit_403(self) -> None:
        module = GitHubSearchModule()
        mock_response = httpx.Response(
            status_code=403,
            json={"message": "API rate limit exceeded"},
            headers={"X-RateLimit-Remaining": "0"},
            request=httpx.Request("GET", GITHUB_URL),
        )

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=mock_response
        ):
            with pytest.raises(httpx.HTTPStatusError, match="rate limit"):
                await module._execute("query")

        await module.close()

    async def test_422_raises_http_status_error(self) -> None:
        module = GitHubSearchModule()
        mock_response = httpx.Response(
            status_code=422,
            json={"message": "Validation Failed"},
            request=httpx.Request("GET", GITHUB_URL),
        )

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=mock_response
        ):
            with pytest.raises(httpx.HTTPStatusError, match="validation"):
                await module._execute("query")

        await module.close()


# ---------------------------------------------------------------------------
# Close
# ---------------------------------------------------------------------------


class TestClose:
    """close() delegates to the underlying httpx client."""

    async def test_close(self) -> None:
        module = GitHubSearchModule()

        with patch.object(module._http, "aclose", new_callable=AsyncMock) as mock_aclose:
            await module.close()

        mock_aclose.assert_awaited_once()
