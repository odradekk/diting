"""Search modules package."""

from diting.modules.arxiv import ArxivSearchModule
from diting.modules.baidu import BaiduSearchModule
from diting.modules.base import BaseSearchModule
from diting.modules.bing import BingSearchModule
from diting.modules.brave import BraveSearchModule
from diting.modules.duckduckgo import DuckDuckGoSearchModule
from diting.modules.github import GitHubSearchModule
from diting.modules.manifest import (
    CostTier,
    LatencyTier,
    ModuleManifest,
    ResultType,
)
from diting.modules.serp import SerpSearchModule
from diting.modules.stackexchange import StackExchangeSearchModule
from diting.modules.wikipedia import WikipediaSearchModule
from diting.modules.x import XSearchModule
from diting.modules.zhihu import ZhihuSearchModule

__all__ = [
    "ArxivSearchModule",
    "BaiduSearchModule",
    "BaseSearchModule",
    "BingSearchModule",
    "BraveSearchModule",
    "CostTier",
    "DuckDuckGoSearchModule",
    "GitHubSearchModule",
    "LatencyTier",
    "ModuleManifest",
    "ResultType",
    "SerpSearchModule",
    "StackExchangeSearchModule",
    "WikipediaSearchModule",
    "XSearchModule",
    "ZhihuSearchModule",
]
