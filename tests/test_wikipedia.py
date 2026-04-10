"""Tests for diting.modules.wikipedia -- WikipediaSearchModule."""

from __future__ import annotations

from unittest.mock import AsyncMock, patch

import httpx

from diting.models import ModuleOutput, SearchResult
from diting.modules.wikipedia import WikipediaSearchModule


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

EN_API_URL = "https://en.wikipedia.org/w/api.php"
ZH_API_URL = "https://zh.wikipedia.org/w/api.php"


def _make_wiki_response(
    results: list[dict],
    *,
    api_url: str = EN_API_URL,
    status_code: int = 200,
) -> httpx.Response:
    """Build a mock httpx.Response mimicking the MediaWiki Action API."""
    body = {"query": {"search": results}}
    return httpx.Response(
        status_code=status_code,
        json=body,
        request=httpx.Request("GET", api_url),
    )


def _sample_en_results() -> list[dict]:
    return [
        {
            "title": "Python (programming language)",
            "snippet": "Python is a <span class=\"searchmatch\">programming</span> language",
        },
        {
            "title": "Monty Python",
            "snippet": "Monty <span class=\"searchmatch\">Python</span> was a comedy group",
        },
    ]


def _sample_zh_results() -> list[dict]:
    return [
        {
            "title": "Python",
            "snippet": "Python\u662f\u4e00\u79cd<span class=\"searchmatch\">\u7f16\u7a0b</span>\u8bed\u8a00",
        },
        {
            "title": "\u5de8\u87d2",
            "snippet": "\u5de8\u87d2\u662f\u4e00\u79cd<span class=\"searchmatch\">\u86c7</span>",
        },
    ]


def _route_wiki_get(en_response: httpx.Response, zh_response: httpx.Response):
    """Return a side_effect callable that routes by URL."""

    async def _side_effect(url, **kwargs):  # noqa: ARG001
        url_str = str(url)
        if "en.wikipedia.org" in url_str:
            return en_response
        if "zh.wikipedia.org" in url_str:
            return zh_response
        raise ValueError(f"Unexpected URL: {url_str}")

    return _side_effect


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


class TestBasicSearch:
    """Basic search with both en + zh responses, interleaved results."""

    async def test_basic_search(self) -> None:
        module = WikipediaSearchModule()

        en_resp = _make_wiki_response(_sample_en_results(), api_url=EN_API_URL)
        zh_resp = _make_wiki_response(_sample_zh_results(), api_url=ZH_API_URL)

        with patch.object(
            module._http, "get", side_effect=_route_wiki_get(en_resp, zh_resp)
        ):
            results = await module._execute("python")

        assert len(results) == 4
        assert all(isinstance(r, SearchResult) for r in results)

        # Interleaved: en[0], zh[0], en[1], zh[1]
        assert results[0].title == "Python (programming language)"
        assert results[0].url == "https://en.wikipedia.org/wiki/Python_(programming_language)"
        assert results[1].title == "Python"
        assert results[1].url == "https://zh.wikipedia.org/wiki/Python"
        assert results[2].title == "Monty Python"
        assert results[2].url == "https://en.wikipedia.org/wiki/Monty_Python"
        assert results[3].title == "\u5de8\u87d2"
        assert results[3].url == "https://zh.wikipedia.org/wiki/%E5%B7%A8%E8%9F%92"

        await module.close()


class TestHTMLStripping:
    """Verify HTML tags are stripped from snippets."""

    async def test_html_stripped_from_snippets(self) -> None:
        module = WikipediaSearchModule()

        en_resp = _make_wiki_response(
            [{"title": "Test", "snippet": 'a <span class="searchmatch">bold</span> word'}],
            api_url=EN_API_URL,
        )
        zh_resp = _make_wiki_response([], api_url=ZH_API_URL)

        with patch.object(
            module._http, "get", side_effect=_route_wiki_get(en_resp, zh_resp)
        ):
            results = await module._execute("test")

        assert results[0].snippet == "a bold word"
        await module.close()


class TestEmptyResults:
    """Both wikis return empty, result is []."""

    async def test_empty_results(self) -> None:
        module = WikipediaSearchModule()

        en_resp = _make_wiki_response([], api_url=EN_API_URL)
        zh_resp = _make_wiki_response([], api_url=ZH_API_URL)

        with patch.object(
            module._http, "get", side_effect=_route_wiki_get(en_resp, zh_resp)
        ):
            results = await module._execute("xyzzy_nonexistent")

        assert results == []
        await module.close()


