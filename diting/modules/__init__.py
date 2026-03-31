"""Search modules package."""

from diting.modules.base import BaseSearchModule
from diting.modules.bing import BingSearchModule
from diting.modules.brave import BraveSearchModule
from diting.modules.duckduckgo import DuckDuckGoSearchModule
from diting.modules.serp import SerpSearchModule

__all__ = [
    "BaseSearchModule",
    "BingSearchModule",
    "BraveSearchModule",
    "DuckDuckGoSearchModule",
    "SerpSearchModule",
]
