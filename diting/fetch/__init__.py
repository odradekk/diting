"""Page content fetching — local extraction with Tavily fallback."""

from diting.fetch.base import Fetcher
from diting.fetch.cache import ContentCache, default_cache_path
from diting.fetch.cached import CachedFetcher
from diting.fetch.composite import CompositeFetcher
from diting.fetch.content_validator import is_cacheable
from diting.fetch.local import LocalFetcher
from diting.fetch.tavily import FetchError, FetchResult, TavilyFetcher

__all__ = [
    "CachedFetcher",
    "CompositeFetcher",
    "ContentCache",
    "Fetcher",
    "FetchError",
    "FetchResult",
    "LocalFetcher",
    "TavilyFetcher",
    "default_cache_path",
    "is_cacheable",
]