class TestDeduplication:
    """Duplicate URLs within the same language wiki are deduplicated."""

    async def test_same_title_deduplication(self) -> None:
        module = WikipediaSearchModule()

        # Two results with the same title from en will produce the same URL.
        # Only the first should survive dedup.
        en_dup_resp = _make_wiki_response(
            [
                {"title": "Python", "snippet": "First"},
                {"title": "Python", "snippet": "Duplicate"},
            ],
            api_url=EN_API_URL,
        )
        zh_resp = _make_wiki_response([], api_url=ZH_API_URL)

        with patch.object(
            module._http, "get", side_effect=_route_wiki_get(en_dup_resp, zh_resp)
        ):
            results = await module._execute("python")

        # Only one should survive dedup
        assert len(results) == 1
        assert results[0].url == "https://en.wikipedia.org/wiki/Python"
        await module.close()


class TestMaxResults:
    """max_results is respected even when wikis return more."""

    async def test_max_results_respected(self) -> None:
        module = WikipediaSearchModule(max_results=5)

        en_items = [{"title": f"EN Article {i}", "snippet": f"en {i}"} for i in range(20)]
        zh_items = [{"title": f"ZH Article {i}", "snippet": f"zh {i}"} for i in range(20)]

        en_resp = _make_wiki_response(en_items, api_url=EN_API_URL)
        zh_resp = _make_wiki_response(zh_items, api_url=ZH_API_URL)

        with patch.object(
            module._http, "get", side_effect=_route_wiki_get(en_resp, zh_resp)
        ):
            results = await module._execute("articles")

        assert len(results) == 5
        await module.close()


class TestOneWikiFails:
    """One wiki fails, the other's results are still returned."""

    async def test_one_wiki_fails(self) -> None:
        module = WikipediaSearchModule()

        en_resp = _make_wiki_response(_sample_en_results(), api_url=EN_API_URL)

        async def _side_effect(url, **kwargs):  # noqa: ARG001
            url_str = str(url)
            if "en.wikipedia.org" in url_str:
                return en_resp
            if "zh.wikipedia.org" in url_str:
                raise httpx.ConnectError("Connection refused")
            raise ValueError(f"Unexpected URL: {url_str}")

        with patch.object(module._http, "get", side_effect=_side_effect):
            results = await module._execute("python")

        # Only en results survive
        assert len(results) == 2
        assert results[0].title == "Python (programming language)"
        assert results[1].title == "Monty Python"
        await module.close()


class TestURLEncoding:
    """Titles with special characters are percent-encoded in URLs."""

    async def test_special_characters_encoded(self) -> None:
        module = WikipediaSearchModule()

        en_resp = _make_wiki_response(
            [{"title": "C#", "snippet": "A programming language"}],
            api_url=EN_API_URL,
        )
        zh_resp = _make_wiki_response([], api_url=ZH_API_URL)

        with patch.object(
            module._http, "get", side_effect=_route_wiki_get(en_resp, zh_resp)
        ):
            results = await module._execute("C#")

        assert results[0].url == "https://en.wikipedia.org/wiki/C%23"
        await module.close()


class TestAllWikiFail:
    """Both wikis failing raises so BaseSearchModule emits ModuleError."""

    async def test_both_wikis_fail_raises(self) -> None:
        module = WikipediaSearchModule()

        async def _side_effect(url, **kwargs):  # noqa: ARG001
            raise httpx.ConnectError("Connection refused")

        with patch.object(module._http, "get", side_effect=_side_effect):
            output = await module.search("python")

        assert output.module == "wikipedia"
        assert output.results == []
        assert output.error is not None
        assert output.error.code == "ERROR"
        await module.close()


class TestBaseClassIntegration:
    """Integration with BaseSearchModule.search() timeout and error wrapping."""

    async def test_search_returns_module_output_on_success(self) -> None:
        module = WikipediaSearchModule(timeout=5)

        en_resp = _make_wiki_response(_sample_en_results(), api_url=EN_API_URL)
        zh_resp = _make_wiki_response(_sample_zh_results(), api_url=ZH_API_URL)

        with patch.object(
            module._http, "get", side_effect=_route_wiki_get(en_resp, zh_resp)
        ):
            output = await module.search("python")

        assert isinstance(output, ModuleOutput)
        assert output.module == "wikipedia"
        assert output.error is None
        assert len(output.results) == 4
        await module.close()

    async def test_search_handles_timeout(self) -> None:
        module = WikipediaSearchModule(timeout=1)

        async def _slow_get(*args, **kwargs):  # noqa: ARG001
            import asyncio

            await asyncio.sleep(10)

        with patch.object(module._http, "get", side_effect=_slow_get):
            output = await module.search("slow query")

        assert output.module == "wikipedia"
        assert output.results == []
        assert output.error is not None
        assert output.error.code == "TIMEOUT"
        assert output.error.retryable is True
        await module.close()


class TestClose:
    """close() delegates to the underlying httpx client."""

    async def test_close(self) -> None:
        module = WikipediaSearchModule()

        with patch.object(module._http, "aclose", new_callable=AsyncMock) as mock_aclose:
            await module.close()

        mock_aclose.assert_awaited_once()
