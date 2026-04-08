"""Page content fetching — local extraction with Tavily fallback."""

from diting.fetch.archive import ArchiveFetcher
from diting.fetch.base import Fetcher
from diting.fetch.cache import ContentCache, default_cache_path
from diting.fetch.cached import CachedFetcher
from diting.fetch.composite import CompositeFetcher, chain_fetchers
from diting.fetch.content_validator import is_cacheable
from diting.fetch.jina_reader import JinaReaderFetcher
from diting.fetch.local import LocalFetcher
from diting.fetch.tavily import FetchError, FetchResult, TavilyFetcher

__all__ = [
    "ArchiveFetcher",
    "CachedFetcher",
    "CompositeFetcher",
    "ContentCache",
    "Fetcher",
    "FetchError",
    "FetchResult",
    "JinaReaderFetcher",
    "LocalFetcher",
    "TavilyFetcher",
    "chain_fetchers",
    "default_cache_path",
    "is_cacheable",
]
