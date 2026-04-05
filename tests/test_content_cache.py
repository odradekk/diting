"""Tests for diting.fetch.cache — SQLite content cache with TTL."""

from __future__ import annotations

import pathlib
import sqlite3
import time

import pytest

from diting.fetch.cache import ContentCache, _ttl_days_for_url, default_cache_path


@pytest.fixture
def cache_path(tmp_path: pathlib.Path) -> pathlib.Path:
    return tmp_path / "test_content.db"


# ---------------------------------------------------------------------------
# get / put basic behaviour
# ---------------------------------------------------------------------------


def test_get_returns_none_for_missing_url(cache_path: pathlib.Path) -> None:
    cache = ContentCache(cache_path)
    try:
        assert cache.get("https://example.com/a") is None
    finally:
        cache.close()


def test_put_then_get_roundtrip(cache_path: pathlib.Path) -> None:
    cache = ContentCache(cache_path)
    try:
        cache.put("https://example.com/a", "hello world")
        assert cache.get("https://example.com/a") == "hello world"
    finally:
        cache.close()


def test_put_overwrites_existing(cache_path: pathlib.Path) -> None:
    cache = ContentCache(cache_path)
    try:
        cache.put("https://example.com/a", "first")
        cache.put("https://example.com/a", "second")
        assert cache.get("https://example.com/a") == "second"
    finally:
        cache.close()


def test_delete_removes_entry(cache_path: pathlib.Path) -> None:
    cache = ContentCache(cache_path)
    try:
        cache.put("https://example.com/a", "hello")
        cache.delete("https://example.com/a")
        assert cache.get("https://example.com/a") is None
    finally:
        cache.close()


def test_size_tracks_rows(cache_path: pathlib.Path) -> None:
    cache = ContentCache(cache_path)
    try:
        assert cache.size() == 0
        cache.put("https://example.com/a", "x")
        cache.put("https://example.com/b", "y")
        assert cache.size() == 2
    finally:
        cache.close()


# ---------------------------------------------------------------------------
# TTL / expiry
# ---------------------------------------------------------------------------


def test_expired_entry_returns_none(cache_path: pathlib.Path) -> None:
    cache = ContentCache(cache_path)
    try:
        # Write a row with an expires_at in the past, bypassing put().
        past = time.time() - 10
        cache._conn.execute(
            "INSERT OR REPLACE INTO content "
            "(url, content, fetched_at, expires_at) VALUES (?, ?, ?, ?)",
            ("https://example.com/old", "stale", past - 1, past),
        )
        assert cache.get("https://example.com/old") is None
    finally:
        cache.close()


def test_expired_entry_is_deleted_on_get(cache_path: pathlib.Path) -> None:
    cache = ContentCache(cache_path)
    try:
        past = time.time() - 10
        cache._conn.execute(
            "INSERT OR REPLACE INTO content "
            "(url, content, fetched_at, expires_at) VALUES (?, ?, ?, ?)",
            ("https://example.com/old", "stale", past - 1, past),
        )
        cache.get("https://example.com/old")  # should opportunistically delete
        assert cache.size() == 0
    finally:
        cache.close()


def test_purge_expired(cache_path: pathlib.Path) -> None:
    cache = ContentCache(cache_path)
    try:
        past = time.time() - 10
        cache._conn.execute(
            "INSERT INTO content (url, content, fetched_at, expires_at) "
            "VALUES ('https://e.com/1', 'x', ?, ?)",
            (past - 1, past),
        )
        cache._conn.execute(
            "INSERT INTO content (url, content, fetched_at, expires_at) "
            "VALUES ('https://e.com/2', 'y', ?, ?)",
            (past - 1, past),
        )
        cache.put("https://e.com/fresh", "z")
        removed = cache.purge_expired()
        assert removed == 2
        assert cache.size() == 1
    finally:
        cache.close()


def test_explicit_ttl_overrides_domain_default(cache_path: pathlib.Path) -> None:
    cache = ContentCache(cache_path)
    try:
        cache.put("https://arxiv.org/abs/1234.5678", "paper", ttl_days=0)
        # Zero TTL → entry is immediately expired.
        time.sleep(0.01)
        assert cache.get("https://arxiv.org/abs/1234.5678") is None
    finally:
        cache.close()


# ---------------------------------------------------------------------------
# Domain-tier TTL
# ---------------------------------------------------------------------------


def test_arxiv_has_long_ttl() -> None:
    assert _ttl_days_for_url("https://arxiv.org/abs/2301.00001") >= 365


def test_wikipedia_has_month_ttl() -> None:
    assert _ttl_days_for_url("https://en.wikipedia.org/wiki/Cat") == 30


def test_github_has_month_ttl() -> None:
    assert _ttl_days_for_url("https://github.com/torvalds/linux") == 30


def test_news_sites_have_day_ttl() -> None:
    assert _ttl_days_for_url("https://www.nytimes.com/2026/04/05/world") == 1
    assert _ttl_days_for_url("https://www.bbc.com/news/world-12345") == 1


def test_unknown_domain_gets_default_ttl() -> None:
    assert _ttl_days_for_url("https://random-blog.example/post") == 7


def test_subdomain_matches_suffix_rule() -> None:
    assert _ttl_days_for_url("https://api.github.com/repos/x/y") == 30


def test_missing_host_falls_back_to_default() -> None:
    assert _ttl_days_for_url("not-a-url") == 7


# ---------------------------------------------------------------------------
# Schema / persistence
# ---------------------------------------------------------------------------


def test_cache_persists_across_instances(cache_path: pathlib.Path) -> None:
    c1 = ContentCache(cache_path)
    c1.put("https://example.com/a", "persisted")
    c1.close()

    c2 = ContentCache(cache_path)
    try:
        assert c2.get("https://example.com/a") == "persisted"
    finally:
        c2.close()


def test_schema_mismatch_rebuilds(cache_path: pathlib.Path) -> None:
    # Simulate a pre-existing DB with the wrong schema version.
    conn = sqlite3.connect(str(cache_path))
    conn.execute(
        "CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)"
    )
    conn.execute(
        "INSERT INTO meta (key, value) VALUES ('schema_version', 'ancient')"
    )
    conn.execute(
        "CREATE TABLE content (url TEXT, garbage_col INTEGER)"
    )
    conn.execute(
        "INSERT INTO content (url, garbage_col) VALUES ('x', 1)"
    )
    conn.commit()
    conn.close()

    # Opening the cache should rebuild the schema silently.
    cache = ContentCache(cache_path)
    try:
        assert cache.size() == 0
        cache.put("https://example.com/a", "fresh")
        assert cache.get("https://example.com/a") == "fresh"
    finally:
        cache.close()


def test_default_cache_path_is_under_user_cache() -> None:
    path = default_cache_path()
    assert path.name == "content.db"
    assert "diting" in path.parts


def test_parent_dirs_are_created(tmp_path: pathlib.Path) -> None:
    nested = tmp_path / "a" / "b" / "c" / "cache.db"
    cache = ContentCache(nested)
    try:
        assert nested.parent.is_dir()
        cache.put("https://example.com/", "ok")
        assert cache.get("https://example.com/") == "ok"
    finally:
        cache.close()
