"""Fetcher protocol — shared interface for all fetcher implementations."""

from __future__ import annotations

from typing import Protocol, runtime_checkable

from diting.fetch.tavily import FetchResult


@runtime_checkable
class Fetcher(Protocol):
    """Structural protocol satisfied by TavilyFetcher, LocalFetcher, and CompositeFetcher."""

    async def fetch(self, url: str) -> str: ...
    async def fetch_many(self, urls: list[str]) -> list[FetchResult]: ...
    async def close(self) -> None: ...
