"""Tests for diting.modules.stackexchange -- StackExchangeSearchModule."""

from __future__ import annotations

from unittest.mock import AsyncMock, patch

import httpx
import pytest

from diting.models import SearchResult
from diting.modules.stackexchange import StackExchangeSearchModule


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

SE_URL = "https://api.stackexchange.com/2.3/search/advanced"


def _make_se_response(
    items: list[dict] | None = None,
    *,
    has_more: bool = False,
    status_code: int = 200,
    headers: dict | None = None,
) -> httpx.Response:
    """Build a mock httpx.Response mimicking the StackExchange API."""
    body = {
        "items": items if items is not None else [],
        "has_more": has_more,
    }
    return httpx.Response(
        status_code=status_code,
        json=body,
        headers=headers or {},
        request=httpx.Request("GET", SE_URL),
    )


def _sample_items(count: int = 3) -> list[dict]:
    """Build a list of raw StackExchange API item dicts."""
    return [
        {
            "title": f"How to do thing {i}?",
            "link": f"https://stackoverflow.com/questions/{i}/how-to-do-thing-{i}",
            "body_excerpt": f"You can do thing {i} by using...",
            "tags": ["python", f"tag-{i}"],
        }
        for i in range(count)
    ]


# ---------------------------------------------------------------------------
# Basic search
# ---------------------------------------------------------------------------


class TestBasicSearch:
    """Successful API response returns parsed SearchResult objects."""

    async def test_basic_search(self) -> None:
        module = StackExchangeSearchModule()
        items = _sample_items(3)
        mock_response = _make_se_response(items)

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=mock_response
        ):
            results = await module._execute("python parsing")

        assert len(results) == 3
        assert all(isinstance(r, SearchResult) for r in results)
        assert results[0].title == "How to do thing 0?"
        assert results[0].url == "https://stackoverflow.com/questions/0/how-to-do-thing-0"
        assert results[0].snippet == "You can do thing 0 by using..."
        await module.close()


# ---------------------------------------------------------------------------
# HTML entity unescaping
# ---------------------------------------------------------------------------


class TestHTMLEntities:
    """Titles with HTML entities are unescaped correctly."""

    async def test_html_entities_in_title(self) -> None:
        module = StackExchangeSearchModule()
        items = [
            {
                "title": "How to use &amp; operator in C&#39;s &lt;stdio.h&gt;?",
                "link": "https://stackoverflow.com/questions/999/how-to-use",
                "body_excerpt": "The & operator is used for...",
                "tags": ["c", "operators"],
            }
        ]
        mock_response = _make_se_response(items)

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=mock_response
        ):
            results = await module._execute("C ampersand operator")

        assert results[0].title == "How to use & operator in C's <stdio.h>?"
        await module.close()


# ---------------------------------------------------------------------------
# Empty results
# ---------------------------------------------------------------------------


class TestEmptyResults:
    """Graceful handling of empty API responses."""

    async def test_empty_items(self) -> None:
        module = StackExchangeSearchModule()
        mock_response = _make_se_response([])

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=mock_response
        ):
            results = await module._execute("obscure query with no results")

        assert results == []
        await module.close()


# ---------------------------------------------------------------------------
# Tag fallback
# ---------------------------------------------------------------------------


class TestTagFallback:
    """When body_excerpt is absent, tags are used as snippet."""

    async def test_missing_body_excerpt_uses_tags(self) -> None:
        module = StackExchangeSearchModule()
        items = [
            {
                "title": "How to parse JSON in Python?",
                "link": "https://stackoverflow.com/questions/123/how-to-parse-json",
                "tags": ["python", "json", "parsing"],
            }
        ]
        mock_response = _make_se_response(items)

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=mock_response
        ):
            results = await module._execute("parse json python")

        assert len(results) == 1
        assert results[0].snippet == "python, json, parsing"
        await module.close()


# ---------------------------------------------------------------------------
# Max results
# ---------------------------------------------------------------------------


class TestMaxResults:
    """Returned results respect max_results limit."""

    async def test_max_results_respected(self) -> None:
        module = StackExchangeSearchModule(max_results=5)
        items = _sample_items(20)
        mock_response = _make_se_response(items)

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=mock_response
        ):
            results = await module._execute("python")

        assert len(results) == 5
        await module.close()


# ---------------------------------------------------------------------------
# Rate limiting
# ---------------------------------------------------------------------------


class TestRateLimiting:
    """HTTP 429 and SE-specific throttle errors raise appropriate exceptions."""

    async def test_rate_limit_429(self) -> None:
        module = StackExchangeSearchModule()
        mock_response = httpx.Response(
            status_code=429,
            json={"error_id": 502, "error_message": "too many requests"},
            request=httpx.Request("GET", SE_URL),
        )

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=mock_response
        ):
            with pytest.raises(httpx.HTTPStatusError, match="rate limit"):
                await module._execute("query")

        await module.close()

    async def test_throttle_error_id_502(self) -> None:
        """SE returns error_id 502 inside JSON body (HTTP 400) for throttling."""
        module = StackExchangeSearchModule()
        mock_response = httpx.Response(
            status_code=400,
            json={
                "error_id": 502,
                "error_message": "too many requests from this IP",
                "error_name": "throttle_violation",
            },
            request=httpx.Request("GET", SE_URL),
        )

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=mock_response
        ):
            with pytest.raises(httpx.HTTPStatusError, match="throttle"):
                await module._execute("query")

        await module.close()

    async def test_non_json_5xx_raises_http_status_error(self) -> None:
        """Non-JSON error response (e.g. 5xx HTML) falls back to raise_for_status."""
        module = StackExchangeSearchModule()
        mock_response = httpx.Response(
            status_code=502,
            text="<html>Bad Gateway</html>",
            request=httpx.Request("GET", SE_URL),
        )

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=mock_response
        ):
            with pytest.raises(httpx.HTTPStatusError):
                await module._execute("query")

        await module.close()

    async def test_non_json_200_raises_value_error(self) -> None:
        """Non-JSON 200 response (WAF/proxy corruption) raises ValueError, not silent empty."""
        module = StackExchangeSearchModule()
        mock_response = httpx.Response(
            status_code=200,
            text="<html>Captcha challenge</html>",
            request=httpx.Request("GET", SE_URL),
        )

        with patch.object(
            module._http, "get", new_callable=AsyncMock, return_value=mock_response
        ):
            with pytest.raises(ValueError):
                await module._execute("query")

        await module.close()


# ---------------------------------------------------------------------------
# Close
# ---------------------------------------------------------------------------


class TestClose:
    """close() delegates to the underlying httpx client."""

    async def test_close(self) -> None:
        module = StackExchangeSearchModule()

        with patch.object(module._http, "aclose", new_callable=AsyncMock) as mock_aclose:
            await module.close()

        mock_aclose.assert_awaited_once()
