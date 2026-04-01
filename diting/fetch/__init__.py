"""Page content fetching — local extraction with Tavily fallback."""

from diting.fetch.base import Fetcher
from diting.fetch.composite import CompositeFetcher
from diting.fetch.local import LocalFetcher
from diting.fetch.tavily import FetchError, FetchResult, TavilyFetcher

__all__ = [
    "CompositeFetcher",
    "Fetcher",
    "FetchError",
    "FetchResult",
    "LocalFetcher",
    "TavilyFetcher",
]
