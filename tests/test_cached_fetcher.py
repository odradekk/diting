"""Tests for diting.fetch.cached — cache-wrapped fetcher."""

from __future__ import annotations

import pathlib
from unittest.mock import AsyncMock, MagicMock

import pytest

from diting.fetch.cache import ContentCache
from diting.fetch.cached import CachedFetcher
from diting.fetch.tavily import FetchError, FetchResult


REAL_ARTICLE = (
    "Kubernetes is an open source container orchestration system for "
    "automating deployment, scaling, and management of applications. "
) * 20


def _cached_fetcher(tmp_path: pathlib.Path, inner) -> tuple[CachedFetcher, ContentCache]:
    cache = ContentCache(tmp_path / "cache.db")
    return CachedFetcher(inner, cache), cache


# ---------------------------------------------------------------------------
# fetch()
# ---------------------------------------------------------------------------


async def test_fetch_misses_then_hits(tmp_path: pathlib.Path) -> None:
    inner = MagicMock()
    inner.fetch = AsyncMock(return_value=REAL_ARTICLE)
    fetcher, _cache = _cached_fetcher(tmp_path, inner)

    # First call: miss → inner invoked → content stored.
    first = await fetcher.fetch("https://example.com/a")
    assert first == REAL_ARTICLE
    assert inner.fetch.await_count == 1

    # Second call: hit → inner NOT invoked.
    second = await fetcher.fetch("https://example.com/a")
    assert second == REAL_ARTICLE
    assert inner.fetch.await_count == 1


async def test_fetch_rejects_login_wall_from_cache(tmp_path: pathlib.Path) -> None:
    """A login-wall response must not pollute the cache — re-fetch on next call."""
    wall_page = "x" * 200 + "登录后查看完整内容" + "y" * 200
    inner = MagicMock()
    inner.fetch = AsyncMock(return_value=wall_page)
    fetcher, cache = _cached_fetcher(tmp_path, inner)

    result = await fetcher.fetch("https://walled.example/post")
    assert result == wall_page  # caller still receives it
    assert cache.size() == 0    # but nothing persisted

    # Next call should re-invoke inner.
    await fetcher.fetch("https://walled.example/post")
    assert inner.fetch.await_count == 2


async def test_fetch_rejects_cloudflare_challenge(tmp_path: pathlib.Path) -> None:
    challenge = "<html>Just a moment...</html>" + "y" * 400
    inner = MagicMock()
    inner.fetch = AsyncMock(return_value=challenge)
    fetcher, cache = _cached_fetcher(tmp_path, inner)

    await fetcher.fetch("https://cf.example/post")
    assert cache.size() == 0


async def test_fetch_rejects_short_content(tmp_path: pathlib.Path) -> None:
    inner = MagicMock()
    inner.fetch = AsyncMock(return_value="tiny")
    fetcher, cache = _cached_fetcher(tmp_path, inner)

    await fetcher.fetch("https://example.com/stub")
    assert cache.size() == 0


async def test_fetch_propagates_inner_error(tmp_path: pathlib.Path) -> None:
    inner = MagicMock()
    inner.fetch = AsyncMock(side_effect=FetchError("upstream 500"))
    fetcher, cache = _cached_fetcher(tmp_path, inner)

    with pytest.raises(FetchError):
        await fetcher.fetch("https://example.com/broken")
    assert cache.size() == 0


async def test_cache_hit_is_returned_even_when_inner_would_fail(
    tmp_path: pathlib.Path,
) -> None:
    inner = MagicMock()
    inner.fetch = AsyncMock(side_effect=FetchError("upstream down"))
    fetcher, cache = _cached_fetcher(tmp_path, inner)
    cache.put("https://example.com/a", REAL_ARTICLE)

    result = await fetcher.fetch("https://example.com/a")
    assert result == REAL_ARTICLE
    inner.fetch.assert_not_awaited()


# ---------------------------------------------------------------------------
# fetch_many()
# ---------------------------------------------------------------------------


