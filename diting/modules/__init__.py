"""Search modules package."""

from diting.modules.baidu import BaiduSearchModule
from diting.modules.base import BaseSearchModule
from diting.modules.bing import BingSearchModule
from diting.modules.brave import BraveSearchModule
from diting.modules.duckduckgo import DuckDuckGoSearchModule
from diting.modules.manifest import (
    CostTier,
    LatencyTier,
    ModuleManifest,
    ResultType,
)
from diting.modules.serp import SerpSearchModule
from diting.modules.x import XSearchModule
from diting.modules.zhihu import ZhihuSearchModule

__all__ = [
    "BaiduSearchModule",
    "BaseSearchModule",
    "BingSearchModule",
    "BraveSearchModule",
    "CostTier",
    "DuckDuckGoSearchModule",
    "LatencyTier",
    "ModuleManifest",
    "ResultType",
    "SerpSearchModule",
    "XSearchModule",
    "ZhihuSearchModule",
]