async def test_fetch_many_splits_hits_and_misses(tmp_path: pathlib.Path) -> None:
    inner = MagicMock()
    # Inner receives only the missing URL, returns its own content.
    inner.fetch_many = AsyncMock(return_value=[
        FetchResult(url="https://example.com/b", content=REAL_ARTICLE, success=True),
    ])
    fetcher, cache = _cached_fetcher(tmp_path, inner)
    cache.put("https://example.com/a", REAL_ARTICLE)

    results = await fetcher.fetch_many([
        "https://example.com/a",
        "https://example.com/b",
    ])

    assert len(results) == 2
    assert results[0].url == "https://example.com/a"
    assert results[0].content == REAL_ARTICLE
    assert results[1].url == "https://example.com/b"
    assert results[1].content == REAL_ARTICLE
    # Inner was called exactly once with just the miss.
    inner.fetch_many.assert_awaited_once_with(["https://example.com/b"])


async def test_fetch_many_preserves_order(tmp_path: pathlib.Path) -> None:
    inner = MagicMock()
    inner.fetch_many = AsyncMock(return_value=[
        FetchResult(url="https://example.com/b", content=REAL_ARTICLE, success=True),
        FetchResult(url="https://example.com/d", content=REAL_ARTICLE, success=True),
    ])
    fetcher, cache = _cached_fetcher(tmp_path, inner)
    cache.put("https://example.com/a", REAL_ARTICLE)
    cache.put("https://example.com/c", REAL_ARTICLE)

    urls = [
        "https://example.com/a",  # hit
        "https://example.com/b",  # miss
        "https://example.com/c",  # hit
        "https://example.com/d",  # miss
    ]
    results = await fetcher.fetch_many(urls)

    assert [r.url for r in results] == urls


async def test_fetch_many_skips_failed_slots_from_caching(
    tmp_path: pathlib.Path,
) -> None:
    inner = MagicMock()
    inner.fetch_many = AsyncMock(return_value=[
        FetchResult(url="https://example.com/ok", content=REAL_ARTICLE, success=True),
        FetchResult(url="https://example.com/bad", content="", success=False, error="timeout"),
    ])
    fetcher, cache = _cached_fetcher(tmp_path, inner)

    results = await fetcher.fetch_many([
        "https://example.com/ok", "https://example.com/bad",
    ])
    assert results[1].success is False
    assert cache.get("https://example.com/bad") is None
    assert cache.get("https://example.com/ok") == REAL_ARTICLE


async def test_fetch_many_no_inner_call_when_all_hit(tmp_path: pathlib.Path) -> None:
    inner = MagicMock()
    inner.fetch_many = AsyncMock()
    fetcher, cache = _cached_fetcher(tmp_path, inner)
    cache.put("https://example.com/a", REAL_ARTICLE)
    cache.put("https://example.com/b", REAL_ARTICLE)

    results = await fetcher.fetch_many([
        "https://example.com/a", "https://example.com/b",
    ])
    assert len(results) == 2
    inner.fetch_many.assert_not_awaited()


async def test_fetch_many_rejects_wall_from_inner(tmp_path: pathlib.Path) -> None:
    inner = MagicMock()
    wall = "x" * 100 + 'href="/i/flow/login"' + "y" * 400
    inner.fetch_many = AsyncMock(return_value=[
        FetchResult(url="https://walled.example/post", content=wall, success=True),
    ])
    fetcher, cache = _cached_fetcher(tmp_path, inner)

    await fetcher.fetch_many(["https://walled.example/post"])
    assert cache.size() == 0


# ---------------------------------------------------------------------------
# close()
# ---------------------------------------------------------------------------


async def test_close_delegates_to_inner(tmp_path: pathlib.Path) -> None:
    inner = MagicMock()
    inner.close = AsyncMock()
    fetcher, _cache = _cached_fetcher(tmp_path, inner)
    await fetcher.close()
    inner.close.assert_awaited_once()
